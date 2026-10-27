// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package redisstore

// upstash_test.go Upstash 实现：miniredis 起内存 Redis，验证绑定/状态镜像、
// TTL、关停语义与 Redis 不可用时的静默降级。写入是异步的：用 wg.Wait（同包可
// 访问）或 Close 等待写完再断言，不 sleep。

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// newUpstash 启动 miniredis 并返回连上的 Upstash；清理时关停。
func newUpstash(t *testing.T) (*Upstash, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	st := New("redis://"+mr.Addr(), "")
	u, ok := st.(*Upstash)
	if !ok {
		t.Fatalf("New 应返回 Upstash，实际 %T", st)
	}
	t.Cleanup(func() { _ = u.Close() })
	return u, mr
}

// New：能连上时返回 Upstash；连不上时静默降级 Noop。
func TestNewConnectionOutcomes(t *testing.T) {
	mr := miniredis.RunT(t)
	if st := New("redis://"+mr.Addr(), ""); len(st.(*Upstash).client.Options().Addr) == 0 {
		t.Fatal("Upstash 客户端地址为空")
	}
	// 回环上不存在的端口：Ping 失败 → Noop（不发外网）。
	if st := New("redis://localhost:1", ""); st != (Noop{}) {
		t.Fatalf("连接失败应降级 Noop，实际 %T", st)
	}
}

// 绑定镜像：带前缀写入、TTL 正确、LoadBinds 只读 bind 前缀、DelBind 删除。
func TestBindMirrorAndTTL(t *testing.T) {
	u, mr := newUpstash(t)
	u.SetBind("k1", "u1", time.Minute)
	u.SetBind("k2", "u2", 0) // ttl<=0 → 默认 keyTTL
	u.wg.Wait()

	if got, err := mr.Get(bindPrefix + "k1"); err != nil || got != "u1" {
		t.Fatalf("k1 = %q, %v", got, err)
	}
	if ttl := mr.TTL(bindPrefix + "k1"); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("k1 TTL = %v", ttl)
	}
	if ttl := mr.TTL(bindPrefix + "k2"); ttl != keyTTL {
		t.Fatalf("k2 应使用默认 TTL %v，实际 %v", keyTTL, ttl)
	}

	// 别的键不能被 LoadBinds 读进来。
	if err := mr.Set("wb2api:other", "x"); err != nil {
		t.Fatal(err)
	}
	binds := u.LoadBinds()
	if len(binds) != 2 || binds["k1"] != "u1" || binds["k2"] != "u2" {
		t.Fatalf("LoadBinds = %v", binds)
	}
	if _, ok := binds["other"]; ok {
		t.Fatalf("LoadBinds 不应包含非 bind 前缀的键: %v", binds)
	}

	u.DelBind("k1")
	u.wg.Wait()
	if _, err := mr.Get(bindPrefix + "k1"); err == nil {
		t.Fatal("DelBind 后键应不存在")
	}
	if binds := u.LoadBinds(); len(binds) != 1 || binds["k2"] != "u2" {
		t.Fatalf("删除后 LoadBinds = %v", binds)
	}
}

// 状态快照：SaveState 后 LoadState 读回同样字节；没有状态时 ok=false。
func TestStateRoundTrip(t *testing.T) {
	u, mr := newUpstash(t)
	if _, ok := u.LoadState(); ok {
		t.Fatal("没有状态时应 ok=false")
	}
	data := []byte(`{"accounts":[{"uid":"u1"}]}`)
	u.SaveState(data)
	u.wg.Wait()
	got, ok := u.LoadState()
	if !ok || string(got) != string(data) {
		t.Fatalf("LoadState = %q, %v", got, ok)
	}
	if ttl := mr.TTL(stateKey); ttl != keyTTL {
		t.Fatalf("状态键 TTL = %v", ttl)
	}
}

