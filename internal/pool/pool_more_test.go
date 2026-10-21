// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package pool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestCloseLifecycle 无 flusher 的 Close 是 no-op；有 flusher 的 Close 停 goroutine 并落盘；
// 重复 Close 幂等。
func TestCloseLifecycle(t *testing.T) {
	p := New("")
	p.Close()
	p.Close() // 幂等 no-op

	fp := filepath.Join(t.TempDir(), "state.json")
	q := New(fp)
	q.Add(&auth.Auth{UID: "u1"})
	q.SetCredits("u1", 42, 0)
	q.Close()
	raw, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("Close 应落盘: %v", err)
	}
	if !strings.Contains(string(raw), `"credits": 42`) {
		t.Errorf("落盘内容 = %s", raw)
	}
	if _, err := os.Stat(fp + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("落盘后不应残留 .tmp 文件: %v", err)
	}
	q.Close() // 幂等
}

// TestCostExploreStatus 探索台账：事件数 + 键内 \x1f 输出为 "|"，返回副本。
func TestCostExploreStatus(t *testing.T) {
	p := New("")
	ts := time.Now()
	p.mu.Lock()
	p.exploreLast["cn\x1fglm-5.2"] = ts
	p.costExploreEvents = 7
	p.mu.Unlock()

	events, last := p.CostExploreStatus()
	if events != 7 || !last["cn|glm-5.2"].Equal(ts) {
		t.Fatalf("events=%d last=%v", events, last)
	}
	last["cn|glm-5.2"] = time.Time{}
	p.mu.RLock()
	still := p.exploreLast["cn\x1fglm-5.2"]
	p.mu.RUnlock()
	if still.IsZero() {
		t.Error("返回的 map 应是副本")
	}
}

// TestConfigSetters 各注入器的边界：非正值保留原值、0 的合法语义、正负值写入。
func TestConfigSetters(t *testing.T) {
	p := New("")
	if p.breakerThreshold != defaultBreakerThreshold || p.breakerCooldown != defaultBreakerCooldown ||
		p.breakerCooldownMax != defaultBreakerCooldownMax {
		t.Fatalf("熔断默认值 = %d/%v/%v", p.breakerThreshold, p.breakerCooldown, p.breakerCooldownMax)
	}
	p.SetBreaker(0, 0, 0)
	if p.breakerThreshold != defaultBreakerThreshold || p.breakerCooldown != defaultBreakerCooldown {
		t.Error("非正值应保留原值")
	}
	p.SetBreaker(5, time.Minute, 2*time.Minute)
	if p.breakerThreshold != 5 || p.breakerCooldown != time.Minute || p.breakerCooldownMax != 2*time.Minute {
		t.Errorf("SetBreaker 写入 = %d/%v/%v", p.breakerThreshold, p.breakerCooldown, p.breakerCooldownMax)
	}

	p.SetSoftRateMax(0)
	if p.softRateMax != 0 {
		t.Error("0 应保留原值")
	}
	p.SetSoftRateMax(time.Hour)
	if p.softRateMax != time.Hour {
		t.Errorf("softRateMax = %v", p.softRateMax)
	}

	p.SetCostExploreInterval(-1)
	if p.costExploreInterval != defaultCostExploreInterval {
		t.Error("负值应保留默认")
	}
	p.SetCostExploreInterval(0)
	if p.costExploreInterval != 0 {
		t.Error("0 是合法关停值")
	}
	p.SetCostExploreInterval(5 * time.Minute)
	if p.costExploreInterval != 5*time.Minute {
		t.Errorf("costExploreInterval = %v", p.costExploreInterval)
	}

	p.SetWeights(0, -1)
	if p.idleWeightPerHour != defaultIdleWeightPerHour || p.idleWeightMax != defaultIdleWeightMax {
		t.Error("非正值应保留原值")
	}
	p.SetWeights(2, 3)
	if p.idleWeightPerHour != 2 || p.idleWeightMax != 3 {
		t.Errorf("weights = %v/%v", p.idleWeightPerHour, p.idleWeightMax)
	}

	if !p.preferExpiring {
		t.Fatal("默认应开启最早到期优先")
	}
	p.SetPreferExpiring(false)
	if p.preferExpiring {
		t.Error("SetPreferExpiring(false) 未生效")
	}

	p.SetDegrade(0, 0, 0)
	if p.degradeThreshold != defaultDegradeThreshold || p.degradeCooldown != defaultDegradeCooldown ||
		p.degradeCooldownMax != defaultDegradeCooldownMax {
		t.Error("非正值应保留默认")
	}
	p.SetDegrade(7, 2*time.Minute, 3*time.Minute)
	if p.degradeThreshold != 7 || p.degradeCooldown != 2*time.Minute || p.degradeCooldownMax != 3*time.Minute {
		t.Errorf("SetDegrade 写入 = %d/%v/%v", p.degradeThreshold, p.degradeCooldown, p.degradeCooldownMax)
	}

	if p.CreditFloor() != 0 {
		t.Error("积分保底默认关闭")
	}
	p.SetCreditFloor(-1)
	if p.CreditFloor() != 0 {
		t.Error("负值应保留原值")
	}
	p.SetCreditFloor(42)
	if p.CreditFloor() != 42 {
		t.Errorf("CreditFloor = %d", p.CreditFloor())
	}

	p.SetMaxInFlight(-1)
	if p.maxInFlight != 0 {
		t.Error("负值应保留原值")
	}
	p.SetMaxInFlight(3)
	if p.maxInFlight != 3 {
		t.Errorf("maxInFlight = %d", p.maxInFlight)
	}
	p.SetMaxInFlightGlobal(-1)
	if p.maxInFlightGlobal != 0 {
		t.Error("负值应保留原值")
	}
	p.SetMaxInFlightGlobal(2)
	if p.maxInFlightGlobal != 2 {
		t.Errorf("maxInFlightGlobal = %d", p.maxInFlightGlobal)
	}

	called := false
	p.SetModelRateOf(func(realm, model string) string { called = true; return "" })
	p.mu.RLock()
	fn := p.modelRateOf
	p.mu.RUnlock()
	if fn == nil {
		t.Fatal("SetModelRateOf 未注入")
	}
	fn("cn", "m")
	if !called {
		t.Error("注入的回调未被保存")
	}
	p.SetModelRateOf(nil)
}

