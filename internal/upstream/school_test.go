package upstream

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestSchoolJSON school 域请求公共形状：走 billingBase + /portal/activity/school、
// Bearer/X-User-Id 头、GET 无体/POST 带体、解信封错误语义。
func TestSchoolJSON(t *testing.T) {
	t.Run("GET 无体并解析 out", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, schoolBase+"/info", 200, `{"code":0,"data":{"value":7}}`)
		c := upClient(f)
		var out struct {
			Value int `json:"value"`
		}
		if err := c.schoolJSON(upAuth(), http.MethodGet, "/info", nil, &out); err != nil {
			t.Fatalf("schoolJSON: %v", err)
		}
		if out.Value != 7 {
			t.Errorf("out.Value = %d，应为 7", out.Value)
		}
		r := f.seen()[0]
		if r.path != schoolBase+"/info" {
			t.Errorf("path = %q", r.path)
		}
		if len(r.body) != 0 {
			t.Errorf("GET 不应带请求体，got %q", r.body)
		}
		if r.header.Get("Authorization") != "Bearer at" || r.header.Get("X-User-Id") != "u-1" {
			t.Errorf("鉴权头不全: %v", r.header)
		}
		if r.header.Get("Content-Type") != "application/json" || r.header.Get("Accept") != "application/json" {
			t.Errorf("内容头不全: %v", r.header)
		}
	})

	t.Run("POST 带体且 out 为 nil", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, schoolBase+"/action", 200, `{"code":0,"data":{"ignored":true}}`)
		c := upClient(f)
		if err := c.schoolJSON(upAuth(), http.MethodPost, "/action", map[string]any{"k": "v"}, nil); err != nil {
			t.Fatalf("schoolJSON: %v", err)
		}
		r := f.seen()[0]
		var body map[string]any
		if err := json.Unmarshal(r.body, &body); err != nil {
			t.Fatalf("请求体解析: %v (%s)", err, r.body)
		}
		if body["k"] != "v" {
			t.Errorf("请求体 = %v", body)
		}
	})

	t.Run("HTTP 400", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, schoolBase+"/info", 400, `{"code":400,"msg":"bad"}`)
		if err := upClient(f).schoolJSON(upAuth(), http.MethodGet, "/info", nil, nil); err == nil {
			t.Fatal("上游 400 应返回错误")
		}
	})

	t.Run("业务 code 非 0", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, schoolBase+"/info", 200, `{"code":42,"msg":"nope"}`)
		err := upClient(f).schoolJSON(upAuth(), http.MethodGet, "/info", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "nope") {
			t.Fatalf("应返回带 msg 的错误，got %v", err)
		}
	})

	t.Run("data 与 out 结构不符", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, schoolBase+"/info", 200, `{"code":0,"data":"oops"}`)
		var out struct {
			Value int `json:"value"`
		}
		if err := upClient(f).schoolJSON(upAuth(), http.MethodGet, "/info", nil, &out); err == nil {
			t.Fatal("data 类型不符应返回解析错误")
		}
	})
}

