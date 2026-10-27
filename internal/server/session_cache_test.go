// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// session_cache_test.go issue #2：换号时先回到这段对话用过的号；未命中提示按对话对照上一轮。
package server

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
)

// TestChatRetryReturnsToWarmAccount 粘性号失败换号时，先回到这段对话用过的其他号（上面留着前缀缓存），
// 不在全池里随便挑。四段对话各自用过不同的号，随机轮换碰巧全对的概率很小。
func TestChatRetryReturnsToWarmAccount(t *testing.T) {
	uids := []string{"u1", "u2", "u3", "u4", "u5", "u6"}
	var auths []*auth.Auth
	for _, u := range uids {
		auths = append(auths, &auth.Auth{UID: u, AccessToken: "at-" + u, ExpiresAt: 9999999999})
	}
	p := testPoolWith(auths...)
	sess := session.New(session.Config{TTL: time.Minute, Available: func() []string { return uids }})
	var mu sync.Mutex
	var order []string
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		mu.Lock()
		order = append(order, strings.TrimPrefix(authz, "Bearer at-"))
		mu.Unlock()
		if authz == "Bearer at-u1" {
			return 500, `{"code":500}`, false
		}
		return 200, sseOK, true
	})
	h := NewHandler(Config{Pool: p, Upstream: up, Session: sess, SoftCooldown: time.Minute})
	for i, warm := range []string{"u2", "u3", "u4", "u5"} {
		key := fmt.Sprintf("conv-warm-%d", i)
		sess.Bind(key, warm) // 这段对话以前在 warm 上成功过
		sess.Bind(key, "u1") // 最近一次在 u1
		mu.Lock()
		order = nil
		mu.Unlock()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
			`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"metadata":{"conversation_id":"`+key+`"}}`)))
		if rec.Code != 200 {
			t.Fatalf("%s：code=%d body=%s", key, rec.Code, rec.Body)
		}
		mu.Lock()
		got := append([]string(nil), order...)
		mu.Unlock()
		first := ""
		for _, u := range got {
			if u != "u1" {
				first = u
				break
			}
		}
		if first != warm {
			t.Fatalf("%s：u1 用不了以后应先回到用过的 %s，实际尝试顺序 %v", key, warm, got)
		}
		if w := sess.WarmUIDs(key); len(w) == 0 || w[0] != warm {
			t.Fatalf("%s：成功的号应排到用过的号最前面: %v", key, w)
		}
	}
}

// TestChatCacheMissNotesConversation 未命中提示拿到的是这段对话的会话键、实际用的号和出站会话 id：
// 同一段对话第一轮命中、第二轮整段未命中，号和会话 id 都没变 → 记 WARN（流式、非流式同口径）。
func TestChatCacheMissNotesConversation(t *testing.T) {
	sseWithHit := func(hit int) string {
		return "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"好\"}}]}\n\n" +
			fmt.Sprintf("data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5000,\"completion_tokens\":1,\"total_tokens\":5001,\"prompt_cache_hit_tokens\":%d}}\n\n", hit) +
			"data: [DONE]\n\n"
	}
	for _, mode := range []struct{ name, extra string }{{"流式", `,"stream":true`}, {"非流式", ``}} {
		t.Run(mode.name, func(t *testing.T) {
			buf, _ := captureCacheLog(t)
			var mu sync.Mutex
			hit := 4000
			up := newFakeUpstream(t, func(string) (int, string, bool) {
				mu.Lock()
				defer mu.Unlock()
				return 200, sseWithHit(hit), true
			})
			p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at-1", ExpiresAt: 9999999999, Nickname: "甲"})
			h := NewHandler(Config{Pool: p, Upstream: up, SoftCooldown: time.Minute})
			head := `{"model":"glm-5.2","messages":[{"role":"system","content":"s"},{"role":"user","content":"` + t.Name() + `"}`
			for i, body := range []string{
				head + `]` + mode.extra + `}`,
				head + `,{"role":"assistant","content":"好"},{"role":"user","content":"继续"}]` + mode.extra + `}`,
			} {
				if i == 1 {
					mu.Lock()
					hit = 0
					mu.Unlock()
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
				if rec.Code != 200 {
					t.Fatalf("第 %d 次：code=%d body=%s", i+1, rec.Code, rec.Body)
				}
			}
			out := buf.String()
			if !strings.Contains(out, "WARN: [server] cache miss: model=glm-5.2 acct=") || !strings.Contains(out, "同一个号、同一个会话 id 这一轮大前缀仍未命中（prompt=5000 miss=5000") {
				t.Fatalf("应记一行同号同会话 id 的 WARN，日志: %q", out)
			}
		})
	}
}
