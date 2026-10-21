// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

// httpdns_test.go HTTPDNS 客户端：响应体解析、conf 失败兜底、强制刷新、缓存与 75%
// 后台刷新、单个 IP 失败换下一个、请求形状。测试用 httptest 的 TLS 服务器，拨号函数
// 指向它，不连任何官方 IP。

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// dnsIPv4 造 192.0.2.0/24（文档保留段）的测试 IP。
func dnsIPv4(n byte) string { return net.IPv4(192, 0, 2, n).String() }

// setDNSConsts 临时替换 HTTPDNS 相关常量列表（测试 IP 现造），结束后还原。
func setDNSConsts(t *testing.T, bootstrap, defaults, fallback []string) {
	t.Helper()
	oldBootstrap, oldDefaults, oldFallback := httpDNSBootstrapIPs, httpDNSDefaultServiceIPs, failoverFallbackIPs
	httpDNSBootstrapIPs, httpDNSDefaultServiceIPs, failoverFallbackIPs = bootstrap, defaults, fallback
	t.Cleanup(func() {
		httpDNSBootstrapIPs, httpDNSDefaultServiceIPs, failoverFallbackIPs = oldBootstrap, oldDefaults, oldFallback
	})
}

// dnsTestReq 假 HTTPDNS 服务器收到的一次请求。
type dnsTestReq struct {
	path   string
	query  string
	host   string
	ua     string
	header http.Header
}

// dnsTestServer 假 HTTPDNS：/conf 按队列返回 conf 响应，/d 按 dn 返回解析响应；
// resolveStatus 按请求的 Host（服务 IP）给失败状态。
type dnsTestServer struct {
	mu            sync.Mutex
	conf          []string
	confStatus    int
	resolve       map[string][]string
	resolveStatus map[string]int
	reqs          []dnsTestReq
	srv           *httptest.Server
}

func newDNSTestServer(t *testing.T) *dnsTestServer {
	t.Helper()
	s := &dnsTestServer{resolve: map[string][]string{}, resolveStatus: map[string]int{}}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *dnsTestServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, dnsTestReq{
		path:   r.URL.Path,
		query:  r.URL.RawQuery,
		host:   r.Host,
		ua:     r.UserAgent(),
		header: r.Header.Clone(),
	})
	status := http.StatusOK
	body := ""
	switch r.URL.Path {
	case httpDNSConfPath:
		if s.confStatus != 0 {
			status = s.confStatus
		}
		if len(s.conf) > 0 {
			body = s.conf[0]
			s.conf = s.conf[1:]
		}
		if body == "" && status == http.StatusOK {
			body = "ip:" + dnsIPv4(10)
		}
	case httpDNSResolvePath:
		if st := s.resolveStatus[r.Host]; st != 0 {
			status = st
		}
		// 解析响应是「粘」的（同一域名重复请求返回同一份），测试换结果用 setResolve。
		if q := s.resolve[r.URL.Query().Get("dn")]; len(q) > 0 {
			body = q[0]
		}
	}
	s.mu.Unlock()
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// requests 已收到的请求副本。
func (s *dnsTestServer) requests() []dnsTestReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]dnsTestReq(nil), s.reqs...)
}

// setResolve 替换某个域名的解析响应队列。
func (s *dnsTestServer) setResolve(host string, bodies ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolve[host] = bodies
}

// client 造一个拨号到假服务器的 HTTPDNS 客户端。
func (s *dnsTestServer) client() *httpDNSClient {
	d := newHTTPDNSClient()
	addr := s.srv.Listener.Addr().String()
	d.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	return d
}

