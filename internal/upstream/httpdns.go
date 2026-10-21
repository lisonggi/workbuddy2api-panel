// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// httpdns.go 腾讯云 HTTPDNS 客户端：给国内版可信入口解析出可直连的 IP。
//
// 依据：官方桌面端 5.8.3 内置 CLI 的 HTTPDNS 模块（10-20 读安装包确认）。官方默认走
// 腾讯云 HTTPDNS（token 恒有值，不走 DoH）：
//   - 先对 httpDNSBootstrapIPs 逐个取服务 IP 列表（conf），任一个解析出非空列表就用它；
//   - 再对服务 IP 逐个解析目标域名，任一个返回非空就用它；
//   - 请求都按 IP 直连、Host 头写 IP 本身、不校验证书（官方行为）。
//
// 缓存两份：服务 IP 列表按 conf 的 ttl（缺省 12 小时，夹在 60 秒到 12 小时）缓存，
// 解析结果按域名缓存 300 秒（夹在 60–600 秒，空结果也一样缓存）。两者都过 75% 后在后台
// 刷新一次（同时只刷一路），刷新期间照用旧值；过期后同步重查。服务 IP 列表刷新失败保留
// 旧列表，下一次取用时再刷；解析结果刷新失败（结果为空）照官方用空结果覆盖。解析结果的
// 后台刷新照官方（skipCache）不读也不写服务 IP 列表缓存：现取 conf，取不到用旧列表或内置列表。
//
// 超时照官方（10-20 读安装包 netdrive-env-route.js 确认）：
//   - 单个请求的 300 毫秒是 Node https.request 的 timeout，即套接字闲置超时：连接、握手、
//     等响应期间连续 300 毫秒没有收发才算超时，不是整个请求的总时长（idleConn）。
//   - 地址层只等 600 毫秒（withTimeout(lookupHttp, 600, [])），到点按没有结果处理，但查询
//     本身不取消，查完照样写进缓存，下一次就能命中。10-20 生产实测：美国机房到 HTTPDNS
//     单次约 0.65 秒，按总时长算每次都超时，按闲置算能查到。
//   - 缓存未命中时每次各查一路，不合并同一域名的并发查询（同官方 lookupHttpDns）。
package upstream

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// httpDNSSocketTimeout 单个 HTTPDNS 请求的闲置超时（见文件头）。
	httpDNSSocketTimeout = 300 * time.Millisecond
	// httpDNSLookupBudget 调用方最多等这么久，到点按「没有结果」返回；查询继续在后台跑完。
	httpDNSLookupBudget = 600 * time.Millisecond
	// httpDNSKeepAliveIdle 连接池里的空闲连接留多久（Node 22 https.globalAgent 的
	// keepAlive timeout 5 秒）。
	httpDNSKeepAliveIdle = 5 * time.Second

	// httpDNSServiceTTLDefault conf 没给 ttl 时的服务 IP 列表缓存时长。
	httpDNSServiceTTLDefault = 12 * time.Hour
	// httpDNSServiceTTLMin / httpDNSServiceTTLMax conf 的 ttl 夹在这个范围里。
	httpDNSServiceTTLMin = 60 * time.Second
	httpDNSServiceTTLMax = 12 * time.Hour
	// httpDNSResolveTTL 域名解析结果的缓存时长（官方写死 300 秒，已在 60–600 秒范围内）。
	httpDNSResolveTTL = 300 * time.Second
)

// httpDNSClient 每个 Client 一份的 HTTPDNS 客户端（服务 IP 列表与解析结果缓存都在这里）。
// 并发安全；transport 独立于对话连接池，不复用对话的连接。
type httpDNSClient struct {
	mu sync.Mutex
	// 服务 IP 列表缓存（serviceIPs 为空表示还没取到过）。
	serviceIPs        []string
	serviceExpires    time.Time // 列表过期时刻，过期后同步重取
	serviceRefreshAt  time.Time // 到这个时刻起取用时在后台刷新
	serviceRefreshing bool
	// resolves 按域名缓存解析结果。
	resolves map[string]*httpDNSResolve

	transport *http.Transport
	// dial 拨号函数；测试替换成连 httptest 服务器的地址，生产用 newDialer。
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// now 可替换时钟（测试推进缓存时间用）。
	now func() time.Time
	// wg 后台刷新计数，测试等刷新结束用。
	wg sync.WaitGroup
}

