package upstream

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// registerReqRecord 假上游记下的单次请求（断言请求方法、URL、头、体用）。
type registerReqRecord struct {
	method string
	url    string
	path   string
	query  string
	header http.Header
	body   []byte
}

// registerFake 国际版注册链路的假上游：按 "METHOD 路径" 分发表，不认识的路径
// 用 t.Errorf 记下方法和完整 URL 并回 400（不发任何真实请求）。
type registerFake struct {
	t      *testing.T
	mu     sync.Mutex
	reqs   []registerReqRecord
	routes map[string]func(body []byte) (*http.Response, error)
}

func newRegisterFake(t *testing.T) *registerFake {
	return &registerFake{t: t, routes: map[string]func([]byte) (*http.Response, error){}}
}

// route 注册一个响应：key 形如 "POST /billing/area/get-country-code"。
// 每次请求都从注册时捕获的 body 重新造一份响应（同一测试里可能多次调同一端点）。
func (f *registerFake) route(key string, resp *http.Response) {
	raw, _ := io.ReadAll(resp.Body)
	status := resp.StatusCode
	f.routes[key] = func([]byte) (*http.Response, error) { return jsonResp(status, string(raw)), nil }
}

func (f *registerFake) roundTrip(r *http.Request) (*http.Response, error) {
	var raw []byte
	if r.Body != nil {
		raw, _ = io.ReadAll(r.Body)
	}
	rec := registerReqRecord{
		method: r.Method,
		url:    r.URL.String(),
		path:   r.URL.Path,
		query:  r.URL.RawQuery,
		header: r.Header.Clone(),
		body:   raw,
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, rec)
	f.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	handler, ok := f.routes[key]
	if !ok {
		f.t.Errorf("假上游收到未预设的请求: %s %s", r.Method, r.URL.String())
		return jsonResp(http.StatusBadRequest, `{"code":400,"msg":"unexpected"}`), nil
	}
	return handler(raw)
}

func (f *registerFake) client(base string) *Client {
	c := testClient(f.roundTrip)
	c.BillingBaseGlobal = base
	return c
}

func (f *registerFake) requests() []registerReqRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]registerReqRecord(nil), f.reqs...)
}

