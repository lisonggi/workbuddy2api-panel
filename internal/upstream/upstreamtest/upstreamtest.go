// Package upstreamtest 给其他包的测试用：按官方接口的形状造积分、签到状态与网页登录态
// （登录码 → client-login）的假响应，免得每个包的假上游各写一遍。只在测试里导入。
package upstreamtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// UUIDv7 / UUIDv7Hex 官方会话 id（带连字符的 UUID v7）与轮次 id、消息 id（去掉连字符的 UUID v7）的形状。
var (
	UUIDv7    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	UUIDv7Hex = regexp.MustCompile(`^[0-9a-f]{12}7[0-9a-f]{3}[89ab][0-9a-f]{15}$`)
)

// 网页登录态的两个端点（与 internal/upstream 的常量相同，upstream 包里有测试核对），以及
// client-login 跳去的页面：网关照浏览器跟随跳转，会 GET 一次这个页面。
const (
	DeviceAuthCodePath = "/v2/plugin/device/auth/code"
	ClientLoginPath    = "/console/client-login"
	WebPagePrefix      = "/profile"
)

// IsWebSessionPath 是不是网页登录流程的请求（登录码、client-login、跳转后打开的页面）。
// 假上游统计业务请求时用它排除。
func IsWebSessionPath(path string) bool {
	return path == DeviceAuthCodePath || path == ClientLoginPath || isWebPage(path)
}

func isWebPage(path string) bool {
	return path == WebPagePrefix || strings.HasPrefix(path, WebPagePrefix+"/")
}

// Token 从请求里取账号令牌：Authorization: Bearer 优先，否则 session Cookie。
// 网页请求改用 Cookie 登录态后，原来按 Authorization 认账号的假上游改用它。
func Token(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie("session"); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

// WebSessionResponse 按官方形状答复网页登录态的请求，供自制 RoundTripper 使用：
//   - 登录码：data.deviceCode = "dc." + 请求里的令牌（Bearer）；
//   - client-login：按 code 去掉 "dc." 前缀种 session Cookie，302 到成长中心页面；
//   - 网页页面（GET /profile…）：200 和一段 HTML。
//
// ok=false 表示不是这些路径，调用方自己处理。不校验请求体。
func WebSessionResponse(r *http.Request) (status int, header http.Header, body string, ok bool) {
	switch r.URL.Path {
	case DeviceAuthCodePath:
		token := Token(r)
		if token == "" {
			return http.StatusUnauthorized, nil, `{"code":10034,"msg":"refresh token is empty"}`, true
		}
		code, _ := json.Marshal("dc." + token)
		return http.StatusOK, http.Header{"Content-Type": []string{"application/json"}},
			`{"code":0,"data":{"deviceCode":` + string(code) + `,"expiresIn":300}}`, true
	case ClientLoginPath:
		code := r.URL.Query().Get("code")
		token := strings.TrimPrefix(code, "dc.")
		if token == "" || token == code {
			return http.StatusBadRequest, nil, `{"code":400,"msg":"invalid code"}`, true
		}
		header := http.Header{"Content-Type": []string{"text/html"}}
		header.Add("Set-Cookie", "session="+token+"; Path=/; Max-Age=604800; HttpOnly; SameSite=Lax")
		header.Set("Location", "/profile/growth-center")
		return http.StatusFound, header, "", true
	}
	if r.Method == http.MethodGet && isWebPage(r.URL.Path) {
		return http.StatusOK, http.Header{"Content-Type": []string{"text/html"}}, "<!doctype html><title>WorkBuddy</title>", true
	}
	return 0, nil, "", false
}

// WebSessionRoundTrip 是 WebSessionResponse 的 RoundTripper 版本：ok=true 时 resp 可直接返回。
func WebSessionRoundTrip(req *http.Request) (*http.Response, bool) {
	status, header, body, ok := WebSessionResponse(req)
	if !ok {
		return nil, false
	}
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}, true
}

// WebSession 是 WebSessionResponse 的 http.Handler 版本；返回 true 表示已答复。
func WebSession(w http.ResponseWriter, r *http.Request) bool {
	status, header, body, ok := WebSessionResponse(r)
	if !ok {
		return false
	}
	for k, vs := range header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
	return true
}

// 官方桌面端查积分与签到用的接口（与 internal/upstream 的常量相同，upstream 包里有测试核对）。
const (
	SummaryPath       = "/billing/meter/get-user-resource-summary"
	PaidPath          = "/billing/meter/get-user-resource-paid-packages"
	FreePath          = "/billing/meter/get-user-resource-free-packages"
	CheckinStatusPath = "/v2/billing/meter/checkin-activity-status"
	CheckinPath       = "/v2/billing/meter/daily-checkin"
)

// Package 一个按周期发放的包：额度 size、剩余 remain，extra 是追加的 JSON 字段（可空）。
func Package(size, remain int64, extra string) string {
	s := fmt.Sprintf(`{"CycleCapacitySizePrecise":"%d","CycleCapacityRemainPrecise":"%d"`, size, remain)
	if extra != "" {
		s += "," + extra
	}
	return s + "}"
}

// List 逐包接口的响应：data.Accounts + TotalCount。
func List(packages ...string) string {
	return fmt.Sprintf(`{"code":0,"data":{"Accounts":[%s],"TotalCount":%d}}`, strings.Join(packages, ","), len(packages))
}

// Respond 给积分三个接口和签到状态的默认响应：汇总 404（网关改用逐包合计）、付费包为空、
// 免费包为 packages、签到状态为活动进行中且今天未签。别的路径 ok=false，由调用方处理。
func Respond(path string, packages ...string) (status int, body string, ok bool) {
	switch path {
	case SummaryPath:
		return http.StatusNotFound, `{"code":404,"msg":"not found"}`, true
	case PaidPath:
		return http.StatusOK, List(), true
	case FreePath:
		return http.StatusOK, List(packages...), true
	case CheckinStatusPath:
		return http.StatusOK, `{"code":0,"data":{"active":true,"today_checked_in":false}}`, true
	}
	return 0, "", false
}

// Write 把 Respond 的结果写进 w；路径不认识时返回 false，什么都不写。
func Write(w http.ResponseWriter, path string, packages ...string) bool {
	status, body, ok := Respond(path, packages...)
	if !ok {
		return false
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
	return true
}
