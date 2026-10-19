package upstream

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestClaimRewardWebEndpoint 领奖走 Web 域（workbuddy.cn）、任务码在路径里、无 body。
// 这是与 CLI 域（copilot.tencent.com/v2/.../reward/claim，task_code 在 body）的关键区别——
// 后者路径不存在，曾导致长期 400 "task not completed" 误判为"上游不支持领取"。
func TestClaimRewardWebEndpoint(t *testing.T) {
	var gotPath, gotMethod, gotBody string
	var gotPlatform, gotReferer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		gotPlatform = r.Header.Get("x-client-platform")
		gotReferer = r.Header.Get("Referer")
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.Write([]byte(`{"code":0,"msg":"OK","data":{"already_claimed":false,"credit":100,"energy":5}}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL}
	a := &auth.Auth{AccessToken: "at", UID: "u1"}

	credit, energy, err := c.ClaimReward(a, "Model_chat_GLM5.2")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if credit != 100 || energy != 5 {
		t.Errorf("credit/energy = %d/%d, want 100/5", credit, energy)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method=%s want POST", gotMethod)
	}
	if want := "/activity/growth/tasks/Model_chat_GLM5.2/claim"; gotPath != want {
		t.Errorf("path=%q want %q（任务码必须在路径里）", gotPath, want)
	}
	if gotBody != "" {
		t.Errorf("claim 不应携带 body，got %q", gotBody)
	}
	if gotPlatform != "web" {
		t.Errorf("x-client-platform=%q want web", gotPlatform)
	}
	if !strings.Contains(gotReferer, "workbuddy.cn") {
		t.Errorf("Referer=%q 应指向 workbuddy.cn", gotReferer)
	}
}

// TestClaimRewardAlreadyClaimed 重复领取：上游返回 already_claimed=true，
// 本地应视为"无新增奖励但不报错"（幂等语义）。
func TestClaimRewardAlreadyClaimed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"msg":"OK","data":{"already_claimed":true}}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), WebBaseCN: srv.URL}
	credit, energy, err := c.ClaimReward(&auth.Auth{AccessToken: "at", UID: "u1"}, "chat_5")
	if err != nil {
		t.Fatalf("already_claimed should not error: %v", err)
	}
	if credit != 0 || energy != 0 {
		t.Errorf("already claimed should yield 0/0, got %d/%d", credit, energy)
	}
}

// TestClaimRewardNotCompleted 未达标：上游 400 + task not completed 应作为错误透出。
func TestClaimRewardNotCompleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{"code": 400, "msg": "task not completed"})
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), WebBaseCN: srv.URL}
	if _, _, err := c.ClaimReward(&auth.Auth{AccessToken: "at", UID: "u1"}, "chat_5"); err == nil {
		t.Fatal("want error for not-completed task")
	}
}

// TestListTasksParseVariants 任务列表解析：平铺字段、progress 对象覆盖、
// progress 零值/非法时保留平铺、claimed/claimable 推算、缺字段零值。
func TestListTasksParseVariants(t *testing.T) {
	cases := []struct {
		name string
		item string
		want Task
	}{
		{
			name: "平铺字段与奖励",
			item: `{"task_code":"chat_5","title":"对话 5 次","description":"去聊天","task_desc":"完成 5 次",
				"reward_credit":100,"reward_energy":5,"has_reward":true,"reward_buddy":true,
				"task_type":"single","tag":"PC","jump_url":"workbuddy://x","locked":true,
				"target":5,"current":2,"accept_status":"accepted","status":"doing"}`,
			want: Task{TaskCode: "chat_5", Title: "对话 5 次", Description: "去聊天", TaskDesc: "完成 5 次",
				Credit: 100, Energy: 5, HasReward: true, RewardBuddy: true, TaskType: "single",
				Tag: "PC", JumpURL: "workbuddy://x", Locked: true, Target: 5, Current: 2,
				AcceptStatus: "accepted", Status: "doing"},
		},
		{
			name: "progress 对象覆盖平铺",
			item: `{"task_code":"chat_3","target":9,"current":9,"progress":{"current":2,"target":5}}`,
			want: Task{TaskCode: "chat_3", Target: 5, Current: 2},
		},
		{
			name: "progress 全零保留平铺",
			item: `{"task_code":"chat_3","target":4,"current":3,"progress":{"current":0,"target":0}}`,
			want: Task{TaskCode: "chat_3", Target: 4, Current: 3},
		},
		{
			name: "progress 为 null 保留平铺",
			item: `{"task_code":"chat_3","target":4,"current":1,"progress":null}`,
			want: Task{TaskCode: "chat_3", Target: 4, Current: 1},
		},
		{
			name: "progress 非法形态保留平铺",
			item: `{"task_code":"chat_3","target":4,"current":1,"progress":5}`,
			want: Task{TaskCode: "chat_3", Target: 4, Current: 1},
		},
		{
			name: "达标可领",
			item: `{"task_code":"done","target":3,"current":3,"accept_status":"accepted"}`,
			want: Task{TaskCode: "done", Target: 3, Current: 3, AcceptStatus: "accepted", Claimable: true},
		},
		{
			name: "已领取",
			item: `{"task_code":"done","target":3,"current":3,"accept_status":"claimed"}`,
			want: Task{TaskCode: "done", Target: 3, Current: 3, AcceptStatus: "claimed", Claimed: true},
		},
		{
			name: "缺字段为零值",
			item: `{}`,
			want: Task{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.on(http.MethodGet, tasksListPath, 200, upTasksJSON(tc.item))
			c := upClient(f)
			tasks, err := c.ListTasks(upAuth())
			if err != nil {
				t.Fatalf("ListTasks: %v", err)
			}
			if len(tasks) != 1 {
				t.Fatalf("tasks = %d，应为 1", len(tasks))
			}
			if tasks[0] != tc.want {
				t.Errorf("task = %+v，应为 %+v", tasks[0], tc.want)
			}
			r := f.seen()[0]
			if r.method != http.MethodGet || r.path != tasksListPath {
				t.Errorf("请求 = %s %s", r.method, r.path)
			}
			if r.header.Get("X-Client-Platform") != "" {
				t.Errorf("默认口径不应带小程序头，got %q", r.header.Get("X-Client-Platform"))
			}
			if r.header.Get("Authorization") != "Bearer at" {
				t.Errorf("Authorization = %q", r.header.Get("Authorization"))
			}
		})
	}
}

// TestListTasksErrors 任务列表的失败语义：HTTP 400、业务 code≠0（带 msg）、非 JSON。
func TestListTasksErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"HTTP 400", `{"code":400,"msg":"bad request"}`},
		{"业务 code 非 0", `{"code":10001,"msg":"list boom"}`},
		{"不是 JSON", `oops`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			status := 200
			if tc.name == "HTTP 400" {
				status = 400
			}
			f.on(http.MethodGet, tasksListPath, status, tc.body)
			if _, err := upClient(f).ListTasks(upAuth()); err == nil {
				t.Fatal("应返回错误")
			}
		})
	}
}

// TestListTasksMP 小程序口径列表：同一路径叠加 X-Client-Platform: miniprogram，
// 解析规则与默认口径一致。
func TestListTasksMP(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, tasksListPath, 200, upTasksJSON(`{"task_code":"mp_task","target":1,"current":1}`))
	c := upClient(f)
	tasks, err := c.ListTasksMP(upAuth())
	if err != nil {
		t.Fatalf("ListTasksMP: %v", err)
	}
	if len(tasks) != 1 || tasks[0].TaskCode != "mp_task" || !tasks[0].Claimable {
		t.Errorf("tasks = %+v", tasks)
	}
	r := f.seen()[0]
	if got := r.header.Get("X-Client-Platform"); got != mpPlatform {
		t.Errorf("X-Client-Platform = %q，应为 %q", got, mpPlatform)
	}
}

// TestAcceptTasks 接受任务：POST accept、body task_codes；default 与 mp 口径仅差
// X-Client-Platform 头；400/code≠0 返回错误。
func TestAcceptTasks(t *testing.T) {
	cases := []struct {
		name string
		call func(c *Client) error
		want string
	}{
		{"默认口径", func(c *Client) error { return c.AcceptTasks(upAuth(), []string{"a", "b"}) }, ""},
		{"小程序口径", func(c *Client) error { return c.AcceptTasksMP(upAuth(), []string{"a", "b"}) }, mpPlatform},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.on(http.MethodPost, tasksAcceptPath, 200, `{"code":0,"msg":"OK"}`)
			if err := tc.call(upClient(f)); err != nil {
				t.Fatalf("accept: %v", err)
			}
			r := f.seen()[0]
			if r.method != http.MethodPost || r.path != tasksAcceptPath {
				t.Errorf("请求 = %s %s", r.method, r.path)
			}
			var body struct {
				TaskCodes []string `json:"task_codes"`
			}
			if err := json.Unmarshal(r.body, &body); err != nil {
				t.Fatalf("请求体: %v (%s)", err, r.body)
			}
			if len(body.TaskCodes) != 2 || body.TaskCodes[0] != "a" || body.TaskCodes[1] != "b" {
				t.Errorf("task_codes = %v", body.TaskCodes)
			}
			if got := r.header.Get("X-Client-Platform"); got != tc.want {
				t.Errorf("X-Client-Platform = %q，应为 %q", got, tc.want)
			}
		})
	}

	t.Run("HTTP 400", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, tasksAcceptPath, 400, `{"code":400,"msg":"task not found"}`)
		if err := upClient(f).AcceptTasks(upAuth(), []string{"x"}); err == nil {
			t.Fatal("上游 400 应返回错误")
		}
	})

	t.Run("业务 code 非 0", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, tasksAcceptPath, 200, `{"code":8,"msg":"accept boom"}`)
		err := upClient(f).AcceptTasksMP(upAuth(), []string{"x"})
		if err == nil || !strings.Contains(err.Error(), "accept boom") {
			t.Fatalf("应返回带 msg 的错误，got %v", err)
		}
	})
}

// TestClaimRewardMP 小程序领奖：chat 域 + mp 头；chat 域 400 时降级 Web 域领奖；
// 非 400 错误不降级。
func TestClaimRewardMP(t *testing.T) {
	newSplitClient := func(f *upFake) *Client {
		c := upClient(f)
		c.ChatBaseCN = "https://chat.example"
		c.WebBaseCN = "https://web.example"
		return c
	}

	t.Run("chat 域成功", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, "/activity/growth/tasks/mp_task/claim", 200, `{"code":0,"data":{"credit":50,"energy":2}}`)
		c := newSplitClient(f)
		credit, energy, err := c.ClaimRewardMP(upAuth(), "mp_task")
		if err != nil || credit != 50 || energy != 2 {
			t.Fatalf("credit=%d energy=%d err=%v，应为 50/2/nil", credit, energy, err)
		}
		r := f.seen()[0]
		if r.header.Get("X-Client-Platform") != mpPlatform {
			t.Errorf("X-Client-Platform = %q", r.header.Get("X-Client-Platform"))
		}
		if r.url != "https://chat.example/activity/growth/tasks/mp_task/claim" {
			t.Errorf("URL = %q", r.url)
		}
	})

	t.Run("chat 域 400 降级 Web 域", func(t *testing.T) {
		f := newUpFake(t)
		f.onHost("chat.example", http.MethodPost, "/activity/growth/tasks/mp_task/claim", 400, `{"code":400,"msg":"bad"}`)
		f.onHost("web.example", http.MethodPost, "/activity/growth/tasks/mp_task/claim", 200, `{"code":0,"data":{"credit":50,"energy":2}}`)
		c := newSplitClient(f)
		credit, energy, err := c.ClaimRewardMP(upAuth(), "mp_task")
		if err != nil || credit != 50 || energy != 2 {
			t.Fatalf("降级后 credit=%d energy=%d err=%v，应为 50/2/nil", credit, energy, err)
		}
		reqs := f.seen()
		if len(reqs) != 2 {
			t.Fatalf("应发 2 次请求（chat→web），实际 %d", len(reqs))
		}
		if reqs[1].url != "https://web.example/activity/growth/tasks/mp_task/claim" {
			t.Errorf("降级 URL = %q", reqs[1].url)
		}
		if got := reqs[1].header.Get("x-client-platform"); got != "web" {
			t.Errorf("降级请求 x-client-platform = %q，应为 web", got)
		}
	})

	t.Run("非 400 错误不降级", func(t *testing.T) {
		f := newUpFake(t)
		f.onHost("chat.example", http.MethodPost, "/activity/growth/tasks/mp_task/claim", 500, `{"code":500,"msg":"server boom"}`)
		c := newSplitClient(f)
		if _, _, err := c.ClaimRewardMP(upAuth(), "mp_task"); err == nil {
			t.Fatal("500 应返回错误")
		}
		if n := len(f.seen()); n != 1 {
			t.Errorf("非 400 不应降级，实际请求 %d 次", n)
		}
	})

	t.Run("成功但 data 非法", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, "/activity/growth/tasks/mp_task/claim", 200, `{"code":0,"data":"nope"}`)
		if _, _, err := newSplitClient(f).ClaimRewardMP(upAuth(), "mp_task"); err == nil {
			t.Fatal("data 类型不符应返回解析错误")
		}
	})
}

// TestParseClaimReward 领奖响应解析：正常、空对象、非法 JSON。
func TestParseClaimReward(t *testing.T) {
	if credit, energy, err := parseClaimReward(json.RawMessage(`{"already_claimed":true,"credit":100,"energy":5}`)); err != nil || credit != 100 || energy != 5 {
		t.Errorf("正常解析 = %d/%d/%v", credit, energy, err)
	}
	if credit, energy, err := parseClaimReward(json.RawMessage(`{}`)); err != nil || credit != 0 || energy != 0 {
		t.Errorf("空对象 = %d/%d/%v", credit, energy, err)
	}
	if _, _, err := parseClaimReward(json.RawMessage(`"nope"`)); err == nil {
		t.Error("非法形态应返回错误")
	}
}

// TestClaimRewardHeadersAndErrors Web 领奖的企业/域头与 code≠0、非 JSON 响应。
func TestClaimRewardHeadersAndErrors(t *testing.T) {
	t.Run("企业头与域名头", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, "/activity/growth/tasks/ent_task/claim", 200, `{"code":0,"data":{"credit":10,"energy":1}}`)
		c := upClient(f)
		c.UserAgent = "custom-ua/1.0"
		a := &auth.Auth{AccessToken: "at", UID: "u-1", EnterpriseID: "ent-9", Domain: "www.workbuddy.cn"}
		credit, energy, err := c.ClaimReward(a, "ent_task")
		if err != nil || credit != 10 || energy != 1 {
			t.Fatalf("credit=%d energy=%d err=%v", credit, energy, err)
		}
		r := f.seen()[0]
		for k, want := range map[string]string{
			"Authorization":   "Bearer at",
			"X-User-Id":       "u-1",
			"X-Enterprise-Id": "ent-9",
			"X-Tenant-Id":     "ent-9",
			"X-Domain":        "www.workbuddy.cn",
			"User-Agent":      "custom-ua/1.0",
		} {
			if got := r.header.Get(k); got != want {
				t.Errorf("%s = %q，应为 %q", k, got, want)
			}
		}
	})

	t.Run("业务 code 非 0", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, "/activity/growth/tasks/x/claim", 200, `{"code":7,"msg":"not eligible"}`)
		_, _, err := upClient(f).ClaimReward(upAuth(), "x")
		if err == nil || !strings.Contains(err.Error(), "not eligible") {
			t.Fatalf("应返回带 msg 的错误，got %v", err)
		}
	})

	t.Run("data 非 JSON", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, "/activity/growth/tasks/x/claim", 200, `{"code":0,"data":"nope"}`)
		if _, _, err := upClient(f).ClaimReward(upAuth(), "x"); err == nil {
			t.Fatal("data 类型不符应返回解析错误")
		}
	})
}