// httpDNSResolve 一个域名的解析缓存。
type httpDNSResolve struct {
	ips        []string
	expires    time.Time // 过期时刻，过期后同步重查
	refreshAt  time.Time // 到这个时刻起取用时在后台刷新
	refreshing bool
}

// httpDNSLookup 一次缓存未命中的查询；done 关闭后 ips 可读。
type httpDNSLookup struct {
	done chan struct{}
	ips  []string
}

// newHTTPDNSClient 构造生产用 HTTPDNS 客户端：独立 Transport，按 IP 直连、证书不校验
// （官方行为：按 IP 连，证书不校验）。
func newHTTPDNSClient() *httpDNSClient {
	d := &httpDNSClient{
		resolves: make(map[string]*httpDNSResolve),
		now:      time.Now,
		dial:     newDialer().DialContext,
	}
	d.transport = &http.Transport{
		// 独立 Transport：对话的连接池按域名建连，这里按 IP 建连，不能混用。
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Node 的 timeout 在连上之前就生效：建连也受 300 毫秒约束。
			dctx, cancel := context.WithTimeout(ctx, httpDNSSocketTimeout)
			defer cancel()
			conn, err := d.dial(dctx, network, addr)
			if err != nil {
				return nil, err
			}
			return newIdleConn(conn, httpDNSSocketTimeout), nil
		},
		// 官方按 IP 直连 HTTPDNS，不校验证书。DisableCompression 让请求只带 Host
		// 与（空串）User-Agent，不带 Go 自动加的 Accept-Encoding。
		TLSClientConfig:    &tls.Config{InsecureSkipVerify: true},
		DisableCompression: true,
		IdleConnTimeout:    httpDNSKeepAliveIdle,
	}
	return d
}

// clock 当前时间（测试替换 now 用）。
func (d *httpDNSClient) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// query 解析域名：返回去重保序的 IP 列表；没有结果或超过 600 毫秒返回 nil（不报错）。
// 超时不取消查询，查完写进缓存（见文件头）。
func (d *httpDNSClient) query(ctx context.Context, host string) []string {
	host = strings.TrimSpace(host)
	if host == "" || httpDNSHostIsLocal(host) {
		return nil
	}
	if ips, ok := d.cachedResolve(host); ok {
		return ips
	}
	l := d.startLookup(host)
	timer := time.NewTimer(httpDNSLookupBudget)
	defer timer.Stop()
	select {
	case <-l.done:
		return append([]string(nil), l.ips...)
	case <-timer.C:
	case <-ctx.Done():
	}
	return nil
}

// httpDNSHostIsLocal 域名是 localhost 或本身就是 IP 时不需要解析。
func httpDNSHostIsLocal(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil
}

// startLookup 缓存未命中时发起一次查询（每次未命中各查一路，同官方）。查询不受调用方的
// 等待时长和取消约束，查完（含空结果）写进缓存，TTL 从发起时算（同官方）。
func (d *httpDNSClient) startLookup(host string) *httpDNSLookup {
	l := &httpDNSLookup{done: make(chan struct{})}
	started := d.clock()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		l.ips = d.resolve(context.Background(), host, false)
		d.mu.Lock()
		d.storeResolveLocked(host, l.ips, started)
		d.mu.Unlock()
		close(l.done)
	}()
	return l
}

// cachedResolve 取没过期的域名解析缓存；没有或已过期返回 false。过了 75% 的 TTL 时在
// 后台刷新一次（同时只刷一个），刷新期间照用旧值；刷新结果（含空结果）覆盖旧值。
func (d *httpDNSClient) cachedResolve(host string) ([]string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e := d.resolves[host]
	now := d.clock()
	if e == nil || !now.Before(e.expires) {
		return nil, false
	}
	if !e.refreshing && !now.Before(e.refreshAt) {
		e.refreshing = true
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			ips := d.resolve(context.Background(), host, true)
			d.mu.Lock()
			e.refreshing = false
			d.storeResolveLocked(host, ips, d.clock())
			d.mu.Unlock()
		}()
	}
	return append([]string(nil), e.ips...), true
}

