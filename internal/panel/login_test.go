package panel

// login_test.go 面板扫码登录（login.go）的测试：发起设备授权、轮询完成落盘
// （CN 个人/企业、global），以及各失败分支。
//
// OAuth 三端点走包级 loginHTTP，测试里换成假 Transport；登录后的签到/余额/
// 注册激活/trial 走面板的 upstream.Client，同样由假上游接住。假上游按
// 「方法 + 主机 + 路径」分发，未登记请求 t.Errorf 并回 400。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

const (
	loginCNHost     = "www.workbuddy.cn"
	loginGlobalHost = "www.workbuddy.ai"

	loginStatePath   = "/v2/plugin/auth/state"
	loginTokenPath   = "/v2/plugin/auth/token"
	loginAccountPath = "/v2/plugin/login/account"

	checkinPathCN        = "/v2/billing/meter/daily-checkin"
	resourcePathCN       = "/v2/billing/meter/get-user-resource"
	resourcePathGlobal   = "/billing/meter/get-user-resource" // global 先试无 /v2 前缀
	enterpriseUsagePath  = "/v2/billing/meter/get-enterprise-user-usage"
	globalRegisterStatus = "/auth/realms/copilot/overseas/user/register"
	globalTrialPath      = "/billing/ide/trial"
)

// newLoginPanel 空号池面板 + 假上游；loginHTTP 同时指向假 Transport。
func newLoginPanel(t *testing.T) (*Panel, *autoUpstream, string) {
	t.Helper()
	up := &autoUpstream{t: t, route: map[string]autoHandler{}}
	dir := t.TempDir()
	p := New(Config{
		Version: "test",
		Pool:    pool.New(""),
		Upstream: &upstream.Client{
			HTTP:              &http.Client{Transport: up},
			GlobalEnabled:     true,
			ChatBaseCN:        "https://" + autoChatHost,
			BillingBaseCN:     "https://" + autoBillingHost,
			WebBaseCN:         "https://" + autoWebHost,
			ChatBaseGlobal:    "https://" + loginGlobalHost,
			BillingBaseGlobal: "https://" + loginGlobalHost,
		},
		AuthDir: dir,
	})
	old := loginHTTP
	loginHTTP = &http.Client{Transport: up}
	t.Cleanup(func() { loginHTTP = old })
	return p, up, dir
}

// loginRouteState / loginRouteToken / loginRouteAccount 登记 OAuth 端点响应。
func loginRouteState(up *autoUpstream, host, state, authURL string) {
	up.on(http.MethodPost, host, loginStatePath, "",
		autoOK(autoEnvelope(fmt.Sprintf(`{"state":%q,"authUrl":%q}`, state, authURL))))
}
func loginRouteToken(up *autoUpstream, host, data string) {
	up.on(http.MethodGet, host, loginTokenPath, "", autoOK(autoEnvelope(data)))
}
func loginRouteAccount(up *autoUpstream, host, data string) {
	up.on(http.MethodGet, host, loginAccountPath, "", autoOK(autoEnvelope(data)))
}

// loginResourceBody 个人余额响应（Response.Data.Accounts[]）。
func loginResourceBody(remain, total int64) string {
	return autoEnvelope(fmt.Sprintf(`{"Response":{"Data":{"Accounts":[{"CapacityRemain":%d,"CapacitySize":%d}]}}}`, remain, total))
}

// seedLogin 直接往面板塞一个进行中的登录会话。
func seedLogin(p *Panel, state, realm string) {
	p.loginMu.Lock()
	p.logins[state] = loginSession{created: time.Now(), realm: realm}
	p.loginMu.Unlock()
}

// loginStartOK 发起登录并断言 200，返回响应体。
func loginStartOK(t *testing.T, p *Panel, body string) map[string]any {
	t.Helper()
	rec := postJSON(t, p, "/panel/api/login/start", body)
	autoWantCode(t, rec, http.StatusOK)
	return autoDecode(t, rec)
}

// loginPollState 轮询指定 state。
func loginPollState(p *Panel, state string) *httptest.ResponseRecorder {
	return get(p, http.MethodGet, "/panel/api/login/poll?state="+state)
}

// validUID：空串、超长、路径字符、中文拒绝；字母数字与 - _ 放行。
func TestLoginUIDValidation(t *testing.T) {
	for _, uid := range []string{"", strings.Repeat("a", 65), "a.b", "a/b", `a\b`, "中文"} {
		if validUID(uid) {
			t.Fatalf("validUID(%q) 应为 false", uid)
		}
	}
	for _, uid := range []string{"a", "abc_123-XYZ", "248890d9-bb26-4131-87a7-4ec74d472344"} {
		if !validUID(uid) {
			t.Fatalf("validUID(%q) 应为 true", uid)
		}
	}
}

