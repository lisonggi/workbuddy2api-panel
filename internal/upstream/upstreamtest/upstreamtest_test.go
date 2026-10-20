package upstreamtest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRespond 认识的路径给官方形状的默认响应，别的路径交给调用方。
func TestRespond(t *testing.T) {
	cases := map[string]int{SummaryPath: 404, PaidPath: 200, FreePath: 200, CheckinStatusPath: 200}
	for path, want := range cases {
		st, body, ok := Respond(path, Package(10, 4, `"PackageName":"p"`))
		if !ok || st != want || body == "" {
			t.Errorf("%s = %d %q %v", path, st, body, ok)
		}
	}
	if _, body, _ := Respond(FreePath, Package(10, 4, "")); !strings.Contains(body, `"CycleCapacityRemainPrecise":"4"`) || !strings.Contains(body, `"TotalCount":1`) {
		t.Errorf("免费包 = %s", body)
	}
	if _, _, ok := Respond(CheckinPath); ok {
		t.Error("签到本身应交给调用方")
	}
}

// TestToken 网页令牌：Bearer 优先，其次 session Cookie。
func TestToken(t *testing.T) {
	bearer := httptest.NewRequest(http.MethodPost, "/x", nil)
	bearer.Header.Set("Authorization", "Bearer at-1")
	if got := Token(bearer); got != "at-1" {
		t.Errorf("Bearer token = %q", got)
	}
	cookie := httptest.NewRequest(http.MethodGet, "/x", nil)
	cookie.AddCookie(&http.Cookie{Name: "session", Value: "at-2"})
	if got := Token(cookie); got != "at-2" {
		t.Errorf("Cookie token = %q", got)
	}
	if got := Token(httptest.NewRequest(http.MethodGet, "/x", nil)); got != "" {
		t.Errorf("无凭证 token = %q", got)
	}
}

// TestWebSession 登录码回 "dc."+令牌；client-login 按码种 session；别的路径不接管。
func TestWebSession(t *testing.T) {
	codeReq := httptest.NewRequest(http.MethodPost, DeviceAuthCodePath, nil)
	codeReq.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	if !WebSession(rec, codeReq) {
		t.Fatal("登录码路径应接管")
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deviceCode":"dc.tok"`) {
		t.Fatalf("登录码响应 = %d %s", rec.Code, rec.Body)
	}

	loginReq := httptest.NewRequest(http.MethodGet, ClientLoginPath+"?code=dc.tok&target=%2Fp", nil)
	rec = httptest.NewRecorder()
	if !WebSession(rec, loginReq) {
		t.Fatal("client-login 路径应接管")
	}
	if rec.Code != http.StatusFound {
		t.Fatalf("client-login 状态 = %d", rec.Code)
	}
	var session string
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" {
			session = c.Value
		}
	}
	if session != "tok" {
		t.Errorf("session Cookie = %q，应为 tok", session)
	}

	if WebSession(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/other", nil)) {
		t.Error("别的路径不应接管")
	}
}

// TestWrite 认识的路径写出状态码与响应体，别的路径什么都不写。
func TestWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	if !Write(rec, SummaryPath) || rec.Code != http.StatusNotFound {
		t.Fatalf("汇总 = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	if Write(rec, "/other") || rec.Body.Len() != 0 {
		t.Fatal("别的路径不应写")
	}
}
