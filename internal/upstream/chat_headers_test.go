// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestChatHeadersOfficialShape chat 出站头对齐官方 CLI 的对话请求（10-19 生产逐项实测）：
// 该带的都带，官方对话请求里没有的一个都不带；企业号加 X-Enterprise-Id 与同值的 X-Tenant-Id。
func TestChatHeadersOfficialShape(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	cnEnt := cnAuth()
	cnEnt.EnterpriseID = "corp-cn"
	glEnt := globalAuth()
	glEnt.EnterpriseID = "corp-g"
	cases := []struct {
		name   string
		a      *auth.Auth
		domain string
		ent    string
	}{
		{"国内版个人号", cnAuth(), "www.codebuddy.cn", ""},
		{"国内版企业号", cnEnt, "www.codebuddy.cn", "corp-cn"},
		{"国内版没有登录域", &auth.Auth{UID: "u-cn", AccessToken: "tok"}, "", ""},
		{"国际版个人号", globalAuth(), "www.workbuddy.ai", ""},
		{"国际版企业号", glEnt, "www.workbuddy.ai", "corp-g"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, "https://x/v2/chat/completions", nil)
			(&Client{}).ChatHeaders(req, tc.a, "", ChatMeta{})
			want := map[string]string{
				"Content-Type":     "application/json",
				"Accept":           "application/json, text/event-stream",
				"X-Requested-With": "XMLHttpRequest",
				"Authorization":    "Bearer tok",
				"X-User-Id":        tc.a.UID,
				"X-Domain":         tc.domain,
				"X-Enterprise-Id":  tc.ent,
				"X-Tenant-Id":      tc.ent,
				"X-Agent-Intent":   "craft",
				"X-Agent-Type":     "main",
				"X-Agent-Purpose":  "conversation",
				"X-Private-Data":   "true",
				"X-Product":        "SaaS",
			}
			for k, v := range want {
				got, present := req.Header.Get(k), len(req.Header.Values(k)) > 0
				if got != v || present != (v != "") {
					t.Errorf("%s = %q（发了=%v），want %q", k, got, present, v)
				}
			}
			for _, k := range []string{
				"Origin", "Referer", "Accept-Language", "X-CodeBuddy-Request", "X-Machine-ID", "X-Session-ID",
				"X-No-Enterprise-Id", "X-No-Department-Info", "X-No-User-Id", "X-No-Authorization", "X-Root-Request-ID",
				"X-Refresh-Token", "X-Expert-Id",
			} {
				if v := req.Header.Values(k); len(v) > 0 {
					t.Errorf("官方对话请求不带 %s，实际 %q", k, v)
				}
			}
			if req.Header.Get("User-Agent") == "" {
				t.Error("User-Agent 必发")
			}
		})
	}
}

// TestChatHeadersPrivateData X-Private-Data：默认一律 "true"（同官方桌面端默认）；打开 model_optimization 后
// 个人号 "false"、企业号仍 "true"。
func TestChatHeadersPrivateData(t *testing.T) {
	ent := cnAuth()
	ent.EnterpriseID = "corp-cn"
	for _, tc := range []struct {
		name string
		opt  bool
		a    *auth.Auth
		want string
	}{
		{"默认·个人号", false, cnAuth(), "true"},
		{"默认·企业号", false, ent, "true"},
		{"打开·个人号", true, cnAuth(), "false"},
		{"打开·企业号", true, ent, "true"},
	} {
		c := &Client{}
		c.ModelOptimization.Store(tc.opt)
		req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
		c.ChatHeaders(req, tc.a, "", ChatMeta{})
		if got := req.Header.Get("X-Private-Data"); got != tc.want {
			t.Errorf("%s: X-Private-Data = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := (*Client)(nil).privateDataValue(cnAuth()); got != "true" {
		t.Errorf("nil Client 应按默认发 true: %q", got)
	}
}

// TestChatHeadersExpertID 专家对话由 ChatMeta.ExpertID 带 X-Expert-Id；普通对话（空）
// 不带这个头（官方专家插件只在激活时追加）。
func TestChatHeadersExpertID(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	(&Client{}).ChatHeaders(req, cnAuth(), "", ChatMeta{ExpertID: "ex_1"})
	if got := req.Header.Get("X-Expert-Id"); got != "ex_1" {
		t.Errorf("X-Expert-Id = %q，应为 ex_1", got)
	}
	plain, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	(&Client{}).ChatHeaders(plain, cnAuth(), "", ChatMeta{})
	if v := plain.Header.Values("X-Expert-Id"); len(v) > 0 {
		t.Errorf("ExpertID 为空时不应带 X-Expert-Id，实际 %q", v)
	}
	if v := req.Header.Values("X-Expert-Team-Task"); len(v) > 0 {
		t.Errorf("单个专家不应带 X-Expert-Team-Task，实际 %q", v)
	}
	// X-Expert-Team-Task 只和 X-Expert-Id 一起发。
	orphan, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	(&Client{}).ChatHeaders(orphan, cnAuth(), "", ChatMeta{ExpertTeamTask: true})
	if v := orphan.Header.Values("X-Expert-Team-Task"); len(v) > 0 {
		t.Errorf("没有专家 id 时不应带 X-Expert-Team-Task，实际 %q", v)
	}
}

// TestChatHeadersTraceContext 官方同发 W3C traceparent 与单行 b3，和 X-B3-* 用同一组 trace/span id；
// 16 位的 B3 TraceId 进 traceparent 时左补零到 32 位。
func TestChatHeadersTraceContext(t *testing.T) {
	traceparent := regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)
	for _, tc := range []struct {
		name, turn, w3c string
	}{
		{"32 位轮次 id", "0123456789ABCDEF0123456789abcdef", "0123456789abcdef0123456789abcdef"},
		{"16 位轮次 id", "0123456789abcdef", "00000000000000000123456789abcdef"},
	} {
		req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
		(&Client{}).ChatHeaders(req, cnAuth(), "", ChatMeta{ConversationRequestID: tc.turn})
		span := req.Header.Get("X-B3-SpanId")
		tp := req.Header.Get("traceparent")
		if !traceparent.MatchString(tp) || tp != "00-"+tc.w3c+"-"+span+"-01" {
			t.Errorf("%s: traceparent = %q（span %q）", tc.name, tp, span)
		}
		if got, want := req.Header.Get("b3"), tc.w3c[32-len(tc.turn):]+"-"+span+"-1"; got != want {
			t.Errorf("%s: b3 = %q, want %q", tc.name, got, want)
		}
		if req.Header.Get("X-B3-TraceId") != tc.turn {
			t.Errorf("%s: X-B3-TraceId = %q", tc.name, req.Header.Get("X-B3-TraceId"))
		}
	}
}