// storeResolveLocked 写入域名解析缓存（空结果也缓存，同官方 rememberAnswer）。调用方持有 d.mu。
func (d *httpDNSClient) storeResolveLocked(host string, ips []string, at time.Time) {
	if d.resolves == nil {
		d.resolves = make(map[string]*httpDNSResolve)
	}
	e := d.resolves[host]
	if e == nil {
		e = &httpDNSResolve{}
		d.resolves[host] = e
	}
	e.ips = append([]string(nil), ips...)
	e.expires = at.Add(httpDNSResolveTTL)
	e.refreshAt = at.Add(httpDNSResolveTTL * 3 / 4)
}

// serviceIPsFor 取服务 IP 列表：缓存没过期就用缓存（过了 75% 在后台刷新，失败保留旧列表、
// 下一次取用时再刷），没有或已过期就同步取 conf（loadServiceIPs）。
func (d *httpDNSClient) serviceIPsFor(ctx context.Context) []string {
	d.mu.Lock()
	now := d.clock()
	if len(d.serviceIPs) > 0 && now.Before(d.serviceExpires) {
		if !d.serviceRefreshing && !now.Before(d.serviceRefreshAt) {
			d.serviceRefreshing = true
			d.wg.Add(1)
			go func() {
				defer d.wg.Done()
				ips, ttl := d.fetchConf(context.Background())
				d.mu.Lock()
				d.serviceRefreshing = false
				if len(ips) > 0 {
					d.rememberServiceLocked(ips, ttl, d.clock())
				}
				d.mu.Unlock()
			}()
		}
		out := append([]string(nil), d.serviceIPs...)
		d.mu.Unlock()
		return out
	}
	d.mu.Unlock()
	return d.loadServiceIPs(ctx, true)
}