func TestParseHTTPDNSConf(t *testing.T) {
	ip1, ip2, ip3 := dnsIPv4(1), dnsIPv4(2), dnsIPv4(3)
	cases := []struct {
		name string
		body string
		want []string
		ttl  time.Duration
	}{
		{"多段与 ttl", "ip:" + ip1 + ";" + ip2 + "|ttl:600", []string{ip1, ip2}, 600 * time.Second},
		{"无 ttl", "ip:" + ip1, []string{ip1}, 0},
		{"ttl 非数字", "ip:" + ip1 + "|ttl:abc", []string{ip1}, 0},
		{"键大小写与空白", " IP : " + ip1 + " | TTL : 60 ", []string{ip1}, 60 * time.Second},
		{"冒号开头的段跳过", ":" + ip1 + "|ip:" + ip2, []string{ip2}, 0},
		{"没有冒号的段跳过", "junk|ip:" + ip1, []string{ip1}, 0},
		{"逗号与空白分隔、非法项丢掉", "ip:" + ip1 + ", not-an-ip " + ip2, []string{ip1, ip2}, 0},
		{"去重保序", "ip:" + ip3 + ";" + ip1 + ";" + ip3, []string{ip3, ip1}, 0},
		{"空响应", "  ", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ttl := parseHTTPDNSConf([]byte(tc.body))
			if len(got) != len(tc.want) {
				t.Fatalf("ips = %v，应为 %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ips = %v，应为 %v", got, tc.want)
				}
			}
			if ttl != tc.ttl {
				t.Fatalf("ttl = %v，应为 %v", ttl, tc.ttl)
			}
		})
	}
}

func TestParseHTTPDNSResolve(t *testing.T) {
	ip1, ip2, ip3 := dnsIPv4(1), dnsIPv4(2), dnsIPv4(3)
	ipv6 := net.ParseIP("2001:db8::1").String()
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"分号分隔", ip1 + ";" + ip2, []string{ip1, ip2}},
		{"逗号带 ttl 收第一截", ip1 + ",3600", []string{ip1}},
		{"0 为空", "0", nil},
		{"空响应", "   ", nil},
		{"竖线后面忽略", ip1 + "|" + ip2, []string{ip1}},
		{"IPv6", ipv6, []string{ipv6}},
		{"逗号多截里挑 IP", "bad," + ip2 + "," + ip3, []string{ip2, ip3}},
		{"空白与分号混合", ip1 + " " + ip2 + ";" + ip3, []string{ip1, ip2, ip3}},
		{"去重保序", ip1 + ";" + ip2 + ";" + ip1, []string{ip1, ip2}},
		{"第一截不是 IP 不按 ttl 收", "x,123", nil},
		{"0 段跳过", "0;" + ip1 + ";0", []string{ip1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseHTTPDNSResolve([]byte(tc.body))
			if len(got) != len(tc.want) {
				t.Fatalf("ips = %v，应为 %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ips = %v，应为 %v", got, tc.want)
				}
			}
		})
	}
}

func TestHTTPDNSLocalhostAndIP(t *testing.T) {
	d := newHTTPDNSClient()
	d.dial = func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("localhost / IP 不应发请求")
		return nil, nil
	}
	for _, host := range []string{"localhost", "LOCALHOST", dnsIPv4(9)} {
		if got := d.query(context.Background(), host); got != nil {
			t.Errorf("query(%q) = %v，应为空", host, got)
		}
	}
}

// TestHTTPDNSRequestShape 请求形状：Host 头是 IP、只带 Host 与空 User-Agent、
// query 用 url.Values 编码（dn 在前），conf 失败退回默认服务 IP。
func TestHTTPDNSRequestShape(t *testing.T) {
	ipA, ipB, ipC := dnsIPv4(1), dnsIPv4(2), dnsIPv4(3)
	setDNSConsts(t, []string{ipA}, []string{ipB}, nil)
	s := newDNSTestServer(t)
	s.conf = []string{"ip:" + ipC + "|ttl:60"}
	s.setResolve("www.workbuddy.cn", ipB+";"+ipA)
	d := s.client()

	got := d.query(context.Background(), "www.workbuddy.cn")
	if len(got) != 2 || got[0] != ipB || got[1] != ipA {
		t.Fatalf("query = %v，应为 [%s %s]", got, ipB, ipA)
	}
	reqs := s.requests()
	if len(reqs) != 2 {
		t.Fatalf("请求数 = %d，应为 2（conf + 解析）", len(reqs))
	}
	if reqs[0].path != httpDNSConfPath || reqs[0].host != ipA {
		t.Errorf("conf 请求 = %s host=%q，应为 %s host=%s", reqs[0].path, reqs[0].host, httpDNSConfPath, ipA)
	}
	if want := (url.Values{"token": {httpDNSToken}}).Encode(); reqs[0].query != want {
		t.Errorf("conf query = %q，应为 %q", reqs[0].query, want)
	}
	if reqs[1].path != httpDNSResolvePath || reqs[1].host != ipC {
		t.Errorf("解析请求 = %s host=%q，应为 %s host=%s", reqs[1].path, reqs[1].host, httpDNSResolvePath, ipC)
	}
	wantQuery := (url.Values{"dn": {"www.workbuddy.cn"}, "token": {httpDNSToken}}).Encode()
	if reqs[1].query != wantQuery {
		t.Errorf("解析 query = %q，应为 %q", reqs[1].query, wantQuery)
	}
	for i, r := range reqs {
		if r.ua != "" {
			t.Errorf("第 %d 个请求带了 User-Agent %q，应不发", i+1, r.ua)
		}
		if len(r.header) != 0 {
			t.Errorf("第 %d 个请求带了额外请求头 %v，应只有 Host", i+1, r.header)
		}
	}
}