// 发起登录：realm 选择（global/其他/无 body）、请求头，以及旧会话清理。
func TestLoginStart(t *testing.T) {
	t.Run("global 走国际版端点", func(t *testing.T) {
		p, up, _ := newLoginPanel(t)
		loginRouteState(up, loginGlobalHost, "st-global-1", "https://auth.example/g")
		resp := loginStartOK(t, p, `{"realm":"global"}`)
		if resp["realm"] != "global" || resp["state"] != "st-global-1" || resp["url"] != "https://auth.example/g" {
			t.Fatalf("global 发起结果不对: %v", resp)
		}
		if got := len(up.requestsTo(http.MethodPost, loginGlobalHost, loginStatePath, "")); got != 1 {
			t.Fatalf("应走国际版端点，实际 %d 次", got)
		}
		if got := len(up.requestsTo(http.MethodPost, loginCNHost, loginStatePath, "")); got != 0 {
			t.Fatalf("不应走国内版端点，实际 %d 次", got)
		}
	})

	t.Run("其他 realm 与无 body 走国内版", func(t *testing.T) {
		for _, body := range []string{`{"realm":"weird"}`, `{"realm":"cn"}`, ``} {
			p, up, _ := newLoginPanel(t)
			loginRouteState(up, loginCNHost, "st-cn", "https://auth.example/c")
			resp := loginStartOK(t, p, body)
			if resp["realm"] != "cn" {
				t.Fatalf("body=%q 应回落 cn: %v", body, resp)
			}
			if got := len(up.requestsTo(http.MethodPost, loginCNHost, loginStatePath, "")); got != 1 {
				t.Fatalf("body=%q 应走国内版端点，实际 %d 次", body, got)
			}
		}
	})

	t.Run("请求头按 commonHeaders", func(t *testing.T) {
		p, up, _ := newLoginPanel(t)
		loginRouteState(up, loginCNHost, "st-cn", "https://auth.example/c")
		loginStartOK(t, p, `{}`)
		reqs := up.requestsTo(http.MethodPost, loginCNHost, loginStatePath, "")
		if len(reqs) != 1 {
			t.Fatalf("应有 1 次 state 请求，实际 %d", len(reqs))
		}
		h := reqs[0].Header
		if h.Get("Origin") != originRefererCN || h.Get("Referer") != originRefererCN+"/" ||
			h.Get("User-Agent") != clientUA || h.Get("Content-Type") != "application/json" ||
			h.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Fatalf("请求头不符合 commonHeaders: %v", h)
		}
	})

	t.Run("过期旧会话被清理", func(t *testing.T) {
		p, up, _ := newLoginPanel(t)
		loginRouteState(up, loginCNHost, "st-new", "https://auth.example/c")
		p.loginMu.Lock()
		p.logins["st-old"] = loginSession{created: time.Now().Add(-2 * loginTTL), realm: "cn"}
		p.loginMu.Unlock()
		loginStartOK(t, p, `{}`)
		p.loginMu.Lock()
		_, oldOK := p.logins["st-old"]
		_, newOK := p.logins["st-new"]
		p.loginMu.Unlock()
		if oldOK || !newOK {
			t.Fatalf("过期会话应清理、新会话应保留: old=%v new=%v", oldOK, newOK)
		}
	})
}

// 发起登录的上游失败形态：HTTP ≥300、非 JSON、code ≠ 0、缺 state/authUrl。
func TestLoginStartUpstreamFailures(t *testing.T) {
	cases := []struct {
		name string
		h    autoHandler
	}{
		{"HTTP 500", autoFail(500, `boom`)},
		{"不是 JSON", autoOK(`not json`)},
		{"code 非 0", autoOK(`{"code":5,"msg":"no"}`)},
		{"缺 state", autoOK(autoEnvelope(`{"state":"","authUrl":"https://a"}`))},
		{"缺 authUrl", autoOK(autoEnvelope(`{"state":"s"}`))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, up, _ := newLoginPanel(t)
			up.on(http.MethodPost, loginCNHost, loginStatePath, "", tc.h)
			rec := postJSON(t, p, "/panel/api/login/start", `{}`)
			autoWantCode(t, rec, http.StatusBadGateway)
		})
	}
}