func globalAuth() *auth.Auth {
	return &auth.Auth{UID: "u-global", AccessToken: "tok", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
}

func cnAuth() *auth.Auth {
	return &auth.Auth{UID: "u-cn", AccessToken: "tok", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
}

const registerTestBase = "https://reg.example"

// TestGlobalRegisterRejectsNonGlobal 国内版账号与 nil 账号调用四个导出函数都直接返回错误，且一个请求都不发。
func TestGlobalRegisterRejectsNonGlobal(t *testing.T) {
	for _, name := range []string{"nil", "cn"} {
		t.Run(name, func(t *testing.T) {
			var a *auth.Auth
			if name == "cn" {
				a = cnAuth()
			}
			f := newRegisterFake(t)
			c := f.client(registerTestBase)
			if _, err := c.GlobalFetchCountries(a, true); err == nil {
				t.Errorf("GlobalFetchCountries(%s) 应返回错误", name)
			}
			if _, _, _, err := c.GlobalRegisterStatus(a); err == nil {
				t.Errorf("GlobalRegisterStatus(%s) 应返回错误", name)
			}
			if err := c.GlobalSubmitRegion(a, GlobalCountry{IOS2: "HK"}); err == nil {
				t.Errorf("GlobalSubmitRegion(%s) 应返回错误", name)
			}
			if _, err := c.GlobalCompleteRegistration(a); err == nil {
				t.Errorf("GlobalCompleteRegistration(%s) 应返回错误", name)
			}
			if n := len(f.requests()); n != 0 {
				t.Errorf("不应发请求，实际 %d 次: %+v", n, f.requests())
			}
		})
	}
}

// TestGlobalRegisterHeaders 注册链路请求头：web UA、Origin/Referer 同域、Bearer token；
// BillingBaseGlobal 为空时 base 落到 defaultGlobalBase。
func TestGlobalRegisterHeaders(t *testing.T) {
	t.Run("自定义 base", func(t *testing.T) {
		f := newRegisterFake(t)
		f.route("POST /billing/area/get-country-code",
			jsonResp(200, `{"code":0,"data":{"data":{"list":[{"IOS2":"HK","Code":"852"}]}}}`))
		c := f.client(registerTestBase)
		if _, err := c.GlobalFetchCountries(globalAuth(), false); err != nil {
			t.Fatalf("拉国家列表: %v", err)
		}
		reqs := f.requests()
		if len(reqs) != 1 {
			t.Fatalf("应发 1 次请求，实际 %d", len(reqs))
		}
		r := reqs[0]
		if r.method != http.MethodPost {
			t.Errorf("方法 = %s，应为 POST", r.method)
		}
		if r.header.Get("User-Agent") != browserUA {
			t.Errorf("UA = %q，应为 browserUA", r.header.Get("User-Agent"))
		}
		if r.header.Get("Origin") != registerTestBase {
			t.Errorf("Origin = %q，应为 %q", r.header.Get("Origin"), registerTestBase)
		}
		if r.header.Get("Referer") != registerTestBase+"/" {
			t.Errorf("Referer = %q，应为 %q", r.header.Get("Referer"), registerTestBase+"/")
		}
		if r.header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Authorization = %q", r.header.Get("Authorization"))
		}
		want := `{"filterForbidden":1}`
		if strings.TrimSpace(string(r.body)) != want {
			t.Errorf("请求体 = %s，应为 %s", r.body, want)
		}
	})

	t.Run("空 base 落默认", func(t *testing.T) {
		f := newRegisterFake(t)
		f.route("GET /auth/realms/copilot/overseas/user/register",
			jsonResp(200, `{"code":200}`))
		c := testClient(f.roundTrip) // BillingBaseGlobal 留空
		if _, _, _, err := c.GlobalRegisterStatus(globalAuth()); err != nil {
			t.Fatalf("查注册状态: %v", err)
		}
		r := f.requests()[0]
		if !strings.HasPrefix(r.url, defaultGlobalBase) {
			t.Errorf("URL = %q，应以 %q 开头", r.url, defaultGlobalBase)
		}
		if r.header.Get("Origin") != defaultGlobalBase || r.header.Get("Referer") != defaultGlobalBase+"/" {
			t.Errorf("Origin/Referer = %q/%q，应为 %q", r.header.Get("Origin"), r.header.Get("Referer"), defaultGlobalBase)
		}
	})

	t.Run("无 token 不带 Authorization", func(t *testing.T) {
		f := newRegisterFake(t)
		f.route("POST /billing/area/get-country-code",
			jsonResp(200, `{"code":0,"data":{"data":{"list":[]}}}`))
		c := f.client(registerTestBase)
		a := globalAuth()
		a.AccessToken = ""
		if _, err := c.GlobalFetchCountries(a, false); err != nil {
			t.Fatalf("拉国家列表: %v", err)
		}
		if got := f.requests()[0].header.Get("Authorization"); got != "" {
			t.Errorf("空 token 不应带 Authorization，got %q", got)
		}
	})
}

// TestGlobalFetchCountriesEnvelope 国家列表的双层信封解析：data 是 JSON 字符串、
// 字符串内非法 JSON、外层 code≠0、响应不是 JSON。
func TestGlobalFetchCountriesEnvelope(t *testing.T) {
	listJSON := `{"data":{"list":[{"EnName":"Hong Kong","Name":"中国香港","IOS2":"HK","IOS3":"HKG","Code":"852"}]}}`
	cases := []struct {
		name     string
		body     string
		wantErr  string // 空 = 期望成功
		wantIOS2 []string
	}{
		{name: "data 是 JSON 字符串", body: `{"code":0,"data":` + mustJSONQuote(listJSON) + `}`, wantIOS2: []string{"HK"}},
		{name: "data 是对象", body: `{"code":0,"data":` + listJSON + `}`, wantIOS2: []string{"HK"}},
		{name: "data 字符串非法 JSON", body: `{"code":0,"data":"not-json{"}`, wantErr: "country list parse"},
		{name: "外层 code 非 0", body: `{"code":7,"msg":"boom","data":null}`, wantErr: "boom (code=7)"},
		{name: "响应不是 JSON", body: `oops`, wantErr: "global register parse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRegisterFake(t)
			f.route("POST /billing/area/get-country-code", jsonResp(200, tc.body))
			c := f.client(registerTestBase)
			list, err := c.GlobalFetchCountries(globalAuth(), false)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("应返回含 %q 的错误，got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			var got []string
			for _, ctry := range list {
				got = append(got, ctry.IOS2)
			}
			if !reflect.DeepEqual(got, tc.wantIOS2) {
				t.Errorf("IOS2 = %v，应为 %v", got, tc.wantIOS2)
			}
			if list[0].Code != "852" || list[0].EnName != "Hong Kong" || list[0].Name != "中国香港" || list[0].IOS3 != "HKG" {
				t.Errorf("字段解析不全: %+v", list[0])
			}
		})
	}
}

// mustJSONQuote 把字符串编码成 JSON 字符串字面量（构造双层信封用）。
func mustJSONQuote(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// TestGlobalFetchCountriesIntlWhitelist intlOnly=true 只留白名单国家且按白名单顺序；
// intlOnly=false 原样返回上游顺序。
func TestGlobalFetchCountriesIntlWhitelist(t *testing.T) {
	// 上游顺序故意打乱，且含白名单外国家。
	list := `{"data":{"list":[` +
		`{"IOS2":"US","Code":"840"},` +
		`{"IOS2":"TH","Code":"764"},` +
		`{"IOS2":"HK","Code":"852"},` +
		`{"IOS2":"SG","Code":"702"},` +
		`{"IOS2":"MO","Code":"446"},` +
		`{"IOS2":"JP","Code":"392"}]}}`
	body := `{"code":0,"data":` + list + `}`
	f := newRegisterFake(t)
	f.route("POST /billing/area/get-country-code", jsonResp(200, body))
	c := f.client(registerTestBase)

	all, err := c.GlobalFetchCountries(globalAuth(), false)
	if err != nil {
		t.Fatalf("全量列表: %v", err)
	}
	var allCodes []string
	for _, ctry := range all {
		allCodes = append(allCodes, ctry.IOS2)
	}
	if want := []string{"US", "TH", "HK", "SG", "MO", "JP"}; !reflect.DeepEqual(allCodes, want) {
		t.Errorf("intlOnly=false 应原样返回 %v，got %v", want, allCodes)
	}

	intl, err := c.GlobalFetchCountries(globalAuth(), true)
	if err != nil {
		t.Fatalf("国际版列表: %v", err)
	}
	var intlCodes []string
	for _, ctry := range intl {
		intlCodes = append(intlCodes, ctry.IOS2)
	}
	if want := []string{"HK", "MO", "SG", "TH"}; !reflect.DeepEqual(intlCodes, want) {
		t.Errorf("intlOnly=true 应按白名单顺序只留 %v，got %v", want, intlCodes)
	}
}

// TestGlobalRegisterStatus 注册状态判定：code=200 已激活；code=500 或 msg 含
// region required（不分大小写）需补地区；其他 code 两者皆 false 且 msg 原样返回。
// 同时断言请求是 GET、URL 带 userId、请求头带 X-User-Id。
func TestGlobalRegisterStatus(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		wantActivated bool
		wantNeeds     bool
		wantMsg       string
	}{
		{name: "code=200 已激活", body: `{"code":200}`, wantActivated: true, wantMsg: "register success"},
		{name: "code=500 需补地区", body: `{"code":500,"msg":"region required"}`, wantNeeds: true, wantMsg: "region required"},
		{name: "msg 含 Region Required 不分大小写", body: `{"code":403,"msg":"Region Required For User"}`, wantNeeds: true, wantMsg: "Region Required For User"},
		{name: "其他 code 原样返回 msg", body: `{"code":400,"msg":"other failure"}`, wantMsg: "other failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRegisterFake(t)
			f.route("GET /auth/realms/copilot/overseas/user/register", jsonResp(200, tc.body))
			c := f.client(registerTestBase)
			a := globalAuth()
			activated, needsRegion, msg, err := c.GlobalRegisterStatus(a)
			if err != nil {
				t.Fatalf("查注册状态: %v", err)
			}
			if activated != tc.wantActivated || needsRegion != tc.wantNeeds || msg != tc.wantMsg {
				t.Errorf("got activated=%v needsRegion=%v msg=%q，应为 %v/%v/%q",
					activated, needsRegion, msg, tc.wantActivated, tc.wantNeeds, tc.wantMsg)
			}
			r := f.requests()[0]
			if r.method != http.MethodGet {
				t.Errorf("方法 = %s，应为 GET", r.method)
			}
			if !strings.Contains(r.url, "userId="+a.UID) {
				t.Errorf("URL = %q，应带 userId=%s", r.url, a.UID)
			}
			if got := r.header.Get("X-User-Id"); got != a.UID {
				t.Errorf("X-User-Id = %q，应为 %q", got, a.UID)
			}
		})
	}

	t.Run("响应不是 JSON", func(t *testing.T) {
		f := newRegisterFake(t)
		f.route("GET /auth/realms/copilot/overseas/user/register", jsonResp(200, `oops`))
		c := f.client(registerTestBase)
		if _, _, _, err := c.GlobalRegisterStatus(globalAuth()); err == nil {
			t.Error("非 JSON 响应应返回错误")
		}
	})
}

