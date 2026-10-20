package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// TestPanelModelsEnterpriseOnly 模型页：域目录用个人号拉；企业号目录里多出来的模型列在后面，
// 标 enterprise_only、不标默认。
func TestPanelModelsEnterpriseOnly(t *testing.T) {
	var mu sync.Mutex
	var tokens []string
	up := &upstream.Client{
		HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			isV3 := strings.HasSuffix(r.URL.Path, "/v3/config")
			if !isV3 && !strings.HasSuffix(r.URL.Path, "/console/enterprises/ent-1/config/models") {
				return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			}
			enterprise := r.Header.Get("X-User-Id") != "" && r.Header.Get("X-Enterprise-Id") != ""
			if !isV3 {
				// 企业模型目录：data 直接是模型数组，只收 tags 含 chat 的。
				models := `{"id":"shared","tags":["chat"]}`
				if enterprise {
					models += `,{"id":"claude-x","tags":["chat"]}`
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}},
					Body: io.NopCloser(strings.NewReader(`{"code":0,"data":[` + models + `]}`))}, nil
			}
			mu.Lock()
			tokens = append(tokens, r.Header.Get("Authorization"))
			mu.Unlock()
			models, ids := `{"id":"shared","isDefault":true}`, `"shared"`
			if enterprise {
				models, ids = `{"id":"shared"},{"id":"claude-x","isDefault":true}`, `"shared","claude-x"`
			}
			body := `{"code":0,"data":{"models":[` + models + `],"agents":[{"name":"cli","models":[` + ids + `]}]}}`
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})},
		ChatBaseCN:    "https://cn.fake",
		BillingBaseCN: "https://cn.fake",
	}
	p := pool.New("")
	ent := &auth.Auth{UID: "a-ent", EnterpriseID: "ent-1", AccessToken: "at-ent", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
	p.Add(ent)
	p.Add(&auth.Auth{UID: "b-per", AccessToken: "at-per", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"})
	if _, err := up.FetchAccountCatalog(ent); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	tokens = nil
	mu.Unlock()
	pn := New(Config{Version: "test", Pool: p, Upstream: up, AuthDir: t.TempDir()})

	rec := httptest.NewRecorder()
	pn.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/api/models", nil))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	var resp struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]any{}
	for _, m := range resp.Models {
		got[m["id"].(string)] = m
	}
	if m := got["cn:claude-x"]; m == nil || m["enterprise_only"] != true || m["is_default"] != false {
		t.Errorf("企业专属模型应列出、标 enterprise_only、不标默认: %v", m)
	}
	if m := got["cn:shared"]; m == nil || m["enterprise_only"] != nil {
		t.Errorf("普通模型不标 enterprise_only: %v", m)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(tokens) == 0 || tokens[0] != "Bearer at-per" {
		t.Errorf("域目录应用个人号拉，got %v", tokens)
	}
}