// 轮询：缺 state 400、未知 state 404、等待中与空 accessToken 都是 done=false。
func TestLoginPollPending(t *testing.T) {
	t.Run("缺 state", func(t *testing.T) {
		p, _, _ := newLoginPanel(t)
		autoWantCode(t, get(p, http.MethodGet, "/panel/api/login/poll"), http.StatusBadRequest)
	})

	t.Run("未知 state", func(t *testing.T) {
		p, _, _ := newLoginPanel(t)
		autoWantCode(t, loginPollState(p, "nope"), http.StatusNotFound)
	})

	t.Run("token 接口还在等", func(t *testing.T) {
		p, up, _ := newLoginPanel(t)
		seedLogin(p, "st-cn", "cn")
		up.on(http.MethodGet, loginCNHost, loginTokenPath, "", autoOK(`{"code":1,"msg":"login ing"}`))
		rec := loginPollState(p, "st-cn")
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["done"] != false {
			t.Fatalf("等待中应 done=false: %v", resp)
		}
		if msg, _ := resp["message"].(string); !strings.Contains(msg, "code=1") {
			t.Fatalf("message 应带上游等待原因: %v", resp)
		}
	})

	t.Run("accessToken 为空", func(t *testing.T) {
		p, up, _ := newLoginPanel(t)
		seedLogin(p, "st-cn", "cn")
		loginRouteToken(up, loginCNHost, `{"accessToken":""}`)
		rec := loginPollState(p, "st-cn")
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["done"] != false || resp["message"] != "waiting for login" {
			t.Fatalf("空 token 应 done=false: %v", resp)
		}
	})
}

// 轮询失败形态：账号信息取不到 uid、uid 含非法字符（且不落盘）。
func TestLoginPollBadAccount(t *testing.T) {
	t.Run("取不到 uid 502", func(t *testing.T) {
		p, up, _ := newLoginPanel(t)
		seedLogin(p, "st-cn", "cn")
		loginRouteToken(up, loginCNHost, `{"accessToken":"at","refreshToken":"rt"}`)
		loginRouteAccount(up, loginCNHost, `{"uid":"","nickname":"x"}`)
		autoWantCode(t, loginPollState(p, "st-cn"), http.StatusBadGateway)
	})

	t.Run("uid 非法 502 且不落盘", func(t *testing.T) {
		p, up, dir := newLoginPanel(t)
		seedLogin(p, "st-cn", "cn")
		loginRouteToken(up, loginCNHost, `{"accessToken":"at","refreshToken":"rt"}`)
		loginRouteAccount(up, loginCNHost, `{"uid":"../evil","nickname":"x"}`)
		autoWantCode(t, loginPollState(p, "st-cn"), http.StatusBadGateway)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("读 AuthDir 失败: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("非法 uid 不得落盘，实际 %d 个文件", len(entries))
		}
	})
}

// 国内版个人号成功：落盘 cn、进池、签到一次、查余额，会话删除。
func TestLoginPollCNPersonalSuccess(t *testing.T) {
	p, up, dir := newLoginPanel(t)
	loginRouteState(up, loginCNHost, "st-cn", "https://auth.example/c")
	loginRouteToken(up, loginCNHost, `{"accessToken":"at-cn","refreshToken":"rt-cn","expiresIn":3600,"domain":"copilot.tencent.com"}`)
	loginRouteAccount(up, loginCNHost, `{"uid":"u-cn","nickname":"新号"}`)
	up.on(http.MethodPost, autoBillingHost, checkinPathCN, "", autoOK(autoEnvelope("{}")))
	up.on(http.MethodPost, autoBillingHost, resourcePathCN, "", autoOK(loginResourceBody(88, 100)))

	start := loginStartOK(t, p, `{}`)
	state := fmt.Sprint(start["state"])
	rec := loginPollState(p, state)
	autoWantCode(t, rec, http.StatusOK)
	resp := autoDecode(t, rec)
	if resp["done"] != true || resp["uid"] != "u-cn" || resp["nickname"] != "新号" ||
		resp["realm"] != "cn" || resp["credits"] != float64(88) || resp["credits_total"] != float64(100) {
		t.Fatalf("登录完成结果不对: %v", resp)
	}
	if msg, _ := resp["checkin_message"].(string); msg != "" {
		t.Fatalf("成功时 checkin_message 应为空: %v", resp)
	}

	a := loadAuth(t, dir, "u-cn")
	if a.Realm() != "cn" || a.AccessToken != "at-cn" || a.RefreshToken != "rt-cn" || a.Nickname != "新号" {
		t.Fatalf("落盘凭证不对: %+v realm=%s", a, a.Realm())
	}
	if p.cfg.Pool.AuthByUID("u-cn") == nil {
		t.Fatal("登录完成应热加载进池")
	}
	autoWantSequence(t, up.requests(),
		"POST "+loginCNHost+loginStatePath,
		"GET "+loginCNHost+loginTokenPath,
		"GET "+loginCNHost+loginAccountPath,
		"POST "+autoBillingHost+checkinPathCN,
		"POST "+autoBillingHost+resourcePathCN,
	)
	if got := len(up.requestsTo(http.MethodPost, autoBillingHost, checkinPathCN, "")); got != 1 {
		t.Fatalf("应签到一次，实际 %d", got)
	}
	// 会话完成后删除：同一个 state 再轮询是 404。
	autoWantCode(t, loginPollState(p, state), http.StatusNotFound)
}