// TestReportMPEvent 小程序指纹上报：POST billingBase /v2/report，数组体注入 mp 公共
// 指纹且业务字段优先；空事件/400/code≠0 都返回错误。
func TestReportMPEvent(t *testing.T) {
	t.Run("成功", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, mpReportPath, 200, `{"code":0,"msg":"OK"}`)
		c := upClient(f)
		err := c.ReportMPEvent(upAuth(),
			map[string]any{"eventCode": "chat_request_send", "conversationId": "conv-1"},
			map[string]any{"eventCode": "expert_summon_click", "ideVersion": "9.9.9"})
		if err != nil {
			t.Fatalf("ReportMPEvent: %v", err)
		}
		r := f.seen()[0]
		if r.method != http.MethodPost || r.path != mpReportPath {
			t.Errorf("请求 = %s %s", r.method, r.path)
		}
		for k, want := range map[string]string{
			"Authorization":     "Bearer at",
			"X-User-Id":         "u-1",
			"X-Client-Product":  "workbuddy-mp",
			"X-Client-Version":  "2.4.0",
			"X-Client-Platform": "mp-weixin",
			"X-Platform":        "wechatmp",
		} {
			if got := r.header.Get(k); got != want {
				t.Errorf("%s = %q，应为 %q", k, got, want)
			}
		}
		var events []map[string]any
		if err := json.Unmarshal(r.body, &events); err != nil {
			t.Fatalf("body 应为数组: %v", err)
		}
		if len(events) != 2 {
			t.Fatalf("events = %d，应为 2", len(events))
		}
		for i, ev := range events {
			if ev["ideType"] != "WorkBuddy_MP" || ev["extName"] != "workbuddy-mp" || ev["platform"] != "mini_program" {
				t.Errorf("events[%d] 缺 mp 指纹: %v", i, ev)
			}
			if ev["userId"] != "u-1" || ev["userNickname"] != "测试号" {
				t.Errorf("events[%d] 用户字段 = %v/%v", i, ev["userId"], ev["userNickname"])
			}
		}
		if events[1]["ideVersion"] != "9.9.9" {
			t.Errorf("业务字段应覆盖指纹，ideVersion = %v", events[1]["ideVersion"])
		}
	})

	t.Run("没有事件", func(t *testing.T) {
		if err := upClient(newUpFake(t)).ReportMPEvent(upAuth()); err == nil {
			t.Fatal("空事件应返回错误")
		}
	})

	t.Run("HTTP 400", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, mpReportPath, 400, `{"code":400,"msg":"bad"}`)
		err := upClient(f).ReportMPEvent(upAuth(), map[string]any{"eventCode": "x"})
		if err == nil || !strings.Contains(err.Error(), "bad") {
			t.Fatalf("应返回错误，got %v", err)
		}
	})

	t.Run("业务 code 非 0", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, mpReportPath, 200, `{"code":9,"msg":"mp boom"}`)
		err := upClient(f).ReportMPEvent(upAuth(), map[string]any{"eventCode": "x"})
		if err == nil || !strings.Contains(err.Error(), "mp boom") {
			t.Fatalf("应返回带 msg 的错误，got %v", err)
		}
	})
}

// TestSchoolEventConstructors 开学季事件构造函数：事件码、会话/请求 id 透传、
// activityId 与 mp 指纹字段。
func TestSchoolEventConstructors(t *testing.T) {
	t.Run("SchoolChatTimesEvents", func(t *testing.T) {
		ev := SchoolChatTimesEvents("conv-school")
		rid, _ := ev["traceId"].(string)
		if ev["eventCode"] != "chat_request_send" || ev["agentName"] != "mp" || ev["agentType"] != "main" {
			t.Errorf("事件字段 = %v", ev)
		}
		if ev["conversationId"] != "conv-school" || ev["parentConversationId"] != "conv-school" {
			t.Errorf("会话 id 未透传: %v", ev)
		}
		if ev["codebuddy.session_id"] != "conv-school" {
			t.Errorf("codebuddy.session_id = %v", ev["codebuddy.session_id"])
		}
		if rid == "" || ev["rootRequestId"] != rid || ev["codebuddy.conversation_request_id"] != rid {
			t.Errorf("请求 id 不一致: trace=%v root=%v cb=%v", rid, ev["rootRequestId"], ev["codebuddy.conversation_request_id"])
		}
		if want := "msg-" + rid[len(rid)-8:]; ev["messageId"] != want {
			t.Errorf("messageId = %v，应为 %v", ev["messageId"], want)
		}
	})

	t.Run("SchoolSeasonChatEvent", func(t *testing.T) {
		ev := SchoolSeasonChatEvent("conv-season")
		if ev["activityId"] != schoolOpenDayActivityID {
			t.Errorf("activityId = %v，应为 %s", ev["activityId"], schoolOpenDayActivityID)
		}
		base := SchoolChatTimesEvents("conv-season")
		for _, k := range []string{"eventCode", "conversationId", "agentName"} {
			if ev[k] != base[k] {
				t.Errorf("字段 %s 应与基础事件一致: %v vs %v", k, ev[k], base[k])
			}
		}
	})

	t.Run("MiniExpertUseEvent 默认值", func(t *testing.T) {
		ev := MiniExpertUseEvent("ex_123", "", "")
		if ev["eventCode"] != "expert_actual_use" || ev["expertType"] != "agent" {
			t.Errorf("默认 expertType 应为 agent: %v", ev)
		}
		if ev["name"] != "ex_123" || ev["expertTitle"] != "ex_123" {
			t.Errorf("空 expertName 应回退 expertID: %v", ev)
		}
		if ev["type"] != "send_message" || ev["source"] != "mini_program" || ev["extVersion"] != "2.2.8" {
			t.Errorf("小程序口径字段 = %v", ev)
		}
		if _, ok := ev["conversationId"]; ok {
			t.Error("MiniExpertUseEvent 不应带 conversationId")
		}
		if _, ok := ev["activityId"]; ok {
			t.Error("MiniExpertUseEvent 不应带 activityId")
		}
	})

	t.Run("MiniExpertUseEvent 显式值", func(t *testing.T) {
		ev := MiniExpertUseEvent("ex_9", "写作专家", "team")
		if ev["expertTitle"] != "写作专家" || ev["expertType"] != "team" {
			t.Errorf("显式值未保留: %v", ev)
		}
	})

	t.Run("MiniChatModelEvent", func(t *testing.T) {
		ev := MiniChatModelEvent("conv-model", "glm-5.2", "GLM-5.2")
		if ev["requestModelId"] != "glm-5.2" || ev["requestModelName"] != "GLM-5.2" {
			t.Errorf("模型字段 = %v/%v", ev["requestModelId"], ev["requestModelName"])
		}
		if ev["eventCode"] != "chat_request_send" || ev["conversationId"] != "conv-model" {
			t.Errorf("基础对话字段丢失: %v", ev)
		}
	})

	t.Run("MiniPlaybookEvents", func(t *testing.T) {
		events := MiniPlaybookEvents("case-1", "案例一")
		if len(events) != 2 {
			t.Fatalf("events = %d，应为 2", len(events))
		}
		if events[0]["eventCode"] != "playbook_cta_click" || events[1]["eventCode"] != "playbook_prompt_send" {
			t.Errorf("事件码 = %v/%v", events[0]["eventCode"], events[1]["eventCode"])
		}
		for i, ev := range events {
			if ev["id"] != "case-1" || ev["name"] != "案例一" || ev["type"] != "document" || ev["extVersion"] != "2.2.8" {
				t.Errorf("events[%d] 载荷 = %v", i, ev)
			}
		}
		conv, _ := events[1]["conversationId"].(string)
		if !strings.HasPrefix(conv, "wb2api-mp-pb-") {
			t.Errorf("prompt_send conversationId = %q", conv)
		}
		if events[1]["isOfficial"] != 1 || events[1]["promptLength"] != 96 {
			t.Errorf("prompt_send 字段 = %v", events[1])
		}
	})
}

