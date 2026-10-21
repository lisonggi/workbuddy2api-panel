// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

// backoff_test.go 轮转退避与抖动：基数、指数增长、封顶、抖动区间与可取消等待。

import (
	"context"
	"math"
	"testing"
	"time"
)

// jitterDur 对正时长施加 ±25% 抖动；非正时长原样返回。
func TestJitterDur(t *testing.T) {
	if got := jitterDur(0); got != 0 {
		t.Fatalf("jitterDur(0)=%v want 0", got)
	}
	if got := jitterDur(-time.Second); got != -time.Second {
		t.Fatalf("非正时长应原样返回，got %v", got)
	}
	d := time.Second
	for i := 0; i < 200; i++ {
		got := jitterDur(d)
		if got < 750*time.Millisecond || got > 1250*time.Millisecond {
			t.Fatalf("抖动越界: %v（应落在 [750ms,1250ms]）", got)
		}
	}
	// 接近上限的时长往上抖会超出 int64，转换结果在 amd64 上是负数：应兜底成 0，不返回负等待。
	// 每次往上抖的概率约一半，200 次里几乎必然碰到。
	for i := 0; i < 200; i++ {
		if got := jitterDur(math.MaxInt64); got < 0 {
			t.Fatalf("超大时长抖动后不应为负: %v", got)
		}
	}
}

// backoffAfter：base=0 不等待；按 base·2^n 增长并以 ±25% 抖动；封顶 8s。
func TestBackoffAfter(t *testing.T) {
	old := rotateBackoffBase
	t.Cleanup(func() { rotateBackoffBase = old })

	rotateBackoffBase = 0
	if got := backoffAfter(5); got != 0 {
		t.Fatalf("base=0 应恒 0，got %v", got)
	}

	rotateBackoffBase = 500 * time.Millisecond
	cases := []struct {
		n    int
		lo   time.Duration
		hi   time.Duration
		name string
	}{
		{0, 375 * time.Millisecond, 625 * time.Millisecond, "首次"},
		{1, 750 * time.Millisecond, 1250 * time.Millisecond, "第二次"},
		{2, 1500 * time.Millisecond, 2500 * time.Millisecond, "第三次"},
		{10, 6 * time.Second, 10 * time.Second, "封顶"},
	}
	for _, tc := range cases {
		for i := 0; i < 50; i++ {
			got := backoffAfter(tc.n)
			if got < tc.lo || got > tc.hi {
				t.Fatalf("%s(n=%d)=%v，应在 [%v,%v]", tc.name, tc.n, got, tc.lo, tc.hi)
			}
		}
	}

	// 极端配置：基数本身就超过封顶 → 直接夹到 8s（再抖动）。
	rotateBackoffBase = 20 * time.Second
	for i := 0; i < 50; i++ {
		if got := backoffAfter(0); got < 6*time.Second || got > 10*time.Second {
			t.Fatalf("基数超过封顶时应夹到 8s±25%%，got %v", got)
		}
	}
}

// sleepCtx：d<=0 立即放行（ctx 已取消则 false）；正时长可被 ctx 取消打断。
func TestSleepCtx(t *testing.T) {
	if !sleepCtx(context.Background(), 0) {
		t.Fatal("d<=0 且 ctx 正常应返回 true")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepCtx(canceled, 0) {
		t.Fatal("d<=0 但 ctx 已取消应返回 false")
	}
	if sleepCtx(canceled, time.Hour) {
		t.Fatal("ctx 已取消应立即返回 false")
	}
	if !sleepCtx(context.Background(), time.Millisecond) {
		t.Fatal("等满应返回 true")
	}
}