// 国内企业号成功：跳过签到，照常查企业额度。
func TestLoginPollCNEnterpriseSuccess(t *testing.T) {
	p, up, dir := newLoginPanel(t)
	loginRouteToken(up, loginCNHost, `{"accessToken":"at-ent","refreshToken":"rt-ent","expiresIn":3600,"domain":"copilot.tencent.com"}`)
	loginRouteAccount(up, loginCNHost, `{"uid":"u-ent","enterpriseId":"corp-9","nickname":"企业号"}`)
	up.on(http.MethodPost, autoBillingHost, enterpriseUsagePath, "", autoOK(autoEnvelope(`{"credit":20,"limitNum":100}`)))
	seedLogin(p, "st-ent", "cn")

	rec := loginPollState(p, "st-ent")
	autoWantCode(t, rec, http.StatusOK)
	resp := autoDecode(t, rec)
	if resp["done"] != true || resp["credits"] != float64(80) || resp["credits_total"] != float64(100) {
		t.Fatalf("企业号余额应为 80/100: %v", resp)
	}
	if got := len(up.requestsTo(http.MethodPost, autoBillingHost, checkinPathCN, "")); got != 0 {
		t.Fatalf("企业号不应签到，实际 %d 次", got)
	}
	if got := len(up.requestsTo(http.MethodPost, autoBillingHost, enterpriseUsagePath, "")); got != 1 {
		t.Fatalf("应查一次企业额度，实际 %d", got)
	}
	if a := loadAuth(t, dir, "u-ent"); a.EnterpriseID != "corp-9" {
		t.Fatalf("企业 ID 未落盘: %+v", a)
	}
}

// 国际版成功：落盘 global，走注册激活 + trial，不签到。
func TestLoginPollGlobalSuccess(t *testing.T) {
	p, up, dir := newLoginPanel(t)
	loginRouteToken(up, loginGlobalHost, `{"accessToken":"at-glb","refreshToken":"rt-glb","expiresIn":3600,"domain":"www.workbuddy.ai"}`)
	loginRouteAccount(up, loginGlobalHost, `{"uid":"u-glb","nickname":"国际号"}`)
	up.on(http.MethodGet, loginGlobalHost, globalRegisterStatus, "", autoOK(`{"code":200}`))
	up.on(http.MethodPost, loginGlobalHost, globalTrialPath, "", autoOK(autoEnvelope("{}")))
	up.on(http.MethodPost, loginGlobalHost, resourcePathGlobal, "", autoOK(loginResourceBody(88, 100)))
	seedLogin(p, "st-glb", "global")

	rec := loginPollState(p, "st-glb")
	autoWantCode(t, rec, http.StatusOK)
	resp := autoDecode(t, rec)
	if resp["done"] != true || resp["realm"] != "global" || resp["credits"] != float64(88) {
		t.Fatalf("国际版登录结果不对: %v", resp)
	}
	if got := len(up.requestsTo(http.MethodPost, autoBillingHost, checkinPathCN, "")); got != 0 {
		t.Fatalf("国际版不应签到，实际 %d 次", got)
	}
	if got := len(up.requestsTo(http.MethodGet, loginGlobalHost, globalRegisterStatus, "")); got != 1 {
		t.Fatalf("应查注册激活状态一次，实际 %d", got)
	}
	if got := len(up.requestsTo(http.MethodPost, loginGlobalHost, globalTrialPath, "")); got != 1 {
		t.Fatalf("应领取 trial 一次，实际 %d", got)
	}
	if a := loadAuth(t, dir, "u-glb"); a.Realm() != "global" {
		t.Fatalf("国际版落盘 realm 应为 global: %s", a.Realm())
	}
}