// loadServiceIPs 同步取 conf：取到就用新列表，remember 为真时记进缓存；全部失败时有旧列表
// 用旧列表，否则用 httpDNSDefaultServiceIPs。
func (d *httpDNSClient) loadServiceIPs(ctx context.Context, remember bool) []string {
	started := d.clock()
	ips, ttl := d.fetchConf(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(ips) > 0 {
		if remember {
			d.rememberServiceLocked(ips, ttl, started)
		}
		return append([]string(nil), ips...)
	}
	if len(d.serviceIPs) > 0 {
		return append([]string(nil), d.serviceIPs...)
	}
	return append([]string(nil), httpDNSDefaultServiceIPs...)
}

// fetchConf 依次对 httpDNSBootstrapIPs 取 conf，返回第一个非空的服务 IP 列表与 ttl；
// 全部失败返回 nil。
func (d *httpDNSClient) fetchConf(ctx context.Context) ([]string, time.Duration) {
	for _, ip := range httpDNSBootstrapIPs {
		raw, ok := d.get(ctx, ip, httpDNSConfPath, url.Values{"token": {httpDNSToken}})
		if !ok {
			continue
		}
		if ips, ttl := parseHTTPDNSConf(raw); len(ips) > 0 {
			return ips, ttl
		}
	}
	return nil, 0
}

// rememberServiceLocked 记下服务 IP 列表：ttl 缺省 12 小时，夹在 60 秒到 12 小时。调用方持有 d.mu。
func (d *httpDNSClient) rememberServiceLocked(ips []string, ttl time.Duration, at time.Time) {
	if ttl <= 0 {
		ttl = httpDNSServiceTTLDefault
	}
	if ttl < httpDNSServiceTTLMin {
		ttl = httpDNSServiceTTLMin
	}
	if ttl > httpDNSServiceTTLMax {
		ttl = httpDNSServiceTTLMax
	}
	d.serviceIPs = append([]string(nil), ips...)
	d.serviceExpires = at.Add(ttl)
	d.serviceRefreshAt = at.Add(ttl * 3 / 4)
}

// resolve 对服务 IP 列表逐个解析 host；全部为空时强制重新取一次 conf，新列表和原来的
// 完全相同就返回空，不同就用新列表再解析一次。skipCache 是解析结果的后台刷新（官方
// queryHttpDns 的 skipCache）：服务 IP 列表不读缓存、现取 conf，取到的也不记进缓存。
func (d *httpDNSClient) resolve(ctx context.Context, host string, skipCache bool) []string {
	var service []string
	if skipCache {
		service = d.loadServiceIPs(ctx, false)
	} else {
		service = d.serviceIPsFor(ctx)
	}
	if ips := d.resolveOn(ctx, service, host); len(ips) > 0 {
		return ips
	}
	// 全部为空：强制重新取一次 conf（不读缓存）。
	fresh := d.loadServiceIPs(ctx, !skipCache)
	if httpDNSSameStrings(service, fresh) {
		return nil
	}
	return d.resolveOn(ctx, fresh, host)
}

// resolveOn 依次用服务 IP 解析 host，返回第一个非空结果。
func (d *httpDNSClient) resolveOn(ctx context.Context, service []string, host string) []string {
	for _, ip := range service {
		if ctx.Err() != nil {
			return nil
		}
		if ips := d.resolveAt(ctx, ip, host); len(ips) > 0 {
			return ips
		}
	}
	return nil
}

// resolveAt 用一个服务 IP 解析域名，解析为空或失败返回 nil。
func (d *httpDNSClient) resolveAt(ctx context.Context, ip, host string) []string {
	raw, ok := d.get(ctx, ip, httpDNSResolvePath, url.Values{"dn": {host}, "token": {httpDNSToken}})
	if !ok {
		return nil
	}
	return parseHTTPDNSResolve(raw)
}

// get 按 IP 直连发一次 GET：Host 头是这个 IP，不发 User-Agent；状态 ≥400 或闲置超时算失败。
// 请求期间打开连接的闲置超时，读完响应后关掉（连接回池等复用）。
// 复用的连接闲置超时时顺带取消这次请求：Go 的 Transport 对复用连接上失败的 GET 会换新
// 连接自动重发，取消后就不重发（官方超时即失败，不重发）。
func (d *httpDNSClient) get(ctx context.Context, ip, path string, q url.Values) ([]byte, bool) {
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var conn *idleConn
	var gen uint64
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		c := info.Conn
		if tc, ok := c.(*tls.Conn); ok {
			c = tc.NetConn()
		}
		if ic, ok := c.(*idleConn); ok {
			conn, gen = ic, ic.activate(cancel)
		}
	}}
	defer func() {
		if conn != nil {
			conn.deactivate(gen)
		}
	}()
	u := httpDNSURL(ip, path, q)
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(reqCtx, trace), http.MethodGet, u, nil)
	if err != nil {
		return nil, false
	}
	req.Host = ip
	// 空串让 Go 不发 User-Agent（官方 HTTPDNS 请求只带 Host）。
	req.Header.Set("User-Agent", "")
	resp, err := d.transport.RoundTrip(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, false
	}
	return raw, true
}

// idleConn 按闲置超时管一条连接，模拟 Node 套接字的 setTimeout：请求进行中每次收发前
// 把读写期限顺延 timeout；请求结束后清掉期限，留在连接池里的空闲连接不受它约束。新建的
// 连接一开始就处于请求中（TLS 握手也算）。请求中超时时调用这次请求登记的 onTimeout。
type idleConn struct {
	net.Conn
	timeout   time.Duration
	mu        sync.Mutex
	active    bool
	gen       uint64
	onTimeout func()
}

func newIdleConn(c net.Conn, timeout time.Duration) *idleConn {
	ic := &idleConn{Conn: c, timeout: timeout, active: true, gen: 1}
	_ = c.SetDeadline(time.Now().Add(timeout))
	return ic
}

func (c *idleConn) touch() {
	c.mu.Lock()
	if c.active {
		_ = c.SetDeadline(time.Now().Add(c.timeout))
	}
	c.mu.Unlock()
}

func (c *idleConn) Read(p []byte) (int, error) {
	c.touch()
	n, err := c.Conn.Read(p)
	c.checkTimeout(err)
	return n, err
}

func (c *idleConn) Write(p []byte) (int, error) {
	c.touch()
	n, err := c.Conn.Write(p)
	c.checkTimeout(err)
	return n, err
}