// TestHTTPDNSConfFallbackToDefaults conf 全部失败且没有缓存时用默认服务 IP。
func TestHTTPDNSConfFailureDefaultIPs(t *testing.T) {
	ipA, ipB, ipC := dnsIPv4(1), dnsIPv4(2), dnsIPv4(3)
	setDNSConsts(t, []string{ipA}, []string{ipB}, nil)
	s := newDNSTestServer(t)
	s.confStatus = http.StatusInternalServerError
	s.setResolve("www.workbuddy.cn", ipC)
	d := s.client()

	got := d.query(context.Background(), "www.workbuddy.cn")
	if len(got) != 1 || got[0] != ipC {
		t.Fatalf("query = %v，应为 [%s]", got, ipC)
	}
	reqs := s.requests()
	if len(reqs) != 2 {
		t.Fatalf("请求数 = %d，应为 2", len(reqs))
	}
	if reqs[0].path != httpDNSConfPath || reqs[0].host != ipA {
		t.Errorf("conf 请求 host=%q，应为 %s", reqs[0].host, ipA)
	}
	if reqs[1].path != httpDNSResolvePath || reqs[1].host != ipB {
		t.Errorf("解析请求 host=%q，应为默认服务 IP %s", reqs[1].host, ipB)
	}
}

// TestHTTPDNSStatusNextIP 单个服务 IP 返回 ≥400 时试下一个。
func TestHTTPDNSStatusNextIP(t *testing.T) {
	ipA, ipB, ipC := dnsIPv4(1), dnsIPv4(2), dnsIPv4(3)
	setDNSConsts(t, []string{ipA}, nil, nil)
	s := newDNSTestServer(t)
	s.conf = []string{"ip:" + ipA + ";" + ipB}
	s.resolveStatus[ipA] = http.StatusBadGateway
	s.setResolve("www.workbuddy.cn", ipC)
	d := s.client()

	got := d.query(context.Background(), "www.workbuddy.cn")
	if len(got) != 1 || got[0] != ipC {
		t.Fatalf("query = %v，应为 [%s]", got, ipC)
	}
	reqs := s.requests()
	if len(reqs) != 3 {
		t.Fatalf("请求数 = %d，应为 3（conf + 两个服务 IP）", len(reqs))
	}
	if reqs[1].host != ipA || reqs[2].host != ipB {
		t.Errorf("服务 IP 顺序 = %q, %q，应为 %s, %s", reqs[1].host, reqs[2].host, ipA, ipB)
	}
}

