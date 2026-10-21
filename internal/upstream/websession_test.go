// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// websession_test.go 网页登录态（登录码 → client-login → Cookie 罐）的请求形状、
// 复用、401 重取、失败语义、并发与多账号隔离。
package upstream

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// wsCookie 取账号登录态罐里的 session 值。
func wsCookie(c *Client, a *auth.Auth) string {
	jar, _ := c.webSessions.get(c.webSessionKey(a)).jarNow()
	return webSessionValue(jar, c.webBase(a))
}

// wsLoginReqs 登录码 / client-login 各被请求了几次。
func wsLoginReqs(f *upFake) (codeReqs, loginReqs int) {
	for _, r := range f.seen() {
		switch r.path {
		case webDeviceAuthCodePath:
			codeReqs++
		case webClientLoginPath:
			loginReqs++
		}
	}
	return codeReqs, loginReqs
}

// TestWebSessionLoginCodeShape 登录码请求是官方后台服务的裸 fetch 形状：无请求体，
// 只带账号头与刷新令牌，不带请求框架的头；企业号多两个企业头，X-Domain 空时不带。
func TestWebSessionLoginCodeShape(t *testing.T) {
	t.Run("个人号", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, "/x", 200, `{"code":0,"data":{}}`)
		a := &auth.Auth{UID: "u-1", Nickname: "测试号", AccessToken: "at", RefreshToken: "rt",
			Domain: "www.workbuddy.cn"}
		if _, err := upClient(f).webJSON(a, http.MethodGet, "/x", webPageGrowthCenter, nil); err != nil {
			t.Fatalf("webJSON: %v", err)
		}
		r := f.seen()[0]
		if r.method != http.MethodPost || r.url != upBase+webDeviceAuthCodePath {
			t.Errorf("登录码请求 = %s %s，应为 POST %s", r.method, r.url, upBase+webDeviceAuthCodePath)
		}
		if len(r.body) != 0 {
			t.Errorf("登录码请求不应带请求体，got %q", r.body)
		}
		wantHeaders(t, r.header, map[string]string{
			"Content-Type":     "application/json",
			"Accept":           "*/*",
			"User-Agent":       "node",
			"X-User-Id":        "u-1",
			"Authorization":    "Bearer at",
			"X-Refresh-Token":  "rt",
			"X-Domain":         "www.workbuddy.cn",
			"X-Enterprise-Id":  "",
			"X-Tenant-Id":      "",
			"X-Request-ID":     "",
			"X-Product":        "",
			"X-Requested-With": "",
			"Origin":           "",
			"Referer":          "",
		})
	})

	t.Run("企业号", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, "/x", 200, `{"code":0,"data":{}}`)
		a := &auth.Auth{UID: "u-ent", AccessToken: "at", RefreshToken: "rt", EnterpriseID: "ent-9"}
		if _, err := upClient(f).webJSON(a, http.MethodGet, "/x", webPageGrowthCenter, nil); err != nil {
			t.Fatalf("webJSON: %v", err)
		}
		r := f.seen()[0]
		wantHeaders(t, r.header, map[string]string{
			"X-Enterprise-Id": "ent-9",
			"X-Tenant-Id":     "ent-9",
			"X-Domain":        "", // 账号令牌没有 domain 时不带
		})
	})
}

