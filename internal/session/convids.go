// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// convids.go 客户端没给会话 id 时，出站 X-Conversation-ID 用哪个值（issue #2）。
//
// 官方客户端一段对话从头到尾只用一个会话 id。上游有的模型要同一个号、同一个会话 id 才命中
// 前缀缓存（10-27 生产实测 glm-5.3-flash：换号或同号换 id 都整段未命中，同号同 id 闲置 20 分钟
// 仍命中），所以同一段对话发往同一个号时 id 不能变。客户端没给 id，网关就按内容认对话
// （见 ParseConversation）：
//   - 第一轮（历史里还没有助手回复）按「账号 + 开头」记一个新生成的 id，标为未认领；开头相同的
//     请求再来，沿用它。
//   - 有了第一条助手回复以后按「账号 + 开头 + 第一条回复」记录。第一次见到时认领第一轮记下的
//     id 接着用；那个 id 已经被开头相同、回复不同的另一段对话认领了，就新生成一个。开头逐字相同
//     的两段对话最多在第一轮共用一个 id，第二轮起分开（10-20 生产实测同一个 id 发不相干的内容
//     不会串会话，共用的代价只是上游把它们记成同一个会话）。
//   - 账号入键：对话换到别的号就换一个 id，同一个会话 id 不会横跨多个账号。
//   - 记录在最后一次使用后保留 7 天，存进数据目录（见 persist.go），网关重启后照旧。
//
// id 都是生成当时的 UUID v7（带连字符），同官方新会话的 id，里面的时刻是真实的。
package session

import (
	"crypto/sha256"
	"encoding/base64"
	"slices"
	"sync"
	"time"
)

const (
	// convIDTTL 会话 id 的记录在最后一次使用后保留多久，同 Redis 里粘性绑定镜像的保留期。
	convIDTTL = 7 * 24 * time.Hour
	// convIDMax 记录条数上限（每条约百来字节），超了先清过期的，再清最久没用的，降到九成。
	convIDMax = 20000
)

// convRecord 一条会话 id 记录。claimed 只用于第一轮的记录：已被某段对话在第二轮认领。
type convRecord struct {
	id      string
	last    time.Time
	claimed bool
}

// convStore 会话 id 记录。键是「种类 + 账号 + 摘要」的 sha256，条目大小固定。
type convStore struct {
	mu    sync.Mutex
	m     map[[32]byte]convRecord
	dirty bool // 有没存盘的改动
	now   func() time.Time
}

func newConvStore() *convStore {
	return &convStore{m: map[[32]byte]convRecord{}, now: time.Now}
}

var convIDs = newConvStore()

// AccountConversationID 客户端没给会话 id 时出站 X-Conversation-ID 的值，规则见文件头。
// 认不出对话（请求里没有 user 消息）时每次新生成。
func AccountConversationID(uid string, c Conversation) string { return convIDs.id(uid, c) }

func convKey(kind, uid, digest string) [32]byte {
	return sha256.Sum256([]byte(kind + "\x00" + uid + "\x00" + digest))
}

func (s *convStore) id(uid string, c Conversation) string {
	if c.Opener == "" {
		return NewUUIDv7()
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	open := convKey("open", uid, c.Opener)
	if c.Seed == "" {
		if r, ok := s.m[open]; ok && !r.claimed && now.Sub(r.last) < convIDTTL {
			s.touchLocked(open, r, now)
			return r.id
		}
		id := dashUUID(uuidV7Hex(now))
		s.putLocked(open, convRecord{id: id, last: now}, now)
		return id
	}
	k := convKey("conv", uid, c.Seed)
	if r, ok := s.m[k]; ok && now.Sub(r.last) < convIDTTL {
		s.touchLocked(k, r, now)
		return r.id
	}
	var id string
	if r, ok := s.m[open]; ok && !r.claimed && now.Sub(r.last) < convIDTTL {
		r.claimed, r.last = true, now
		s.m[open] = r
		id = r.id
	} else {
		id = dashUUID(uuidV7Hex(now))
	}
	s.putLocked(k, convRecord{id: id, last: now}, now)
	return id
}

// touchLocked 记下这次使用。使用时刻跨了整点才算改动：存盘的时刻最多晚一小时，对 7 天的
// 保留期无关紧要，省得每个请求都重写文件。
func (s *convStore) touchLocked(k [32]byte, r convRecord, now time.Time) {
	if !sameHour(r.last, now) {
		s.dirty = true
	}
	r.last = now
	s.m[k] = r
}

func (s *convStore) putLocked(k [32]byte, r convRecord, now time.Time) {
	if _, ok := s.m[k]; !ok && len(s.m) >= convIDMax {
		evictStale(s.m, func(r convRecord) time.Time { return r.last }, now, convIDTTL, convIDMax)
	}
	s.m[k] = r
	s.dirty = true
}

func sameHour(a, b time.Time) bool { return a.Truncate(time.Hour).Equal(b.Truncate(time.Hour)) }

// evictStale 清掉 ttl 内没用过的记录；还超过上限的九成，就再清最久没用的。
func evictStale[V any](m map[[32]byte]V, last func(V) time.Time, now time.Time, ttl time.Duration, max int) {
	for k, v := range m {
		if now.Sub(last(v)) >= ttl {
			delete(m, k)
		}
	}
	keep := max * 9 / 10
	if len(m) <= keep {
		return
	}
	keys := make([][32]byte, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b [32]byte) int { return last(m[a]).Compare(last(m[b])) })
	for _, k := range keys[:len(keys)-keep] {
		delete(m, k)
	}
}

// convSnap 存盘的一条会话 id 记录。
type convSnap struct {
	K  string `json:"k"`           // 键（sha256，base64url）
	ID string `json:"id"`          // 会话 id
	T  int64  `json:"t"`           // 最后一次使用（Unix 秒）
	C  bool   `json:"c,omitempty"` // 第一轮的记录已被认领
}

// takeDirty 返回有没有没存盘的改动，并清掉标记。
func (s *convStore) takeDirty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.dirty
	s.dirty = false
	return d
}

func (s *convStore) markDirty() {
	s.mu.Lock()
	s.dirty = true
	s.mu.Unlock()
}

// snapshot 没过期的记录，顺手清掉过期的。
func (s *convStore) snapshot() []convSnap {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]convSnap, 0, len(s.m))
	for k, r := range s.m {
		if now.Sub(r.last) >= convIDTTL {
			delete(s.m, k)
			continue
		}
		out = append(out, convSnap{K: base64.RawURLEncoding.EncodeToString(k[:]), ID: r.id, T: r.last.Unix(), C: r.claimed})
	}
	return out
}

// restore 读回存盘的记录：跳过过期和格式不对的，内存里已有的以内存为准。返回读回的条数。
func (s *convStore) restore(recs []convSnap) int {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, rec := range recs {
		k, ok := decodeKey(rec.K)
		last := time.Unix(rec.T, 0)
		if !ok || rec.ID == "" || now.Sub(last) >= convIDTTL {
			continue
		}
		if _, exists := s.m[k]; exists {
			continue
		}
		if len(s.m) >= convIDMax {
			break
		}
		s.m[k] = convRecord{id: rec.ID, last: last, claimed: rec.C}
		n++
	}
	return n
}

func decodeKey(s string) ([32]byte, bool) {
	var k [32]byte
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != len(k) {
		return k, false
	}
	copy(k[:], b)
	return k, true
}