// TestHTTPDNSForceConfOnEmpty 全部服务 IP 都解析为空时强制重新取 conf：新列表相同
// 返回空，不同就用新列表再解析一次。
func TestHTTPDNSForceConfOnEmpty(t *testing.T) {
	ipA, ipB, ipC := dnsIPv4(1), dnsIPv4(2), dnsIPv4(3)
	setDNSConsts(t, []string{ipA}, nil, nil)

	t.Run("新列表相同返回空", func(t *testing.T) {
		s := newDNSTestServer(t)
		s.conf = []string{"ip:" + ipA, "ip:" + ipA}
		// 解析响应为空（默认空体）。
		d := s.client()
		if got := d.query(context.Background(), "www.workbuddy.cn"); len(got) != 0 {
			t.Fatalf("query = %v，应为空", got)
		}
		reqs := s.requests()
		if len(reqs) != 3 {
			t.Fatalf("请求数 = %d，应为 3（conf、解析、强制 conf）", len(reqs))
		}
		if reqs[2].path != httpDNSConfPath {
			t.Errorf("第 3 个请求 = %s，应为强制 conf", reqs[2].path)
		}
	})

	t.Run("新列表不同再解析", func(t *testing.T) {
		s := newDNSTestServer(t)
		s.conf = []string{"ip:" + ipA, "ip:" + ipB}
		s.resolveStatus[ipA] = http.StatusBadGateway
		s.setResolve("www.workbuddy.cn", ipC)
		d := s.client()
		got := d.query(context.Background(), "www.workbuddy.cn")
		if len(got) != 1 || got[0] != ipC {
			t.Fatalf("query = %v，应为 [%s]", got, ipC)
		}
		reqs := s.requests()
		if len(reqs) != 4 {
			t.Fatalf("请求数 = %d，应为 4（conf、解析、强制 conf、再解析）", len(reqs))
		}
		if reqs[2].path != httpDNSConfPath || reqs[3].host != ipB {
			t.Errorf("强制刷新后请求 = %s host=%q，应为 conf 后解析 %s", reqs[2].path, reqs[3].host, ipB)
		}
	})
}

// TestHTTPDNSResolveCache 解析结果缓存 300 秒：75% 前命中，75% 后台刷新且先用旧值。
func TestHTTPDNSResolveCache(t *testing.T) {
	ipA, ipX, ipY := dnsIPv4(1), dnsIPv4(2), dnsIPv4(3)
	setDNSConsts(t, []string{ipA}, nil, nil)
	s := newDNSTestServer(t)
	s.conf = []string{"ip:" + ipA + "|ttl:3600"}
	s.setResolve("www.workbuddy.cn", ipX)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	d := s.client()
	d.now = func() time.Time { return now }

	if got := d.query(context.Background(), "www.workbuddy.cn"); len(got) != 1 || got[0] != ipX {
		t.Fatalf("首次 query = %v，应为 [%s]", got, ipX)
	}
	if n := len(s.requests()); n != 2 {
		t.Fatalf("首次请求数 = %d，应为 2", n)
	}

	// 200 秒（< 75%×300=225）命中缓存。
	now = now.Add(200 * time.Second)
	if got := d.query(context.Background(), "www.workbuddy.cn"); len(got) != 1 || got[0] != ipX {
		t.Fatalf("缓存命中 query = %v，应为 [%s]", got, ipX)
	}
	if n := len(s.requests()); n != 2 {
		t.Fatalf("缓存命中时请求数 = %d，应仍为 2", n)
	}

	// 到 75%：后台刷新（现取 conf 再解析，见 TestHTTPDNSRefreshSkipsServiceCache），本次仍返回旧值。
	s.setResolve("www.workbuddy.cn", ipY)
	now = now.Add(25 * time.Second)
	if got := d.query(context.Background(), "www.workbuddy.cn"); len(got) != 1 || got[0] != ipX {
		t.Fatalf("刷新期间 query = %v，应为旧值 [%s]", got, ipX)
	}
	d.wg.Wait()
	if n := len(s.requests()); n != 4 {
		t.Fatalf("后台刷新后请求数 = %d，应为 4（conf + 解析）", n)
	}
	now = now.Add(time.Second)
	if got := d.query(context.Background(), "www.workbuddy.cn"); len(got) != 1 || got[0] != ipY {
		t.Fatalf("刷新后 query = %v，应为新值 [%s]", got, ipY)
	}
}

