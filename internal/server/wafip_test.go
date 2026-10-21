// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

// wafip_test.go WAF IP 级拦截状态机：不同号计数阈值、窗口剪枝、激活期不续期、
// 到期自然解除。时间通过直接构造 hits/until 的过去时刻验证，不依赖真实等待。

import (
	"testing"
	"time"
)

// 单号反复命中永不触发 IP 级拦截（同号覆盖不累计）。
func TestWafIPGateSameAccountNeverTriggers(t *testing.T) {
	var g wafIPGate
	for i := 0; i < 5; i++ {
		if g.noteWaf("u1") {
			t.Fatalf("第 %d 次单号命中不应激活", i+1)
		}
	}
	if len(g.hits) != 1 {
		t.Fatalf("同号命中应只记一条，实际 %d", len(g.hits))
	}
	if g.active() {
		t.Fatal("未达阈值不应激活")
	}
}

// 窗口内不同账号数达到阈值（2）即激活；激活时清空判定窗。
func TestWafIPGateThresholdActivates(t *testing.T) {
	var g wafIPGate
	if g.noteWaf("u1") {
		t.Fatal("第一个号不应激活")
	}
	if !g.noteWaf("u2") {
		t.Fatal("第二个不同号应激活 IP 级拦截")
	}
	if !g.active() {
		t.Fatal("激活后 active() 应为 true")
	}
	if len(g.hits) != 0 {
		t.Fatalf("激活后应清空判定窗，实际 %d 条", len(g.hits))
	}
}

// 激活期内新命中：返回 true 但不续期、不记账（自然解除语义）。
func TestWafIPGateActiveDoesNotExtend(t *testing.T) {
	var g wafIPGate
	g.noteWaf("u1")
	g.noteWaf("u2")
	if !g.active() {
		t.Fatal("前置激活失败")
	}
	until := g.until
	if !g.noteWaf("u3") {
		t.Fatal("激活期内新命中应直接返回 true")
	}
	if !g.until.Equal(until) {
		t.Fatalf("激活期不应续期: until %v -> %v", until, g.until)
	}
	if len(g.hits) != 0 {
		t.Fatalf("激活期内不应记账，实际 %d 条", len(g.hits))
	}
}

// 窗口外的旧命中在下次记账时被剪掉，不参与阈值判定。
func TestWafIPGatePrunesOldHits(t *testing.T) {
	var g wafIPGate
	old := time.Now().Add(-2 * wafIPWindow)
	g.hits = map[string]time.Time{"stale": old}
	if g.noteWaf("u1") {
		t.Fatal("旧命中应被剪掉，不同号数只有 1，不应激活")
	}
	if _, ok := g.hits["stale"]; ok {
		t.Fatal("窗口外命中应被删除")
	}
	if len(g.hits) != 1 {
		t.Fatalf("应只保留本次命中，实际 %d", len(g.hits))
	}
}

// active()：未激活 false；until 已过 false。
func TestWafIPGateActiveExpiry(t *testing.T) {
	var g wafIPGate
	if g.active() {
		t.Fatal("零值应未激活")
	}
	g.until = time.Now().Add(-time.Second)
	if g.active() {
		t.Fatal("until 已过应解除激活")
	}
	g.until = time.Now().Add(time.Minute)
	if !g.active() {
		t.Fatal("until 未到应处于激活")
	}
}
