package upstream

// refresh_check_test.go 刷新令牌的官方形状：请求体 {}、根 span 链路头，以及刷新后用新令牌
// 取账号（国内版 /v2/plugin/account，国际版 /v2/plugin/accounts 的列表）并合并进本地账号。
// 假上游用 blackcat_test.go 的 newUpFake，不连外网。

import (
	"net/http"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

const (
	refreshTestPath = "/v2/plugin/auth/token/refresh"
	refreshTestNew  = `{"code":0,"data":{"accessToken":"at-new","refreshToken":"rt-new","expiresIn":2592000}}`
)

// refreshTestAuth 待刷新的账号（global 为真时是国际版）。
func refreshTestAuth(global bool) *auth.Auth {
	a := &auth.Auth{UID: "u-1", AccessToken: "at-old", RefreshToken: "rt-old", ExpiresAt: 1, Domain: "dom.example"}
	if global {
		a.Domain = "www.workbuddy.ai"
	}
	return a
}

func refreshTestClient(f *upFake) *Client {
	c := upClient(f)
	c.ChatBaseGlobal = upBase
	return c
}

// TestRefreshOfficialShape 请求体是 {}；刷新和刷新后取账号带同一条根 span 的链路头；取账号用新令牌，
// 国内版 X-Domain 用令牌的 domain，国际版固定 www.workbuddy.ai。
func TestRefreshOfficialShape(t *testing.T) {
	for _, tc := range []struct {
		name, acctPath, acctBody, domain string
		global                           bool
	}{
		{"国内版", "/v2/plugin/account", `{"code":0,"data":{"uid":"u-1"}}`, "dom.example", false},
		{"国际版", "/v2/plugin/accounts", `{"code":0,"data":{"accounts":[{"uid":"u-x"},{"uid":"u-1"}]}}`, "www.workbuddy.ai", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.on(http.MethodPost, refreshTestPath, 200, refreshTestNew)
			f.on(http.MethodGet, tc.acctPath, 200, tc.acctBody)
			a := refreshTestAuth(tc.global)
			if err := refreshTestClient(f).RefreshToken(a); err != nil {
				t.Fatalf("刷新: %v", err)
			}
			if a.AccessToken != "at-new" || a.RefreshToken != "rt-new" {
				t.Fatalf("令牌没写回: %s / %s", a.AccessToken, a.RefreshToken)
			}
			reqs := f.seen()
			if len(reqs) != 2 || reqs[0].path != refreshTestPath || reqs[1].path != tc.acctPath {
				t.Fatalf("请求顺序不对: %d 个", len(reqs))
			}
			if string(reqs[0].body) != "{}" {
				t.Errorf("刷新请求体 = %q，官方是 {}", reqs[0].body)
			}
			r, acct := reqs[0].header, reqs[1].header
			trace := r.Get("X-Trace-ID")
			if !isHex(trace, 32) || !isHex(r.Get("X-B3-SpanId"), 16) {
				t.Fatalf("刷新的链路头不对: X-Trace-ID=%q X-B3-SpanId=%q", trace, r.Get("X-B3-SpanId"))
			}
			if r.Get("b3") != trace+"-"+r.Get("X-B3-SpanId")+"-1-" || r.Get("X-B3-TraceId") != trace || r.Get("X-B3-Sampled") != "1" {
				t.Errorf("b3 / X-B3-* 不对: %v", r)
			}
			if v := r.Values("X-B3-ParentSpanId"); len(v) != 1 || v[0] != "" {
				t.Errorf("X-B3-ParentSpanId 应发空值: %q", v)
			}
			for _, k := range []string{"X-Trace-ID", "b3", "X-B3-SpanId"} {
				if acct.Get(k) != r.Get(k) {
					t.Errorf("取账号的 %s = %q，应和刷新同一条 span %q", k, acct.Get(k), r.Get(k))
				}
			}
			if acct.Get("Authorization") != "Bearer at-new" || acct.Get("X-Domain") != tc.domain {
				t.Errorf("取账号应用新令牌、X-Domain=%s: %q %q", tc.domain, acct.Get("Authorization"), acct.Get("X-Domain"))
			}
		})
	}
}