// 签到 / 注册激活 / trial 失败：登录仍完成，失败原因拼进 checkin_message。
func TestLoginPollAuxFailures(t *testing.T) {
	t.Run("CN 签到失败仍完成", func(t *testing.T) {
		p, up, _ := newLoginPanel(t)
		loginRouteToken(up, loginCNHost, `{"accessToken":"at-cn","refreshToken":"rt-cn","expiresIn":3600,"domain":"copilot.tencent.com"}`)
		loginRouteAccount(up, loginCNHost, `{"uid":"u-cn","nickname":"新号"}`)
		up.on(http.MethodPost, autoBillingHost, checkinPathCN, "", autoFail(400, `{"code":10001,"msg":"checkin boom"}`))
		up.on(http.MethodPost, autoBillingHost, resourcePathCN, "", autoOK(loginResourceBody(88, 100)))
		seedLogin(p, "st-cn", "cn")

		rec := loginPollState(p, "st-cn")
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["done"] != true {
			t.Fatalf("签到失败不应影响登录: %v", resp)
		}
		if msg, _ := resp["checkin_message"].(string); !strings.Contains(msg, "checkin boom") {
			t.Fatalf("失败原因应进 checkin_message: %v", resp)
		}
		if got := len(up.requestsTo(http.MethodPost, autoBillingHost, resourcePathCN, "")); got != 1 {
			t.Fatalf("签到失败仍应查余额，实际 %d 次", got)
		}
	})

	t.Run("global 注册激活与 trial 失败用分号连接", func(t *testing.T) {
		p, up, _ := newLoginPanel(t)
		loginRouteToken(up, loginGlobalHost, `{"accessToken":"at-glb","refreshToken":"rt-glb","expiresIn":3600,"domain":"www.workbuddy.ai"}`)
		loginRouteAccount(up, loginGlobalHost, `{"uid":"u-glb","nickname":"国际号"}`)
		up.on(http.MethodGet, loginGlobalHost, globalRegisterStatus, "", autoOK(`{"code":1,"msg":"register boom"}`))
		up.on(http.MethodPost, loginGlobalHost, globalTrialPath, "", autoFail(400, `{"code":400,"msg":"trial boom"}`))
		up.on(http.MethodPost, loginGlobalHost, resourcePathGlobal, "", autoOK(loginResourceBody(88, 100)))
		seedLogin(p, "st-glb", "global")

		rec := loginPollState(p, "st-glb")
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["done"] != true {
			t.Fatalf("注册/trial 失败不应影响登录: %v", resp)
		}
		msg, _ := resp["checkin_message"].(string)
		if !strings.Contains(msg, "注册激活失败") || !strings.Contains(msg, "trial 领取失败") || !strings.Contains(msg, "；") {
			t.Fatalf("多段失败应用分号连接: %q", msg)
		}
		if got := len(up.requestsTo(http.MethodPost, autoBillingHost, checkinPathCN, "")); got != 0 {
			t.Fatalf("国际版不应签到，实际 %d 次", got)
		}
	})
}

// joinMsg：空段跳过，非空段用「；」连接。
func TestJoinMsg(t *testing.T) {
	if got := joinMsg("", "a", "", "b"); got != "a；b" {
		t.Fatalf("joinMsg 拼接不对: %q", got)
	}
	if got := joinMsg("", "", ""); got != "" {
		t.Fatalf("全空应返回空串: %q", got)
	}
	if got := joinMsg("only"); got != "only" {
		t.Fatalf("单段应原样返回: %q", got)
	}
}

// 地区列表：固定 7 个地区，顺序 HK、MO、SG、TH、PH、MY、ID。
func TestLoginRegions(t *testing.T) {
	p, _, _ := newLoginPanel(t)
	rec := get(p, http.MethodGet, "/panel/api/login/regions")
	autoWantCode(t, rec, http.StatusOK)
	resp := autoDecode(t, rec)
	regions, _ := resp["regions"].([]any)
	var codes []string
	for _, v := range regions {
		m, _ := v.(map[string]any)
		codes = append(codes, fmt.Sprint(m["code"]))
	}
	if strings.Join(codes, ",") != "HK,MO,SG,TH,PH,MY,ID" {
		t.Fatalf("地区顺序不对: %v", codes)
	}
}
