package upstream

// event_fingerprint_test.go 上报事件的公共字段：按 5.8.3 的事件处理器、macOS 版官方客户端
// （desktop.go 的 eventFingerprint）。不连外网。

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestEventFingerprintMacOS 公共字段的取值；官方 macOS 版没有值的字段一概不带。
func TestEventFingerprintMacOS(t *testing.T) {
	a := &auth.Auth{UID: "u-1", Nickname: "测试号"}
	before := time.Now().UnixMilli()
	fp := (&Client{}).desktopFingerprint(a)
	want := map[string]any{
		"reportDelay": eventReportDelay, "timezone": "Asia/Shanghai",
		"userId": "u-1", "username": "测试号", "userNickname": "测试号",
		"product": "SaaS", "releaseDate": desktopReleaseDate, "commit": desktopCommit,
		"ideName": "WorkBuddy", "ideType": "WorkBuddy", "ideVersion": "5.8.3",
		"machineId": desktopMachineID(a), "sessionId": "",
		"extName": "workbuddy-desktop", "extVersion": "5.8.3",
		"os": "darwin", "arch": "arm64", "osVersion": "24.6.0",
		"cpuModel": "Apple M2", "cpuCores": 8, "memorySize": 16,
	}
	for k, v := range want {
		if fp[k] != v {
			t.Errorf("%s = %v，应为 %v", k, fp[k], v)
		}
	}
	ts, _ := fp["timestamp"].(int64)
	if ts > before-eventReportDelay+1000 || ts < before-eventReportDelay-1000 {
		t.Errorf("timestamp = %d，应比现在早 %d 毫秒左右", ts, eventReportDelay)
	}
	for _, k := range []string{"qimei36", "presentAt", "downloadChannel", "clientIp", "enterprise", "enterpriseId", "buddyId", "expKeys"} {
		if _, ok := fp[k]; ok {
			t.Errorf("不应带 %s", k)
		}
	}
	if len(fp) != len(want)+1 {
		t.Errorf("字段数 = %d，应为 %d：%v", len(fp), len(want)+1, fp)
	}
	ent := (&Client{}).desktopFingerprint(&auth.Auth{UID: "u-2", EnterpriseID: "ent-1"})
	if ent["enterpriseId"] != "ent-1" {
		t.Errorf("企业号应带 enterpriseId: %v", ent["enterpriseId"])
	}
}

// TestReportChatActivityFingerprint 对话活跃上报（内置 CLI 的事件）带公共字段，sessionId 是
// CLI 进程的 id；业务字段不被公共字段覆盖，presentAt 与 timestamp 同一时刻。
func TestReportChatActivityFingerprint(t *testing.T) {
	var body []byte
	c := testClient(func(r *http.Request) (*http.Response, error) {
		body, _ = io.ReadAll(r.Body)
		return jsonResp(200, `{"code":0,"msg":"OK"}`), nil
	})
	a := &auth.Auth{AccessToken: "at", UID: "u-active", Nickname: "活跃号"}
	if err := c.ReportChatActivity(a, "conv-1", "req-1"); err != nil {
		t.Fatalf("ReportChatActivity: %v", err)
	}
	var events []map[string]any
	if err := json.Unmarshal(body, &events); err != nil || len(events) != 1 {
		t.Fatalf("body = %s err=%v", body, err)
	}
	ev := events[0]
	for k, v := range map[string]any{
		"eventCode": "chat_request_send", "userId": "u-active", "conversationId": "conv-1",
		"sessionId": cliSessionID(a), "machineId": desktopMachineID(a), "os": "darwin",
		"cpuModel": "Apple M2", "extName": "workbuddy-desktop", "username": "活跃号",
		"reportDelay": float64(eventReportDelay), "agentType": "conversation",
	} {
		if ev[k] != v {
			t.Errorf("%s = %v，应为 %v", k, ev[k], v)
		}
	}
	if ev["presentAt"] != ev["timestamp"] {
		t.Errorf("presentAt = %v，应等于 timestamp %v", ev["presentAt"], ev["timestamp"])
	}
}