// TestWebSessionClientLogin client-login：code/target 参数、浏览器导航头、session 进罐；照浏览器
// 跟随跳转打开 target 页面，用同一套导航头、带上刚种下的 Cookie、不带 Referer。
func TestWebSessionClientLogin(t *testing.T) {
	f := newUpFake(t)
	var page *upReq
	// client-login 跳到 target（webPageUsage）。
	f.onFunc(http.MethodGet, "/profile/usage", func(r *upReq) (*http.Response, error) {
		page = r
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"text/html"}},
			Body: io.NopCloser(strings.NewReader("<!doctype html>"))}, nil
	})
	f.onFunc(http.MethodGet, webClientLoginPath, func(*upReq) (*http.Response, error) {
		h := http.Header{}
		h.Add("Set-Cookie", "session=at; Path=/; Max-Age=604800; HttpOnly; SameSite=Lax")
		h.Set("Location", webPageUsage)
		return &http.Response{StatusCode: http.StatusFound, Status: "302 Found", Header: h,
			Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	f.on(http.MethodGet, "/x", 200, `{"code":0,"data":{}}`)
	a := &auth.Auth{UID: "u-1", AccessToken: "at", RefreshToken: "rt"}
	c := upClient(f)
	if _, err := c.webJSON(a, http.MethodGet, "/x", webPageUsage, nil); err != nil {
		t.Fatalf("webJSON: %v", err)
	}

	var login *upReq
	for _, r := range f.seen() {
		if r.path == webClientLoginPath {
			login = r
		}
	}
	if login == nil {
		t.Fatal("未发 client-login 请求")
	}
	q, err := url.ParseQuery(login.query)
	if err != nil {
		t.Fatalf("client-login query: %v", err)
	}
	if got := q.Get("code"); got != "dc.at" {
		t.Errorf("code = %q，应为登录码 dc.at", got)
	}
	if got := q.Get("target"); got != webPageUsage {
		t.Errorf("target = %q，应为触发请求的页面 %q", got, webPageUsage)
	}
	nav := map[string]string{
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
		"User-Agent":                browserUA,
		"Upgrade-Insecure-Requests": "1",
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-User":            "?1",
		"Sec-CH-UA-Mobile":          "?0",
		"Sec-CH-UA-Platform":        `"macOS"`,
		"Accept-Language":           "zh-CN,zh;q=0.9",
		"Referer":                   "",
		"Origin":                    "",
	}
	wantHeaders(t, login.header, nav)
	if page == nil {
		t.Fatal("应照浏览器跟随跳转打开 target 页面")
	}
	wantHeaders(t, page.header, nav)
	if got := page.header.Get("Cookie"); !strings.Contains(got, "session=at") {
		t.Errorf("跟随跳转的 Cookie = %q，应带刚种下的 session", got)
	}
	if got := wsCookie(c, a); got != "at" {
		t.Errorf("罐里 session = %q，应为 at", got)
	}
}

// TestWebSessionReusesCookie 第一次请求先登录，之后的请求直接带 Cookie、不再登录；
// 网页请求不带 Authorization，并带浏览器自动头。
func TestWebSessionReusesCookie(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, "/a", 200, `{"code":0,"data":{}}`)
	f.on(http.MethodGet, "/b", 200, `{"code":0,"data":{}}`)
	c := upClient(f)
	a := upAuth()
	for _, p := range []string{"/a", "/b"} {
		if _, err := c.webJSON(a, http.MethodGet, p, webPageGrowthCenter, nil); err != nil {
			t.Fatalf("webJSON %s: %v", p, err)
		}
	}
	codeReqs, loginReqs := wsLoginReqs(f)
	if codeReqs != 1 || loginReqs != 1 {
		t.Fatalf("登录码 %d 次 / client-login %d 次，应各 1 次", codeReqs, loginReqs)
	}
	biz := f.bizSeen()
	if len(biz) != 2 {
		t.Fatalf("业务请求 %d 个，应为 2", len(biz))
	}
	for _, r := range biz {
		wantHeaders(t, r.header, map[string]string{
			"Authorization":       "",
			"X-Client-Platform":   "web",
			"User-Agent":          browserUA,
			"Accept-Language":     "zh-CN,zh;q=0.9",
			"Sec-CH-UA-Mobile":    "?0",
			"Sec-CH-UA-Platform":  `"macOS"`,
			"Sec-Fetch-Site":      "same-origin",
			"Sec-Fetch-Mode":      "cors",
			"Sec-Fetch-Dest":      "empty",
			"X-User-Id":           "",
			"X-Domain":            "",
			"X-Tenant-Id":         "",
			"X-Product":           "",
			"X-Request-ID":        "",
			"X-Requested-With":    "",
			"X-CodeBuddy-Request": "",
			"Accept-Encoding":     acceptEncodingChrome,
		})
		if got := r.header.Get("Cookie"); !strings.Contains(got, "session=at") {
			t.Errorf("%s Cookie = %q，应含 session=at", r.path, got)
		}
	}
	if got := biz[0].header.Get("Sec-CH-UA"); got != `"Chromium";v="152", "Not?A_Brand";v="24", "Google Chrome";v="152"` {
		t.Errorf("Sec-CH-UA = %q", got)
	}
}

// TestWebSessionInternationalLanguage 国际版网页的 Accept-Language 是 en-US。
func TestWebSessionInternationalLanguage(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, "/x", 200, `{"code":0,"data":{}}`)
	c := upGlobalClient(f)
	if _, err := c.webJSON(globalAuth(), http.MethodGet, "/x", webPageGrowthCenter, nil); err != nil {
		t.Fatalf("webJSON: %v", err)
	}
	r := f.bizSeen()[0]
	if got := r.header.Get("Accept-Language"); got != "en-US,en;q=0.9" {
		t.Errorf("Accept-Language = %q，应为国际版取值", got)
	}
	if got := r.header.Get("Referer"); got != defaultGlobalBase+webPageGrowthCenter {
		t.Errorf("Referer = %q", got)
	}
}

