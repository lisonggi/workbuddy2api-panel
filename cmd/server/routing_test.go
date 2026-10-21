// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/redisstore"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// catalogUpstream 假上游：目录按企业身份下发（X-User-Id + X-Enterprise-Id 都带才给企业名单），
// 记下每个 /v3/config 请求用的 token。
type catalogUpstream struct {
	mu     sync.Mutex
	tokens []string
}

func (u *catalogUpstream) client() *upstream.Client {
	return &upstream.Client{
		HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/v3/config") {
				u.mu.Lock()
				u.tokens = append(u.tokens, r.Header.Get("Authorization"))
				u.mu.Unlock()
			}
			models, ids := `{"id":"shared"},{"id":"personal-only"}`, `"shared","personal-only"`
			if r.Header.Get("X-User-Id") != "" && r.Header.Get("X-Enterprise-Id") != "" {
				models, ids = `{"id":"shared"},{"id":"claude-x"}`, `"shared","claude-x"`
			}
			body := `{"code":0,"data":{"models":[` + models + `],"agents":[{"name":"cli","models":[` + ids + `]}]}}`
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})},
		ChatBaseCN:    "https://cn.fake",
		BillingBaseCN: "https://cn.fake",
	}
}

func catalogPool() *pool.Pool {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "a-ent", EnterpriseID: "ent-1", AccessToken: "at-ent", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "b-per", AccessToken: "at-per", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"})
	return p
}

// TestWireModelCatalog 装配后选号按目录走：企业专属模型只给企业号，企业号目录里没有的模型只给个人号。
func TestWireModelCatalog(t *testing.T) {
	fake := &catalogUpstream{}
	up := fake.client()
	p := catalogPool()
	wireModelCatalog(p, up)
	if _, err := up.FetchModels(p.AuthByUID("b-per")); err != nil {
		t.Fatal(err)
	}
	if _, err := up.FetchAccountCatalog(p.AuthByUID("a-ent")); err != nil {
		t.Fatal(err)
	}
	for model, want := range map[string][]string{
		"claude-x":      {"a-ent"},
		"personal-only": {"b-per"},
		"shared":        {"a-ent", "b-per"},
	} {
		got := p.AvailableUIDsForModel(model)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v want %v", model, got, want)
		}
	}
}

// TestSessionConfigRealmAware 粘性路由按 handler 定好的域分配：指定域只在该域挑，域为空（跨域合池）两域都算。
func TestSessionConfigRealmAware(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	p := pool.New("")
	p.Add(&auth.Auth{UID: "cn1", AccessToken: "at", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "g1", AccessToken: "at", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"})
	cfg := sessionConfig(time.Minute, time.Minute, redisstore.Noop{}, p)
	if cfg.TTL != time.Minute || cfg.GCInterval != time.Minute || cfg.Available == nil || cfg.AssignableForModel == nil {
		t.Fatalf("配置没带全: %+v", cfg)
	}
	for realm, want := range map[string][]string{"cn": {"cn1"}, "global": {"g1"}, "": {"cn1", "g1"}} {
		got := cfg.AvailableForModel("m", realm)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("realm=%q: got %v want %v", realm, got, want)
		}
	}
	// 新会话分配按成本分层：cn1 对 m 实测免费，合池时新会话只分给它（关掉条件探索，免得这一次切去无观测的号）。
	p.SetCostExploreInterval(0)
	p.NoteModelCost("cn1", "m", 0, 1000)
	if got := cfg.AssignableForModel("m", ""); !reflect.DeepEqual(got, []string{"cn1"}) {
		t.Errorf("新会话分配应只留免费号，got %v", got)
	}
	r := session.New(cfg)
	if uid, ok := r.ResolveForModel("k-global", "m", "global"); !ok || uid != "g1" {
		t.Errorf("指定国际版时会话应绑国际版号，got %q %v", uid, ok)
	}
}

// TestWarmModelRates 启动预热用个人号拉国内版域目录；进程已在退出时不发请求。
func TestWarmModelRates(t *testing.T) {
	fake := &catalogUpstream{}
	up := fake.client()
	p := catalogPool()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	warmModelRates(ctx, up, p)
	if len(fake.tokens) != 0 {
		t.Fatalf("ctx 已取消不应拉目录，got %v", fake.tokens)
	}

	warmModelRates(context.Background(), up, p)
	if len(fake.tokens) != 1 || fake.tokens[0] != "Bearer at-per" {
		t.Fatalf("应用个人号拉一次域目录，got %v", fake.tokens)
	}
	if !up.RealmHasModel("cn", "personal-only") || up.RealmHasModel("cn", "claude-x") {
		t.Error("域目录应是个人号视角")
	}
}
