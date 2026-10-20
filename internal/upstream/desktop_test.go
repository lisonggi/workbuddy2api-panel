package upstream

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// testDesktopUA 任务中心桌面端请求的 UA：国内版桌面端 5.8.3、内置 CLI 2.161.0（跟随网关其余请求）。
const testDesktopUA = "WorkBuddy/5.8.3 WorkBuddy/5.8.3 CLI/2.161.0"

// TestReportDesktopEventFingerprint 断言桌面指纹上报：走 chatBase、UA 为桌面形状、
// 事件数组自动注入 workbuddy-desktop 公共指纹（覆盖同名业务键）。
func TestReportDesktopEventFingerprint(t *testing.T) {
	var got []map[string]any
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/report" {
			t.Errorf("path=%s want /v2/report", r.URL.Path)
		}
		gotUA = r.Header.Get("User-Agent")
		if r.Header.Get("X-User-Id") != "u-dt" {
			t.Errorf("X-User-Id=%q want u-dt", r.Header.Get("X-User-Id"))
		}
		if r.Header.Get("X-Product") != "SaaS" {
			t.Errorf("X-Product=%q want SaaS", r.Header.Get("X-Product"))
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("body must be array: %v", err)
		}
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	err := c.ReportDesktopEvent(&auth.Auth{AccessToken: "at", UID: "u-dt", Nickname: "RenJie"},
		DesktopEvent{"eventCode": "agent_task_created", "mode": "craft"})
	if err != nil {
		t.Fatalf("report desktop: %v", err)
	}
	if gotUA != testDesktopUA {
		t.Errorf("UA=%q want %q", gotUA, testDesktopUA)
	}
	if len(got) != 1 {
		t.Fatalf("events=%d want 1", len(got))
	}
	ev := got[0]
	if ev["extName"] != "workbuddy-desktop" {
		t.Errorf("extName=%v want workbuddy-desktop", ev["extName"])
	}
	if ev["ideType"] != "WorkBuddy" || ev["ideName"] != "WorkBuddy" {
		t.Errorf("ideType/ideName=%v/%v want WorkBuddy", ev["ideType"], ev["ideName"])
	}
	if ev["userId"] != "u-dt" {
		t.Errorf("userId=%v want u-dt", ev["userId"])
	}
	if ev["userNickname"] != "RenJie" {
		t.Errorf("userNickname=%v want RenJie", ev["userNickname"])
	}
	if ev["mode"] != "craft" {
		t.Errorf("业务字段 mode=%v 被指纹覆盖或缺失", ev["mode"])
	}
	// machineId 应为 uid 稳定派生（36 hex）。
	if len(ev["machineId"].(string)) != 36 {
		t.Errorf("machineId 长度=%d want 36", len(ev["machineId"].(string)))
	}
}

// TestDesktopChatSequenceShape 断言完整对话事件链的事件码顺序与关键字段。
func TestDesktopChatSequenceShape(t *testing.T) {
	events := DesktopChatSequence("conv-1", "req-1", "msg-1", "fast-model", "fast-model")
	wantCodes := []string{
		"agent_task_created", "chat_message_send", "chat_request_send",
		"chat_message_response", "chat_message_status", "chat_request_response",
	}
	if len(events) != len(wantCodes) {
		t.Fatalf("events=%d want %d", len(events), len(wantCodes))
	}
	for i, want := range wantCodes {
		if events[i]["eventCode"] != want {
			t.Errorf("events[%d].eventCode=%v want %v", i, events[i]["eventCode"], want)
		}
	}
	resp := events[3]
	if resp["isSuccessful"] != true {
		t.Errorf("chat_message_response.isSuccessful=%v want true（RichMeow 判据）", resp["isSuccessful"])
	}
	if resp["finishReason"] != "stop" {
		t.Errorf("finishReason=%v want stop", resp["finishReason"])
	}
	if resp["conversationId"] != "conv-1" {
		t.Errorf("conversationId=%v want conv-1", resp["conversationId"])
	}
}

