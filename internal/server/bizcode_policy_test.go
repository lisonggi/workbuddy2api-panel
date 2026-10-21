// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// TestChat14003CoolsOnlyThatModel 上游 429 + 14003「模型繁忙」：只冷却这个号的这个模型半分钟，
// 账号级不冷却，换模型照样选它（以前落进通用 429，整个号冷却约 10 分钟）。
func TestChat14003CoolsOnlyThatModel(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		if authz == "Bearer at-bad" {
			return 429, `{"code":14003,"msg":"too many requests","displayMsg":{"zh":"模型繁忙，请换模型或稍后重试"}}`, false
		}
		return 200, sseOK, true
	})
	p := testPoolWith(
		&auth.Auth{UID: "bad", AccessToken: "at-bad", ExpiresAt: 9999999999},
		&auth.Auth{UID: "good", AccessToken: "at-good", ExpiresAt: 9999999999},
	)
	p.SetCredits("bad", 2000, 0)
	p.SetCredits("good", 1000, 0)
	h := NewHandler(Config{Pool: p, Upstream: up})
	if rec := post(h, "/v1/chat/completions", `{"model":"deepseek-v4.1-flash","messages":[]}`); rec.Code != 200 {
		t.Fatalf("code=%d body=%s（应换号成功）", rec.Code, rec.Body)
	}
	st, _ := p.Status("bad")
	if st.Cooling {
		t.Fatalf("14003 不该冷却整个号：%+v", st)
	}
	if len(st.RateLimitedModels) != 1 {
		t.Fatalf("应只有一行模型冷却：%+v", st.RateLimitedModels)
	}
	row := st.RateLimitedModels[0]
	if row.Model != "deepseek-v4.1-flash" || row.Reason != "14003 model busy" || row.Kind != "rate_limit" {
		t.Errorf("台账 %+v", row)
	}
	if rem := time.Until(row.Until); rem < 20*time.Second || rem > 38*time.Second {
		t.Errorf("模型冷却剩余 %v，want 30 秒上下", rem)
	}
	p.SetRandomSource(func(n int64) int64 { return 0 })
	if got := p.PickExcludingForModel(nil, "deepseek-v4.1-flash"); got == nil || got.UID != "good" {
		t.Errorf("同模型应避开 bad，got %+v", got)
	}
	if got := p.PickExcludingForModel(nil, "glm-5.2"); got == nil || got.UID != "bad" {
		t.Errorf("换模型应照样选 bad，got %+v", got)
	}
}

// TestChat14003AllAccountsBusyRetryable 所有号这个模型都「模型繁忙」：回 429 + Retry-After 让客户端
// 稍等重试（不回 400「换个模型」）；冷却期内同模型不再打上游，换模型照常可用。
func TestChat14003AllAccountsBusyRetryable(t *testing.T) {
	var calls int
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		calls++
		return 429, `{"code":14003,"msg":"too many requests"}`, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "a", AccessToken: "at-a", ExpiresAt: 9999999999},
		&auth.Auth{UID: "b", AccessToken: "at-b", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})
	for i := range 2 {
		rec := post(h, "/v1/chat/completions", `{"model":"deepseek-v4.1-flash","messages":[]}`)
		ra, _ := strconv.Atoi(rec.Header().Get("Retry-After"))
		if rec.Code != 429 || !strings.Contains(rec.Body.String(), "rate_limit_exceeded") || ra < 1 || ra > 39 {
			t.Fatalf("第 %d 次：code=%d Retry-After=%q body=%s", i+1, rec.Code, rec.Header().Get("Retry-After"), rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "model_blocked: 2 account(s)") {
			t.Errorf("第 %d 次 hint 应说明两个号都在冷却这个模型：%s", i+1, rec.Body)
		}
	}
	if calls != 2 {
		t.Errorf("上游调用 %d 次，want 2（第二次请求不该再打上游）", calls)
	}
	for _, uid := range []string{"a", "b"} {
		if st, _ := p.Status(uid); st.Cooling {
			t.Errorf("%s 不该整号冷却", uid)
		}
	}
}

// TestApplyErrorPolicyBizCodes 限流细分与账号故障原因：Retry-After 优先、缺模型名退回账号级、
// 10105 账号级短冷却、14015 原因带业务码。
func TestApplyErrorPolicyBizCodes(t *testing.T) {
	newRig := func() (*Handler, func(string) (time.Duration, string, bool)) {
		p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
		h := NewHandler(Config{Pool: p})
		status := func(model string) (time.Duration, string, bool) {
			st, _ := p.Status("u1")
			if model == "" {
				return time.Until(st.Until), st.Reason, st.Cooling
			}
			for _, r := range st.RateLimitedModels {
				if r.Model == model {
					return time.Until(r.Until), r.Reason, st.Cooling
				}
			}
			return 0, "", st.Cooling
		}
		return h, status
	}

	h, status := newRig()
	h.applyErrorPolicy("u1", upstream.ErrSoftRate, `{"code":6002}`, "m", &upstream.Error{RetryAfter: 5 * time.Second})
	if rem, reason, cooling := status("m"); cooling || rem <= 3*time.Second || rem > 5*time.Second || reason != "6002 model rate limit" {
		t.Errorf("Retry-After：剩余 %v reason=%q 账号冷却=%v", rem, reason, cooling)
	}

	h, status = newRig()
	h.applyErrorPolicy("u1", upstream.ErrSoftRate, `{"code":14003}`, "", nil)
	if rem, reason, cooling := status(""); !cooling || rem < 20*time.Second || rem > 38*time.Second || reason != "14003 model busy" {
		t.Errorf("缺模型名：剩余 %v reason=%q 账号冷却=%v", rem, reason, cooling)
	}

	h, status = newRig()
	h.applyErrorPolicy("u1", upstream.ErrSoftRate, `{"code":10105,"msg":"conversation limit exceeded"}`, "m", nil)
	if rem, reason, cooling := status(""); !cooling || rem < 40*time.Second || rem > 76*time.Second || reason != "10105 too many active sessions" {
		t.Errorf("10105：剩余 %v reason=%q 账号冷却=%v", rem, reason, cooling)
	}
	h, status = newRig()
	h.applyErrorPolicy("u1", upstream.ErrSoftRate, `{"code":10105}`, "m", &upstream.Error{RetryAfter: 3 * time.Second})
	if rem, _, cooling := status(""); !cooling || rem > 3*time.Second {
		t.Errorf("10105 + Retry-After：剩余 %v 账号冷却=%v", rem, cooling)
	}

	// 14003 带重置文案：对齐到重置时间，仍只落在模型上
	h, status = newRig()
	reset := time.Now().Add(3 * time.Minute)
	ts := reset.In(upstream.SoftRateResetLoc()).Format("2006-01-02 15:04:05")
	h.applyErrorPolicy("u1", upstream.ErrSoftRate, `{"code":14003,"msg":"将在 `+ts+` UTC+8 重置"}`, "m", nil)
	if rem, reason, cooling := status("m"); cooling || (rem-time.Until(reset)).Abs() > time.Second || reason != "14003 model busy" {
		t.Errorf("14003 + 重置文案：剩余 %v reason=%q 账号冷却=%v", rem, reason, cooling)
	}

	h, status = newRig()
	h.applyErrorPolicy("u1", upstream.ErrAccountFault, `{"code":14015,"msg":"license expired"}`, "m", nil)
	if _, reason, cooling := status(""); !cooling || reason != "account fault (14015 license expired)" {
		t.Errorf("14015：reason=%q 账号冷却=%v", reason, cooling)
	}
}