// TestHTTPDNSServiceTTLClamp conf 的 ttl 夹在 60 秒到 12 小时之间：ttl=1 按 60 秒算，
// 45 秒（75%）触发后台刷新。
func TestHTTPDNSServiceTTLClamp(t *testing.T) {
	ipA, ipX := dnsIPv4(1), dnsIPv4(2)
	setDNSConsts(t, []string{ipA}, nil, nil)
	s := newDNSTestServer(t)
	s.conf = []string{"ip:" + ipA + "|ttl:1"}
	// 每次查询用不同域名，避开解析缓存（缓存命中不会去碰服务 IP 列表）。
	s.setResolve("first.test", ipX)
	s.setResolve("second.test", ipX)
	s.setResolve("third.test", ipX)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	d := s.client()
	d.now = func() time.Time { return now }

	if got := d.query(context.Background(), "first.test"); len(got) != 1 {
		t.Fatalf("首次 query = %v", got)
	}
	confCount := func() int {
		n := 0
		for _, r := range s.requests() {
			if r.path == httpDNSConfPath {
				n++
			}
		}
		return n
	}
	if n := confCount(); n != 1 {
		t.Fatalf("conf 请求数 = %d，应为 1", n)
	}
	now = now.Add(44 * time.Second)
	_ = d.query(context.Background(), "second.test")
	if n := confCount(); n != 1 {
		t.Fatalf("44 秒时 conf 请求数 = %d，应为 1（未到 75%%）", n)
	}
	now = now.Add(time.Second) // 45 秒
	_ = d.query(context.Background(), "third.test")
	d.wg.Wait()
	if n := confCount(); n != 2 {
		t.Fatalf("45 秒时 conf 请求数 = %d，应为 2（75%% 后台刷新）", n)
	}
}

// TestHTTPDNSBudget 调用方最多等 600 毫秒：请求挂住时按「没有结果」返回，不报错；挂住的
// 请求闲置 300 毫秒后算失败，后台查询随后结束并缓存空结果。
func TestHTTPDNSBudget(t *testing.T) {
	ipA := dnsIPv4(1)
	setDNSConsts(t, []string{ipA}, nil, nil)
	var hits atomic.Int32
	d, release := dnsSlowClient(t, func(w http.ResponseWriter, r *http.Request, release <-chan struct{}) {
		hits.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	start := time.Now()
	if got := d.query(context.Background(), "www.workbuddy.cn"); len(got) != 0 {
		t.Fatalf("query = %v，应为空", got)
	}
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Fatalf("查询耗时 %v，应在 600ms 左右返回", elapsed)
	}
	d.wg.Wait()
	release()
	n := hits.Load()
	if n != 2 {
		t.Fatalf("请求数 = %d，应为 2（conf 与强制 conf 各一次，都闲置超时）", n)
	}
	if got := d.query(context.Background(), "www.workbuddy.cn"); len(got) != 0 || hits.Load() != n {
		t.Fatalf("空结果应缓存：query = %v，请求数 %d → %d", got, n, hits.Load())
	}
}

// TestHTTPDNSNoUserAgentHeader 单独确认请求头只有 Host（服务端 r.Header 为空）。
func TestHTTPDNSNoUserAgentHeader(t *testing.T) {
	ipA := dnsIPv4(1)
	setDNSConsts(t, []string{ipA}, nil, nil)
	s := newDNSTestServer(t)
	s.setResolve("www.workbuddy.cn", ipA)
	d := s.client()
	_ = d.query(context.Background(), "www.workbuddy.cn")
	for _, r := range s.requests() {
		if strings.Contains(strings.ToLower(r.header.Get("User-Agent")), "go-http") {
			t.Fatalf("请求带了 Go 默认 User-Agent: %q", r.header.Get("User-Agent"))
		}
	}
}

// dnsSlowClient 造一个拨号到自定义处理函数的 HTTPDNS 客户端。release 放行挂住的处理函数，
// 测试结束时自动放行、等后台查询结束再关服务器（后台查询会读被 setDNSConsts 换掉的常量）。
func dnsSlowClient(t *testing.T, h func(http.ResponseWriter, *http.Request, <-chan struct{})) (*httpDNSClient, func()) {
	t.Helper()
	ch := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(ch) }) }
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h(w, r, ch) }))
	d := newHTTPDNSClient()
	addr := srv.Listener.Addr().String()
	d.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	t.Cleanup(func() {
		release()
		d.wg.Wait()
		srv.Close()
	})
	return d, release
}

// dnsTrickle 分几段慢慢写响应：每段之间停 gap（小于 300 毫秒的闲置超时），整个响应超过
// 300 毫秒。
func dnsTrickle(w http.ResponseWriter, body string, parts int, gap time.Duration) {
	fl, _ := w.(http.Flusher)
	step := (len(body) + parts - 1) / parts
	for i := 0; i < parts; i++ {
		time.Sleep(gap)
		lo, hi := i*step, (i+1)*step
		if lo > len(body) {
			lo = len(body)
		}
		if hi > len(body) {
			hi = len(body)
		}
		_, _ = io.WriteString(w, body[lo:hi])
		if fl != nil {
			fl.Flush()
		}
	}
}

