package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
)

// globalChatUpstream 只有国际版的假上游：记下每次 chat 请求的会话头，chat 回 chatStatus/chatBody。
type globalChatUpstream struct {
	mu         sync.Mutex
	convIDs    []string
	turnIDs    []string
	chatStatus int
	chatBody   string
}

func (u *globalChatUpstream) client() *upstream.Client {
	catalog, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{
		"models": []map[string]any{{"id": "m-g"}}, "agents": []map[string]any{{"name": "cli", "models": []string{"m-g"}}},
	}})
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(catalog)))}, nil
			}
			u.mu.Lock()
			u.convIDs = append(u.convIDs, r.Header.Get("X-Conversation-ID"))
			u.turnIDs = append(u.turnIDs, r.Header.Get("X-Conversation-Request-ID"))
			status, body := u.chatStatus, u.chatBody
			u.mu.Unlock()
			if status == 0 {
				status, body = 200, sseOK
			}
			ct := "text/event-stream"
			if status != 200 {
				ct = "application/json"
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{ct}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})},
		ChatBaseGlobal:    "https://global.fake",
		BillingBaseGlobal: "https://global.fake",
		GlobalEnabled:     true,
	}
}

func newGlobalChatHandler(t *testing.T, fake *globalChatUpstream) (*Handler, *auth.Auth) {
	t.Helper()
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	gl := &auth.Auth{UID: "g1", AccessToken: "at-global", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
	p := testPoolWith(gl)
	up := fake.client()
	_, _ = up.FetchModels(gl)
	return NewHandler(Config{Pool: p, Upstream: up, GlobalEnabled: true, PromptMode: "passthrough"}), gl
}

// TestChatGlobalSendsConversationID 国际版：客户端没给 conversation_id 时也要带 X-Conversation-ID（值是本轮的
// 轮次 id）。开头相同的两段对话各是各的，不会撞成同一个会话；客户端给了就原样透传。
func TestChatGlobalSendsConversationID(t *testing.T) {
	fake := &globalChatUpstream{}
	h, _ := newGlobalChatHandler(t, fake)
	for _, body := range []string{
		`{"model":"global:m-g","messages":[{"role":"user","content":"你好"}]}`,
		`{"model":"global:m-g","messages":[{"role":"user","content":"你好"}]}`,
		`{"model":"global:m-g","messages":[{"role":"user","content":"你好"}],"metadata":{"conversation_id":"conv-client"}}`,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.convIDs) != 3 {
		t.Fatalf("want 3 chat calls, got %d", len(fake.convIDs))
	}
	for i := 0; i < 2; i++ {
		if !upstreamtest.UUIDv7.MatchString(fake.convIDs[i]) || !upstreamtest.UUIDv7Hex.MatchString(fake.turnIDs[i]) {
			t.Fatalf("第 %d 次：X-Conversation-ID = %q、轮次 id = %q，应是官方新会话的形状", i+1, fake.convIDs[i], fake.turnIDs[i])
		}
	}
	if fake.convIDs[0] == fake.convIDs[1] {
		t.Fatalf("开头相同的两段对话不应共用会话 id：%q", fake.convIDs[0])
	}
	if fake.convIDs[2] != "conv-client" {
		t.Fatalf("客户端的 conversation_id 应原样透传: got %q", fake.convIDs[2])
	}
}

// TestChatGlobal11128DoesNotDisable 上游 400 code=11128 "request illegal" 是请求被拦截，和账号无关：
// 回客户端 400，账号不禁用。以前它被当成 11140 封号，一条请求就把所有国际版号禁用了。
func TestChatGlobal11128DoesNotDisable(t *testing.T) {
	fake := &globalChatUpstream{
		chatStatus: 400,
		chatBody:   `{"code":11128,"msg":"request illegal","displayMsg":{"en":"Request blocked. Please send it again"}}`,
	}
	h, gl := newGlobalChatHandler(t, fake)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"global:m-g","messages":[{"role":"user","content":"你好"}]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	st, ok := h.cfg.Pool.Status(gl.UID)
	if !ok || st.Disabled || st.Cooling {
		t.Fatalf("11128 不应禁用或冷却账号: ok=%v %+v", ok, st)
	}
}
