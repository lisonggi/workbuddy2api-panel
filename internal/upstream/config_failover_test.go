package upstream

// config_failover_test.go 拉配置（/v3/config）的超时与失败切换：主入口 → 备用入口 →
// 备用 IP、data 无效才换、504/500 不换、5 秒预算（测试调小）、国际版与不可信 base
// 只发一次。全部走假 RoundTripper，不连外网。

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const cfgValidBody = `{"code":0,"data":{"models":[{"id":"m1","maxOutputTokens":1000}]}}`

// cfgReq 假上游收到的一次配置请求。
type cfgReq struct {
	host   string
	pinned bool
}

// cfgResp 一次配置请求的响应。
type cfgResp struct {
	status int
	body   string
	err    error
	// waitCtx 等请求 ctx 结束后返回它的错误（模拟挂到超时）。
	waitCtx bool
}

// cfgFake 拉配置的假上游：按 host 与是否钉 IP 分发。
type cfgFake struct {
	mu        sync.Mutex
	reqs      []cfgReq
	pinnedIPs []string
	main      cfgResp
	backup    cfgResp
	backupPin cfgResp
}

func (f *cfgFake) roundTrip(r *http.Request) (*http.Response, error) {
	return f.serve(r, false)
}

func (f *cfgFake) pin(ip string) http.RoundTripper {
	return rtFunc(func(r *http.Request) (*http.Response, error) {
		f.mu.Lock()
		f.pinnedIPs = append(f.pinnedIPs, ip)
		f.mu.Unlock()
		return f.serve(r, true)
	})
}

func (f *cfgFake) serve(r *http.Request, pinned bool) (*http.Response, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, cfgReq{host: r.URL.Host, pinned: pinned})
	f.mu.Unlock()
	var resp cfgResp
	switch {
	case pinned:
		resp = f.backupPin
	case r.URL.Host == foBackupHost:
		resp = f.backup
	default:
		resp = f.main
	}
	if resp.waitCtx {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	if resp.err != nil {
		return nil, resp.err
	}
	return jsonResp(resp.status, resp.body), nil
}

func (f *cfgFake) seen() []cfgReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cfgReq(nil), f.reqs...)
}

// cfgClient 国内版主入口 + 假上游（拉配置用）。
func cfgClient(f *cfgFake) *Client {
	return &Client{
		HTTP:          &http.Client{Transport: rtFunc(f.roundTrip)},
		ChatBaseCN:    cnProductEndpoint,
		GlobalEnabled: true,
		PinIP:         f.pin,
	}
}

// setConfigBudgets 临时调小拉配置的预算，结束后还原。
func setConfigBudgets(t *testing.T, budget, minBudget, timeoutBudget time.Duration) {
	t.Helper()
	oldBudget, oldMin, oldTimeout := configFetchBudget, configFetchMinBudget, configFetchTimeoutBudget
	configFetchBudget, configFetchMinBudget, configFetchTimeoutBudget = budget, minBudget, timeoutBudget
	t.Cleanup(func() {
		configFetchBudget, configFetchMinBudget, configFetchTimeoutBudget = oldBudget, oldMin, oldTimeout
	})
}

// TestConfigFetchMainToBackupToFallbackIP 主入口 503 → 备用入口 503 → 备用 IP 成功。
func TestConfigFetchMainBackupFallbackIP(t *testing.T) {
	f := &cfgFake{
		main:      cfgResp{status: http.StatusServiceUnavailable, body: `{"code":503}`},
		backup:    cfgResp{status: http.StatusServiceUnavailable, body: `{"code":503}`},
		backupPin: cfgResp{status: http.StatusOK, body: cfgValidBody},
	}
	c := cfgClient(f)
	out, err := c.fetchV3ConfigModelMap(upAuth())
	if err != nil {
		t.Fatalf("fetchV3ConfigModelMap: %v", err)
	}
	if _, ok := out["m1"]; !ok {
		t.Fatalf("目录 = %+v，应含 m1", out)
	}
	reqs := f.seen()
	if len(reqs) != 3 {
		t.Fatalf("请求数 = %d，应为 3（主入口、备用入口、备用 IP）", len(reqs))
	}
	if reqs[0].host != foMainHost || reqs[0].pinned {
		t.Errorf("第 1 次 = %+v，应为主入口且未钉 IP", reqs[0])
	}
	if reqs[1].host != foBackupHost || reqs[1].pinned {
		t.Errorf("第 2 次 = %+v，应为备用入口且未钉 IP", reqs[1])
	}
	if reqs[2].host != foBackupHost || !reqs[2].pinned {
		t.Errorf("第 3 次 = %+v，应为备用入口且钉 IP", reqs[2])
	}
	if len(f.pinnedIPs) != 1 || len(failoverFallbackIPs) == 0 || f.pinnedIPs[0] != failoverFallbackIPs[0] {
		t.Errorf("钉的 IP = %v，应为 failoverFallbackIPs[0]", f.pinnedIPs)
	}
}

// TestConfigFetchDataInvalidSwitches 200 但 data 是空对象：按官方口径换入口。
func TestConfigFetchDataInvalidSwitches(t *testing.T) {
	f := &cfgFake{
		main:   cfgResp{status: http.StatusOK, body: `{"code":0,"data":{}}`},
		backup: cfgResp{status: http.StatusOK, body: cfgValidBody},
	}
	c := cfgClient(f)
	out, err := c.fetchV3ConfigModelMap(upAuth())
	if err != nil || out["m1"].ID != "m1" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if reqs := f.seen(); len(reqs) != 2 || reqs[1].host != foBackupHost {
		t.Fatalf("请求 = %+v，应为两次（主入口 data 无效后换备用入口）", reqs)
	}
}