// TestWebSessionConfiguredUserAgent 配置了 user_agent：业务与 client-login 用它，
// 不补 Sec-CH-UA*；登录码仍是 Node fetch 的 "node"。
func TestWebSessionConfiguredUserAgent(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, "/x", 200, `{"code":0,"data":{}}`)
	c := upClient(f)
	c.UserAgent = "custom-ua/1.0"
	if _, err := c.webJSON(upAuth(), http.MethodGet, "/x", webPageGrowthCenter, nil); err != nil {
		t.Fatalf("webJSON: %v", err)
	}
	biz := f.bizSeen()[0]
	wantHeaders(t, biz.header, map[string]string{
		"User-Agent":         "custom-ua/1.0",
		"Sec-CH-UA":          "",
		"Sec-CH-UA-Mobile":   "",
		"Sec-CH-UA-Platform": "",
	})
	for _, r := range f.seen() {
		switch r.path {
		case webDeviceAuthCodePath:
			if got := r.header.Get("User-Agent"); got != "node" {
				t.Errorf("登录码 UA = %q，应为 node", got)
			}
		case webClientLoginPath:
			wantHeaders(t, r.header, map[string]string{
				"User-Agent":       "custom-ua/1.0",
				"Sec-CH-UA":        "",
				"Sec-CH-UA-Mobile": "",
			})
		}
	}
}

// TestWebSession401Renewal 网页请求 401 → 清罐重取一次并重发；第二次成功。
// 连续 401 → 返回错误，登录态只重取一次。
func TestWebSession401Renewal(t *testing.T) {
	t.Run("重发后成功", func(t *testing.T) {
		f := newUpFake(t)
		calls := 0
		f.onFunc(http.MethodGet, "/x", func(*upReq) (*http.Response, error) {
			calls++
			if calls == 1 {
				return jsonResp(http.StatusUnauthorized, `{"code":401,"msg":"session expired"}`), nil
			}
			return jsonResp(200, `{"code":0,"data":{"ok":1}}`), nil
		})
		data, err := upClient(f).webJSON(upAuth(), http.MethodGet, "/x", webPageGrowthCenter, nil)
		if err != nil {
			t.Fatalf("重取登录态后应成功: %v", err)
		}
		if string(data) != `{"ok":1}` {
			t.Errorf("data = %s", data)
		}
		if calls != 2 {
			t.Errorf("业务请求 %d 次，应为 2（首次 401 + 重发）", calls)
		}
		codeReqs, loginReqs := wsLoginReqs(f)
		if codeReqs != 2 || loginReqs != 2 {
			t.Errorf("登录码 %d / client-login %d，应为 2/2（首次 + 401 后重取一次）", codeReqs, loginReqs)
		}
	})

	t.Run("连续 401 只重取一次", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, "/x", 401, `{"code":401,"msg":"still expired"}`)
		_, err := upClient(f).webJSON(upAuth(), http.MethodGet, "/x", webPageGrowthCenter, nil)
		ue, ok := err.(*Error)
		if !ok || ue.Status != http.StatusUnauthorized {
			t.Fatalf("err = %T %v，应为 401 *Error", err, err)
		}
		codeReqs, loginReqs := wsLoginReqs(f)
		if codeReqs != 2 || loginReqs != 2 {
			t.Errorf("登录码 %d / client-login %d，应为 2/2（只重取一次）", codeReqs, loginReqs)
		}
		if n := len(f.bizSeen()); n != 2 {
			t.Errorf("业务请求 %d 次，应为 2（首次 + 重发一次）", n)
		}
	})
}

// TestWebSessionAcquireFailures 登录态获取失败：登录码 401 / code≠0 / 空码、
// client-login 不种 session → 网页请求返回错误，不发业务请求，也不回落到 Bearer。
func TestWebSessionAcquireFailures(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *upFake)
	}{
		{"登录码 401", func(f *upFake) {
			f.on(http.MethodPost, webDeviceAuthCodePath, 401, `{"code":10034,"msg":"refresh token is empty"}`)
		}},
		{"登录码业务码非 0", func(f *upFake) {
			f.on(http.MethodPost, webDeviceAuthCodePath, 200, `{"code":10034,"msg":"refresh token is empty"}`)
		}},
		{"登录码为空", func(f *upFake) {
			f.on(http.MethodPost, webDeviceAuthCodePath, 200, `{"code":0,"data":{}}`)
		}},
		{"client-login 不种 session", func(f *upFake) {
			f.on(http.MethodGet, webClientLoginPath, 200, "")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.on(http.MethodGet, "/x", 200, `{"code":0,"data":{}}`)
			tc.setup(f)
			if _, err := upClient(f).webJSON(upAuth(), http.MethodGet, "/x", webPageGrowthCenter, nil); err == nil {
				t.Fatal("登录态获取失败应返回错误")
			}
			if biz := f.bizSeen(); len(biz) != 0 {
				t.Fatalf("不应发出业务请求，实际 %+v", biz)
			}
			for _, r := range f.seen() {
				if r.path != webDeviceAuthCodePath && r.path != webClientLoginPath {
					t.Errorf("只应有登录态请求，收到 %s %s", r.method, r.path)
				}
			}
		})
	}
}