// TestRefreshAccountCheckFailures 刷新后取不到账号时不写回新令牌，错误带 refresh_failed。
func TestRefreshAccountCheckFailures(t *testing.T) {
	for _, tc := range []struct {
		name, acctPath, acctBody string
		status                   int
		global                   bool
		want                     string
	}{
		{"国内版 data 为空", "/v2/plugin/account", `{"code":0,"data":null}`, 200, false, "no account after refresh"},
		{"国际版列表为空", "/v2/plugin/accounts", `{"code":0,"data":{"accounts":[]}}`, 200, true, "account list is empty"},
		{"取账号 HTTP 失败", "/v2/plugin/account", `{"code":500,"msg":"boom"}`, 500, false, "fetch account after refresh"},
		{"取账号业务码非 0", "/v2/plugin/account", `{"code":12153,"msg":"session dead"}`, 200, false, "fetch account after refresh"},
		{"取账号响应不是对象", "/v2/plugin/account", `{"code":0,"data":[1]}`, 200, false, "parse account after refresh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.on(http.MethodPost, refreshTestPath, 200, refreshTestNew)
			f.on(http.MethodGet, tc.acctPath, tc.status, tc.acctBody)
			a := refreshTestAuth(tc.global)
			err := refreshTestClient(f).RefreshToken(a)
			if err == nil || !strings.Contains(err.Error(), "refresh_failed") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v，应含 refresh_failed 与 %q", err, tc.want)
			}
			if a.AccessToken != "at-old" || a.RefreshToken != "rt-old" || a.ExpiresAt != 1 {
				t.Fatalf("核对失败不应写回: %s / %s / %d", a.AccessToken, a.RefreshToken, a.ExpiresAt)
			}
		})
	}
}

// TestRefreshSameTokenFails 刷新回来的 accessToken 和旧的一样：官方当刷新失败，不去取账号。
func TestRefreshSameTokenFails(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, refreshTestPath, 200, `{"code":0,"data":{"accessToken":"at-old","refreshToken":"rt-new"}}`)
	a := refreshTestAuth(false)
	err := refreshTestClient(f).RefreshToken(a)
	if err == nil || !strings.Contains(err.Error(), "did not produce a new access token") {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.seen()); n != 1 {
		t.Errorf("令牌没换就不该取账号，实际 %d 个请求", n)
	}
	if a.RefreshToken != "rt-old" {
		t.Errorf("不应写回: %s", a.RefreshToken)
	}
}

// TestRefreshAccountCheckNoUID 本地没有 uid（老的认证文件）时只要取得到账号就算通过；国内版没有
// domain 时 X-Domain 用产品入口域。
func TestRefreshAccountCheckNoUID(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, refreshTestPath, 200, refreshTestNew)
	f.on(http.MethodGet, "/v2/plugin/account", 200, `{"code":0,"data":{"uid":"u-anything"}}`)
	a := &auth.Auth{AccessToken: "at-old", RefreshToken: "rt-old", ExpiresAt: 1}
	if err := refreshTestClient(f).RefreshToken(a); err != nil {
		t.Fatalf("刷新: %v", err)
	}
	if a.AccessToken != "at-new" {
		t.Fatalf("应写回: %s", a.AccessToken)
	}
	if got := f.seen()[1].header.Get("X-Domain"); got != "www.workbuddy.cn" {
		t.Errorf("没有 domain 时 X-Domain = %q，应为 www.workbuddy.cn", got)
	}
}

// TestLoginGlobalSnapshotFailureIgnored 国际版登录后取账号列表失败只记日志，登录照常完成。
func TestLoginGlobalSnapshotFailureIgnored(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, loginTestAccountGlobal, 200, loginTestAcctBody)
	f.on(http.MethodGet, "/v2/plugin/accounts", 500, `{"code":500,"msg":"boom"}`)
	acct, err := loginTestClient(f).LoginFetchAccount("global", "st-1", LoginToken{AccessToken: "at-1"}, LoginTrace{})
	if err != nil || acct.UID != "u-1" {
		t.Fatalf("取账号列表失败不应影响登录: %+v %v", acct, err)
	}
	if n := f.count(http.MethodGet, "/v2/plugin/accounts"); n != 1 {
		t.Errorf("应取账号列表 1 次，实际 %d", n)
	}
}