// TestSetAppearanceTheme 断言主题设置端点形状（点亮 Hp_Appearance 的 API）。
func TestSetAppearanceTheme(t *testing.T) {
	var got map[string]any
	var gotHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/user-asset/appearance/set" {
			t.Errorf("path=%s want /v2/user-asset/appearance/set", r.URL.Path)
		}
		gotHeader = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &got)
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	a := &auth.Auth{AccessToken: "at", UID: "u1", Domain: "www.codebuddy.cn"}
	if err := c.SetAppearanceTheme(a, "theme-tkmw7j"); err != nil {
		t.Fatalf("set theme: %v", err)
	}
	if gotHeader.Get("User-Agent") != testDesktopUA {
		t.Errorf("UA=%q want desktop UA", gotHeader.Get("User-Agent"))
	}
	for k, want := range map[string]string{
		"Authorization":    "Bearer at",
		"Content-Type":     "application/json",
		"User-Agent":       testDesktopUA,
		"X-Domain":         "www.codebuddy.cn",
		"X-Product":        "SaaS",
		"X-Requested-With": "XMLHttpRequest",
		"X-User-Id":        "u1",
	} {
		if got := gotHeader.Get(k); got != want {
			t.Errorf("%s = %q，应为 %q", k, got, want)
		}
	}
	if got := gotHeader.Get("X-Request-ID"); !hex32.MatchString(got) {
		t.Errorf("X-Request-ID = %q，应为 32 位十六进制", got)
	}
	if got["kind"] != "theme" || got["resource_key"] != "theme-tkmw7j" {
		t.Errorf("body=%v want kind=theme resource_key=theme-tkmw7j", got)
	}
}

// TestDeriveID 设备标识稳定派生：同账号同 salt 恒定，换账号/换 salt 变化，
// 形状为 36 位 hex。
func TestDeriveID(t *testing.T) {
	a := &auth.Auth{UID: "u-1"}
	first := deriveID(a, "machine")
	if len(first) != 36 {
		t.Fatalf("len = %d，应为 36", len(first))
	}
	if _, err := hex.DecodeString(first); err != nil {
		t.Fatalf("不是 hex: %v", err)
	}
	if again := deriveID(a, "machine"); again != first {
		t.Errorf("同账号同 salt 结果应相同: %q vs %q", first, again)
	}
	if other := deriveID(&auth.Auth{UID: "u-2"}, "machine"); other == first {
		t.Error("换账号结果应变")
	}
	if other := deriveID(a, "session"); other == first {
		t.Error("换 salt 结果应变")
	}
}

// TestReportDesktopEventErrors 空事件不请求；上游 400 返回错误。
func TestReportDesktopEventErrors(t *testing.T) {
	f := newUpFake(t)
	c := upClient(f)
	if err := c.ReportDesktopEvent(upAuth()); err == nil {
		t.Fatal("空事件应返回错误")
	}
	if n := len(f.seen()); n != 0 {
		t.Fatalf("空事件不应发请求，实际 %d", n)
	}

	f.on(http.MethodPost, desktopReportPath, 400, `{"code":400,"msg":"report rejected"}`)
	if err := c.ReportDesktopEvent(upAuth(), DesktopEvent{"eventCode": "x"}); err == nil {
		t.Fatal("上游 400 应返回错误")
	}
}

