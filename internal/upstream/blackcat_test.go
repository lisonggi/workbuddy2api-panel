package upstream

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
)

// upReq 假上游记录的请求（断言方法/路径/头/体）。
type upReq struct {
	method string
	url    string
	path   string
	query  string
	header http.Header
	body   []byte
}

// upRoute 单端点响应构造函数（同一端点可能被多次调用，每次重新造响应）。
type upRoute func(r *upReq) (*http.Response, error)

// upFake 通用假上游：按 "METHOD 路径" 分发；未预设的请求记测试失败并回 400。
type upFake struct {
	t      *testing.T
	mu     sync.Mutex
	reqs   []*upReq
	routes map[string]upRoute
}

func newUpFake(t *testing.T) *upFake {
	f := &upFake{t: t, routes: map[string]upRoute{}}
	// 网页登录态的两个端点统一由 upstreamtest 答复：登录码回 "dc."+令牌，client-login 按码
	// 种 session Cookie，免得每个走 webJSON 的用例各自搭登录流程。需要特制（如 401）的用例
	// 用 onFunc 覆盖同名路由。
	webSession := func(r *upReq) (*http.Response, error) {
		fakeReq := &http.Request{
			Method: r.method,
			URL:    &url.URL{Path: r.path, RawQuery: r.query},
			Header: r.header,
		}
		status, hdr, body, ok := upstreamtest.WebSessionResponse(fakeReq)
		if !ok {
			f.t.Errorf("网页登录态端点未识别: %s %s", r.method, r.path)
			return jsonResp(http.StatusBadRequest, `{"code":400,"msg":"unexpected"}`), nil
		}
		return &http.Response{
			StatusCode: status,
			Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
			Header:     hdr,
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}
	f.onFunc(http.MethodPost, upstreamtest.DeviceAuthCodePath, webSession)
	f.onFunc(http.MethodGet, upstreamtest.ClientLoginPath, webSession)
	// client-login 跳去的页面（网关照浏览器跟随跳转）。
	for _, page := range []string{webPageGrowthCenter, webPageUsage, webPageProfile, webPageAccountSettings} {
		f.onFunc(http.MethodGet, page, webSession)
	}
	return f
}

func (f *upFake) on(method, path string, status int, body string) {
	f.routes[method+" "+path] = func(*upReq) (*http.Response, error) {
		return jsonResp(status, body), nil
	}
}

// onHost 按 host 注册响应（同一路径在 chat 域与 web 域行为不同的降级用例）。
func (f *upFake) onHost(host, method, path string, status int, body string) {
	f.routes[host+"|"+method+" "+path] = func(*upReq) (*http.Response, error) {
		return jsonResp(status, body), nil
	}
}

func (f *upFake) onFunc(method, path string, fn upRoute) {
	f.routes[method+" "+path] = fn
}

func (f *upFake) roundTrip(r *http.Request) (*http.Response, error) {
	var raw []byte
	if r.Body != nil {
		raw, _ = io.ReadAll(r.Body)
	}
	rec := &upReq{
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
	fn, ok := f.routes[r.URL.Host+"|"+r.Method+" "+r.URL.Path]
	if !ok {
		fn, ok = f.routes[r.Method+" "+r.URL.Path]
	}
	if !ok {
		f.t.Errorf("假上游收到未预设请求: %s %s", r.Method, r.URL.String())
		return jsonResp(400, `{"code":400,"msg":"unexpected"}`), nil
	}
	return fn(rec)
}

func (f *upFake) client() *http.Client { return &http.Client{Transport: rtFunc(f.roundTrip)} }

func (f *upFake) seen() []*upReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*upReq(nil), f.reqs...)
}

// bizSeen 只返回业务请求，去掉网页登录流程的请求（登录码、client-login、跳转后的页面）。
func (f *upFake) bizSeen() []*upReq {
	var out []*upReq
	for _, r := range f.seen() {
		if upstreamtest.IsWebSessionPath(r.path) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (f *upFake) count(method, path string) int {
	n := 0
	for _, r := range f.seen() {
		if r.method == method && r.path == path {
			n++
		}
	}
	return n
}

const upBase = "https://up.example"

// upClient 默认国内版客户端：三个 base 都指向假上游。
func upClient(f *upFake) *Client {
	return &Client{HTTP: f.client(), ChatBaseCN: upBase, BillingBaseCN: upBase, WebBaseCN: upBase, GlobalEnabled: true}
}

func upAuth() *auth.Auth {
	return &auth.Auth{AccessToken: "at", UID: "u-1", Nickname: "测试号", ExpiresAt: 9999999999}
}

func upTasksJSON(items string) string { return `{"code":0,"data":{"tasks":[` + items + `]}}` }

// TestInNightWindow 夜猫子窗口边界：23:00–08:00（含 23 点与 7 点，不含 8 点）。
func TestInNightWindow(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"22:59 窗口外", time.Date(2026, 10, 19, 22, 59, 0, 0, time.Local), false},
		{"23:00 窗口起点", time.Date(2026, 10, 19, 23, 0, 0, 0, time.Local), true},
		{"00:30 半夜", time.Date(2026, 10, 20, 0, 30, 0, 0, time.Local), true},
		{"07:59 窗口末尾", time.Date(2026, 10, 20, 7, 59, 59, 0, time.Local), true},
		{"08:00 窗口外", time.Date(2026, 10, 20, 8, 0, 0, 0, time.Local), false},
		{"12:00 白天", time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local), false},
	}
	for _, tc := range cases {
		if got := InNightWindow(tc.at); got != tc.want {
			t.Errorf("%s: InNightWindow(%s) = %v，应为 %v", tc.name, tc.at.Format("15:04"), got, tc.want)
		}
	}
}

