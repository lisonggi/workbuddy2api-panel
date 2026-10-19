package pool

import (
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestNoteFailuresBelowThreshold 连续失败未到阈值不降级，只累计计数并置 dirty。
func TestNoteFailuresBelowThreshold(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	for i := 0; i < defaultDegradeThreshold-1; i++ {
		p.NoteFailures("u1")
	}
	e := p.byUID["u1"]
	if e.consecutiveFails != defaultDegradeThreshold-1 {
		t.Errorf("consecutiveFails = %d，应为 %d", e.consecutiveFails, defaultDegradeThreshold-1)
	}
	if !e.degradeUntil.IsZero() {
		t.Error("未到阈值不应降级")
	}
	if !p.dirty.Load() {
		t.Error("计数变更应置 dirty")
	}
}

// TestNoteFailuresAtThreshold 达到阈值触发降权：计数清零、截止 = now + 配置时长。
func TestNoteFailuresAtThreshold(t *testing.T) {
	p := New("")
	p.SetDegrade(3, 10*time.Minute, time.Hour)
	p.Add(&auth.Auth{UID: "u1"})
	before := time.Now()
	for i := 0; i < 3; i++ {
		p.NoteFailures("u1")
	}
	e := p.byUID["u1"]
	if e.consecutiveFails != 0 {
		t.Errorf("达阈后计数应清零，got %d", e.consecutiveFails)
	}
	if e.degradeUntil.Before(before.Add(9*time.Minute)) || e.degradeUntil.After(time.Now().Add(11*time.Minute)) {
		t.Errorf("degradeUntil = %v，应约为 now+10m", e.degradeUntil)
	}
}

// TestNoteFailuresDuringDegradeDoesNotExtend 降权期内再次达阈不延长截止（防重试堆厚）。
func TestNoteFailuresDuringDegradeDoesNotExtend(t *testing.T) {
	p := New("")
	p.SetDegrade(2, 10*time.Minute, time.Hour)
	p.Add(&auth.Auth{UID: "u1"})
	p.NoteFailures("u1")
	p.NoteFailures("u1")
	first := p.byUID["u1"].degradeUntil
	if first.IsZero() {
		t.Fatal("应已降权")
	}
	// 期内再来一轮：第二次达阈时只清零，不延长。
	for i := 0; i < 2; i++ {
		p.NoteFailures("u1")
	}
	e := p.byUID["u1"]
	if !e.degradeUntil.Equal(first) {
		t.Errorf("降权期内不应延长: %v → %v", first, e.degradeUntil)
	}
	if e.consecutiveFails != 0 {
		t.Errorf("再次达阈后计数应清零，got %d", e.consecutiveFails)
	}
}

// TestNoteFailuresAfterDegradeExpiry 降权到期后再达阈重新降权。
func TestNoteFailuresAfterDegradeExpiry(t *testing.T) {
	p := New("")
	p.SetDegrade(1, 10*time.Minute, time.Hour)
	p.Add(&auth.Auth{UID: "u1"})
	p.NoteFailures("u1")
	e := p.byUID["u1"]
	e.degradeUntil = time.Now().Add(-time.Minute) // 已过期
	p.NoteFailures("u1")
	if !p.byUID["u1"].degradeUntil.After(time.Now()) {
		t.Errorf("过期后再达阈应重新降权，got %v", p.byUID["u1"].degradeUntil)
	}
}

// TestNoteSuccessClearsDegrade 成功清零连败计数与降权截止。
func TestNoteSuccessClearsDegrade(t *testing.T) {
	p := New("")
	p.SetDegrade(1, 10*time.Minute, time.Hour)
	p.Add(&auth.Auth{UID: "u1"})
	p.NoteFailures("u1")
	if p.byUID["u1"].degradeUntil.IsZero() {
		t.Fatal("应已降权")
	}
	p.NoteSuccess("u1")
	e := p.byUID["u1"]
	if e.consecutiveFails != 0 || !e.degradeUntil.IsZero() {
		t.Errorf("成功应清零降权状态: fails=%d until=%v", e.consecutiveFails, e.degradeUntil)
	}
	if e.successCount != 1 {
		t.Errorf("successCount = %d", e.successCount)
	}
}

// TestNoteFailuresUnknownUID 不存在的 uid 不 panic、不改 dirty。
func TestNoteFailuresUnknownUID(t *testing.T) {
	p := New("")
	p.NoteFailures("missing") // 不应 panic
	if p.dirty.Load() {
		t.Error("未知 uid 不应置 dirty")
	}
	if len(p.byUID) != 0 {
		t.Errorf("不应创建条目: %v", p.byUID)
	}
}

// TestDegradeDurationLocked 时长取配置值，非法/未配置回落默认，超出封顶取 max。
func TestDegradeDurationLocked(t *testing.T) {
	cases := []struct {
		name     string
		cooldown time.Duration
		max      time.Duration
		want     time.Duration
	}{
		{"配置值在范围内", 10 * time.Minute, time.Hour, 10 * time.Minute},
		{"超过封顶取 max", 2 * time.Hour, time.Hour, time.Hour},
		{"cooldown 为 0 回落默认", 0, time.Hour, defaultDegradeCooldown},
		{"cooldown 为负回落默认", -time.Minute, 0, defaultDegradeCooldown},
		{"max 为 0 回落默认封顶", 10 * time.Minute, 0, 10 * time.Minute},
		{"配置超默认封顶", 5 * time.Hour, 0, defaultDegradeCooldownMax},
		{"两者都为 0 用默认", 0, 0, defaultDegradeCooldown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := New("")
			p.degradeCooldown = tc.cooldown
			p.degradeCooldownMax = tc.max
			if got := p.degradeDurationLocked(); got != tc.want {
				t.Errorf("degradeDurationLocked = %v，应为 %v", got, tc.want)
			}
		})
	}
}