// TestReportDesktopEventHeadersAndOverride 桌面上报的请求头与业务字段覆盖指纹
// （设备标识对齐真实设备）。
func TestReportDesktopEventHeadersAndOverride(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, desktopReportPath, 200, `{"code":0,"msg":"OK"}`)
	c := upClient(f)
	a := &auth.Auth{AccessToken: "at", UID: "u-9", Nickname: "昵称", Domain: "www.codebuddy.cn"}
	err := c.ReportDesktopEvent(a, DesktopEvent{
		"eventCode": "chat_message_response", "machineId": "real-machine", "qimei36": "q-36",
	})
	if err != nil {
		t.Fatalf("ReportDesktopEvent: %v", err)
	}
	r := f.seen()[0]
	for k, want := range map[string]string{
		"Authorization":    "Bearer at",
		"Accept":           "application/json, text/plain, */*",
		"Content-Type":     "application/json;charset=UTF-8",
		"User-Agent":       testDesktopUA,
		"X-Domain":         "www.codebuddy.cn",
		"X-Product":        "SaaS",
		"X-Requested-With": "XMLHttpRequest",
		"X-User-Id":        "u-9",
	} {
		if got := r.header.Get(k); got != want {
			t.Errorf("%s = %q，应为 %q", k, got, want)
		}
	}
	// 官方 CommonHeaderHttpInterceptor 的 X-Request-ID：32 位十六进制，不是十进制时间戳。
	if got := r.header.Get("X-Request-ID"); !hex32.MatchString(got) {
		t.Errorf("X-Request-ID = %q，应为 32 位十六进制", got)
	}
	var events []map[string]any
	if err := json.Unmarshal(r.body, &events); err != nil {
		t.Fatalf("body 应为数组: %v", err)
	}
	ev := events[0]
	if ev["machineId"] != "real-machine" || ev["qimei36"] != "q-36" {
		t.Errorf("业务字段应覆盖指纹: %v", ev)
	}
	if ev["eventCode"] != "chat_message_response" || ev["userId"] != "u-9" {
		t.Errorf("事件字段 = %v", ev)
	}
}

// TestSetAppearanceThemeError 主题接口失败返回错误（成功形状已有用例）。
func TestSetAppearanceThemeError(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, desktopAppearanceSet, 400, `{"code":400,"msg":"theme rejected"}`)
	c := upClient(f)
	err := c.SetAppearanceTheme(upAuth(), "light")
	if err == nil || !strings.Contains(err.Error(), "theme rejected") {
		t.Fatalf("应返回带 msg 的错误，got %v", err)
	}
	r := f.seen()[0]
	if r.header.Get("X-Product") != "SaaS" || r.header.Get("X-User-Id") != "u-1" {
		t.Errorf("请求头 = %v", r.header)
	}
}

// TestDesktopChatSequencePassthrough 对话链的关键 id 与模型字段逐一透传。
func TestDesktopChatSequencePassthrough(t *testing.T) {
	events := DesktopChatSequence("conv-9", "req-9", "msg-9", "fast-model", "Fast Model")
	if len(events) != 6 {
		t.Fatalf("events = %d，应为 6", len(events))
	}
	created := events[0]
	if created["conversationId"] != "conv-9" || created["messageId"] != "msg-9" {
		t.Errorf("agent_task_created id = %v/%v", created["conversationId"], created["messageId"])
	}
	if created["requestModelId"] != "fast-model" || created["requestModelName"] != "Fast Model" {
		t.Errorf("模型字段 = %v/%v", created["requestModelId"], created["requestModelName"])
	}
	for i, ev := range events[1:] {
		if ev["rootRequestId"] != "req-9" || ev["parentConversationId"] != "conv-9" {
			t.Errorf("events[%d] 的 JOIN 字段不全: %v", i+1, ev)
		}
	}
	// traceId 只出现在前四个请求事件里（chat_request_response 不带）。
	for i := 1; i <= 4; i++ {
		if events[i]["traceId"] != "req-9" {
			t.Errorf("events[%d].traceId = %v，应为 req-9", i, events[i]["traceId"])
		}
	}
	if events[1]["messageId"] != "msg-9-assistant" {
		t.Errorf("chat_message_send.messageId = %v", events[1]["messageId"])
	}
	if events[2]["codebuddy.session_id"] != "conv-9" || events[2]["codebuddy.conversation_request_id"] != "req-9" {
		t.Errorf("chat_request_send codebuddy 字段 = %v", events[2])
	}
	if events[3]["responseModelId"] != "fast-model" || events[3]["messageId"] != "msg-9-assistant" {
		t.Errorf("chat_message_response 字段 = %v", events[3])
	}
}