// TestBlackcatNeed 剩余差额：任务不存在/已领取/已达标返回 0，否则 Target−Current；
// 拉取任务列表失败返回错误。
func TestBlackcatNeed(t *testing.T) {
	cases := []struct {
		name string
		task string
		want int64
	}{
		{"没有 black_cat 任务", `{"task_code":"other","target":3,"current":1}`, 0},
		{"已领取", `{"task_code":"black_cat","target":3,"current":1,"accept_status":"claimed"}`, 0},
		{"已达标", `{"task_code":"black_cat","target":3,"current":3}`, 0},
		{"超额完成", `{"task_code":"black_cat","target":3,"current":5}`, 0},
		{"差 2 次", `{"task_code":"black_cat","target":3,"current":1}`, 2},
		{"差 1 次（进度来自 progress）", `{"task_code":"black_cat","progress":{"current":2,"target":3}}`, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.on(http.MethodGet, tasksSubscribeStatusPath, 200, `{"code":0,"data":{}}`)
			f.on(http.MethodGet, tasksListPath, 200, upTasksJSON(tc.task))
			c := upClient(f)
			got, err := c.BlackcatNeed(upAuth())
			if err != nil {
				t.Fatalf("BlackcatNeed: %v", err)
			}
			if got != tc.want {
				t.Errorf("need = %d，应为 %d", got, tc.want)
			}
			reqs := f.seen()
			r := reqs[len(reqs)-1]
			if r.method != http.MethodGet || r.path != tasksListPath {
				t.Errorf("请求 = %s %s", r.method, r.path)
			}
		})
	}

	t.Run("列表请求失败", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, tasksSubscribeStatusPath, 200, `{"code":0,"data":{}}`)
		f.on(http.MethodGet, tasksListPath, 400, `{"code":400,"msg":"bad request"}`)
		c := upClient(f)
		if _, err := c.BlackcatNeed(upAuth()); err == nil {
			t.Fatal("上游 400 应返回错误")
		}
	})
}

// useZeroNightGap 把夜猫子对话间隔调成 0（测试不需要真等）。
func useZeroNightGap(t *testing.T) {
	t.Helper()
	old := nightChatGap
	nightChatGap = 0
	t.Cleanup(func() { nightChatGap = old })
}

// TestRunNightChatsSuccess 成功完成 need 次对话：每次对话 + 一次事件上报，
// 返回成功次数、无错误。
func TestRunNightChatsSuccess(t *testing.T) {
	useZeroNightGap(t)
	f := newUpFake(t)
	f.on(http.MethodPost, chatCompletionsPath, 200, "data: {\"id\":\"x\"}\n\n")
	f.on(http.MethodPost, reportPath, 200, `{"code":0,"msg":"OK"}`)
	c := upClient(f)

	ok, err := c.RunNightChats(upAuth(), 3)
	if err != nil || ok != 3 {
		t.Fatalf("ok=%d err=%v，应为 3/nil", ok, err)
	}
	if n := f.count(http.MethodPost, chatCompletionsPath); n != 3 {
		t.Errorf("对话请求 %d 次，应为 3", n)
	}
	if n := f.count(http.MethodPost, reportPath); n != 3 {
		t.Errorf("上报请求 %d 次，应为 3", n)
	}
	// 对话体固定 glm-5.2、流式；上报体带 glm-5.2 与 wb2api-night 会话前缀。
	for _, r := range f.seen() {
		if r.path == chatCompletionsPath && !strings.Contains(string(r.body), `"model":"glm-5.2"`) {
			t.Errorf("对话体 model 不是 glm-5.2: %s", r.body)
		}
		if r.path == reportPath && !strings.Contains(string(r.body), "wb2api-night-") {
			t.Errorf("上报体缺少 wb2api-night 会话: %s", r.body)
		}
	}
}