// TestHTTPDNSIdleTimeout 单个请求的 300 毫秒是闲置超时：一直有数据往来时整个请求可以超过
// 300 毫秒；连续 300 毫秒没有数据就算失败，复用的连接上失败也不换连接重发。
func TestHTTPDNSIdleTimeout(t *testing.T) {
	ipA := dnsIPv4(1)
	var stuck atomic.Int32
	d, _ := dnsSlowClient(t, func(w http.ResponseWriter, r *http.Request, _ <-chan struct{}) {
		switch r.URL.Query().Get("dn") {
		case "slow.test":
			dnsTrickle(w, ipA+";"+dnsIPv4(2), 4, 120*time.Millisecond)
		case "stuck.test":
			stuck.Add(1)
			time.Sleep(500 * time.Millisecond)
			_, _ = io.WriteString(w, ipA)
		}
	})
	start := time.Now()
	raw, ok := d.get(context.Background(), ipA, httpDNSResolvePath, url.Values{"dn": {"slow.test"}})
	if took := time.Since(start); !ok || string(raw) != ipA+";"+dnsIPv4(2) || took < 400*time.Millisecond {
		t.Fatalf("一直有数据的慢请求：ok=%v raw=%q 用时 %v，应成功且超过 300ms", ok, raw, took)
	}
	start = time.Now()
	if _, ok := d.get(context.Background(), ipA, httpDNSResolvePath, url.Values{"dn": {"stuck.test"}}); ok {
		t.Fatal("闲置 500ms 的请求应失败")
	}
	if took := time.Since(start); took < 250*time.Millisecond || took > 480*time.Millisecond {
		t.Fatalf("闲置超时用时 %v，应在 300ms 左右", took)
	}
	if n := stuck.Load(); n != 1 {
		t.Fatalf("超时的请求发了 %d 次，应只发 1 次（不自动重发）", n)
	}
	// 复用池里的连接：前一个请求结束后连接闲着不受超时影响，再发照样成功。
	time.Sleep(400 * time.Millisecond)
	if _, ok := d.get(context.Background(), ipA, httpDNSResolvePath, url.Values{"dn": {"slow.test"}}); !ok {
		t.Fatal("空闲 400ms 后再发应成功")
	}
}

// TestHTTPDNSSlowAnswerCached 查询超过 600 毫秒：这一次按没有结果返回，查询在后台跑完并写
// 进缓存，下一次直接命中、不再发请求。
func TestHTTPDNSSlowAnswerCached(t *testing.T) {
	ipA, ipX := dnsIPv4(1), dnsIPv4(2)
	setDNSConsts(t, []string{ipA}, nil, nil)
	var hits atomic.Int32
	d, _ := dnsSlowClient(t, func(w http.ResponseWriter, r *http.Request, _ <-chan struct{}) {
		hits.Add(1)
		if r.URL.Path == httpDNSConfPath {
			dnsTrickle(w, "ip:"+ipA+"|ttl:3600", 4, 120*time.Millisecond)
			return
		}
		dnsTrickle(w, ipX, 4, 120*time.Millisecond)
	})
	start := time.Now()
	if got := d.query(context.Background(), "www.workbuddy.cn"); len(got) != 0 {
		t.Fatalf("首次 query = %v，超过 600ms 应先返回空", got)
	}
	if took := time.Since(start); took > 900*time.Millisecond {
		t.Fatalf("首次 query 用时 %v，应在 600ms 左右返回", took)
	}
	d.wg.Wait()
	n := hits.Load()
	if got := d.query(context.Background(), "www.workbuddy.cn"); len(got) != 1 || got[0] != ipX {
		t.Fatalf("第二次 query = %v，应命中后台查完的 [%s]", got, ipX)
	}
	if hits.Load() != n {
		t.Fatalf("命中缓存不应再发请求：%d → %d", n, hits.Load())
	}
}

