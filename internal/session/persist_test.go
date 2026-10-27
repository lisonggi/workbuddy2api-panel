// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package session

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMirror 不联网的 Redis 镜像：记下写入，读时回 load。
type fakeMirror struct {
	mu    sync.Mutex
	saved [][]byte
	load  []byte
}

func (m *fakeMirror) SaveSessions(d []byte) {
	m.mu.Lock()
	m.saved = append(m.saved, append([]byte(nil), d...))
	m.mu.Unlock()
}

func (m *fakeMirror) LoadSessions() ([]byte, bool) { return m.load, m.load != nil }

func (m *fakeMirror) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.saved)
}

// testPersist 用自己的会话 id 记录，不碰进程里共用的那份。
func testPersist(path string, r *Router, m Mirror) *Persist {
	p := NewPersist(path, r, m)
	p.convs = newConvStore()
	return p
}

// snapFor 一条第二轮以后的会话 id 记录（账号 + Seed）。
func snapFor(uid, seed, id string, at time.Time) convSnap {
	k := convKey("conv", uid, seed)
	return convSnap{K: base64.RawURLEncoding.EncodeToString(k[:]), ID: id, T: at.Unix()}
}

func writePersistFile(t *testing.T, path string, f persistFile) {
	t.Helper()
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

// 写下再读回：会话 id 与用过的号都在，文件只有本人可读写。
func TestPersistRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "sessions.json")
	avail := []string{"u1"}
	r := warmRouter(&avail, "u1")
	p := testPersist(path, r, nil)
	turn2 := Conversation{Opener: "o", Seed: "A"}
	id := p.convs.id("u1", turn2)
	r.Bind("k", "u2")
	p.flush(false)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("应写出文件: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("文件权限 = %v", info.Mode().Perm())
	}

	buf := captureLog(t)
	r2 := warmRouter(&avail, "u1")
	p2 := testPersist(path, r2, nil)
	p2.Load()
	if got := p2.convs.id("u1", turn2); got != id {
		t.Fatalf("重启后同一段对话应沿用原来的会话 id: %q / %q", id, got)
	}
	if got := r2.WarmUIDs("k"); !slices.Equal(got, []string{"u2"}) {
		t.Fatalf("重启后应记得用过的号: %v", got)
	}
	if !strings.Contains(buf.String(), "恢复 1 条会话 id、1 段对话用过的号") {
		t.Fatalf("应记一行恢复日志: %q", buf.String())
	}
}

// 没有改动不写；有改动写一次；Flush 不管有没有改动都写。镜像拿到的和文件内容一样。
func TestPersistFlushOnlyWhenDirty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	m := &fakeMirror{}
	p := testPersist(path, nil, m)
	p.flush(false)
	if m.count() != 0 {
		t.Fatal("没有改动不应写")
	}
	p.convs.id("u1", Conversation{Opener: "o"})
	p.flush(false)
	p.flush(false)
	if m.count() != 1 {
		t.Fatalf("有改动只写一次，got %d", m.count())
	}
	p.Flush()
	if m.count() != 2 {
		t.Fatalf("Flush 应强制写，got %d", m.count())
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Equal(raw, m.saved[1]) {
		t.Fatal("镜像内容应与文件一致")
	}
	f, err := parsePersistFile(raw)
	if err != nil || len(f.Conversations) != 1 || f.Accounts != nil {
		t.Fatalf("文件内容: %+v %v", f, err)
	}
}