// TestRunNightChatsConversationFailure 第 k 次对话失败：返回前面成功次数和错误，
// 错误信息含“第 k 次对话失败”。
func TestRunNightChatsConversationFailure(t *testing.T) {
	useZeroNightGap(t)
	f := newUpFake(t)
	var calls int
	f.onFunc(http.MethodPost, chatCompletionsPath, func(*upReq) (*http.Response, error) {
		calls++
		if calls == 2 {
			return jsonResp(400, `{"code":400,"msg":"chat boom"}`), nil
		}
		return jsonResp(200, "data: {}\n\n"), nil
	})
	f.on(http.MethodPost, reportPath, 200, `{"code":0,"msg":"OK"}`)
	c := upClient(f)

	ok, err := c.RunNightChats(upAuth(), 3)
	if ok != 1 {
		t.Errorf("ok = %d，应为 1", ok)
	}
	if err == nil || !strings.Contains(err.Error(), "第 2 次对话失败") {
		t.Fatalf("err = %v，应含“第 2 次对话失败”", err)
	}
	if n := f.count(http.MethodPost, reportPath); n != 1 {
		t.Errorf("失败后不应继续上报，上报次数 = %d", n)
	}
}

// TestRunNightChatsReportFailure 第 k 次上报失败：返回前面成功次数和错误，
// 错误信息含“第 k 次上报失败”。
func TestRunNightChatsReportFailure(t *testing.T) {
	useZeroNightGap(t)
	f := newUpFake(t)
	f.on(http.MethodPost, chatCompletionsPath, 200, "data: {}\n\n")
	var reports int
	f.onFunc(http.MethodPost, reportPath, func(*upReq) (*http.Response, error) {
		reports++
		if reports == 2 {
			return jsonResp(400, `{"code":400,"msg":"report boom"}`), nil
		}
		return jsonResp(200, `{"code":0,"msg":"OK"}`), nil
	})
	c := upClient(f)

	ok, err := c.RunNightChats(upAuth(), 3)
	if ok != 1 {
		t.Errorf("ok = %d，应为 1", ok)
	}
	if err == nil || !strings.Contains(err.Error(), "第 2 次上报失败") {
		t.Fatalf("err = %v，应含“第 2 次上报失败”", err)
	}
}

// TestRunNightChatsTransportError 传输层错误也归入对话失败。
func TestRunNightChatsTransportError(t *testing.T) {
	useZeroNightGap(t)
	f := newUpFake(t)
	f.onFunc(http.MethodPost, chatCompletionsPath, func(*upReq) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	})
	c := upClient(f)
	ok, err := c.RunNightChats(upAuth(), 1)
	if ok != 0 || err == nil || !strings.Contains(err.Error(), "第 1 次对话失败") {
		t.Fatalf("ok=%d err=%v，应含“第 1 次对话失败”", ok, err)
	}
}

