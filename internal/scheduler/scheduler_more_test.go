package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
)

// schedCounter 记录各路径命中次数的假上游；respond 可按请求定制响应（返回 false 走默认：积分与签到
// 状态按 upstreamtest 的默认响应，其余回成功）。
type schedCounter struct {
	mu      sync.Mutex
	hits    map[string]int
	respond func(w http.ResponseWriter, r *http.Request, path string) bool
}

func newSchedCounter() *schedCounter {
	return &schedCounter{hits: map[string]int{}}
}

func (c *schedCounter) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamtest.WebSession(w, r) {
			return
		}
		c.mu.Lock()
		c.hits[r.URL.Path]++
		c.mu.Unlock()
		if c.respond != nil && c.respond(w, r, r.URL.Path) {
			return
		}
		if upstreamtest.Write(w, r.URL.Path) {
			return
		}
		w.Write([]byte(`{"code":0,"msg":"ok","data":{}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (c *schedCounter) count(sub string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for p, v := range c.hits {
		if strings.Contains(p, sub) {
			n += v
		}
	}
	return n
}

func schedWithPool(srv *httptest.Server, p *pool.Pool, cfg Config) *Scheduler {
	cfg.Pool = p
	cfg.Upstream = &upstream.Client{
		HTTP:          srv.Client(),
		ChatBaseCN:    srv.URL,
		BillingBaseCN: srv.URL,
		WebBaseCN:     srv.URL,
	}
	return New(cfg)
}

// waitUntil 轮询直到条件成立（最长 2s，每 5ms 一次）。
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

// TestSetExpiringSoonWindow 快过期窗口热更新：负值归零、值可读、重复设置幂等。
func TestSetExpiringSoonWindow(t *testing.T) {
	p := pool.New("")
	s := New(Config{Pool: p})
	if got := s.ExpiringSoonWindow(); got != 0 {
		t.Errorf("默认窗口 = %v，应为 0", got)
	}
	s.SetExpiringSoonWindow(-time.Hour)
	if got := s.ExpiringSoonWindow(); got != 0 {
		t.Errorf("负值应归零，got %v", got)
	}
	s.SetExpiringSoonWindow(2 * time.Hour)
	if got := s.ExpiringSoonWindow(); got != 2*time.Hour {
		t.Errorf("窗口 = %v，应为 2h", got)
	}
	s.SetExpiringSoonWindow(2 * time.Hour) // 无变化路径
	if got := s.ExpiringSoonWindow(); got != 2*time.Hour {
		t.Errorf("重复设置后 = %v", got)
	}
}

// TestSetGrowthHookAndRunBatch 成长回调：挂载后 runBatch 调用一次；未挂载/空批次不 panic。
func TestSetGrowthHookAndRunBatch(t *testing.T) {
	var calls atomic.Int32
	s := newTestScheduler()
	s.SetGrowthHook(func() { calls.Add(1) })
	s.runBatch(context.Background(), []taskKind{taskGrowth})
	if calls.Load() != 1 {
		t.Errorf("hook calls = %d，应为 1", calls.Load())
	}

	// 未挂载：跳过、不 panic。
	s2 := newTestScheduler()
	s2.runBatch(context.Background(), []taskKind{taskGrowth})
	s2.runBatch(context.Background(), nil)
}

// TestReconfigure 热改时点与开关：下一轮按新配置排程；关掉的任务不再触发；
// 空 hours 保留原值；两个唤醒通道都被 poke。
func TestReconfigure(t *testing.T) {
	s := New(Config{
		CheckinHours:   []int{9},
		KeepaliveHours: []int{22},
		TravelHours:    []int{9},
		ActivityHours:  []int{10},
		GrowthHours:    []int{1},
	})
	s.Reconfigure(
		[]int{3}, nil, nil, []int{4}, nil, []int{5},
		false, true, true, false, true, false,
	)
	if len(s.rearmSchedule) != 1 || len(s.rearmBalance) != 1 {
		t.Error("Reconfigure 应 poke 两个唤醒通道")
	}

	at, kinds := s.nextWake(time.Date(2026, 10, 19, 0, 0, 0, 0, time.Local))
	if !at.Equal(time.Date(2026, 10, 19, 3, 0, 0, 0, time.Local)) || !hasKind(kinds, taskCheckin) {
		t.Errorf("新签到时点 = %v/%v，应为 03:00 签到", at, kinds)
	}
	if hasKind(kinds, taskTravel) || hasKind(kinds, taskActivity) || hasKind(kinds, taskBlackcat) {
		t.Errorf("已关闭的任务不应再进候选: %v", kinds)
	}

	// 04:00 保活；空 hours 保留原值（签到仍为 3 点，下一日是次日 03:00）。
	at, kinds = s.nextWake(time.Date(2026, 10, 19, 3, 30, 0, 0, time.Local))
	if !at.Equal(time.Date(2026, 10, 19, 4, 0, 0, 0, time.Local)) || !hasKind(kinds, taskKeepalive) {
		t.Errorf("新保活时点 = %v/%v，应为 04:00 保活", at, kinds)
	}
	at, kinds = s.nextWake(time.Date(2026, 10, 19, 4, 30, 0, 0, time.Local))
	if !at.Equal(time.Date(2026, 10, 19, 5, 0, 0, 0, time.Local)) || !hasKind(kinds, taskGrowth) {
		t.Errorf("新成长时点 = %v/%v，应为 05:00 成长", at, kinds)
	}
	at, _ = s.nextWake(time.Date(2026, 10, 19, 5, 30, 0, 0, time.Local))
	if !at.Equal(time.Date(2026, 10, 20, 3, 0, 0, 0, time.Local)) {
		t.Errorf("次日签到 = %v，应为次日 03:00（空 hours 保留原值）", at)
	}

	// 全部关闭：nextWake 零值（Run 只等 rearm/退出）。
	s.Reconfigure(nil, nil, nil, nil, nil, nil, true, true, true, true, true, true)
	if at, kinds := s.nextWake(time.Now()); !at.IsZero() || len(kinds) != 0 {
		t.Errorf("全关后 = %v/%v，应为零值", at, kinds)
	}
}

// TestPoke 非阻塞发信号：缓冲 1 的通道第二次发送丢弃；nil 通道走 default 不阻塞。
func TestPoke(t *testing.T) {
	ch := make(chan struct{}, 1)
	poke(ch)
	poke(ch)
	if len(ch) != 1 {
		t.Errorf("缓冲通道应只保留 1 个信号，got %d", len(ch))
	}
	var nilCh chan struct{}
	poke(nilCh) // 不应阻塞/panic
}

// TestSleepCtx 可取消等待：d<=0 立即按 ctx 状态返回；到时返回 true；取消返回 false。
func TestSleepCtx(t *testing.T) {
	if !sleepCtx(context.Background(), 0) || !sleepCtx(context.Background(), -time.Second) {
		t.Error("d<=0 且 ctx 未取消应返回 true")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepCtx(canceled, 0) {
		t.Error("d<=0 且 ctx 已取消应返回 false")
	}
	if !sleepCtx(context.Background(), 10*time.Millisecond) {
		t.Error("等满应返回 true")
	}
	ctx, cancel2 := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel2()
	}()
	start := time.Now()
	if sleepCtx(ctx, time.Hour) {
		t.Error("取消应返回 false")
	}
	if time.Since(start) > time.Second {
		t.Errorf("取消响应过慢: %v", time.Since(start))
	}
}

// TestAwaitWakeupGrace 迟到补跑的网络宽限：准点零延迟放行、ctx 取消立即放弃、
// 迟到时等满缩短后的宽限。
func TestAwaitWakeupGrace(t *testing.T) {
	old := wakeupGraceDelay
	wakeupGraceDelay = 20 * time.Millisecond
	t.Cleanup(func() { wakeupGraceDelay = old })

	if !awaitWakeupGrace(context.Background(), time.Now()) {
		t.Error("准点触发应零延迟放行")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if awaitWakeupGrace(canceled, time.Now()) {
		t.Error("准点但 ctx 已取消应返回 false")
	}

	late := time.Now().Add(-time.Minute)
	if awaitWakeupGrace(canceled, late) {
		t.Error("迟到补跑且 ctx 已取消应立即返回 false")
	}
	start := time.Now()
	if !awaitWakeupGrace(context.Background(), late) {
		t.Error("迟到补跑应等满宽限后继续")
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Errorf("未等满宽限: %v", elapsed)
	}
}

// TestRunRearmAndCancel Run 收到 rearm 后重算、ctx 取消后退出（不空转）。
func TestRunRearmAndCancel(t *testing.T) {
	s := New(Config{
		CheckinHours:      []int{9},
		CheckinDisabled:   true,
		TravelDisabled:    true,
		ActivityDisabled:  true,
		KeepaliveDisabled: true,
		BlackcatDisabled:  true,
		GrowthDisabled:    true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	time.Sleep(10 * time.Millisecond)
	s.rearmSchedule <- struct{}{} // slotRearm → continue 路径
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未在 ctx 取消后退出")
	}
}

// TestRunCheckinGates 签到门控：已签到幂等、无 refreshToken、global、企业号行为。
func TestRunCheckinGates(t *testing.T) {
	c := newSchedCounter()
	c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
		switch {
		case strings.HasSuffix(path, "/daily-checkin"):
			w.Write([]byte(`{"code":10001,"msg":"今天已签到"}`))
			return true
		case path == upstreamtest.FreePath:
			w.Write([]byte(upstreamtest.List(upstreamtest.Package(1000, 150, ""))))
			return true
		}
		return false
	}
	srv := c.server(t)

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "no-refresh", AccessToken: "at", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "global1", AccessToken: "at", RefreshToken: "rt", Domain: "www.workbuddy.ai", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "ent1", AccessToken: "at", RefreshToken: "rt", EnterpriseID: "e1", ExpiresAt: 9999999999})
	s := schedWithPool(srv, p, Config{})

	s.RunCheckinNow()

	if got := c.count("/daily-checkin"); got != 1 {
		t.Errorf("签到请求 = %d，应只有 u1 一次（global 跳过、企业号跳过）", got)
	}
	st, _ := p.Status("u1")
	if !st.CheckinDone {
		t.Error("上游已签到应记为 CheckinDone")
	}
	if st.Credits != 150 {
		t.Errorf("u1 credits = %d，应为 150", st.Credits)
	}
	if got := c.count(upstreamtest.FreePath); got == 0 {
		t.Error("签到后应查询余额")
	}
}

// TestRunKeepaliveIncludeDisabled 保活：禁用号默认跳过、开关打开后也刷新、无 refreshToken 跳过。
func TestRunKeepaliveIncludeDisabled(t *testing.T) {
	c := newSchedCounter()
	c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
		if strings.HasSuffix(path, "/token/refresh") {
			w.Write([]byte(`{"code":0,"data":{"accessToken":"new","expiresIn":3600}}`))
			return true
		}
		return false
	}
	srv := c.server(t)
	p := pool.New("")
	p.Add(&auth.Auth{UID: "dis", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1})
	p.Add(&auth.Auth{UID: "ok", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1})
	p.Add(&auth.Auth{UID: "no-refresh", AccessToken: "at", ExpiresAt: 1})
	p.Disable("dis", "test")
	s := schedWithPool(srv, p, Config{})

	s.RunKeepaliveNow()
	if got := c.count("/token/refresh"); got != 1 {
		t.Errorf("默认应只刷新 ok 号，got %d", got)
	}
	s.SetIncludeDisabledInTasks(true)
	s.RunKeepaliveNow()
	if got := c.count("/token/refresh"); got != 3 {
		t.Errorf("打开开关后应再刷新 dis+ok（无 rt 跳过），累计 3，got %d", got)
	}
}

// TestRunActivityCancelBetweenAccounts ctx 在账号间取消：立即放弃剩余账号。
func TestRunActivityCancelBetweenAccounts(t *testing.T) {
	old := activityAccountDelay
	activityAccountDelay = time.Hour // 若未取消会卡住
	t.Cleanup(func() { activityAccountDelay = old })

	ctx, cancel := context.WithCancel(context.Background())
	c := newSchedCounter()
	c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
		if path == "/v2/report" {
			cancel() // 首个账号上报后取消
			w.Write([]byte(`{"code":0,"msg":"OK"}`))
			return true
		}
		return false
	}
	srv := c.server(t)
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "u2", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	s := schedWithPool(srv, p, Config{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runActivity(ctx)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runActivity 未在取消后立即退出")
	}
	if got := c.count("/v2/report"); got != 1 {
		t.Errorf("取消后应只上报 1 个账号，got %d", got)
	}
}

// TestStartBalanceRefresh 余额后台刷新：按间隔刷新、热改间隔生效、暂停/取消后停止。
func TestStartBalanceRefresh(t *testing.T) {
	var replies atomic.Int64
	replies.Store(123)
	c := newSchedCounter()
	c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
		if path == upstreamtest.FreePath {
			w.Write([]byte(upstreamtest.List(upstreamtest.Package(1000, replies.Load(), ""))))
			return true
		}
		return false
	}
	srv := c.server(t)
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	s := schedWithPool(srv, p, Config{})

	// interval<=0 不启动。
	s.StartBalanceRefresh(context.Background(), 0)
	if got := c.count(upstreamtest.FreePath); got != 0 {
		t.Errorf("interval<=0 不应启动刷新，got %d", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartBalanceRefresh(ctx, 10*time.Millisecond)
	waitUntil(t, "首轮余额刷新", func() bool { return c.count(upstreamtest.FreePath) >= 1 })
	waitUntil(t, "号池余额更新", func() bool {
		st, ok := p.Status("u1")
		return ok && st.Credits == 123
	})

	// 热改间隔后继续刷新。
	replies.Store(456)
	s.SetBalanceInterval(5 * time.Millisecond)
	waitUntil(t, "新间隔下的刷新", func() bool {
		st, ok := p.Status("u1")
		return ok && st.Credits == 456
	})

	// 暂停后取消：不再发请求。
	s.SetBalanceInterval(0)
	if got := s.balanceInterval.Load(); got != 0 {
		t.Errorf("SetBalanceInterval(0) 后 = %d，应为 0", got)
	}
	s.SetBalanceInterval(-time.Minute) // 负值归零
	if got := s.balanceInterval.Load(); got != 0 {
		t.Errorf("负值应归零，got %d", got)
	}
	cancel()
	time.Sleep(30 * time.Millisecond)
	before := c.count(upstreamtest.FreePath)
	time.Sleep(30 * time.Millisecond)
	if after := c.count(upstreamtest.FreePath); after != before {
		t.Errorf("取消后仍在刷新: %d → %d", before, after)
	}
}
