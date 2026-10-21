// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package scheduler

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
)

// fixNightClock 钉住夜猫子窗口判定时钟（生产恒为 time.Now）。
func fixNightClock(t *testing.T, at time.Time) {
	t.Helper()
	old := nowForNightWindow
	nowForNightWindow = func() time.Time { return at }
	t.Cleanup(func() { nowForNightWindow = old })
}

func nightAt(hour, minute int) time.Time {
	return time.Date(2026, 10, 19, hour, minute, 0, 0, time.Local)
}

// tasksReply 构造任务列表响应（tasks 为 JSON 数组体）。
func tasksReply(tasks string) string {
	return `{"code":0,"data":{"tasks":[` + tasks + `]}}`
}

func newBlackcatPool(uids ...string) *pool.Pool {
	p := pool.New("")
	for _, uid := range uids {
		p.Add(&auth.Auth{UID: uid, AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	}
	return p
}

// TestRunBlackcatNowOutsideWindow 窗口外直接跳过，不发任何上游请求。
func TestRunBlackcatNowOutsideWindow(t *testing.T) {
	fixNightClock(t, nightAt(12, 0))
	c := newSchedCounter()
	srv := c.server(t)
	s := schedWithPool(srv, newBlackcatPool("u1"), Config{})

	s.RunBlackcatNow()
	if n := c.count(""); n != 0 {
		t.Errorf("窗口外不应发请求，实际 %d 次", n)
	}
}

// TestRunBlackcatNowGates 窗口内门控：禁用/暂停/无 token/global/企业号都跳过。
func TestRunBlackcatNowGates(t *testing.T) {
	fixNightClock(t, nightAt(23, 30))
	fastActivity(t)
	c := newSchedCounter()
	c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
		switch {
		case strings.HasSuffix(path, "/activity/growth/tasks"):
			w.Write([]byte(tasksReply(`{"task_code":"black_cat","target":1,"current":0}`)))
			return true
		case path == "/v2/chat/completions":
			w.Write([]byte("data: {}\n\n"))
			return true
		case path == "/v2/report":
			w.Write([]byte(`{"code":0,"msg":"OK"}`))
			return true
		}
		return false
	}
	srv := c.server(t)
	p := pool.New("")
	p.Add(&auth.Auth{UID: "ok", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "dis", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "paused", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "notoken", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "g", AccessToken: "at", RefreshToken: "rt", Domain: "www.workbuddy.ai", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "e", AccessToken: "at", RefreshToken: "rt", EnterpriseID: "e1", ExpiresAt: 9999999999})
	p.Disable("dis", "test")
	p.Pause("paused")
	s := schedWithPool(srv, p, Config{})

	s.RunBlackcatNow()

	if got := c.count("/activity/growth/tasks"); got != 1 {
		t.Errorf("任务查询 = %d，应只有 ok 账号一次", got)
	}
	if got := c.count("/v2/chat/completions"); got != 1 {
		t.Errorf("夜间对话 = %d，应为 1", got)
	}
	if got := c.count("/v2/report"); got != 1 {
		t.Errorf("事件上报 = %d，应为 1", got)
	}
}

// TestRunBlackcatNeedZero 没有 black_cat 任务或已达标/已领取时不对话。
func TestRunBlackcatNeedZero(t *testing.T) {
	cases := []struct {
		name string
		task string
	}{
		{"没有任务", `{"task_code":"other","target":1,"current":0}`},
		{"已达标", `{"task_code":"black_cat","target":1,"current":1}`},
		{"已领取", `{"task_code":"black_cat","target":1,"current":1,"accept_status":"claimed"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixNightClock(t, nightAt(23, 30))
			c := newSchedCounter()
			c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
				if strings.HasSuffix(path, "/activity/growth/tasks") {
					w.Write([]byte(tasksReply(tc.task)))
					return true
				}
				return false
			}
			s := schedWithPool(c.server(t), newBlackcatPool("u1"), Config{})
			s.RunBlackcatNow()
			if got := c.count("/v2/chat/completions"); got != 0 {
				t.Errorf("无需补足时不应对话，got %d", got)
			}
		})
	}
}

// TestRunBlackcatNeedError 任务列表失败：记录后继续（不对话）。
func TestRunBlackcatNeedError(t *testing.T) {
	fixNightClock(t, nightAt(3, 0))
	c := newSchedCounter()
	c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
		if strings.HasSuffix(path, "/activity/growth/tasks") {
			w.WriteHeader(400)
			w.Write([]byte(`{"code":400,"msg":"list boom"}`))
			return true
		}
		return false
	}
	s := schedWithPool(c.server(t), newBlackcatPool("u1"), Config{})
	s.RunBlackcatNow()
	if got := c.count("/v2/chat/completions"); got != 0 {
		t.Errorf("任务查询失败不应对话，got %d", got)
	}
}

// TestRunBlackcatChatFailure 对话失败：RunNightChats 返回错误，按源码记录并继续
// （不进行事件上报）。
func TestRunBlackcatChatFailure(t *testing.T) {
	fixNightClock(t, nightAt(23, 30))
	fastActivity(t)
	c := newSchedCounter()
	c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
		switch {
		case strings.HasSuffix(path, "/activity/growth/tasks"):
			w.Write([]byte(tasksReply(`{"task_code":"black_cat","target":1,"current":0}`)))
			return true
		case path == "/v2/chat/completions":
			w.WriteHeader(400)
			w.Write([]byte(`{"code":400,"msg":"chat boom"}`))
			return true
		}
		return false
	}
	s := schedWithPool(c.server(t), newBlackcatPool("u1"), Config{})
	s.RunBlackcatNow()
	if got := c.count("/v2/chat/completions"); got != 1 {
		t.Errorf("对话请求 = %d，应为 1", got)
	}
	if got := c.count("/v2/report"); got != 0 {
		t.Errorf("对话失败不应上报，got %d", got)
	}
}