// TestDesktopBuddyAppSequence 进入 Buddy 应用五连事件。
func TestDesktopBuddyAppSequence(t *testing.T) {
	events := DesktopBuddyAppSequence("cb_1", "企鹅教师助手")
	wantCodes := []string{
		"buddyapp_discover_click", "buddyapp_show", "buddyapp_enter_click",
		"buddyapp_auth_confirm_click", "buddyapp_bindaccount_skip_click",
	}
	if len(events) != len(wantCodes) {
		t.Fatalf("events = %d，应为 %d", len(events), len(wantCodes))
	}
	for i, want := range wantCodes {
		if events[i]["eventCode"] != want {
			t.Errorf("events[%d].eventCode = %v，应为 %v", i, events[i]["eventCode"], want)
		}
		if events[i]["mode"] != "LOCAL" || events[i]["buddyId"] != "cb_1" || events[i]["buddyName"] != "企鹅教师助手" {
			t.Errorf("events[%d] 公共字段 = %v", i, events[i])
		}
	}
	if events[1]["elementId"] != "cb_1" || events[1]["position"] != 2 {
		t.Errorf("buddyapp_show = %v", events[1])
	}
	if events[2]["isFirstPage"] != "1" {
		t.Errorf("buddyapp_enter_click.isFirstPage = %v", events[2]["isFirstPage"])
	}
}

// TestDesktopAutomationCreateEvent 定时任务创建事件。
func TestDesktopAutomationCreateEvent(t *testing.T) {
	ev := DesktopAutomationCreateEvent("每日备份")
	if ev["eventCode"] != "automated_task_create_suc" || ev["name"] != "每日备份" {
		t.Errorf("事件 = %v", ev)
	}
	if ev["source"] != "manually" || ev["modelId"] != "fast-model" || ev["scheduleType"] != "once" || ev["mode"] != "LOCAL" {
		t.Errorf("事件字段 = %v", ev)
	}
}

// TestReportWebEvent Web 端指纹上报：走 webBase、浏览器头、事件字段；400 返回错误。
func TestReportWebEvent(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, "/v2/report", 200, `{"code":0,"msg":"OK"}`)
	c := upClient(f)
	c.WebBaseCN = "https://www.workbuddy.cn"
	a := &auth.Auth{AccessToken: "at", UID: "u-web", Nickname: "网页号", EnterpriseID: "ent-1"}
	if err := c.ReportWebEvent(a, "library_doc_intro_click", "https://www.workbuddy.cn/library/1", "btn-intro", "介绍"); err != nil {
		t.Fatalf("ReportWebEvent: %v", err)
	}
	r := f.seen()[0]
	if r.url != "https://www.workbuddy.cn/v2/report" {
		t.Errorf("URL = %q", r.url)
	}
	for k, want := range map[string]string{
		"x-client-platform": "web",
		"Origin":            "https://www.workbuddy.cn",
		"Referer":           "https://www.workbuddy.cn/library/1",
		"X-User-Id":         "u-web",
	} {
		if got := r.header.Get(k); got != want {
			t.Errorf("%s = %q，应为 %q", k, got, want)
		}
	}
	if !strings.Contains(r.header.Get("User-Agent"), "Mozilla/5.0") {
		t.Errorf("UA 应为浏览器形状: %q", r.header.Get("User-Agent"))
	}
	var events []map[string]any
	if err := json.Unmarshal(r.body, &events); err != nil || len(events) != 1 {
		t.Fatalf("body 应为单元素数组: %v %s", err, r.body)
	}
	ev := events[0]
	if ev["eventCode"] != "library_doc_intro_click" || ev["pageURL"] != "https://www.workbuddy.cn/library/1" ||
		ev["elementId"] != "btn-intro" || ev["elementName"] != "介绍" {
		t.Errorf("事件字段 = %v", ev)
	}
	if ev["os"] != "Win32" || ev["userId"] != "u-web" || ev["userNickname"] != "网页号" || ev["enterpriseId"] != "ent-1" {
		t.Errorf("web 指纹字段 = %v", ev)
	}

	f2 := newUpFake(t)
	f2.on(http.MethodPost, "/v2/report", 400, `{"code":400,"msg":"web report rejected"}`)
	c2 := upClient(f2)
	c2.WebBaseCN = "https://www.workbuddy.cn"
	if err := c2.ReportWebEvent(a, "x", "u", "e", "n"); err == nil {
		t.Fatal("上游 400 应返回错误")
	}
}