// TestSchoolVouchers 券码列表：GET billingBase /portal/activity/school/vouchers，
// 解析 data.items；空列表、400、业务错误。
func TestSchoolVouchers(t *testing.T) {
	t.Run("成功", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, schoolBase+"/vouchers", 200, `{"code":0,"data":{"items":[
			{"grant_id":11,"draw_uuid":"d-1","sku_code":"kfc_ice_cream","prize_name":"肯德基冰淇淋","code":"KF-123","valid_to":"2026-10-24"},
			{"grant_id":12,"sku_code":"voucher_luckin","prize_name":"瑞幸","code":"LK-9"}
		]}}`)
		vouchers, err := upClient(f).SchoolVouchers(upAuth())
		if err != nil {
			t.Fatalf("SchoolVouchers: %v", err)
		}
		if len(vouchers) != 2 {
			t.Fatalf("vouchers = %d，应为 2", len(vouchers))
		}
		v := vouchers[0]
		if v.GrantID != 11 || v.DrawUUID != "d-1" || v.SKUCode != "kfc_ice_cream" ||
			v.PrizeName != "肯德基冰淇淋" || v.Code != "KF-123" || v.ValidTo != "2026-10-24" {
			t.Errorf("券字段解析 = %+v", v)
		}
		r := f.seen()[0]
		if r.method != http.MethodGet || r.path != schoolBase+"/vouchers" {
			t.Errorf("请求 = %s %s", r.method, r.path)
		}
	})

	t.Run("空列表", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, schoolBase+"/vouchers", 200, `{"code":0,"data":{"items":[]}}`)
		vouchers, err := upClient(f).SchoolVouchers(upAuth())
		if err != nil || len(vouchers) != 0 {
			t.Fatalf("vouchers=%v err=%v，应为空/nil", vouchers, err)
		}
	})

	t.Run("HTTP 400", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, schoolBase+"/vouchers", 400, `{"code":400,"msg":"forbidden"}`)
		if _, err := upClient(f).SchoolVouchers(&auth.Auth{AccessToken: "at"}); err == nil {
			t.Fatal("上游 400 应返回错误")
		}
	})

	t.Run("业务 code 非 0", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, schoolBase+"/vouchers", 200, `{"code":7,"msg":"activity ended"}`)
		_, err := upClient(f).SchoolVouchers(upAuth())
		if err == nil || !strings.Contains(err.Error(), "activity ended") {
			t.Fatalf("应返回带 msg 的错误，got %v", err)
		}
	})
}