// 会话记录快照：SaveSessions 后 LoadSessions 读回同样字节，和池状态快照各用各的键。
func TestSessionsRoundTrip(t *testing.T) {
	u, mr := newUpstash(t)
	if _, ok := u.LoadSessions(); ok {
		t.Fatal("没有快照时应 ok=false")
	}
	data := []byte(`{"version":1,"conversations":[]}`)
	u.SaveSessions(data)
	u.wg.Wait()
	got, ok := u.LoadSessions()
	if !ok || string(got) != string(data) {
		t.Fatalf("LoadSessions = %q, %v", got, ok)
	}
	if ttl := mr.TTL(sessionsKey); ttl != keyTTL {
		t.Fatalf("会话记录键 TTL = %v", ttl)
	}
	if _, ok := u.LoadState(); ok {
		t.Fatal("会话记录不应写到池状态的键上")
	}
}

// Close：等已提交的写跑完、幂等、关停后提交的写直接丢弃、零值 Upstash 可用。
func TestCloseSemantics(t *testing.T) {
	t.Run("零值 Upstash 不崩溃", func(t *testing.T) {
		var z Upstash
		var ran atomic.Bool
		z.goWrite(func() { ran.Store(true) })
		z.wg.Wait()
		if !ran.Load() {
			t.Fatal("零值 Upstash 的 goWrite 应执行")
		}
		if err := z.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := z.Close(); err != nil {
			t.Fatalf("重复 Close: %v", err)
		}
	})

	t.Run("Close 前提交的写全部执行", func(t *testing.T) {
		u, _ := newUpstash(t)
		u.sem = make(chan struct{}, 1) // 串行化，确保有排队写
		var done atomic.Int32
		for i := 0; i < 5; i++ {
			u.goWrite(func() { done.Add(1) })
		}
		if err := u.Close(); err != nil {
			t.Fatal(err)
		}
		if got := done.Load(); got != 5 {
			t.Fatalf("Close 应等全部写完成，实际 %d", got)
		}
	})

	t.Run("Close 后提交的写丢弃", func(t *testing.T) {
		u, mr := newUpstash(t)
		if err := u.Close(); err != nil {
			t.Fatal(err)
		}
		u.SetBind("after", "u", time.Minute)
		u.wg.Wait()
		if _, err := mr.Get(bindPrefix + "after"); err == nil {
			t.Fatal("Close 后提交的写应被丢弃")
		}
	})
}

// Redis 不可用：写静默失败不 panic，读返回空。
func TestRedisDownIsSilent(t *testing.T) {
	u, mr := newUpstash(t)
	mr.Close()
	u.SetBind("k", "u", time.Minute)
	u.SaveState([]byte(`{}`))
	u.SaveSessions([]byte(`{}`))
	u.wg.Wait()
	if _, ok := u.LoadState(); ok {
		t.Fatal("Redis 不可用时 LoadState 应 ok=false")
	}
	if _, ok := u.LoadSessions(); ok {
		t.Fatal("Redis 不可用时 LoadSessions 应 ok=false")
	}
	if binds := u.LoadBinds(); len(binds) != 0 {
		t.Fatalf("Redis 不可用时 LoadBinds 应为空: %v", binds)
	}
	if err := u.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// Noop：所有方法空操作并返回零值。
func TestNoopAllMethods(t *testing.T) {
	var n Noop
	n.SetBind("k", "u", time.Minute)
	n.DelBind("k")
	n.SaveState([]byte("{}"))
	n.SaveSessions([]byte("{}"))
	if data, ok := n.LoadSessions(); ok || data != nil {
		t.Fatalf("Noop.LoadSessions = %q, %v", data, ok)
	}
	if binds := n.LoadBinds(); binds != nil {
		t.Fatalf("Noop.LoadBinds 应为 nil: %v", binds)
	}
	if data, ok := n.LoadState(); ok || data != nil {
		t.Fatalf("Noop.LoadState = %q, %v", data, ok)
	}
	if err := n.Close(); err != nil {
		t.Fatalf("Noop.Close: %v", err)
	}
}