// TestDesktopTemplateUseSequence 模板任务事件组：对话链 + 两个模板事件，
// 模板 id/name/requestId 透传。
func TestDesktopTemplateUseSequence(t *testing.T) {
	events := DesktopTemplateUseSequence("conv-t", "req-t", "tpl-1", "日报模板")
	if len(events) != 8 {
		t.Fatalf("events = %d，应为 8", len(events))
	}
	if events[0]["eventCode"] != "agent_task_created" || events[3]["eventCode"] != "chat_message_response" {
		t.Errorf("前 6 个应是对话链: %v", events[0]["eventCode"])
	}
	if events[0]["conversationId"] != "conv-t" {
		t.Errorf("对话链会话 id = %v", events[0]["conversationId"])
	}
	if events[0]["messageId"] != "msg-tpl-1" {
		t.Errorf("模板对话 messageId = %v，应为 msg-tpl-1", events[0]["messageId"])
	}
	created := events[6]
	if created["eventCode"] != "agent_task_created_with_template" || created["id"] != "tpl-1" ||
		created["name"] != "日报模板" || created["requestId"] != "req-t" || created["mode"] != "working" {
		t.Errorf("agent_task_created_with_template = %v", created)
	}
	used := events[7]
	if used["eventCode"] != "template_used" || used["template_id"] != "tpl-1" || used["task_mode"] != "working" {
		t.Errorf("template_used = %v", used)
	}
}

// TestDesktopPlaybookPromptSequence 灵感案例事件组：对话链 + 三个 playbook 事件。
func TestDesktopPlaybookPromptSequence(t *testing.T) {
	events := DesktopPlaybookPromptSequence("conv-p", "req-p", "case-7", "案例七")
	if len(events) != 9 {
		t.Fatalf("events = %d，应为 9", len(events))
	}
	click := events[6]
	if click["eventCode"] != "web_element_click" || click["pageName"] != "playbook_detail" ||
		click["elementId"] != "playbook_ctaClick" || click["elementName"] != "案例七" || click["source"] != "discover" {
		t.Errorf("web_element_click = %v", click)
	}
	cta := events[7]
	if cta["eventCode"] != "playbook_cta_click" || cta["id"] != "case-7" || cta["name"] != "案例七" ||
		cta["type"] != "document" || cta["position"] != 0 || cta["source"] != "discover" {
		t.Errorf("playbook_cta_click = %v", cta)
	}
	send := events[8]
	if send["eventCode"] != "playbook_prompt_send" || send["conversationId"] != "conv-p" ||
		send["requestId"] != "req-p" || send["id"] != "case-7" || send["type"] != "document" {
		t.Errorf("playbook_prompt_send = %v", send)
	}
}

// TestDesktopDesignCanvasSequence 设计画布事件组：对话链 + 创建/打开画布。
func TestDesktopDesignCanvasSequence(t *testing.T) {
	req := "0123456789abcdef0123456789abcdef"
	events := DesktopDesignCanvasSequence("conv-c", req)
	if len(events) != 8 {
		t.Fatalf("events = %d，应为 8", len(events))
	}
	create := events[6]
	if create["eventCode"] != "wbx_design_canvas_task_create" || create["conversationId"] != "conv-c" ||
		create["requestId"] != req || create["source"] != "summon_keyword" || create["isSuccessful"] != true {
		t.Errorf("create = %v", create)
	}
	open := events[7]
	wantID := "ardot-file-" + req[len(req)-8:]
	if open["eventCode"] != "wbx_design_canvas_open" || open["id"] != wantID || open["type"] != "page" {
		t.Errorf("open = %v，id 应为 %v", open, wantID)
	}
}