// TestGlobalSubmitRegion 提交地区：attributes 的 countryCode/countryFullName/countryName
// 各是单元素数组，值分别取 Code/EnName/IOS2；code≠0 返回错误。
func TestGlobalSubmitRegion(t *testing.T) {
	country := GlobalCountry{EnName: "Hong Kong", Name: "中国香港", IOS2: "HK", IOS3: "HKG", Code: "852"}
	t.Run("成功", func(t *testing.T) {
		f := newRegisterFake(t)
		f.route("POST /console/login/account", jsonResp(200, `{"code":0}`))
		c := f.client(registerTestBase)
		if err := c.GlobalSubmitRegion(globalAuth(), country); err != nil {
			t.Fatalf("提交地区: %v", err)
		}
		r := f.requests()[0]
		if r.method != http.MethodPost || r.path != "/console/login/account" {
			t.Errorf("请求 = %s %s", r.method, r.path)
		}
		var body struct {
			Attributes map[string][]string `json:"attributes"`
		}
		if err := json.Unmarshal(r.body, &body); err != nil {
			t.Fatalf("请求体解析: %v (%s)", err, r.body)
		}
		want := map[string][]string{
			"countryCode":     {"852"},
			"countryFullName": {"Hong Kong"},
			"countryName":     {"HK"},
		}
		if !reflect.DeepEqual(body.Attributes, want) {
			t.Errorf("attributes = %v，应为 %v", body.Attributes, want)
		}
	})

	t.Run("code 非 0", func(t *testing.T) {
		f := newRegisterFake(t)
		f.route("POST /console/login/account", jsonResp(200, `{"code":9,"msg":"bad region"}`))
		c := f.client(registerTestBase)
		err := c.GlobalSubmitRegion(globalAuth(), country)
		if err == nil || !strings.Contains(err.Error(), "bad region") || !strings.Contains(err.Error(), "code=9") {
			t.Fatalf("应返回含 msg 和 code 的错误，got %v", err)
		}
	})
}

