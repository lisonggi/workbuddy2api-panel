// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// loginRealmRecorder 记录登录请求的完整 URL（方法 + 地址 + 查询参数）与头，按路径回假响应：
// auth/state 回 state+authUrl，auth/token 回业务码 11217（等待中），别的路径按成功信封回。
type loginRealmRecorder struct {
	mu      sync.Mutex
	urls    []string
	headers []http.Header
}

func (r *loginRealmRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.urls = append(r.urls, req.Method+" "+req.URL.String())
	r.headers = append(r.headers, req.Header.Clone())
	r.mu.Unlock()
	body := `{"code":0,"data":{"state":"st","authUrl":"https://auth.example/x"}}`
	if strings.HasSuffix(req.URL.Path, "/auth/token") {
		body = `{"code":11217,"msg":"login ing"}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

// TestLoginRealmRoutes 面板登录按 realm 选域：global → workbuddy.ai、platform=workbuddy-ai；
// cn/空/非法值 → workbuddy.cn、platform=workbuddy。发起授权与轮询令牌的完整 URL 都断言。
func TestLoginRealmRoutes(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantRealm string
		host      string
		platform  string
	}{
		{"cn", `{}`, "cn", "www.workbuddy.cn", "workbuddy"},
		{"global", `{"realm":"global"}`, "global", "www.workbuddy.ai", "workbuddy-ai"},
		{"非法 realm 回落 cn", `{"realm":"weird"}`, "cn", "www.workbuddy.cn", "workbuddy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := &loginRealmRecorder{}
			p, _, _ := newLoginPanel(t)
			p.cfg.Upstream.HTTP = &http.Client{Transport: rec}
			resp := loginStartOK(t, p, c.body)
			if fmt.Sprint(resp["realm"]) != c.wantRealm {
				t.Fatalf("realm=%v want %s", resp["realm"], c.wantRealm)
			}
			wantStateURL := "POST https://" + c.host + "/v2/plugin/auth/state?platform=" + c.platform
			if len(rec.urls) != 1 || rec.urls[0] != wantStateURL {
				t.Fatalf("发起授权 URL = %v，want %s", rec.urls, wantStateURL)
			}
			state := fmt.Sprint(resp["state"])
			prec := loginPollState(p, state)
			autoWantCode(t, prec, http.StatusOK)
			if got := autoDecode(t, prec); got["done"] != false {
				t.Fatalf("11217 应 done=false: %v", got)
			}
			wantTokenURL := "GET https://" + c.host + "/v2/plugin/auth/token?state=" + state
			if len(rec.urls) != 2 || rec.urls[1] != wantTokenURL {
				t.Fatalf("轮询令牌 URL = %v，want %s", rec.urls, wantTokenURL)
			}
			// 面板登录请求不带旧 CLI 的 Origin/Referer。
			for _, h := range rec.headers {
				if h.Get("Origin") != "" || h.Get("Referer") != "" {
					t.Fatalf("登录请求不应带 Origin/Referer: %v", h)
				}
			}
		})
	}
}