// 本地文件和 Redis 快照取较新的一份；Redis 快照坏了或版本不认识就用本地的。
func TestPersistLoadPicksNewer(t *testing.T) {
	now := time.Now()
	local := persistFile{Version: persistVersion, Saved: now.Add(-time.Hour), Conversations: []convSnap{snapFor("u1", "L", "id-local", now)}}
	remote := persistFile{Version: persistVersion, Saved: now, Conversations: []convSnap{snapFor("u1", "R", "id-remote", now)}}
	remoteRaw, _ := json.Marshal(remote)
	hasLocal := func(p *Persist) bool { return p.convs.id("u1", Conversation{Opener: "o", Seed: "L"}) == "id-local" }
	hasRemote := func(p *Persist) bool { return p.convs.id("u1", Conversation{Opener: "o", Seed: "R"}) == "id-remote" }

	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	writePersistFile(t, path, local)
	captureLog(t)

	p := testPersist(path, nil, &fakeMirror{load: remoteRaw})
	p.Load()
	if !hasRemote(p) || hasLocal(p) {
		t.Fatal("Redis 快照较新时应用它")
	}

	local.Saved = now.Add(time.Hour)
	writePersistFile(t, path, local)
	p = testPersist(path, nil, &fakeMirror{load: remoteRaw})
	p.Load()
	if !hasLocal(p) || hasRemote(p) {
		t.Fatal("本地较新时应用本地的")
	}

	for _, bad := range [][]byte{[]byte("not json"), []byte(`{"version":99}`)} {
		p = testPersist(path, nil, &fakeMirror{load: bad})
		p.Load()
		if !hasLocal(p) {
			t.Fatalf("Redis 快照 %q 读不了时应用本地的", bad)
		}
	}

	p = testPersist(filepath.Join(dir, "missing.json"), nil, &fakeMirror{load: remoteRaw})
	p.Load()
	if !hasRemote(p) {
		t.Fatal("本地没有文件时应用 Redis 快照")
	}

	p = testPersist("", nil, nil)
	p.Load()
	if len(p.convs.m) != 0 {
		t.Fatal("都没有时从空开始")
	}
}

// 本地文件坏了：记一行 WARN，从空开始；文件不存在不记。
func TestPersistLoadBadLocalFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	buf := captureLog(t)
	testPersist(filepath.Join(dir, "missing.json"), nil, nil).Load()
	if buf.Len() != 0 {
		t.Fatalf("文件不存在不应记日志: %q", buf.String())
	}
	for _, content := range []string{"not json", `{"version":99,"conversations":[]}`} {
		buf.Reset()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		p := testPersist(path, nil, nil)
		p.Load()
		if !strings.Contains(buf.String(), "WARN: [session] 读 "+path+" 失败") || len(p.convs.m) != 0 {
			t.Fatalf("%q：应记 WARN 并从空开始，日志 %q", content, buf.String())
		}
	}
}

// 写不了文件：记 WARN，改动标记留着下次再写，也不镜像半截状态。
func TestPersistWriteFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	buf := captureLog(t)
	m := &fakeMirror{}
	avail := []string{"u1"}
	r := warmRouter(&avail, "u1")
	p := testPersist(filepath.Join(blocker, "sessions.json"), r, m)
	p.convs.id("u1", Conversation{Opener: "o"})
	r.Bind("k", "u1")
	p.flush(false)
	if !strings.Contains(buf.String(), "WARN: [session] 写 ") {
		t.Fatalf("应记 WARN: %q", buf.String())
	}
	if m.count() != 0 {
		t.Fatal("写文件失败时不应镜像")
	}
	if !p.convs.takeDirty() || !r.takeWarmDirty() {
		t.Fatal("改动标记应留着下次再写")
	}

	// 临时文件写不了（目录只读）也一样。
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(filepath.Join(ro, "sessions.json"), []byte("{}")); err == nil && os.Geteuid() != 0 {
		t.Fatal("只读目录里应写不了")
	}
	// 改名失败：目标是个非空目录。
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(filepath.Join(target, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(target, []byte("{}")); err == nil {
		t.Fatal("目标是非空目录时改名应失败")
	}
}

// 路径为空：不写文件，只镜像。
func TestPersistEmptyPathMirrorsOnly(t *testing.T) {
	m := &fakeMirror{}
	p := testPersist("", nil, m)
	p.convs.id("u1", Conversation{Opener: "o"})
	p.flush(false)
	if m.count() != 1 {
		t.Fatalf("应镜像一次，got %d", m.count())
	}
}

// 后台定时写有改动就写；Stop 最后再写一次，可重复调用；没 Start 过 Stop 也会写。
func TestPersistStartStop(t *testing.T) {
	old := persistInterval
	persistInterval = 10 * time.Millisecond
	t.Cleanup(func() { persistInterval = old })

	m := &fakeMirror{}
	p := testPersist("", nil, m)
	p.Start()
	p.convs.id("u1", Conversation{Opener: "o"})
	deadline := time.Now().Add(5 * time.Second)
	for m.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if m.count() == 0 {
		t.Fatal("后台应把改动写下去")
	}
	n := m.count()
	p.Stop()
	p.Stop()
	if m.count() != n+1 {
		t.Fatalf("Stop 应最后写一次，got %d → %d", n, m.count())
	}

	m2 := &fakeMirror{}
	testPersist("", nil, m2).Stop()
	if m2.count() != 1 {
		t.Fatal("没 Start 过 Stop 也应写一次")
	}
}
