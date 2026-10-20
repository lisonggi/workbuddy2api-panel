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
				"X-Refresh-Token",
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