// TestMarketExpertList 专家市场列表：请求形状、解析、expert_type 可选、
// 400 与 data 非法返回错误。
func TestMarketExpertList(t *testing.T) {
	const path = "/portal/operation-platform/market/expert/list"
	t.Run("成功且带 expert_type", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200, `{"code":0,"data":{"experts":[
			{"expert_id":"ex_1","expert_type":"agent","display_name_zh":"写作专家","profession_zh":"写作","version":"1.2.0","categories":["expert-writing"]},
			{"expert_id":"ex_2","expert_type":"team","display_name_zh":"团队","profession_zh":"综合","version":"","categories":[]}
		]}}`)
		c := upClient(f)
		a := upAuth()
		a.Domain = "www.codebuddy.cn"
		experts, err := c.MarketExpertList(a, "agent")
		if err != nil {
			t.Fatalf("MarketExpertList: %v", err)
		}
		if len(experts) != 2 || experts[0].ExpertID != "ex_1" || experts[0].DisplayNameZH != "写作专家" ||
			experts[0].Version != "1.2.0" || experts[1].ExpertType != "team" {
			t.Errorf("experts = %+v", experts)
		}
		r := f.seen()[0]
		if r.method != http.MethodPost || r.path != path {
			t.Errorf("请求 = %s %s", r.method, r.path)
		}
		for k, want := range map[string]string{
			"Authorization":    "Bearer at",
			"Content-Type":     "application/json",
			"User-Agent":       testDesktopUA,
			"X-Domain":         "www.codebuddy.cn",
			"X-Product":        "SaaS",
			"X-Requested-With": "XMLHttpRequest",
			"X-User-Id":        "u-1",
		} {
			if got := r.header.Get(k); got != want {
				t.Errorf("%s = %q，应为 %q", k, got, want)
			}
		}
		if got := r.header.Get("X-Request-ID"); !hex32.MatchString(got) {
			t.Errorf("X-Request-ID = %q，应为 32 位十六进制", got)
		}
		var body map[string]any
		if err := json.Unmarshal(r.body, &body); err != nil {
			t.Fatalf("请求体: %v", err)
		}
		if body["expert_type"] != "agent" || body["page"] != float64(1) || body["page_size"] != float64(20) ||
			body["sort_by"] != "reco_rank" || body["sort_order"] != "desc" {
			t.Errorf("请求体 = %v", body)
		}
	})

	t.Run("空 expert_type 不传", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200, `{"code":0,"data":{"experts":[]}}`)
		experts, err := upClient(f).MarketExpertList(upAuth(), "")
		if err != nil || len(experts) != 0 {
			t.Fatalf("experts=%v err=%v", experts, err)
		}
		var body map[string]any
		json.Unmarshal(f.seen()[0].body, &body)
		if _, ok := body["expert_type"]; ok {
			t.Errorf("空 expert_type 不应写入请求体: %v", body)
		}
	})

	t.Run("HTTP 400", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 400, `{"code":400,"msg":"market boom"}`)
		if _, err := upClient(f).MarketExpertList(upAuth(), "agent"); err == nil {
			t.Fatal("上游 400 应返回错误")
		}
	})

	t.Run("data 非法", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200, `{"code":0,"data":"nope"}`)
		if _, err := upClient(f).MarketExpertList(upAuth(), "agent"); err == nil {
			t.Fatal("data 类型不符应返回解析错误")
		}
	})
}

// chunkReader 按块返回响应体（模拟 SSE 分块到达）。
type chunkReader struct {
	chunks [][]byte
	i      int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.i >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.i])
	r.i++
	return n, nil
}

func (r *chunkReader) Close() error { return nil }

