// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// turn_ids_test.go X-Conversation-Request-ID 按对话轮分配：客户端没给会话 id 时，
// 逐字相同的两个开轮请求拿到不同的 ID，各自的工具调用续轮找回自己那一轮的 ID。
package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// turnUpstream 假上游：每次 chat 记下 X-Conversation-Request-ID，回一个带工具调用的流，
// 工具调用 id 按调用次序编号（call_1、call_2…），和真实上游一样每次回复各不相同。
type turnUpstream struct {
	mu     sync.Mutex
	reqIDs []string
}

func (u *turnUpstream) client() *upstream.Client {
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			u.mu.Lock()
			u.reqIDs = append(u.reqIDs, r.Header.Get("X-Conversation-Request-ID"))
			n := len(u.reqIDs)
			u.mu.Unlock()
			body := fmt.Sprintf(
				"data: {\"id\":\"chatcmpl-%d\",\"object\":\"chat.completion.chunk\",\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_%d\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":\"{}\"}}]}}]}\n\n"+
					"data: {\"id\":\"chatcmpl-%d\",\"object\":\"chat.completion.chunk\",\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n"+
					"data: [DONE]\n\n", n, n, n)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
}

func (u *turnUpstream) last() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.reqIDs[len(u.reqIDs)-1]
}

func TestChatTurnIDsForIdenticalOpeners(t *testing.T) {
	fake := &turnUpstream{}
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at-1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: fake.client(), SoftCooldown: time.Minute})
	send := func(body string) (string, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
		}
		return fake.last(), rec.Body.String()
	}
	const head = `"model":"glm-5.2","messages":[{"role":"system","content":"s"},{"role":"user","content":"你好"}`
	opener := func(extra string) string { return `{` + head + `]` + extra + `}` }
	step := func(callID, extra string) string {
		return `{` + head + `,{"role":"assistant","content":null,"tool_calls":[{"id":"` + callID +
			`","type":"function","function":{"name":"bash","arguments":"{}"}}]},{"role":"tool","tool_call_id":"` + callID + `","content":"ok"}]` + extra + `}`
	}

	for _, mode := range []struct{ name, extra string }{
		{"流式", `,"stream":true`},
		{"非流式", ``},
	} {
		t.Run(mode.name, func(t *testing.T) {
			idA, outA := send(opener(mode.extra))
			idB, outB := send(opener(mode.extra))
			if idA == "" || idA == idB {
				t.Fatalf("逐字相同的两个开轮请求应拿到不同的轮 ID: %q %q", idA, idB)
			}
			callA, callB := callIDIn(t, outA), callIDIn(t, outB)
			if got, _ := send(step(callA, mode.extra)); got != idA {
				t.Errorf("A 的续轮应沿用 A 的轮 ID: got %q want %q", got, idA)
			}
			if got, _ := send(step(callB, mode.extra)); got != idB {
				t.Errorf("B 的续轮应沿用 B 的轮 ID: got %q want %q", got, idB)
			}
		})
	}

	// 客户端给了会话 id：会话 id 已区分对话，沿用纯派生（同一会话里重发同一句得到同一 ID）。
	meta := `,"metadata":{"conversation_id":"conv-1"}`
	c1, _ := send(opener(meta))
	c2, _ := send(opener(meta))
	if c1 != c2 {
		t.Errorf("带会话 id 时轮 ID 按会话 id + 轮级键派生: %q %q", c1, c2)
	}
	if got, _ := send(step("call_x", meta)); got != c1 {
		t.Errorf("带会话 id 的续轮应与开轮同 ID: got %q want %q", got, c1)
	}
}

// callIDIn 从回给客户端的响应（SSE 或 JSON）里取第一个工具调用 id。
func callIDIn(t *testing.T, out string) string {
	t.Helper()
	i := strings.Index(out, `"id":"call_`)
	if i < 0 {
		t.Fatalf("响应里没有工具调用 id: %s", out)
	}
	rest := out[i+len(`"id":"`):]
	return rest[:strings.IndexByte(rest, '"')]
}