// TestHTTPDNSLookupPerMiss 缓存未命中时每次各查一路（同官方 lookupHttpDns），同一域名并发
// 未命中不合并。
func TestHTTPDNSLookupPerMiss(t *testing.T) {
	ipA, ipX := dnsIPv4(1), dnsIPv4(2)
	setDNSConsts(t, []string{ipA}, nil, nil)
	var resolves atomic.Int32
	d, _ := dnsSlowClient(t, func(w http.ResponseWriter, r *http.Request, _ <-chan struct{}) {
		if r.URL.Path == httpDNSConfPath {
			_, _ = io.WriteString(w, "ip:"+ipA)
			return
		}
		resolves.Add(1)
		dnsTrickle(w, ipX, 2, 100*time.Millisecond)
	})
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := d.query(context.Background(), "www.workbuddy.cn"); len(got) != 1 || got[0] != ipX {
				t.Errorf("query = %v", got)
			}
		}()
	}
	wg.Wait()
	if n := resolves.Load(); n != 5 {
		t.Fatalf("解析请求 %d 次，应为 5（每次未命中各查一路）", n)
	}
}

// TestHTTPDNSRefreshSkipsServiceCache 解析结果的后台刷新照官方 skipCache：现取 conf、
// 用取到的列表解析，但不写进服务 IP 列表缓存；之后的未命中照用缓存里的列表。
func TestHTTPDNSRefreshSkipsServiceCache(t *testing.T) {
	ipA, ipB, ipX := dnsIPv4(1), dnsIPv4(2), dnsIPv4(3)
	setDNSConsts(t, []string{ipA}, nil, nil)
	s := newDNSTestServer(t)
	s.conf = []string{"ip:" + ipA + "|ttl:3600", "ip:" + ipB + "|ttl:3600"}
	s.setResolve("a.test", ipX)
	s.setResolve("b.test", ipX)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	d := s.client()
	d.now = func() time.Time { return now }
	count := func(path string) (n int, lastHost string) {
		for _, r := range s.requests() {
			if r.path == path {
				n++
				lastHost = r.host
			}
		}
		return n, lastHost
	}

	_ = d.query(context.Background(), "a.test")
	now = now.Add(225 * time.Second)
	_ = d.query(context.Background(), "a.test")
	d.wg.Wait()
	if n, _ := count(httpDNSConfPath); n != 2 {
		t.Fatalf("后台刷新应现取 conf：conf 请求 %d 次，应为 2", n)
	}
	if _, host := count(httpDNSResolvePath); host != ipB {
		t.Fatalf("后台刷新应用现取的列表解析，实际用 %s", host)
	}

	_ = d.query(context.Background(), "b.test")
	if n, _ := count(httpDNSConfPath); n != 2 {
		t.Fatalf("未命中应读服务 IP 列表缓存，不再取 conf：conf 请求 %d 次", n)
	}
	if _, host := count(httpDNSResolvePath); host != ipA {
		t.Fatalf("后台刷新取到的列表不应写进缓存：未命中用了 %s，应为 %s", host, ipA)
	}
}

// TestHTTPDNSEmptyAnswerCached 空结果和正常结果一样缓存 300 秒、75% 后台刷新；刷新查到空
// 结果时覆盖旧值（同官方 rememberAnswer）。
func TestHTTPDNSEmptyAnswerCached(t *testing.T) {
	ipA, ipX := dnsIPv4(1), dnsIPv4(2)
	setDNSConsts(t, []string{ipA}, nil, nil)
	s := newDNSTestServer(t)
	s.conf = []string{"ip:" + ipA + "|ttl:3600"}
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	d := s.client()
	d.now = func() time.Time { return now }

	if got := d.query(context.Background(), "empty.test"); len(got) != 0 {
		t.Fatalf("query = %v，应为空", got)
	}
	n := len(s.requests())
	now = now.Add(200 * time.Second)
	if got := d.query(context.Background(), "empty.test"); len(got) != 0 || len(s.requests()) != n {
		t.Fatalf("200 秒内应命中空结果缓存：got=%v 请求 %d → %d", got, n, len(s.requests()))
	}

	s.setResolve("www.workbuddy.cn", ipX)
	if got := d.query(context.Background(), "www.workbuddy.cn"); len(got) != 1 {
		t.Fatalf("query = %v", got)
	}
	s.setResolve("www.workbuddy.cn")
	now = now.Add(225 * time.Second)
	if got := d.query(context.Background(), "www.workbuddy.cn"); len(got) != 1 || got[0] != ipX {
		t.Fatalf("刷新期间应照用旧值: %v", got)
	}
	d.wg.Wait()
	now = now.Add(time.Second)
	if got := d.query(context.Background(), "www.workbuddy.cn"); len(got) != 0 {
		t.Fatalf("刷新查到空结果应覆盖旧值: %v", got)
	}

	// 过期（300 秒）后同步重查。
	s.setResolve("www.workbuddy.cn", ipX)
	now = now.Add(300 * time.Second)
	if got := d.query(context.Background(), "www.workbuddy.cn"); len(got) != 1 || got[0] != ipX {
		t.Fatalf("过期后应同步重查: %v", got)
	}
}