// TestDesktopChatWithExpert 真实桌面 chat：请求头/体形状、从 SSE 抓服务端
// requestId（跳过不合规的 id）、上游失败与找不到 id 的错误。
func TestDesktopChatWithExpert(t *testing.T) {
	const path = "/v2/chat/completions"
	validID := "cmb-0123456789abcdef0123456789abcdef"
	const bareID = "fedcba9876543210fedcba9876543210"

	t.Run("成功并透出会话与请求 id", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200, "data: {\"id\":\""+validID+"\"}\n\n")
		c := upClient(f)
		conv, reqID, err := c.DesktopChatWithExpert(upAuth(), "ex_1")
		if err != nil {
			t.Fatalf("DesktopChatWithExpert: %v", err)
		}
		if reqID != validID {
			t.Errorf("requestID = %q，应为 %q", reqID, validID)
		}
		if !strings.HasPrefix(conv, "wb2api-conv-") {
			t.Errorf("conversationID = %q", conv)
		}
		r := f.seen()[0]
		for k, want := range map[string]string{
			"Authorization":       "Bearer at",
			"Accept":              "text/event-stream",
			"User-Agent":          testDesktopUA,
			"X-Domain":            upBase,
			"X-Product":           "SaaS",
			"X-User-Id":           "u-1",
			"X-Conversation-ID":   conv,
			"X-Agent-Intent":      "craft",
			"X-Agent-Type":        "main",
			"X-IDE-Name":          "WorkBuddy",
			"X-IDE-Type":          "WorkBuddy",
			"X-IDE-Version":       "5.8.3",
			"x-codebuddy-request": "1",
			"X-Expert-Id":         "ex_1",
		} {
			if got := r.header.Get(k); got != want {
				t.Errorf("%s = %q，应为 %q", k, got, want)
			}
		}
		var body map[string]any
		if err := json.Unmarshal(r.body, &body); err != nil {
			t.Fatalf("请求体: %v", err)
		}
		if body["model"] != "fast-model" || body["stream"] != true || body["agent"] != "cli" {
			t.Errorf("请求体 = %v", body)
		}
	})

	t.Run("先遇到不合规 id 再遇到合规 id", func(t *testing.T) {
		f := newUpFake(t)
		f.onFunc(http.MethodPost, path, func(*upReq) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: &chunkReader{chunks: [][]byte{
				[]byte("data: {\"id\":\"chatcmpl-not-a-request-id\"}\n\n"),
				[]byte("data: {\"id\":\"" + validID + "\"}\n\n"),
			}}}, nil
		})
		_, reqID, err := upClient(f).DesktopChatWithExpert(upAuth(), "")
		if err != nil || reqID != validID {
			t.Fatalf("reqID=%q err=%v，应为 %q", reqID, err, validID)
		}
	})

	t.Run("同一块里先有不合规 id、空 id，后有合规 id", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200,
			"data: {\"id\":\"chatcmpl-not-a-request-id\",\"x\":{\"id\":\"\"}}\n\ndata: {\"id\":\""+validID+"\"}\n\n")
		_, reqID, err := upClient(f).DesktopChatWithExpert(upAuth(), "")
		if err != nil || reqID != validID {
			t.Fatalf("reqID=%q err=%v，应为 %q", reqID, err, validID)
		}
	})

	t.Run("id 被拆在两块里", func(t *testing.T) {
		f := newUpFake(t)
		f.onFunc(http.MethodPost, path, func(*upReq) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: &chunkReader{chunks: [][]byte{
				[]byte("data: {\"id\":\"" + validID[:10]),
				[]byte(validID[10:] + "\"}\n\n"),
			}}}, nil
		})
		_, reqID, err := upClient(f).DesktopChatWithExpert(upAuth(), "")
		if err != nil || reqID != validID {
			t.Fatalf("reqID=%q err=%v，应为 %q", reqID, err, validID)
		}
	})

	t.Run("裸 32hex 也接受", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200, "data: {\"id\":\""+bareID+"\"}\n\n")
		_, reqID, err := upClient(f).DesktopChatWithExpert(upAuth(), "")
		if err != nil || reqID != bareID {
			t.Fatalf("reqID=%q err=%v", reqID, err)
		}
		if f.seen()[0].header.Get("X-Expert-Id") != "" {
			t.Error("空专家 id 不应带 X-Expert-Id")
		}
	})

	t.Run("上游 400", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 400, `{"code":400,"msg":"bad chat"}`)
		_, _, err := upClient(f).DesktopChatWithExpert(upAuth(), "")
		if err == nil || !strings.Contains(err.Error(), "chat http 400") {
			t.Fatalf("err = %v，应含 chat http 400", err)
		}
	})

	t.Run("SSE 没有合规 id", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200, "data: {\"id\":\"nope\"}\n\n")
		_, _, err := upClient(f).DesktopChatWithExpert(upAuth(), "")
		if err == nil || !strings.Contains(err.Error(), "未找到服务端 requestId") {
			t.Fatalf("err = %v，应提示未找到 requestId", err)
		}
	})
}