// TestWebSessionConcurrentAcquire 同一账号多个 goroutine 并发发网页请求：只获取一次登录态，
// 且全部成功（-race 下跑）。
func TestWebSessionConcurrentAcquire(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, "/x", 200, `{"code":0,"data":{}}`)
	c := upClient(f)
	a := upAuth()
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.webJSON(a, http.MethodGet, "/x", webPageGrowthCenter, nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("并发 webJSON: %v", err)
		}
	}
	if codeReqs, loginReqs := wsLoginReqs(f); codeReqs != 1 || loginReqs != 1 {
		t.Errorf("登录码 %d / client-login %d，应为 1/1", codeReqs, loginReqs)
	}
	if got := len(f.bizSeen()); got != workers {
		t.Errorf("业务请求 %d 个，应为 %d", got, workers)
	}
}

// TestWebSessionPerAccount 两个账号各自一个罐，Cookie 不串。
func TestWebSessionPerAccount(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, "/x", 200, `{"code":0,"data":{}}`)
	c := upClient(f)
	a1 := &auth.Auth{UID: "u-1", AccessToken: "at-1", RefreshToken: "rt-1"}
	a2 := &auth.Auth{UID: "u-2", AccessToken: "at-2", RefreshToken: "rt-2"}
	for _, a := range []*auth.Auth{a1, a2} {
		if _, err := c.webJSON(a, http.MethodGet, "/x", webPageGrowthCenter, nil); err != nil {
			t.Fatalf("webJSON %s: %v", a.UID, err)
		}
	}
	jar1 := wsCookie(c, a1)
	jar2 := wsCookie(c, a2)
	if jar1 != "at-1" || jar2 != "at-2" {
		t.Errorf("罐值 = %q / %q，应为 at-1 / at-2", jar1, jar2)
	}
	biz := f.bizSeen()
	if len(biz) != 2 {
		t.Fatalf("业务请求 %d 个，应为 2", len(biz))
	}
	if got := biz[0].header.Get("Cookie"); !strings.Contains(got, "session=at-1") {
		t.Errorf("账号 1 Cookie = %q", got)
	}
	if got := biz[1].header.Get("Cookie"); !strings.Contains(got, "session=at-2") {
		t.Errorf("账号 2 Cookie = %q", got)
	}
}

// TestReportWebEventLoginTarget 网页上报的登录码 target 用上报页面的路径（不是固定成长中心）。
func TestReportWebEventLoginTarget(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, "/v2/report", 200, `{"code":0,"msg":"OK"}`)
	c := upClient(f)
	c.WebBaseCN = upBase
	a := &auth.Auth{UID: "u-web", AccessToken: "at"}
	if err := c.ReportWebEvent(a, "library_doc_intro_click", upBase+"/library/1?from=home", "btn", "介绍"); err != nil {
		t.Fatalf("ReportWebEvent: %v", err)
	}
	var login *upReq
	for _, r := range f.seen() {
		if r.path == webClientLoginPath {
			login = r
		}
	}
	if login == nil {
		t.Fatal("未发 client-login")
	}
	q, _ := url.ParseQuery(login.query)
	if got := q.Get("target"); got != "/library/1?from=home" {
		t.Errorf("target = %q，应为上报页面路径", got)
	}
}

// TestWebSessionFollowEdges 跟随跳转的边界：最多 20 跳；只跟 http / https；打开页面出错不影响
// 登录态（Cookie 在 client-login 就种下了）。
func TestWebSessionFollowEdges(t *testing.T) {
	redirect := func(loc string) upRoute {
		return func(*upReq) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusFound, Status: "302 Found",
				Header: http.Header{"Location": {loc}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
	}
	for _, tc := range []struct {
		name  string
		route upRoute
		want  int // GET /profile/growth-center 的次数
	}{
		{"循环跳转停在 20 跳", redirect(webPageGrowthCenter), webMaxRedirects},
		{"不跟 http 以外的地址", redirect("weixin://open"), 1},
		{"打开页面出错", func(*upReq) (*http.Response, error) { return nil, io.ErrUnexpectedEOF }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.onFunc(http.MethodGet, webPageGrowthCenter, tc.route)
			f.on(http.MethodGet, "/x", 200, `{"code":0,"data":{}}`)
			c := upClient(f)
			a := upAuth()
			if _, err := c.webJSON(a, http.MethodGet, "/x", webPageGrowthCenter, nil); err != nil {
				t.Fatalf("webJSON: %v", err)
			}
			if n := f.count(http.MethodGet, webPageGrowthCenter); n != tc.want {
				t.Errorf("打开页面 %d 次，应为 %d", n, tc.want)
			}
		})
	}
}