// TestConfigFetchNoSwitchStatuses 504/500 不换；国际版与不可信 base 只发一次。
func TestConfigFetchNoSwitchStatuses(t *testing.T) {
	for _, status := range []int{http.StatusGatewayTimeout, http.StatusInternalServerError} {
		f := &cfgFake{
			main:   cfgResp{status: status, body: `{"code":1}`},
			backup: cfgResp{status: http.StatusOK, body: cfgValidBody},
		}
		c := cfgClient(f)
		if _, err := c.fetchV3ConfigModelMap(upAuth()); err == nil || !strings.Contains(err.Error(), "status") {
			t.Fatalf("status %d: err = %v，应原样返回", status, err)
		}
		if reqs := f.seen(); len(reqs) != 1 {
			t.Fatalf("status %d: 请求数 = %d，应只发一次", status, len(reqs))
		}
	}

	t.Run("国际版只发一次", func(t *testing.T) {
		f := &cfgFake{
			main:   cfgResp{status: http.StatusServiceUnavailable, body: `{"code":503}`},
			backup: cfgResp{status: http.StatusOK, body: cfgValidBody},
		}
		c := cfgClient(f)
		c.ChatBaseGlobal = cnProductEndpoint
		if _, err := c.fetchV3ConfigModelMap(globalAuth()); err == nil {
			t.Fatal("应返回错误")
		}
		if reqs := f.seen(); len(reqs) != 1 {
			t.Fatalf("国际版请求数 = %d，应只发一次", len(reqs))
		}
	})

	t.Run("不可信 base 只发一次", func(t *testing.T) {
		f := &cfgFake{
			main:   cfgResp{status: http.StatusServiceUnavailable, body: `{"code":503}`},
			backup: cfgResp{status: http.StatusOK, body: cfgValidBody},
		}
		c := cfgClient(f)
		c.ChatBaseCN = upBase
		if _, err := c.fetchV3ConfigModelMap(upAuth()); err == nil {
			t.Fatal("应返回错误")
		}
		if reqs := f.seen(); len(reqs) != 1 {
			t.Fatalf("不可信 base 请求数 = %d，应只发一次", len(reqs))
		}
	})
}

// TestConfigFetchBudget 总预算用尽后不再发备用入口；超时类失败给 3 秒独立预算。
func TestConfigFetchBudget(t *testing.T) {
	t.Run("预算不够不换", func(t *testing.T) {
		setConfigBudgets(t, 60*time.Millisecond, 250*time.Millisecond, 3*time.Second)
		f := &cfgFake{
			main:   cfgResp{status: http.StatusServiceUnavailable, body: `{"code":503}`},
			backup: cfgResp{status: http.StatusOK, body: cfgValidBody},
		}
		c := cfgClient(f)
		if _, err := c.fetchV3ConfigModelMap(upAuth()); err == nil {
			t.Fatal("应返回错误")
		}
		if reqs := f.seen(); len(reqs) != 1 {
			t.Fatalf("请求数 = %d，预算不足时应只发一次", len(reqs))
		}
	})

	t.Run("超时给独立预算", func(t *testing.T) {
		setConfigBudgets(t, 40*time.Millisecond, 10*time.Millisecond, 300*time.Millisecond)
		f := &cfgFake{
			main:   cfgResp{waitCtx: true},
			backup: cfgResp{status: http.StatusServiceUnavailable, body: `{"code":503}`},
		}
		c := cfgClient(f)
		start := time.Now()
		if _, err := c.fetchV3ConfigModelMap(upAuth()); err == nil {
			t.Fatal("应返回错误")
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("耗时 %v，应受测试预算约束（不能真等 5 秒）", elapsed)
		}
		reqs := f.seen()
		if len(reqs) != 2 || reqs[1].host != foBackupHost {
			t.Fatalf("请求 = %+v，主入口超时后应发备用入口（独立预算）", reqs)
		}
	})
}

// TestConfigFetchBreakerSkipsFront 前门熔断打开、备用入口关着：直接发备用入口，失败后
// 直接试备用 IP（不再发前门）。
func TestConfigFetchBreakerDirectBackup(t *testing.T) {
	f := &cfgFake{
		main:      cfgResp{status: http.StatusOK, body: cfgValidBody},
		backup:    cfgResp{status: http.StatusServiceUnavailable, body: `{"code":503}`},
		backupPin: cfgResp{status: http.StatusOK, body: cfgValidBody},
	}
	c := cfgClient(f)
	for i := 0; i < failoverThreshold; i++ {
		c.failover.noteInfraFailure(cnProductEndpoint)
	}
	out, err := c.fetchV3ConfigModelMap(upAuth())
	if err != nil || out["m1"].ID != "m1" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	reqs := f.seen()
	if len(reqs) != 2 {
		t.Fatalf("请求 = %+v，应直接发备用入口再试备用 IP", reqs)
	}
	if reqs[0].host != foBackupHost || reqs[0].pinned {
		t.Errorf("第 1 次 = %+v，应直接发备用入口且未钉 IP", reqs[0])
	}
	if reqs[1].host != foBackupHost || !reqs[1].pinned {
		t.Errorf("第 2 次 = %+v，应为备用入口备用 IP", reqs[1])
	}
}