// TestGlobalCompleteRegistration 一键注册的状态机：已激活只查一次；不需要补地区
// 直接报错且不拉国家列表；需补地区时拉白名单、提交首个、再查状态；白名单为空 /
// 提交失败 / 再查仍未激活各自报错。
func TestGlobalCompleteRegistration(t *testing.T) {
	const statusPath = "GET /auth/realms/copilot/overseas/user/register"
	const countriesPath = "POST /billing/area/get-country-code"
	const submitPath = "POST /console/login/account"

	t.Run("已激活只发一次状态请求", func(t *testing.T) {
		f := newRegisterFake(t)
		f.route(statusPath, jsonResp(200, `{"code":200}`))
		c := f.client(registerTestBase)
		ok, err := c.GlobalCompleteRegistration(globalAuth())
		if err != nil || !ok {
			t.Fatalf("activated=%v err=%v", ok, err)
		}
		if n := len(f.requests()); n != 1 {
			t.Errorf("应只发 1 次请求，实际 %d: %+v", n, f.requests())
		}
	})

	t.Run("未激活且不需补地区", func(t *testing.T) {
		f := newRegisterFake(t)
		f.route(statusPath, jsonResp(200, `{"code":400,"msg":"nope"}`))
		c := f.client(registerTestBase)
		ok, err := c.GlobalCompleteRegistration(globalAuth())
		if ok || err == nil || !strings.Contains(err.Error(), "nope") {
			t.Fatalf("应返回错误，got activated=%v err=%v", ok, err)
		}
		if n := len(f.requests()); n != 1 {
			t.Errorf("不应拉国家列表，实际请求 %d 次: %+v", n, f.requests())
		}
	})

	t.Run("需补地区走完整流程", func(t *testing.T) {
		f := newRegisterFake(t)
		var statusCalls int
		f.routes[statusPath] = func([]byte) (*http.Response, error) {
			statusCalls++
			if statusCalls == 1 {
				return jsonResp(200, `{"code":500,"msg":"region required"}`), nil
			}
			return jsonResp(200, `{"code":200}`), nil
		}
		// 上游顺序 SG 在前，国际版白名单应选 HK。
		f.route(countriesPath, jsonResp(200,
			`{"code":0,"data":{"data":{"list":[{"IOS2":"SG","Code":"702"},{"IOS2":"HK","Code":"852","EnName":"Hong Kong"}]}}}`))
		f.route(submitPath, jsonResp(200, `{"code":0}`))
		c := f.client(registerTestBase)
		ok, err := c.GlobalCompleteRegistration(globalAuth())
		if err != nil || !ok {
			t.Fatalf("activated=%v err=%v", ok, err)
		}
		reqs := f.requests()
		if len(reqs) != 4 {
			t.Fatalf("应发 4 次请求（状态/国家/提交/状态），实际 %d: %+v", len(reqs), reqs)
		}
		var submitted struct {
			Attributes map[string][]string `json:"attributes"`
		}
		if err := json.Unmarshal(reqs[2].body, &submitted); err != nil {
			t.Fatalf("提交体解析: %v", err)
		}
		if got := submitted.Attributes["countryName"]; !reflect.DeepEqual(got, []string{"HK"}) {
			t.Errorf("应提交白名单首个 HK，got %v", got)
		}
	})

	t.Run("白名单国家为空", func(t *testing.T) {
		f := newRegisterFake(t)
		f.route(statusPath, jsonResp(200, `{"code":500,"msg":"region required"}`))
		f.route(countriesPath, jsonResp(200,
			`{"code":0,"data":{"data":{"list":[{"IOS2":"US","Code":"840"}]}}}`))
		c := f.client(registerTestBase)
		ok, err := c.GlobalCompleteRegistration(globalAuth())
		if ok || err == nil || !strings.Contains(err.Error(), "no countries") {
			t.Fatalf("应返回 no countries 错误，got activated=%v err=%v", ok, err)
		}
	})

	t.Run("提交失败", func(t *testing.T) {
		f := newRegisterFake(t)
		f.route(statusPath, jsonResp(200, `{"code":500,"msg":"region required"}`))
		f.route(countriesPath, jsonResp(200,
			`{"code":0,"data":{"data":{"list":[{"IOS2":"HK","Code":"852"}]}}}`))
		f.route(submitPath, jsonResp(200, `{"code":3,"msg":"submit boom"}`))
		c := f.client(registerTestBase)
		ok, err := c.GlobalCompleteRegistration(globalAuth())
		if ok || err == nil || !strings.Contains(err.Error(), "submit boom") {
			t.Fatalf("应返回提交错误，got activated=%v err=%v", ok, err)
		}
	})

	t.Run("提交后再查仍未激活", func(t *testing.T) {
		f := newRegisterFake(t)
		statusCalls := 0
		f.routes[statusPath] = func([]byte) (*http.Response, error) {
			statusCalls++
			if statusCalls == 1 {
				return jsonResp(200, `{"code":500,"msg":"region required"}`), nil
			}
			return jsonResp(200, `{"code":400,"msg":"still off"}`), nil
		}
		f.route(countriesPath, jsonResp(200,
			`{"code":0,"data":{"data":{"list":[{"IOS2":"HK","Code":"852"}]}}}`))
		f.route(submitPath, jsonResp(200, `{"code":0}`))
		c := f.client(registerTestBase)
		ok, err := c.GlobalCompleteRegistration(globalAuth())
		if ok || err == nil || !strings.Contains(err.Error(), "still off") {
			t.Fatalf("应返回仍未激活错误，got activated=%v err=%v", ok, err)
		}
	})

	t.Run("拉国家列表失败", func(t *testing.T) {
		f := newRegisterFake(t)
		f.route(statusPath, jsonResp(200, `{"code":500,"msg":"region required"}`))
		f.route(countriesPath, jsonResp(200, `{"code":7,"msg":"countries boom"}`))
		c := f.client(registerTestBase)
		ok, err := c.GlobalCompleteRegistration(globalAuth())
		if ok || err == nil || !strings.Contains(err.Error(), "countries boom") {
			t.Fatalf("应返回拉国家列表错误，got activated=%v err=%v", ok, err)
		}
	})

	t.Run("第二次查状态失败", func(t *testing.T) {
		f := newRegisterFake(t)
		statusCalls := 0
		f.routes[statusPath] = func([]byte) (*http.Response, error) {
			statusCalls++
			if statusCalls == 1 {
				return jsonResp(200, `{"code":500,"msg":"region required"}`), nil
			}
			return jsonResp(200, `oops`), nil
		}
		f.route(countriesPath, jsonResp(200,
			`{"code":0,"data":{"data":{"list":[{"IOS2":"HK","Code":"852"}]}}}`))
		f.route(submitPath, jsonResp(200, `{"code":0}`))
		c := f.client(registerTestBase)
		ok, err := c.GlobalCompleteRegistration(globalAuth())
		if ok || err == nil {
			t.Fatalf("第二次查状态解析失败应返回错误，got activated=%v err=%v", ok, err)
		}
	})
}