// TestDesktopExpertSequences 专家事件构造：召唤三连与使用事件（craft/LOCAL），
// 分类与版本兜底。
func TestDesktopExpertSequences(t *testing.T) {
	full := MarketExpert{
		ExpertID: "ex_1", ExpertType: "agent", DisplayNameZH: "写作专家", ProfessionZH: "写作",
		Version: "2.0.0", Categories: []any{"expert-writing"},
	}
	summon := DesktopExpertSummonSequence(full)
	codes := []string{"web_element_click", "expert_summon_click", "expert_summoned"}
	if len(summon) != len(codes) {
		t.Fatalf("events = %d", len(summon))
	}
	for i, want := range codes {
		if summon[i]["eventCode"] != want {
			t.Errorf("summon[%d] = %v", i, summon[i]["eventCode"])
		}
	}
	if summon[0]["source"] != "ex_1" || summon[0]["type"] != "expert-writing" || summon[0]["version"] != "2.0.0" {
		t.Errorf("web_element_click = %v", summon[0])
	}
	if summon[1]["id"] != "ex_1" || summon[1]["name"] != "写作专家" || summon[1]["expertTitle"] != "写作" ||
		summon[1]["expertType"] != "agent" || summon[1]["version"] != "2.0.0" || summon[1]["mode"] != "LOCAL" {
		t.Errorf("expert_summon_click = %v", summon[1])
	}
	if summon[2]["id"] != "ex_1" || summon[2]["name"] != "写作专家" {
		t.Errorf("expert_summoned = %v", summon[2])
	}

	// 分类/版本缺失时兜底：type=expert-all、version=1.0.0。
	bare := MarketExpert{ExpertID: "ex_2", DisplayNameZH: "裸专家", ProfessionZH: "综合"}
	bareSummon := DesktopExpertSummonSequence(bare)
	if bareSummon[0]["type"] != "expert-all" || bareSummon[0]["version"] != "1.0.0" {
		t.Errorf("兜底 = %v/%v", bareSummon[0]["type"], bareSummon[0]["version"])
	}
	// Categories[0] 不是字符串时也兜底。
	odd := MarketExpert{ExpertID: "ex_3", Categories: []any{42}}
	if got := DesktopExpertSummonSequence(odd)[0]["type"]; got != "expert-all" {
		t.Errorf("非字符串分类应兜底，got %v", got)
	}

	requestID := "abcdef0123456789abcdef0123456789"
	craft := DesktopExpertActualUseEvent(full, "conv-1", requestID)
	if craft["mode"] != "craft" || craft["eventCode"] != "expert_actual_use" || craft["id"] != "ex_1" ||
		craft["type"] != "expert-writing" || craft["version"] != "2.0.0" {
		t.Errorf("craft 使用事件 = %v", craft)
	}
	if craft["messageId"] != "msg-"+requestID[len(requestID)-8:] || craft["conversationId"] != "conv-1" || craft["requestId"] != requestID {
		t.Errorf("使用事件 JOIN 字段 = %v", craft)
	}
	local := DesktopExpertActualUseLocal(bare, "conv-2", requestID)
	if local["mode"] != "LOCAL" || local["type"] != "expert-all" || local["version"] != "1.0.0" {
		t.Errorf("LOCAL 使用事件 = %v", local)
	}
}