// TestClaimGift 新手礼包按网页顺序：先 POST check-gift-claimed，active 且未领才 POST
// claim-gift。未开放、已领、查询失败都不发领取请求。
func TestClaimGift(t *testing.T) {
	const (
		checkPath = "/billing/meter/check-gift-claimed"
		claimPath = "/billing/meter/claim-gift"
	)

	t.Run("正常领取", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, checkPath, 200, `{"code":0,"data":{"active":true,"claimed":false}}`)
		f.on(http.MethodPost, claimPath, 200, `{"code":0,"data":{"credit":100}}`)
		credit, err := upClient(f).ClaimGift(upAuth())
		if err != nil || credit != 100 {
			t.Fatalf("credit=%d err=%v，应为 100/nil", credit, err)
		}
		reqs := f.bizSeen()
		if len(reqs) != 2 {
			t.Fatalf("请求数 = %d，应为 check + claim 两次", len(reqs))
		}
		if reqs[0].method != http.MethodPost || reqs[0].path != checkPath {
			t.Errorf("第一次请求 = %s %s，应为 POST %s", reqs[0].method, reqs[0].path, checkPath)
		}
		if reqs[1].method != http.MethodPost || reqs[1].path != claimPath {
			t.Errorf("第二次请求 = %s %s，应为 POST %s", reqs[1].method, reqs[1].path, claimPath)
		}
		for _, r := range reqs {
			if len(r.body) != 0 {
				t.Errorf("%s 不应带请求体，got %q", r.path, r.body)
			}
			if got := r.header.Get("Content-Type"); got != "" {
				t.Errorf("%s 无请求体不应带 Content-Type，got %q", r.path, got)
			}
			if got := r.header.Get("Authorization"); got != "" {
				t.Errorf("%s 不应带 Authorization，got %q", r.path, got)
			}
			if got := r.header.Get("Cookie"); !strings.Contains(got, "session=at") {
				t.Errorf("%s Cookie = %q，应含登录态 session=at", r.path, got)
			}
			if got := r.header.Get("Referer"); got != upBase+webPageUsage {
				t.Errorf("%s Referer = %q，应为 %q", r.path, got, upBase+webPageUsage)
			}
		}
	})

	t.Run("未开放", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, checkPath, 200, `{"code":0,"data":{"active":false,"claimed":false}}`)
		_, err := upClient(f).ClaimGift(upAuth())
		if err == nil || !strings.Contains(err.Error(), "新手礼包未开放") {
			t.Fatalf("未开放应返回错误，got %v", err)
		}
		if n := len(f.bizSeen()); n != 1 {
			t.Errorf("请求数 = %d，未开放不应领奖", n)
		}
	})

	t.Run("已领", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, checkPath, 200, `{"code":0,"data":{"active":true,"claimed":true}}`)
		_, err := upClient(f).ClaimGift(upAuth())
		if err == nil || !strings.Contains(err.Error(), "已领取") {
			t.Fatalf("已领应返回错误，got %v", err)
		}
		if n := len(f.bizSeen()); n != 1 {
			t.Errorf("请求数 = %d，已领不应再次领奖", n)
		}
	})

	t.Run("查询失败", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, checkPath, 400, `{"code":400,"msg":"check boom"}`)
		_, err := upClient(f).ClaimGift(upAuth())
		if err == nil || !strings.Contains(err.Error(), "check boom") {
			t.Fatalf("查询失败应原样返回错误，got %v", err)
		}
		if n := len(f.bizSeen()); n != 1 {
			t.Errorf("请求数 = %d，查询失败不应领奖", n)
		}
	})
}

// TestHeatmapYesterdayMissed 昨日漏签判定：昨天 score=0 → true；>0 → false；
// 没有昨天的格子 → false；日期短于 10 位的格子忽略。
func TestHeatmapYesterdayMissed(t *testing.T) {
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"昨天 score=0", `{"cells":[{"date":"` + yesterday + `","score":0}]}`, true},
		{"昨天 score>0", `{"cells":[{"date":"` + yesterday + `","score":3}]}`, false},
		{"没有昨天的格子", `{"cells":[{"date":"2020-01-01","score":0}]}`, false},
		{"空列表", `{"cells":[]}`, false},
		{"日期短字段忽略", `{"cells":[{"date":"2020","score":0},{"date":"` + yesterday + `T00:00:00","score":0}]}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.on(http.MethodGet, "/activity/growth/heatmap", 200, `{"code":0,"data":`+tc.data+`}`)
			got, err := upClient(f).HeatmapYesterdayMissed(upAuth())
			if err != nil {
				t.Fatalf("HeatmapYesterdayMissed: %v", err)
			}
			if got != tc.want {
				t.Errorf("missed = %v，应为 %v（昨天 %s）", got, tc.want, yesterday)
			}
		})
	}

	t.Run("请求失败", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, "/activity/growth/heatmap", 400, `{"code":400,"msg":"boom"}`)
		if _, err := upClient(f).HeatmapYesterdayMissed(upAuth()); err == nil {
			t.Fatal("上游 400 应返回错误")
		}
	})

	t.Run("data 形态不对", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, "/activity/growth/heatmap", 200, `{"code":0,"data":"not-an-object"}`)
		if _, err := upClient(f).HeatmapYesterdayMissed(upAuth()); err == nil {
			t.Fatal("data 不是对象应返回解析错误")
		}
	})
}

// TestUseMakeupCard 补签卡：POST target_date；成功 nil，失败返回错误。
func TestUseMakeupCard(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, "/activity/growth/makeup-cards/use", 200, `{"code":0,"msg":"OK"}`)
	c := upClient(f)
	if err := c.UseMakeupCard(upAuth(), "2026-10-18"); err != nil {
		t.Fatalf("UseMakeupCard: %v", err)
	}
	r := f.bizSeen()[0]
	var body map[string]any
	if err := json.Unmarshal(r.body, &body); err != nil {
		t.Fatalf("请求体: %v", err)
	}
	if body["target_date"] != "2026-10-18" {
		t.Errorf("target_date = %v", body["target_date"])
	}

	f2 := newUpFake(t)
	f2.on(http.MethodPost, "/activity/growth/makeup-cards/use", 400, `{"code":400,"msg":"no card"}`)
	if err := upClient(f2).UseMakeupCard(upAuth(), "2026-10-18"); err == nil || !strings.Contains(err.Error(), "no card") {
		t.Fatalf("应返回带 msg 的错误，got %v", err)
	}
}
