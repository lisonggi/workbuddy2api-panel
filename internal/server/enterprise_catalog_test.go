// enterprise_catalog_test.go 企业号按自己的目录接模型：企业专属模型只派给目录里有它的企业号，
// 个人号照常接普通模型；/v1/models 列出企业专属模型并标 enterprise_only。
package server

import (
	"encoding/json"
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

// enterpriseUpstream 假上游：目录按请求里的企业身份下发（X-User-Id + X-Enterprise-Id 都带才给企业名单），
// 记录每次 chat 用的是哪个号的 token。
type enterpriseUpstream struct {
	mu    sync.Mutex
	chats []string // 每次 chat 的 Authorization
}

func (u *enterpriseUpstream) client() *upstream.Client {
	catalog := func(ids ...string) string {
		ms := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			ms = append(ms, map[string]any{"id": id})
		}
		b, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{
			"models": ms, "agents": []map[string]any{{"name": "cli", "models": ids}},
		}})
		return string(b)
	}
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body, ct := "", "application/json"
			switch {
			case strings.HasSuffix(r.URL.Path, "/chat/completions"):
				u.mu.Lock()
				u.chats = append(u.chats, r.Header.Get("Authorization"))
				u.mu.Unlock()
				body, ct = sseOK, "text/event-stream"
			case r.Header.Get("X-User-Id") != "" && r.Header.Get("X-Enterprise-Id") != "":
				body = catalog("shared", "claude-x")
			default:
				body = catalog("shared", "personal-only")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{ct}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})},
		ChatBaseCN:    "https://cn.fake",
		BillingBaseCN: "https://cn.fake",
	}
}

func (u *enterpriseUpstream) lastChat() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.chats) == 0 {
		return ""
	}
	return u.chats[len(u.chats)-1]
}

func TestEnterpriseOnlyModelRouting(t *testing.T) {
	dynamicModelsCache.Lock()
	saved := struct {
		ids             []upstream.ModelInfo
		fetched, failed time.Time
	}{dynamicModelsCache.ids, dynamicModelsCache.fetched, dynamicModelsCache.lastFail}
	dynamicModelsCache.ids, dynamicModelsCache.fetched, dynamicModelsCache.lastFail = nil, time.Time{}, time.Time{}
	dynamicModelsCache.Unlock()
	t.Cleanup(func() {
		dynamicModelsCache.Lock()
		dynamicModelsCache.ids, dynamicModelsCache.fetched, dynamicModelsCache.lastFail = saved.ids, saved.fetched, saved.failed
		dynamicModelsCache.Unlock()
	})

	per := &auth.Auth{UID: "per1", AccessToken: "at-per", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
	ent := &auth.Auth{UID: "ent1", EnterpriseID: "ent-1", AccessToken: "at-ent", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
	p := testPoolWith(per, ent)
	// 个人号积分更高：不按目录过滤时普通选号会挑它
	p.SetCredits("per1", 1000, 0)
	p.SetCredits("ent1", 100, 0)
	fake := &enterpriseUpstream{}
	up := fake.client()
	p.SetModelServes(up.ServesModel)
	// 启动时的两步：个人号拉域目录，企业号单独拉自己的目录
	if _, err := up.FetchModels(per); err != nil {
		t.Fatalf("域目录: %v", err)
	}
	if _, err := up.FetchAccountCatalog(ent); err != nil {
		t.Fatalf("企业号目录: %v", err)
	}
	h := NewHandler(Config{Pool: p, Upstream: up})

	send := func(model string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
			`{"model":"`+model+`","messages":[{"role":"user","content":"hi `+model+`"}],"stream":true}`)))
		if rec.Code != 200 {
			t.Fatalf("%s: code=%d body=%s", model, rec.Code, rec.Body)
		}
		return fake.lastChat()
	}
	// 顺序有讲究：刚选过的号会被防惊群短暂跳过（100ms）。企业专属模型放第一个发，
	// 此时没有刚用过的号、个人号积分又高，不按目录过滤就会落到个人号；
	// personal-only 放在个人号刚被用过之后，不按目录过滤就会被挤到企业号。
	if got := send("claude-x"); got != "Bearer at-ent" {
		t.Errorf("企业专属模型应派给企业号，got %q", got)
	}
	if got := send("shared"); got != "Bearer at-per" {
		t.Errorf("两边都有的模型照常可派给个人号，got %q", got)
	}
	if got := send("personal-only"); got != "Bearer at-per" {
		t.Errorf("企业号目录里没有的模型不派给企业号，got %q", got)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	flags := map[string]bool{}
	for _, m := range resp.Data {
		eo, _ := m["enterprise_only"].(bool)
		flags[m["id"].(string)] = eo
	}
	if eo, ok := flags["cn:claude-x"]; !ok || !eo {
		t.Errorf("/v1/models 应列出 cn:claude-x 并标 enterprise_only，got %v", flags)
	}
	if eo, ok := flags["cn:shared"]; !ok || eo {
		t.Errorf("普通模型不标 enterprise_only，got %v", flags)
	}
}