// TestInFlightLimitGlobalTier 在途上限按 realm 分档：global 优先专用档，回落通用档。
func TestInFlightLimitGlobalTier(t *testing.T) {
	p := New("")
	p.SetMaxInFlight(3)
	p.SetMaxInFlightGlobal(2)
	p.Add(&auth.Auth{UID: "g1", Domain: "www.workbuddy.ai"})
	p.Add(&auth.Auth{UID: "c1", Domain: "www.codebuddy.cn"})
	if got := p.inFlightLimit(p.byUID["g1"]); got != 2 {
		t.Errorf("global 上限 = %d，应为 2", got)
	}
	if got := p.inFlightLimit(p.byUID["c1"]); got != 3 {
		t.Errorf("cn 上限 = %d，应为 3", got)
	}
	p.SetMaxInFlightGlobal(0)
	if got := p.inFlightLimit(p.byUID["g1"]); got != 3 {
		t.Errorf("global 专用档为 0 时应回落通用档，got %d", got)
	}
}

// TestServableAndServesModel 目录过滤：未注入/空模型放行；注入后按回调判定，
// 账号不健康时不看目录。
func TestServableAndServesModel(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1", Domain: "www.codebuddy.cn"})
	e := p.byUID["u1"]
	now := time.Now()

	if !p.servableFor(e, now, "any") {
		t.Error("未注入目录回调时应放行")
	}

	var gotUID, gotRealm, gotModel string
	var gotEnterprise bool
	p.SetModelServes(func(uid, realm string, enterprise bool, model string) bool {
		gotUID, gotRealm, gotEnterprise, gotModel = uid, realm, enterprise, model
		return model != "blocked"
	})
	if !p.servableFor(e, now, "ok") {
		t.Error("目录回调放行时可选")
	}
	if gotUID != "u1" || gotRealm != "cn" || gotEnterprise || gotModel != "ok" {
		t.Errorf("回调参数 = %q/%q/%v/%q", gotUID, gotRealm, gotEnterprise, gotModel)
	}
	if p.servableFor(e, now, "blocked") {
		t.Error("目录回调拒绝时不可选")
	}
	if !p.servesModel(e, "") {
		t.Error("空模型名应放行")
	}

	// 不健康时先于目录判定返回 false。
	p.Disable("u1", "test")
	if p.servableFor(e, now, "ok") {
		t.Error("禁用账号不可选")
	}
}

// TestUpsertKeepsState Add 同 uid 只换凭证、保留运行态；Remove 未知 uid 返回 nil。
func TestUpsertKeepsState(t *testing.T) {
	p := New("")
	a1 := &auth.Auth{UID: "u1", AccessToken: "old"}
	p.Add(a1)
	p.SetCredits("u1", 100, 0)
	a2 := &auth.Auth{UID: "u1", AccessToken: "new"}
	p.Add(a2)
	e := p.byUID["u1"]
	if e.a != a2 {
		t.Error("Add 已存在账号应替换凭证指针")
	}
	if e.credits != 100 {
		t.Errorf("运行态应保留，credits = %d", e.credits)
	}
	if p.Remove("nope") != nil {
		t.Error("Remove 未知 uid 应返回 nil")
	}
	removed := p.Remove("u1")
	if removed == nil || removed.UID != "u1" {
		t.Errorf("Remove 已存在账号应返回凭证: %+v", removed)
	}
	if _, ok := p.byUID["u1"]; ok {
		t.Error("移除后不应还在池内")
	}
}

// TestSyncToDirRemovesMissing SyncToDir 剔除消失账号并落盘。
func TestSyncToDirRemovesMissing(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "state.json")
	p := New(fp)
	defer p.Close()
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})
	p.SyncToDir([]*auth.Auth{{UID: "u1"}})
	if _, ok := p.byUID["u2"]; ok {
		t.Error("消失账号应被剔除")
	}
	if _, ok := p.byUID["u1"]; !ok {
		t.Error("保留账号不应被删")
	}
	raw, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("剔除后应落盘: %v", err)
	}
	if strings.Contains(string(raw), `"u2"`) {
		t.Errorf("落盘不应包含已删账号: %s", raw)
	}
}
