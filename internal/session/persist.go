// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// persist.go 会话记录存盘（issue #2）：出站会话 id（convids.go）和每段对话用过的号（Router 的
// warm），写在数据目录的 sessions.json，配置了 Redis 时再镜像一份。网关重启后，同一段对话照旧
// 回到原来的号、用原来的会话 id，上游的前缀缓存接得上。
package session

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// persistInterval 有改动时多久写一次。没写下的最多是这么久里新开的对话，丢了也只是缓存断一次。
// var 供测试缩短。
var persistInterval = time.Minute

const persistVersion = 1

// Mirror 会话记录快照的 Redis 镜像（redisstore.Store 满足，Noop 空实现）。
type Mirror interface {
	SaveSessions(data []byte)
	LoadSessions() ([]byte, bool)
}

// persistFile sessions.json 的内容。
type persistFile struct {
	Version       int        `json:"version"`
	Saved         time.Time  `json:"saved"`
	Conversations []convSnap `json:"conversations"`
	Accounts      []warmSnap `json:"accounts,omitempty"`
}

// Persist 会话记录的读回与定时存盘。router 为 nil（粘性关闭）时只存会话 id。
type Persist struct {
	path   string
	router *Router
	mirror Mirror
	convs  *convStore

	mu       sync.Mutex // 串行化写文件
	started  bool
	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// NewPersist path 为空时不写文件（只镜像到 Redis，测试用）；mirror 可为 nil。
func NewPersist(path string, router *Router, mirror Mirror) *Persist {
	return &Persist{path: path, router: router, mirror: mirror, convs: convIDs,
		stop: make(chan struct{}), done: make(chan struct{})}
}

// Load 启动时读回：本地文件和 Redis 快照取较新的一份。都没有或都读不了就从空开始。
func (p *Persist) Load() {
	var local, remote *persistFile
	if p.path != "" {
		f, err := readPersistFile(p.path)
		switch {
		case err == nil:
			local = f
		case !errors.Is(err, fs.ErrNotExist):
			log.Printf("WARN: [session] 读 %s 失败，会话记录从空开始: %v", p.path, err)
		}
	}
	if p.mirror != nil {
		if raw, ok := p.mirror.LoadSessions(); ok {
			if f, err := parsePersistFile(raw); err == nil {
				remote = f
			}
		}
	}
	pick, from := local, p.path
	if remote != nil && (local == nil || remote.Saved.After(local.Saved)) {
		pick, from = remote, "Redis"
	}
	if pick == nil {
		return
	}
	n := p.convs.restore(pick.Conversations)
	w := p.router.restoreWarm(pick.Accounts)
	log.Printf("[session] 从 %s 恢复 %d 条会话 id、%d 段对话用过的号", from, n, w)
}

func readPersistFile(path string) (*persistFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parsePersistFile(raw)
}

func parsePersistFile(raw []byte) (*persistFile, error) {
	var f persistFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	if f.Version != persistVersion {
		return nil, errors.New("版本不认识")
	}
	return &f, nil
}

// Start 启动后台定时存盘，Stop 前一直运行。
func (p *Persist) Start() {
	p.started = true
	interval := persistInterval // 起 goroutine 前取值，免得和测试恢复它的写入竞争
	go func() {
		defer close(p.done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-p.stop:
				p.Flush()
				return
			case <-t.C:
				p.flush(false)
			}
		}
	}()
}

// Stop 停掉后台存盘并最后写一次；幂等。没 Start 过也会写。
func (p *Persist) Stop() {
	p.stopOnce.Do(func() { close(p.stop) })
	if p.started {
		<-p.done
		return
	}
	p.Flush()
}

// Flush 立即写一次（停机时用）。
func (p *Persist) Flush() { p.flush(true) }

func (p *Persist) flush(force bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// 两个标记都要取：只写了其中一边的改动，另一边的标记也得清掉。
	convDirty, warmDirty := p.convs.takeDirty(), p.router.takeWarmDirty()
	if !convDirty && !warmDirty && !force {
		return
	}
	raw, err := json.Marshal(persistFile{
		Version:       persistVersion,
		Saved:         time.Now().UTC(),
		Conversations: p.convs.snapshot(),
		Accounts:      p.router.warmSnapshot(),
	})
	if err == nil && p.path != "" {
		err = writeFileAtomic(p.path, raw)
	}
	if err != nil {
		log.Printf("WARN: [session] 写 %s 失败: %v", p.path, err)
		p.convs.markDirty()
		p.router.markWarmDirty()
		return
	}
	if p.mirror != nil {
		p.mirror.SaveSessions(raw)
	}
}

// writeFileAtomic 先写临时文件再改名，写到一半断电也不会留下半个文件。
func writeFileAtomic(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