// checkTimeout 请求中读写超时：调用这次请求登记的 onTimeout（只调一次）。
func (c *idleConn) checkTimeout(err error) {
	if err == nil || !errors.Is(err, os.ErrDeadlineExceeded) {
		return
	}
	c.mu.Lock()
	fn := c.onTimeout
	if !c.active {
		fn = nil
	}
	c.onTimeout = nil
	c.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// activate 一个请求开始用这条连接：打开闲置超时，登记超时时要调的 onTimeout，返回这次的序号。
func (c *idleConn) activate(onTimeout func()) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.active = true
	c.onTimeout = onTimeout
	_ = c.SetDeadline(time.Now().Add(c.timeout))
	return c.gen
}

// deactivate 请求结束：序号还是自己的才关掉闲置超时（连接可能已被下一个请求取走）。
func (c *idleConn) deactivate(gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		return
	}
	c.active = false
	c.onTimeout = nil
	_ = c.SetDeadline(time.Time{})
}

// httpDNSURL 按 IP 拼请求地址（IPv6 加方括号），查询串用 url.Values 编码。
func httpDNSURL(ip, path string, q url.Values) string {
	host := ip
	if strings.Contains(ip, ":") {
		host = "[" + ip + "]"
	}
	return "https://" + host + path + "?" + q.Encode()
}

// parseHTTPDNSConf 解析 conf 响应体：去掉首尾空白后按 | 分段，每段按第一个 :
// 拆成键和值（键去空白转小写）。ip 的值按 ; , 空白拆开，只留合法 IP 并去重保序；
// ttl 的值全是数字时当秒数，否则按没有处理。
func parseHTTPDNSConf(raw []byte) (ips []string, ttl time.Duration) {
	seen := make(map[string]bool)
	for _, part := range strings.Split(strings.TrimSpace(string(raw)), "|") {
		key, val, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue // ":" 在第 0 位的段
		}
		val = strings.TrimSpace(val)
		switch key {
		case "ip":
			for _, f := range strings.FieldsFunc(val, func(r rune) bool {
				return r == ';' || r == ',' || unicode.IsSpace(r)
			}) {
				if net.ParseIP(f) != nil && !seen[f] {
					seen[f] = true
					ips = append(ips, f)
				}
			}
		case "ttl":
			if httpDNSAllDigits(val) {
				if sec, err := strconv.Atoi(val); err == nil {
					ttl = time.Duration(sec) * time.Second
				}
			}
		}
	}
	return ips, ttl
}

// parseHTTPDNSResolve 解析解析响应体：去掉首尾空白；空串或 0 → 空；取第一个 |
// 之前的部分，按 ; 或空白拆成若干段。每段整段是 IP 就收下；否则按 , 拆，第一截是
// IP 且第二截全是数字就收第一截；再否则把每截里是 IP 的都收下。最后去重保序。
func parseHTTPDNSResolve(raw []byte) []string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "0" {
		return nil
	}
	if i := strings.IndexByte(s, '|'); i >= 0 {
		s = s[:i]
	}
	var out []string
	seen := make(map[string]bool)
	add := func(ip string) {
		if ip != "" && net.ParseIP(ip) != nil && !seen[ip] {
			seen[ip] = true
			out = append(out, ip)
		}
	}
	for _, seg := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ';' || unicode.IsSpace(r)
	}) {
		seg = strings.TrimSpace(seg)
		if seg == "" || seg == "0" {
			continue
		}
		if net.ParseIP(seg) != nil {
			add(seg)
			continue
		}
		parts := strings.Split(seg, ",")
		first := strings.TrimSpace(parts[0])
		if len(parts) >= 2 && net.ParseIP(first) != nil && httpDNSAllDigits(strings.TrimSpace(parts[1])) {
			add(first)
			continue
		}
		for _, p := range parts {
			if ip := strings.TrimSpace(p); net.ParseIP(ip) != nil {
				add(ip)
			}
		}
	}
	return out
}

// httpDNSAllDigits 判断字符串是否全为数字（空串不算）。
func httpDNSAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// httpDNSSameStrings 按顺序比较两个字符串列表。
func httpDNSSameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
