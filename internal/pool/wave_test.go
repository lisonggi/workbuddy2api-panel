// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package pool

import (
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// wavePool 三个国内版号 + 一个国际版号，余额都充足。
func wavePool(t *testing.T) *Pool {
	t.Helper()
	p := New("")
	for _, uid := range []string{"c1", "c2", "c3"} {
		p.Add(&auth.Auth{UID: uid, Domain: "www.codebuddy.cn"})
		p.SetCredits(uid, 1000, 0)
	}
	p.Add(&auth.Auth{UID: "g1", Domain: "www.workbuddy.ai"})
	p.SetCredits("g1", 1000, 0)
	return p
}

// restrict 模拟 handler 的 11140 处理：先禁用再记风控波。
func restrict(p *Pool, uid string) int {
	p.Disable(uid, "11140")
	return p.NoteRestriction(uid)
}

func TestRestrictionWavePausesRealm(t *testing.T) {
	p := wavePool(t)
	if n := restrict(p, "c1"); n != 0 {
		t.Fatalf("第一个号被限不应触发暂停，暂停了 %d 个", n)
	}
	if st, _ := p.Status("c2"); st.Cooling {
		t.Fatalf("未达阈值时同域的号不应冷却: %+v", st)
	}
	if n := restrict(p, "c2"); n != 1 {
		t.Fatalf("第二个号被限应暂停同域剩下的 1 个号，实际 %d", n)
	}
	st, _ := p.Status("c3")
	if !st.Cooling || st.CoolKind != "restriction_wave" || !strings.Contains(st.Reason, "restriction wave") {
		t.Fatalf("c3 应处风控波暂停: %+v", st)
	}
	if st.CoolRemaining < int64((29 * time.Minute).Seconds()) {
		t.Fatalf("默认暂停 30 分钟，剩余 %d 秒", st.CoolRemaining)
	}
	if st, _ := p.Status("g1"); st.Cooling {
		t.Fatalf("别的域不受影响: %+v", st)
	}
	if st, _ := p.Status("c1"); !st.Disabled || st.CoolKind != "" {
		t.Fatalf("已禁用的号保持禁用，不再叠暂停: %+v", st)
	}
	if acct := p.PickExcludingForRealm(nil, "", "cn"); acct != nil {
		t.Fatalf("暂停期的号不参与兜底，实际选中 %s", acct.UID)
	}
	if acct := p.Pick(); acct == nil || acct.UID != "g1" {
		t.Fatalf("应只选到国际版号，实际 %+v", acct)
	}
	if len(p.waveHits["cn"]) != 0 {
		t.Fatalf("触发后该域记录应清空: %+v", p.waveHits["cn"])
	}
}

func TestRestrictionWaveKeepsLongerCooldown(t *testing.T) {
	p := wavePool(t)
	p.Cooldown("c3", CoolHard, 10*time.Hour, "credit exhausted")
	restrict(p, "c1")
	if n := restrict(p, "c2"); n != 0 {
		t.Fatalf("已有更晚截止的号不应改成暂停，实际暂停 %d 个", n)
	}
	if st, _ := p.Status("c3"); st.CoolKind != "hard_credit" || st.CoolRemaining < int64((9*time.Hour).Seconds()) {
		t.Fatalf("积分冷却不应被缩短: %+v", st)
	}
}

func TestRestrictionWaveWindowAndDedupe(t *testing.T) {
	p := wavePool(t)
	restrict(p, "c1")
	p.ReviveDisabled("c1")
	if n := restrict(p, "c1"); n != 0 {
		t.Fatalf("同一个号重复被限只算一次，实际暂停 %d 个", n)
	}
	// 把 c1 的记录挪到窗口外：再有一个号被限也不够阈值。
	p.mu.Lock()
	p.waveHits["cn"][0].at = time.Now().Add(-16 * time.Minute)
	p.mu.Unlock()
	if n := restrict(p, "c2"); n != 0 {
		t.Fatalf("窗口外的记录不计数，实际暂停 %d 个", n)
	}
	if got := len(p.waveHits["cn"]); got != 1 {
		t.Fatalf("过期记录应被剔除，剩 %d 条", got)
	}
	if n := p.NoteRestriction("ghost"); n != 0 {
		t.Fatalf("不存在的账号不记录，实际暂停 %d 个", n)
	}
}

func TestRestrictionWaveConfig(t *testing.T) {
	p := wavePool(t)
	restrict(p, "c1")
	p.SetRestrictionWave(0, 0, 0)
	if p.waveHits != nil {
		t.Fatalf("关闭时应清空记录: %+v", p.waveHits)
	}
	if n := restrict(p, "c2"); n != 0 {
		t.Fatalf("关闭后不应暂停，实际 %d 个", n)
	}

	p2 := wavePool(t)
	p2.SetRestrictionWave(-1, -time.Minute, 0)
	if p2.waveThreshold != defaultRestrictionWaveThreshold || p2.waveWindow != defaultRestrictionWaveWindow || p2.wavePause != defaultRestrictionWavePause {
		t.Fatalf("非法值应保留默认: %d %s %s", p2.waveThreshold, p2.waveWindow, p2.wavePause)
	}
	p2.SetRestrictionWave(3, time.Minute, time.Hour)
	restrict(p2, "c1")
	if n := restrict(p2, "c2"); n != 0 {
		t.Fatalf("阈值 3 时两个号不够，实际暂停 %d 个", n)
	}
	if n := restrict(p2, "c3"); n != 0 {
		t.Fatalf("国内版号都被禁用了，没有可暂停的号，实际 %d 个", n)
	}
	if st, _ := p2.Status("g1"); st.Cooling {
		t.Fatalf("别的域不受影响: %+v", st)
	}
}

func TestRestrictionWavePauseNotShortened(t *testing.T) {
	p := wavePool(t)
	restrict(p, "c1")
	restrict(p, "c2")
	p.Cooldown("c3", CoolSoft, time.Minute, "upstream 404")
	p.CooldownSoftRate("c3", time.Minute, time.Time{}, "429")
	p.CooldownSoftRate("c3", time.Minute, time.Now().Add(time.Minute), "429 reset")
	p.CooldownSoftForModel("c3", time.Minute, time.Time{}, "m", "429")
	p.ReenableIfCredits("c3", 500, 1000)
	st, _ := p.Status("c3")
	if st.CoolKind != "restriction_wave" || st.CoolRemaining < int64((29*time.Minute).Seconds()) {
		t.Fatalf("暂停期内更短的冷却和余额刷新都不应解除暂停: %+v", st)
	}
	p.Cooldown("c3", CoolHard, 10*time.Hour, "credit exhausted")
	if st, _ := p.Status("c3"); st.CoolKind != "hard_credit" {
		t.Fatalf("更晚截止的冷却照常记: %+v", st)
	}

	p2 := wavePool(t)
	restrict(p2, "c1")
	restrict(p2, "c2")
	if !p2.Revive("c3") {
		t.Fatal("Revive 应找到 c3")
	}
	if st, _ := p2.Status("c3"); st.Cooling {
		t.Fatalf("面板解冻应提前解除暂停: %+v", st)
	}
}