// TestHTTPDNSServiceExpiry 服务 IP 列表过期后同步重取 conf，失败时用旧列表；75% 后台
// 刷新失败不退避，下一次取用时再刷。
func TestHTTPDNSServiceExpiry(t *testing.T) {
	ipA, ipB, ipX := dnsIPv4(1), dnsIPv4(2), dnsIPv4(3)
	setDNSConsts(t, []string{ipA}, []string{ipB}, nil)
	s := newDNSTestServer(t)
	s.conf = []string{"ip:" + ipA + "|ttl:60"}
	for _, h := range []string{"a.test", "b.test", "c.test", "d.test"} {
		s.setResolve(h, ipX)
	}
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	d := s.client()
	d.now = func() time.Time { return now }
	confCount := func() int {
		n := 0
		for _, r := range s.requests() {
			if r.path == httpDNSConfPath {
				n++
			}
		}
		return n
	}
	lastResolveHost := func() string {
		reqs := s.requests()
		for i := len(reqs) - 1; i >= 0; i-- {
			if reqs[i].path == httpDNSResolvePath {
				return reqs[i].host
			}
		}
		return ""
	}

	_ = d.query(context.Background(), "a.test")
	s.mu.Lock()
	s.confStatus = http.StatusInternalServerError
	s.mu.Unlock()
	now = now.Add(45 * time.Second)
	_ = d.query(context.Background(), "b.test")
	d.wg.Wait()
	if n := confCount(); n != 2 {
		t.Fatalf("45 秒 conf 请求数 = %d，应为 2（后台刷新一次）", n)
	}
	now = now.Add(time.Second)
	_ = d.query(context.Background(), "c.test")
	d.wg.Wait()
	if n := confCount(); n != 3 {
		t.Fatalf("46 秒 conf 请求数 = %d，应为 3（刷新失败不退避）", n)
	}

	now = now.Add(20 * time.Second) // 66 秒，过期
	if got := d.query(context.Background(), "d.test"); len(got) != 1 || got[0] != ipX {
		t.Fatalf("过期后 query = %v", got)
	}
	if n := confCount(); n != 4 {
		t.Fatalf("过期后 conf 请求数 = %d，应为 4（同步重取）", n)
	}
	if h := lastResolveHost(); h != ipA {
		t.Fatalf("重取失败应用旧列表 %s，实际解析发往 %s", ipA, h)
	}
}

// TestIdleConnGeneration 连接被下一个请求取走后，上一个请求的结束不关掉闲置超时；
// 请求中超时调用这次请求登记的 onTimeout；自己的请求结束后连接闲着不再超时。
func TestIdleConnGeneration(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ic := newIdleConn(a, 50*time.Millisecond)
	var stale, fired atomic.Int32
	g1 := ic.activate(func() { stale.Add(1) })
	g2 := ic.activate(func() { fired.Add(1) })
	ic.deactivate(g1)
	start := time.Now()
	if _, err := ic.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("旧序号的结束不应关掉闲置超时: err=%v", err)
	}
	if stale.Load() != 0 || fired.Load() != 1 {
		t.Fatalf("超时回调：旧请求 %d 次、当前请求 %d 次，应为 0 / 1", stale.Load(), fired.Load())
	}
	ic.deactivate(g2)
	go func() {
		time.Sleep(150 * time.Millisecond)
		_, _ = b.Write([]byte("x"))
	}()
	if _, err := ic.Read(make([]byte, 1)); err != nil {
		t.Fatalf("请求结束后闲着不应超时: %v", err)
	}
	go func() { _, _ = b.Read(make([]byte, 1)) }()
	if _, err := ic.Write([]byte("y")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if fired.Load() != 1 {
		t.Fatalf("请求结束后不应再调超时回调: %d", fired.Load())
	}
}