// TestGlobalRegisterTransportErrors 非法 base 在构造请求时失败、传输层错误原样
// 上抛：四个函数都不 panic，且不产生任何“成功”结果。
func TestGlobalRegisterTransportErrors(t *testing.T) {
	t.Run("非法 base", func(t *testing.T) {
		f := newRegisterFake(t)
		c := f.client("://bad-base")
		a := globalAuth()
		if _, err := c.GlobalFetchCountries(a, false); err == nil {
			t.Error("GlobalFetchCountries 应返回错误")
		}
		if _, _, _, err := c.GlobalRegisterStatus(a); err == nil {
			t.Error("GlobalRegisterStatus 应返回错误")
		}
		if err := c.GlobalSubmitRegion(a, GlobalCountry{IOS2: "HK"}); err == nil {
			t.Error("GlobalSubmitRegion 应返回错误")
		}
		if _, err := c.GlobalCompleteRegistration(a); err == nil {
			t.Error("GlobalCompleteRegistration 应返回错误")
		}
		if n := len(f.requests()); n != 0 {
			t.Errorf("请求构造失败不应到达传输层，实际 %d 次", n)
		}
	})

	t.Run("传输层错误", func(t *testing.T) {
		c := testClient(func(*http.Request) (*http.Response, error) {
			return nil, errTransportBoom
		})
		c.BillingBaseGlobal = registerTestBase
		a := globalAuth()
		if _, err := c.GlobalFetchCountries(a, false); err == nil {
			t.Error("GlobalFetchCountries 应返回错误")
		}
		if _, _, _, err := c.GlobalRegisterStatus(a); err == nil {
			t.Error("GlobalRegisterStatus 应返回错误")
		}
		if err := c.GlobalSubmitRegion(a, GlobalCountry{IOS2: "HK"}); err == nil {
			t.Error("GlobalSubmitRegion 应返回错误")
		}
	})
}

// errTransportBoom 传输层固定错误。
var errTransportBoom = fmt.Errorf("transport boom")
