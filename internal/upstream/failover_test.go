// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

// failover_test.go 国内版对话入口切换（NetworkFailover）的测试：全部走假 RoundTripper，
// 不连外网；熔断器用可替换时钟推进，不依赖真实等待。

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

const (
	foMainHost   = "www.workbuddy.cn"
	foAltHost    = "copilot.tencent.com"
	foBackupHost = "wb.tencentbuddy.com"
)

// failoverClient 国内版主入口 + 假上游（chat 域由 onHost 按 host 分发）。
func failoverClient(f *upFake) *Client {
	return &Client{HTTP: f.client(), ChatBaseCN: cnProductEndpoint, GlobalEnabled: true}
}

// foChat 发一条普通国内版对话（请求体统一，便于断言两次请求体一致）。
func foChat(c *Client, ctx context.Context) (io.ReadCloser, int, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return c.ChatStreamContext(ctx, upAuth(), []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`), "", ChatMeta{})
}

// foHosts 按顺序返回假上游收到的请求 host。
func foHosts(f *upFake) []string {
	out := make([]string, 0, len(f.seen()))
	for _, r := range f.seen() {
		u, err := url.Parse(r.url)
		if err != nil {
			out = append(out, r.url)
			continue
		}
		out = append(out, u.Host)
	}
	return out
}

// foReadAll 读完并关闭流。
func foReadAll(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	if rc == nil {
		t.Fatal("成功分支应返回响应流")
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("读取流: %v", err)
	}
	return string(raw)
}

// foSetClock 把客户端熔断器的时钟换成可推进的假时钟。
func foSetClock(c *Client, now *time.Time) {
	c.failover.now = func() time.Time { return *now }
}

func TestChatFailoverMain502ToBackup(t *testing.T) {
	f := newUpFake(t)
	f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502,"msg":"bad gateway"}`)
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "data: from-backup\n\n")
	c := failoverClient(f)

	rc, status, respBody, err := foChat(c, nil)
	if err != nil || status != 200 || respBody != nil {
		t.Fatalf("status=%d respBody=%q err=%v，应为 200/nil/nil", status, respBody, err)
	}
	if got := foReadAll(t, rc); got != "data: from-backup\n\n" {
		t.Fatalf("调用方读到的流 = %q", got)
	}
	reqs := f.seen()
	if len(reqs) != 2 {
		t.Fatalf("请求数 = %d，应为 2（主入口、备用入口）", len(reqs))
	}
	if hosts := foHosts(f); hosts[0] != foMainHost || hosts[1] != foBackupHost {
		t.Fatalf("请求 host 顺序 = %v", hosts)
	}
	for i := 1; i < len(reqs); i++ {
		if reqs[i].method != reqs[0].method || reqs[i].path != reqs[0].path {
			t.Errorf("第二次请求 = %s %s，应为 %s %s", reqs[i].method, reqs[i].path, reqs[0].method, reqs[0].path)
		}
		if string(reqs[i].body) != string(reqs[0].body) {
			t.Errorf("两次请求体不同:\n%s\n%s", reqs[0].body, reqs[i].body)
		}
		if got, want := reqs[i].header.Get("Authorization"), reqs[0].header.Get("Authorization"); got != want {
			t.Errorf("Authorization = %q，应为 %q", got, want)
		}
		if got, want := reqs[i].header.Get("X-Conversation-ID"), reqs[0].header.Get("X-Conversation-ID"); got != want {
			t.Errorf("X-Conversation-ID = %q，应为 %q", got, want)
		}
	}
	if got := reqs[0].header.Get("Authorization"); got != "Bearer at" {
		t.Errorf("Authorization = %q", got)
	}
}

// TestChatFailoverRetryStatus 状态码口径：408/502/503/504 切（retry_policy 没拦），
// 500/429/400 不切；retry_policy.retry=false 不切，true 切。
func TestChatFailoverRetryStatus(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantSwitch bool
	}{
		{"408 切", 408, `{"code":408,"msg":"request timeout"}`, true},
		{"503 切", 503, `{"code":503,"msg":"unavailable"}`, true},
		{"504 切", 504, `{"code":504,"msg":"gateway timeout"}`, true},
		{"500 不切", 500, `{"code":500,"msg":"boom"}`, false},
		{"429 不切", 429, `{"code":429,"msg":"rate limited"}`, false},
		{"400 不切", 400, `{"code":400,"msg":"bad request"}`, false},
		{"502 error.retry_policy=false 不切", 502, `{"error":{"retry_policy":{"retry":false}}}`, false},
		{"502 data.retry_policy=false 不切", 502, `{"data":{"retry_policy":{"retry":false}}}`, false},
		{"502 retry_policy=true 切", 502, `{"error":{"retry_policy":{"retry":true}}}`, true},
		{"502 非 JSON 切", 502, `<html>bad gateway</html>`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, tc.status, tc.body)
			f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "data: ok\n\n")
			c := failoverClient(f)

			rc, status, respBody, err := foChat(c, nil)
			hosts := foHosts(f)
			if tc.wantSwitch {
				if err != nil || status != 200 {
					t.Fatalf("status=%d err=%v，应切到备用入口并成功", status, err)
				}
				if len(hosts) != 2 || hosts[0] != foMainHost || hosts[1] != foBackupHost {
					t.Fatalf("请求 host = %v，应为主入口后备用入口", hosts)
				}
				if got := foReadAll(t, rc); got != "data: ok\n\n" {
					t.Fatalf("流 = %q", got)
				}
				return
			}
			if len(hosts) != 1 || hosts[0] != foMainHost {
				t.Fatalf("请求 host = %v，应只有主入口一次", hosts)
			}
			if status != tc.status {
				t.Fatalf("status = %d，应为 %d", status, tc.status)
			}
			var ue *Error
			if !errors.As(err, &ue) || ue.Status != tc.status {
				t.Fatalf("err = %v，应为带原状态码的 *Error", err)
			}
			if len(respBody) == 0 {
				t.Errorf("不切时应原样返回响应体")
			}
		})
	}
}

// TestFailoverRetryDeniedPaths retry_policy 的查找顺序与取值口径。
func TestFailoverRetryDeniedPaths(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"顶层", `{"retry_policy":{"retry":false}}`, true},
		{"data", `{"data":{"retry_policy":{"retry":false}}}`, true},
		{"error", `{"error":{"retry_policy":{"retry":false}}}`, true},
		{"error.data", `{"error":{"data":{"retry_policy":{"retry":false}}}}`, true},
		{"error.error", `{"error":{"error":{"retry_policy":{"retry":false}}}}`, true},
		{"error.error.data", `{"error":{"error":{"data":{"retry_policy":{"retry":false}}}}}`, true},
		{"retry=true", `{"error":{"retry_policy":{"retry":true}}}`, false},
		{"取第一个带键的对象（data 先于 error）", `{"data":{"retry_policy":{"retry":false}},"error":{"retry_policy":{"retry":true}}}`, true},
		{"第一个值不是对象", `{"retry_policy":"no"}`, false},
		{"retry 不是 bool", `{"data":{"retry_policy":{"retry":"false"}}}`, false},
		{"空体", ``, false},
		{"非 JSON", `not json`, false},
		{"data 不是对象", `{"data":"x","error":{"retry_policy":{"retry":false}}}`, true},
		{"error 是 null", `{"error":null}`, false},
		{"没有 retry_policy", `{"code":502,"msg":"bad"}`, false},
	}
	for _, tc := range cases {
		if got := failoverRetryDenied([]byte(tc.body)); got != tc.want {
			t.Errorf("%s: failoverRetryDenied(%s) = %v，应为 %v", tc.name, tc.body, got, tc.want)
		}
	}
}

type foTimeoutErr struct{}

func (foTimeoutErr) Error() string   { return "i/o timeout" }
func (foTimeoutErr) Timeout() bool   { return true }
func (foTimeoutErr) Temporary() bool { return true }

// TestChatFailoverTransportErrors 传输错误口径：DNS/拒连/重置/EOF/建连超时/响应头超时
// 都切；调用方取消不切。
func TestChatFailoverTransportErrors(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		cancelCtx  bool
		wantSwitch bool
	}{
		{"DNS 解析失败", &net.DNSError{Err: "no such host", Name: foMainHost}, false, true},
		{"连接被拒", &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, false, true},
		{"连接重置", &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}, false, true},
		{"EOF", io.EOF, false, true},
		{"调用方取消", context.Canceled, true, false},
		{"非建连阶段超时", foTimeoutErr{}, false, true},
		{"建连超时", &net.OpError{Op: "dial", Net: "tcp", Err: foTimeoutErr{}}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.onFunc(http.MethodPost, chatCompletionsPath, func(*upReq) (*http.Response, error) {
				return nil, tc.err
			})
			f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "data: ok\n\n")
			c := failoverClient(f)
			ctx := context.Background()
			if tc.cancelCtx {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			rc, status, _, err := foChat(c, ctx)
			hosts := foHosts(f)
			if !tc.wantSwitch {
				// 调用方取消时 http.Client 可能连 RoundTripper 都不进，
				// 这里只要求没有出现备用入口的请求。
				if err == nil {
					if rc != nil {
						rc.Close()
					}
					t.Fatal("传输错误应返回错误")
				}
				for _, h := range hosts {
					if h == foBackupHost {
						t.Fatalf("请求 host = %v，不应切到备用入口", hosts)
					}
				}
				if len(hosts) > 1 {
					t.Fatalf("请求 host = %v，应只有首选入口", hosts)
				}
				if status != 0 {
					t.Errorf("status = %d，应为 0", status)
				}
				return
			}
			if err != nil || status != 200 {
				t.Fatalf("status=%d err=%v，应切到备用入口并成功", status, err)
			}
			if len(hosts) != 2 || hosts[0] != foMainHost || hosts[1] != foBackupHost {
				t.Fatalf("请求 host = %v，应为主入口后备用入口", hosts)
			}
			if got := foReadAll(t, rc); got != "data: ok\n\n" {
				t.Fatalf("流 = %q", got)
			}
		})
	}
}

// TestChatFailoverSuccessBody 2xx 有数据不切，且第一个字节没丢。
func TestChatFailoverSuccessBody(t *testing.T) {
	const payload = "data: hello\n\n"
	f := newUpFake(t)
	f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 200, payload)
	c := failoverClient(f)

	rc, status, _, err := foChat(c, nil)
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if got := foReadAll(t, rc); got != payload {
		t.Fatalf("流 = %q，应为 %q（首字节不能丢）", got, payload)
	}
	if hosts := foHosts(f); len(hosts) != 1 || hosts[0] != foMainHost {
		t.Fatalf("请求 host = %v，应为一次主入口", hosts)
	}
}

// TestChatFailoverEmptySuccess 2xx 空响应体切备用入口；调用方读到备用入口的数据。
func TestChatFailoverEmptySuccess(t *testing.T) {
	f := newUpFake(t)
	f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 200, "")
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "data: backup\n\n")
	c := failoverClient(f)

	rc, status, _, err := foChat(c, nil)
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if got := foReadAll(t, rc); got != "data: backup\n\n" {
		t.Fatalf("流 = %q，应为备用入口的数据", got)
	}
	if hosts := foHosts(f); len(hosts) != 2 || hosts[1] != foBackupHost {
		t.Fatalf("请求 host = %v，应为先主入口后备用入口", hosts)
	}
}

// foErrReader 总是返回同一个读错误（模拟 2xx 后读第一个字节就失败）。
type foErrReader struct{ err error }

func (r foErrReader) Read([]byte) (int, error) { return 0, r.err }
func (foErrReader) Close() error               { return nil }

// TestChatFailover2xxReadError 2xx 读第一个字节出错（非 EOF）不切，错误留给调用方读。
func TestChatFailover2xxReadError(t *testing.T) {
	f := newUpFake(t)
	f.onFunc(http.MethodPost, chatCompletionsPath, func(*upReq) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       foErrReader{err: errors.New("stream broke")},
		}, nil
	})
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "data: backup\n\n")
	c := failoverClient(f)

	rc, status, _, err := foChat(c, nil)
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v，应把错误留给调用方", status, err)
	}
	if hosts := foHosts(f); len(hosts) != 1 || hosts[0] != foMainHost {
		t.Fatalf("请求 host = %v，读首字节出错不应切", hosts)
	}
	defer rc.Close()
	if _, rerr := io.ReadAll(rc); rerr == nil || !strings.Contains(rerr.Error(), "stream broke") {
		t.Fatalf("调用方读流应拿到原错误，got %v", rerr)
	}
}

// TestChatFailoverBackupAlsoFails 备用入口也失败时只切一次，返回备用入口的结果。
func TestChatFailoverBackupAlsoFails(t *testing.T) {
	t.Run("备用入口 503", func(t *testing.T) {
		f := newUpFake(t)
		f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
		f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 503, `{"code":503,"msg":"down"}`)
		c := failoverClient(f)

		_, status, _, err := foChat(c, nil)
		if status != 503 {
			t.Fatalf("status = %d，应为备用入口的 503", status)
		}
		var ue *Error
		if !errors.As(err, &ue) || ue.Status != 503 {
			t.Fatalf("err = %v，应为备用入口的 *Error", err)
		}
		if hosts := foHosts(f); len(hosts) != 2 {
			t.Fatalf("请求 host = %v，最多切一次", hosts)
		}
	})

	t.Run("备用入口 500 不记基础设施失败", func(t *testing.T) {
		f := newUpFake(t)
		f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
		f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 500, `{"code":500}`)
		c := failoverClient(f)

		_, status, _, err := foChat(c, nil)
		if status != 500 {
			t.Fatalf("status = %d", status)
		}
		var ue *Error
		if !errors.As(err, &ue) || ue.Status != 500 {
			t.Fatalf("err = %v", err)
		}
		if c.failover.isOpen(failoverBackupOrigin) {
			t.Fatal("500 不应触发备用入口熔断")
		}
	})

	t.Run("备用入口基础设施传输错误记熔断", func(t *testing.T) {
		now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
		f := newUpFake(t)
		f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
		f.onFunc(http.MethodPost, chatCompletionsPath, func(*upReq) (*http.Response, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
		})
		c := failoverClient(f)
		foSetClock(c, &now)

		_, status, _, err := foChat(c, nil)
		if err == nil || status != 0 || !errors.Is(err, syscall.ECONNREFUSED) {
			t.Fatalf("status=%d err=%v，应为备用入口的拒连错误", status, err)
		}
		// 这次失败已记进备用入口熔断器：再记 2 次就打开。
		c.failover.noteInfraFailure(failoverBackupOrigin)
		c.failover.noteInfraFailure(failoverBackupOrigin)
		if !c.failover.isOpen(failoverBackupOrigin) {
			t.Fatal("备用入口的传输错误应记进熔断器")
		}
	})

	t.Run("备用入口传输错误", func(t *testing.T) {
		f := newUpFake(t)
		f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
		f.onFunc(http.MethodPost, chatCompletionsPath, func(r *upReq) (*http.Response, error) {
			return nil, io.EOF
		})
		c := failoverClient(f)

		_, status, _, err := foChat(c, nil)
		if err == nil || status != 0 {
			t.Fatalf("status=%d err=%v，应为传输错误", status, err)
		}
		if errors.Is(err, io.EOF) == false && !strings.Contains(err.Error(), "EOF") {
			t.Fatalf("err = %v，应为备用入口的传输错误", err)
		}
		if hosts := foHosts(f); len(hosts) != 2 {
			t.Fatalf("请求 host = %v，最多切一次", hosts)
		}
	})
}

// TestChatFailoverUntrustedBase ChatBase 配成备用入口或非可信域时整套逻辑不生效。
func TestChatFailoverUntrustedBase(t *testing.T) {
	cases := []struct {
		name     string
		base     string
		wantHost string
	}{
		{"配成备用入口", failoverBackupOrigin, foBackupHost},
		{"配成非可信中转", upBase, "up.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			// 只注册首选入口：如果误切到备用入口，假上游会按未预设请求记失败。
			f.onHost(tc.wantHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
			c := &Client{HTTP: f.client(), ChatBaseCN: tc.base, GlobalEnabled: true}

			_, status, _, err := foChat(c, nil)
			if status != 502 {
				t.Fatalf("status = %d，应原样返回 502", status)
			}
			var ue *Error
			if !errors.As(err, &ue) {
				t.Fatalf("err = %v", err)
			}
			if hosts := foHosts(f); len(hosts) != 1 || hosts[0] != tc.wantHost {
				t.Fatalf("请求 host = %v，非可信 base 不应切", hosts)
			}
		})
	}
}

// TestChatFailoverCopilotFront 备选入口（前门之一）出错切备用入口。
func TestChatFailoverCopilotFront(t *testing.T) {
	f := newUpFake(t)
	f.onHost(foAltHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "data: backup\n\n")
	c := &Client{HTTP: f.client(), ChatBaseCN: failoverAltOrigin, GlobalEnabled: true}

	rc, status, _, err := foChat(c, nil)
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if got := foReadAll(t, rc); got != "data: backup\n\n" {
		t.Fatalf("流 = %q", got)
	}
	if hosts := foHosts(f); len(hosts) != 2 || hosts[0] != foAltHost || hosts[1] != foBackupHost {
		t.Fatalf("请求 host = %v", hosts)
	}
}

// TestChatFailoverGlobalAccount 国际版账号不切（即便 global base 恰好是可信主入口）。
func TestChatFailoverGlobalAccount(t *testing.T) {
	f := newUpFake(t)
	f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "data: backup\n\n")
	c := &Client{HTTP: f.client(), ChatBaseCN: cnProductEndpoint, ChatBaseGlobal: cnProductEndpoint, GlobalEnabled: true}
	ga := &auth.Auth{AccessToken: "at", UID: "u-global", Domain: "www.workbuddy.ai", ExpiresAt: 9999999999}

	_, status, _, err := c.ChatStreamContext(context.Background(), ga, []byte(`{"model":"m","messages":[]}`), "", ChatMeta{})
	if status != 502 {
		t.Fatalf("status = %d，国际版应原样返回 502", status)
	}
	var ue *Error
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v", err)
	}
	if hosts := foHosts(f); len(hosts) != 1 || hosts[0] != foMainHost {
		t.Fatalf("请求 host = %v，国际版不应切", hosts)
	}
}

// TestChatFailoverBreakerOpens 窗口内 3 次 502 后：第 4 个请求直接发备用入口，
// 主入口收不到请求；备用入口失败也不再切。
func TestChatFailoverBreakerOpens(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	f := newUpFake(t)
	f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "data: ok\n\n")
	c := failoverClient(f)
	foSetClock(c, &now)

	for i := 0; i < 3; i++ {
		rc, status, _, err := foChat(c, nil)
		if err != nil || status != 200 {
			t.Fatalf("第 %d 次: status=%d err=%v", i+1, status, err)
		}
		if got := foReadAll(t, rc); got != "data: ok\n\n" {
			t.Fatalf("第 %d 次流 = %q", i+1, got)
		}
	}
	if !c.failover.isOpen(cnProductEndpoint) {
		t.Fatal("3 次 502 后主入口熔断器应打开")
	}

	// 第 4 个请求：主入口熔断中、备用入口关着 → 直接发备用入口；备用入口失败不再切。
	before := len(f.seen())
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
	_, status, _, err := foChat(c, nil)
	if status != 502 {
		t.Fatalf("status = %d，应为备用入口的 502", status)
	}
	var ue *Error
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v", err)
	}
	reqs := f.seen()
	if len(reqs)-before != 1 {
		t.Fatalf("第 4 个请求发了 %d 次，应只发备用入口一次", len(reqs)-before)
	}
	if hosts := foHosts(f); hosts[len(hosts)-1] != foBackupHost {
		t.Fatalf("第 4 个请求 host = %v", hosts[len(hosts)-1])
	}
}

// TestChatFailoverBreakerReopens 打开 15 秒后下一个请求回到主入口。
func TestChatFailoverBreakerReopens(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	f := newUpFake(t)
	f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "data: ok\n\n")
	c := failoverClient(f)
	foSetClock(c, &now)

	for i := 0; i < 3; i++ {
		if _, status, _, err := foChat(c, nil); err != nil || status != 200 {
			t.Fatalf("第 %d 次: status=%d err=%v", i+1, status, err)
		}
	}
	now = now.Add(15 * time.Second)
	f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 200, "data: main\n\n")
	before := len(f.seen())
	rc, status, _, err := foChat(c, nil)
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	reqs := f.seen()
	if len(reqs)-before != 1 {
		t.Fatalf("打开期过后应只发主入口一次，实际 %d 次", len(reqs)-before)
	}
	if hosts := foHosts(f); hosts[len(hosts)-1] != foMainHost {
		t.Fatalf("打开期过后应回主入口，got %v", hosts[len(hosts)-1])
	}
	if got := foReadAll(t, rc); got != "data: main\n\n" {
		t.Fatalf("流 = %q", got)
	}
}

// TestChatFailoverBothOpenSendsFrontFirst 前门和备用入口都打开时仍先发前门，
// 失败后还能切备用入口（打开期内再失败按退避延长）。
func TestChatFailoverBothOpen(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	f := newUpFake(t)
	f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "data: ok\n\n")
	c := failoverClient(f)
	foSetClock(c, &now)

	// 打开前门。
	for i := 0; i < 3; i++ {
		if _, status, _, err := foChat(c, nil); err != nil || status != 200 {
			t.Fatalf("打开前门第 %d 次: status=%d err=%v", i+1, status, err)
		}
	}
	// 前门打开后请求直接发备用入口；让备用入口失败 3 次把它也打开。
	now = now.Add(time.Second)
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
	for i := 0; i < 3; i++ {
		if _, status, _, _ := foChat(c, nil); status != 502 {
			t.Fatalf("打开备用入口第 %d 次: status=%d", i+1, status)
		}
	}
	if !c.failover.isOpen(failoverBackupOrigin) {
		t.Fatal("3 次备用入口 502 后备用入口熔断器应打开")
	}
	// 两个都打开：先发前门，前门再失败 → 打开期延长 → 切备用入口。
	now = now.Add(time.Second)
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "data: backup\n\n")
	before := len(f.seen())
	rc, status, _, err := foChat(c, nil)
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	hosts := foHosts(f)[before:]
	if len(hosts) != 2 || hosts[0] != foMainHost || hosts[1] != foBackupHost {
		t.Fatalf("请求 host = %v，应先前门后备用入口", hosts)
	}
	if got := foReadAll(t, rc); got != "data: backup\n\n" {
		t.Fatalf("流 = %q", got)
	}
}

// TestChatFailoverDirectBackupEmptySuccess 前门打开时直发备用入口，空 2xx 不能再切，
// 按改造前行为把空流交给调用方。
func TestChatFailoverDirectBackupEmpty(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	f := newUpFake(t)
	f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "data: ok\n\n")
	c := failoverClient(f)
	foSetClock(c, &now)
	for i := 0; i < 3; i++ {
		if _, status, _, err := foChat(c, nil); err != nil || status != 200 {
			t.Fatalf("第 %d 次: status=%d err=%v", i+1, status, err)
		}
	}
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "")
	before := len(f.seen())
	rc, status, _, err := foChat(c, nil)
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if got := foReadAll(t, rc); got != "" {
		t.Fatalf("流 = %q，应为空", got)
	}
	if hosts := foHosts(f)[before:]; len(hosts) != 1 || hosts[0] != foBackupHost {
		t.Fatalf("请求 host = %v，应只有备用入口一次", hosts)
	}
}

// TestChatFailoverBreaker4xxIgnores 4xx 不计数：之后的主入口 502 仍会先打主入口。
func TestChatFailover4xxNotCounted(t *testing.T) {
	f := newUpFake(t)
	f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 400, `{"code":400,"msg":"bad request"}`)
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "data: ok\n\n")
	c := failoverClient(f)
	for i := 0; i < 3; i++ {
		if _, status, _, _ := foChat(c, nil); status != 400 {
			t.Fatalf("第 %d 次 status = %d", i+1, status)
		}
	}
	if c.failover.isOpen(cnProductEndpoint) {
		t.Fatal("4xx 不应打开熔断器")
	}
	f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
	before := len(f.seen())
	if _, status, _, err := foChat(c, nil); err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	hosts := foHosts(f)[before:]
	if len(hosts) != 2 || hosts[0] != foMainHost || hosts[1] != foBackupHost {
		t.Fatalf("请求 host = %v，应仍是先主入口再切备用入口", hosts)
	}
}

// TestFailoverBreakerBackoff 打开期内再失败：打开期按 30/60/120/120 秒延长。
func TestFailoverBreakerBackoff(t *testing.T) {
	var now time.Time
	var c Client
	foSetClock(&c, &now)
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	now = base
	entry := cnProductEndpoint

	for i := 0; i < 3; i++ {
		c.failover.noteInfraFailure(entry)
	}
	if !c.failover.isOpen(entry) {
		t.Fatal("3 次失败后应打开")
	}

	// 打开期内再失败：第 n 次延长依次 30、60、120、120 秒。
	check := func(label string, at time.Time, openFor time.Duration) {
		t.Helper()
		now = at
		c.failover.noteInfraFailure(entry)
		now = at.Add(openFor - time.Second)
		if !c.failover.isOpen(entry) {
			t.Errorf("%s: 打开期内应仍打开", label)
		}
		now = at.Add(openFor)
		if c.failover.isOpen(entry) {
			t.Errorf("%s: 到了延长后的打开期应关闭", label)
		}
	}
	check("第一次延长 30 秒", base.Add(1*time.Second), 30*time.Second)
	check("第二次延长 60 秒", base.Add(2*time.Second), 60*time.Second)
	check("第三次延长 120 秒", base.Add(3*time.Second), 120*time.Second)
	check("之后仍是 120 秒", base.Add(4*time.Second), 120*time.Second)
}

// TestFailoverBreakerWindow 窗口外的旧失败不计数。
func TestFailoverBreakerWindow(t *testing.T) {
	var now time.Time
	var c Client
	foSetClock(&c, &now)
	now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	entry := cnProductEndpoint

	for i := 0; i < 3; i++ {
		c.failover.noteInfraFailure(entry)
	}
	now = now.Add(61 * time.Second)
	c.failover.noteInfraFailure(entry)
	c.failover.noteInfraFailure(entry)
	if c.failover.isOpen(entry) {
		t.Fatal("窗口外的旧失败不应计数（2 次新失败不该打开）")
	}
	c.failover.noteInfraFailure(entry)
	if !c.failover.isOpen(entry) {
		t.Fatal("窗口内第 3 次失败应打开")
	}
}

// TestFailoverBreakerSuccess 一次成功全部清零（失败记录、退避次数、打开状态）。
func TestFailoverBreakerSuccess(t *testing.T) {
	var now time.Time
	var c Client
	foSetClock(&c, &now)
	now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	entry := cnProductEndpoint

	for i := 0; i < 3; i++ {
		c.failover.noteInfraFailure(entry)
	}
	if !c.failover.isOpen(entry) {
		t.Fatal("应先打开")
	}
	// 打开期内再失败一次，把退避次数推到 1（下次延长应为 30 秒）。
	c.failover.noteInfraFailure(entry)
	c.failover.noteSuccess(entry)
	if c.failover.isOpen(entry) {
		t.Fatal("成功后应清零")
	}
	c.failover.noteInfraFailure(entry)
	c.failover.noteInfraFailure(entry)
	if c.failover.isOpen(entry) {
		t.Fatal("成功后失败记录应清零（2 次失败不该打开）")
	}
	c.failover.noteInfraFailure(entry)
	if !c.failover.isOpen(entry) {
		t.Fatal("第 3 次应打开")
	}
	// 退避次数也清零了：打开期内再失败是 30 秒，不是 120 秒。
	now = now.Add(time.Second)
	c.failover.noteInfraFailure(entry)
	now = now.Add(29 * time.Second)
	if !c.failover.isOpen(entry) {
		t.Fatal("退避次数应清零，延长后 30 秒内仍打开")
	}
	now = now.Add(time.Second)
	if c.failover.isOpen(entry) {
		t.Fatal("退避次数应清零，延长后 30 秒即到期")
	}
}

// TestFailoverStateHelpers 覆盖入口解析、基础设施判定、传输错误判定等纯函数。
func TestFailoverStateHelpers(t *testing.T) {
	t.Run("failoverOrigin", func(t *testing.T) {
		cases := []struct{ base, want string }{
			{"https://www.workbuddy.cn", "https://www.workbuddy.cn"},
			{"https://www.workbuddy.cn/", "https://www.workbuddy.cn"},
			{"https://www.workbuddy.cn/some/path", "https://www.workbuddy.cn"},
			{"  https://copilot.tencent.com  ", "https://copilot.tencent.com"},
			{"://bad", ""},
			{"", ""},
			{"www.workbuddy.cn", ""},
		}
		for _, tc := range cases {
			if got := failoverOrigin(tc.base); got != tc.want {
				t.Errorf("failoverOrigin(%q) = %q，应为 %q", tc.base, got, tc.want)
			}
		}
	})

	t.Run("failoverSwapOrigin", func(t *testing.T) {
		cases := []struct{ base, origin, want string }{
			{"https://www.workbuddy.cn", failoverBackupOrigin, failoverBackupOrigin},
			{"https://www.workbuddy.cn/prefix", failoverBackupOrigin, "https://wb.tencentbuddy.com/prefix"},
			{"://bad", failoverBackupOrigin, failoverBackupOrigin},
			{"https://www.workbuddy.cn", "://bad", "://bad"},
			{"https://www.workbuddy.cn", "", ""},
		}
		for _, tc := range cases {
			if got := failoverSwapOrigin(tc.base, tc.origin); got != tc.want {
				t.Errorf("failoverSwapOrigin(%q, %q) = %q，应为 %q", tc.base, tc.origin, got, tc.want)
			}
		}
	})

	t.Run("failoverHost", func(t *testing.T) {
		cases := []struct{ base, want string }{
			{"https://www.workbuddy.cn", "www.workbuddy.cn"},
			{"https://www.workbuddy.cn/", "www.workbuddy.cn"},
			{"://bad", "://bad"},
			{"not-a-url", "not-a-url"},
			{"", ""},
		}
		for _, tc := range cases {
			if got := failoverHost(tc.base); got != tc.want {
				t.Errorf("failoverHost(%q) = %q，应为 %q", tc.base, got, tc.want)
			}
		}
	})

	t.Run("failoverIsInfraStatus", func(t *testing.T) {
		for _, status := range []int{502, 503, 504} {
			if !failoverIsInfraStatus(status) {
				t.Errorf("%d 应是基础设施类失败", status)
			}
		}
		for _, status := range []int{400, 408, 429, 500, 501} {
			if failoverIsInfraStatus(status) {
				t.Errorf("%d 不应是基础设施类失败", status)
			}
		}
	})

	t.Run("failoverIsInfraError", func(t *testing.T) {
		cases := []struct {
			name string
			err  error
			want bool
		}{
			{"nil", nil, false},
			{"普通错误", errors.New("boom"), false},
			{"DNS", &net.DNSError{Err: "no such host"}, true},
			{"ECONNRESET", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, true},
			{"ECONNABORTED", syscall.ECONNABORTED, true},
			{"ECONNREFUSED", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, true},
			{"ETIMEDOUT", syscall.ETIMEDOUT, true},
			{"net.Error 超时", foTimeoutErr{}, true},
		}
		for _, tc := range cases {
			if got := failoverIsInfraError(tc.err); got != tc.want {
				t.Errorf("%s: failoverIsInfraError(%v) = %v，应为 %v", tc.name, tc.err, got, tc.want)
			}
		}
	})

	t.Run("failoverRetryIPStatus", func(t *testing.T) {
		for _, status := range []int{408, 502, 503} {
			if !failoverRetryIPStatus(status) {
				t.Errorf("%d 应换 IP", status)
			}
		}
		for _, status := range []int{400, 429, 500, 501, 504, 505} {
			if failoverRetryIPStatus(status) {
				t.Errorf("%d 不应换 IP", status)
			}
		}
	})

	t.Run("failoverRetryStatus", func(t *testing.T) {
		for _, status := range []int{408, 502, 503, 504} {
			if !failoverRetryStatus(status, []byte(`{"code":1}`)) {
				t.Errorf("%d 应切", status)
			}
		}
		for _, status := range []int{400, 429, 500, 501, 505} {
			if failoverRetryStatus(status, nil) {
				t.Errorf("%d 不应切", status)
			}
		}
		if failoverRetryStatus(502, []byte(`{"retry_policy":{"retry":false}}`)) {
			t.Error("retry_policy.retry=false 时 502 不应切")
		}
	})

	t.Run("peekedBody 满足 ReadCloser", func(t *testing.T) {
		var body io.ReadCloser = &peekedBody{Reader: bufio.NewReader(strings.NewReader("x")), Closer: io.NopCloser(strings.NewReader(""))}
		raw, err := io.ReadAll(body)
		body.Close()
		if err != nil || string(raw) != "x" {
			t.Fatalf("raw=%q err=%v", raw, err)
		}
	})
}

// TestChatFailoverConcurrent 并发请求在 -race 下不报竞态。
func TestChatFailoverConcurrent(t *testing.T) {
	f := newUpFake(t)
	f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "data: ok\n\n")
	c := failoverClient(f)

	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rc, status, _, err := foChat(c, nil)
			if err != nil || status != 200 {
				errs <- errors.New("status/err 不是 200/nil")
				return
			}
			defer rc.Close()
			raw, err := io.ReadAll(rc)
			if err != nil || string(raw) != "data: ok\n\n" {
				errs <- errors.New("读取流内容不对")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// foPinLog 记录地址层钉过的 IP。
type foPinLog struct {
	mu  sync.Mutex
	ips []string
}

func (l *foPinLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ips...)
}

// foAddress 给客户端装地址层：query 按 host 给候选，pin 记录 IP 并返回响应。
func foAddress(c *Client, query func(context.Context, string) []string, pin func(ip string) (*http.Response, error)) *foPinLog {
	log := &foPinLog{}
	c.HTTPDNSQuery = query
	c.PinIP = func(ip string) http.RoundTripper {
		return rtFunc(func(*http.Request) (*http.Response, error) {
			log.mu.Lock()
			log.ips = append(log.ips, ip)
			log.mu.Unlock()
			return pin(ip)
		})
	}
	return log
}

// TestChatFailoverBackupIPRotation 备用入口的地址依次试：HTTPDNS 两个 + 备用 IP 一个，
// 全失败返回最后一次的 502。
func TestChatFailoverBackupIPRotation(t *testing.T) {
	ipB1, ipB2, ipF := dnsIPv4(11), dnsIPv4(12), dnsIPv4(13)
	setDNSConsts(t, nil, nil, []string{ipF})
	f := newUpFake(t)
	f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
	c := failoverClient(f)
	log := foAddress(c, func(_ context.Context, host string) []string {
		if host == foBackupHost {
			return []string{ipB1, ipB2}
		}
		return nil
	}, func(string) (*http.Response, error) {
		return jsonResp(502, `{"code":502}`), nil
	})

	_, status, _, err := foChat(c, nil)
	if status != 502 {
		t.Fatalf("status = %d，应为最后的 502", status)
	}
	var ue *Error
	if !errors.As(err, &ue) || ue.Status != 502 {
		t.Fatalf("err = %v，应为 502 的 *Error", err)
	}
	hosts := foHosts(f)
	if len(hosts) != 2 || hosts[0] != foMainHost || hosts[1] != foBackupHost {
		t.Fatalf("未钉 IP 的请求 host = %v，应为主入口、备用入口", hosts)
	}
	want := []string{ipB1, ipB2, ipF}
	if got := log.all(); !sameStrings(got, want) {
		t.Fatalf("钉 IP 顺序 = %v，应为 %v", got, want)
	}
}

// TestChatFailoverBackup504NoIPSwitch 备用入口 504 不换 IP；钉 IP 的 504 也不换。
func TestChatFailoverBackup504NoIPSwitch(t *testing.T) {
	t.Run("备用入口未钉 IP 504", func(t *testing.T) {
		f := newUpFake(t)
		f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
		f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 504, `{"code":504}`)
		c := failoverClient(f)
		log := foAddress(c, func(context.Context, string) []string { return []string{dnsIPv4(11)} },
			func(string) (*http.Response, error) { return jsonResp(200, "data: x\n\n"), nil })

		_, status, _, _ := foChat(c, nil)
		if status != 504 {
			t.Fatalf("status = %d，应为 504", status)
		}
		if got := log.all(); len(got) != 0 {
			t.Fatalf("钉 IP = %v，504 不应换 IP", got)
		}
	})

	t.Run("钉 IP 后 504 不再换 IP", func(t *testing.T) {
		ip1, ip2 := dnsIPv4(11), dnsIPv4(12)
		f := newUpFake(t)
		f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
		f.onFunc(http.MethodPost, chatCompletionsPath, func(*upReq) (*http.Response, error) {
			return nil, io.EOF
		})
		c := failoverClient(f)
		log := foAddress(c, func(_ context.Context, host string) []string {
			if host == foBackupHost {
				return []string{ip1, ip2}
			}
			return nil
		}, func(string) (*http.Response, error) {
			return jsonResp(504, `{"code":504}`), nil
		})

		_, status, _, _ := foChat(c, nil)
		if status != 504 {
			t.Fatalf("status = %d，应为 504", status)
		}
		if got := log.all(); !sameStrings(got, []string{ip1}) {
			t.Fatalf("钉 IP = %v，应只试第一个（504 不换 IP）", got)
		}
	})
}

// TestChatFailoverDNSFirstPinsIP 主入口 DNS 失败：先钉主入口的 HTTPDNS IP，钉 IP 后
// 再失败（传输错误）才换备用入口。
func TestChatFailoverDNSFirstPinsIP(t *testing.T) {
	ipM := dnsIPv4(11)
	f := newUpFake(t)
	f.onFunc(http.MethodPost, chatCompletionsPath, func(r *upReq) (*http.Response, error) {
		return nil, &net.DNSError{Err: "no such host", Name: foMainHost}
	})
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "data: backup\n\n")
	c := failoverClient(f)
	log := foAddress(c, func(_ context.Context, host string) []string {
		if host == foMainHost {
			return []string{ipM}
		}
		return nil
	}, func(string) (*http.Response, error) {
		return nil, io.EOF
	})

	rc, status, _, err := foChat(c, nil)
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v，应切到备用入口成功", status, err)
	}
	if got := foReadAll(t, rc); got != "data: backup\n\n" {
		t.Fatalf("流 = %q", got)
	}
	if got := log.all(); !sameStrings(got, []string{ipM}) {
		t.Fatalf("钉 IP = %v，应只钉主入口的 %s", got, ipM)
	}
	hosts := foHosts(f)
	if len(hosts) != 2 || hosts[0] != foMainHost || hosts[1] != foBackupHost {
		t.Fatalf("请求 host = %v，应为主入口、备用入口", hosts)
	}
}

// TestChatFailoverDNSEmptyCandidates 主入口 DNS 失败且候选为空：直接换备用入口。
func TestChatFailoverDNSEmptyCandidates(t *testing.T) {
	f := newUpFake(t)
	f.onFunc(http.MethodPost, chatCompletionsPath, func(*upReq) (*http.Response, error) {
		return nil, &net.DNSError{Err: "no such host", Name: foMainHost}
	})
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 200, "data: backup\n\n")
	c := failoverClient(f)
	log := foAddress(c, func(context.Context, string) []string { return nil },
		func(string) (*http.Response, error) { return jsonResp(200, "data: x\n\n"), nil })

	rc, status, _, err := foChat(c, nil)
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	_ = foReadAll(t, rc)
	if got := log.all(); len(got) != 0 {
		t.Fatalf("钉 IP = %v，候选为空不应钉 IP", got)
	}
	hosts := foHosts(f)
	if len(hosts) != 2 || hosts[1] != foBackupHost {
		t.Fatalf("请求 host = %v，应直接换备用入口", hosts)
	}
}

// TestChatFailoverRetryDeniedNoIP 503 + retry_policy.retry=false：入口与地址都不换。
func TestChatFailoverRetryDeniedNoIP(t *testing.T) {
	f := newUpFake(t)
	f.onFunc(http.MethodPost, chatCompletionsPath, func(*upReq) (*http.Response, error) {
		return jsonResp(503, `{"retry_policy":{"retry":false}}`), nil
	})
	c := failoverClient(f)
	log := foAddress(c, func(context.Context, string) []string { return []string{dnsIPv4(11)} },
		func(string) (*http.Response, error) { return jsonResp(200, "data: x\n\n"), nil })

	_, status, _, err := foChat(c, nil)
	if status != 503 {
		t.Fatalf("status = %d，应为 503", status)
	}
	var ue *Error
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v", err)
	}
	if got := log.all(); len(got) != 0 {
		t.Fatalf("钉 IP = %v，retry=false 不应换 IP", got)
	}
	if hosts := foHosts(f); len(hosts) != 1 || hosts[0] != foMainHost {
		t.Fatalf("请求 host = %v，不应换入口", hosts)
	}
}

// TestChatFailoverPinnedSuccessAndMemo 钉 IP 成功记成功、记备忘；同 ID 下一次调用在
// 备用入口上首发就钉同一个 IP。
func TestChatFailoverPinnedSuccessAndMemo(t *testing.T) {
	ip1, ip2 := dnsIPv4(11), dnsIPv4(12)
	setDNSConsts(t, nil, nil, nil)
	const rid = "conv-req-pin"
	f := newUpFake(t)
	f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
	f.onFunc(http.MethodPost, chatCompletionsPath, func(*upReq) (*http.Response, error) {
		return nil, io.EOF
	})
	c := failoverClient(f)
	log := foAddress(c, func(_ context.Context, host string) []string {
		if host == foBackupHost {
			return []string{ip1, ip2}
		}
		return nil
	}, func(ip string) (*http.Response, error) {
		if ip == ip2 {
			return jsonResp(200, "data: pinned\n\n"), nil
		}
		return nil, io.EOF
	})
	meta := ChatMeta{ConversationRequestID: rid}
	body := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)

	rc, status, _, err := c.ChatStreamContext(context.Background(), upAuth(), body, "", meta)
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	_ = foReadAll(t, rc)
	if got := log.all(); !sameStrings(got, []string{ip1, ip2}) {
		t.Fatalf("钉 IP 顺序 = %v，应为 [%s %s]", got, ip1, ip2)
	}
	if c.addr.lastOK[foBackupHost] != ip2 {
		t.Fatalf("上次成功的 IP = %q，应为 %s", c.addr.lastOK[foBackupHost], ip2)
	}
	if m, ok := c.addr.pinFor(rid); !ok || m.ip != ip2 || m.host != foBackupHost {
		t.Fatalf("钉 IP 备忘 = %+v ok=%v，应为 %s/%s", m, ok, foBackupHost, ip2)
	}
	// 同 host 的候选排序：上次成功的 ip2 放第一个，30 秒内失败过的 ip1 被排除。
	if got := c.addressCandidates(context.Background(), foBackupHost); !sameStrings(got, []string{ip2}) {
		t.Fatalf("候选顺序 = %v，应为 [%s]", got, ip2)
	}

	// 前门熔断打开后直接发备用入口：备忘让第一次就钉 ip2。
	for i := 0; i < failoverThreshold; i++ {
		c.failover.noteInfraFailure(cnProductEndpoint)
	}
	before := len(log.all())
	rc, status, _, err = c.ChatStreamContext(context.Background(), upAuth(), body, "", meta)
	if err != nil || status != 200 {
		t.Fatalf("第二次 status=%d err=%v", status, err)
	}
	_ = foReadAll(t, rc)
	after := log.all()
	if len(after) != before+1 || after[len(after)-1] != ip2 {
		t.Fatalf("第二次钉 IP = %v，应首发就钉 %s", after[before:], ip2)
	}
}

// TestChatFailoverInFlightSameRequestID 同请求 ID 在途换入口时，第二个调用直接返回错误。
func TestChatFailoverInFlightSameRequestID(t *testing.T) {
	const rid = "conv-req-inflight"
	entered := make(chan struct{})
	release := make(chan struct{})
	f := newUpFake(t)
	f.onHost(foMainHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
	f.onFunc(http.MethodPost, chatCompletionsPath, func(r *upReq) (*http.Response, error) {
		if u, err := url.Parse(r.url); err == nil && u.Host == foBackupHost {
			close(entered)
			<-release
			return jsonResp(200, "data: backup\n\n"), nil
		}
		return jsonResp(404, `{"code":404}`), nil
	})
	c := failoverClient(f)
	meta := ChatMeta{ConversationRequestID: rid}
	body := []byte(`{"model":"glm-5.2","messages":[]}`)

	done := make(chan error, 1)
	go func() {
		rc, status, _, err := c.ChatStreamContext(context.Background(), upAuth(), body, "", meta)
		if err == nil && status == 200 {
			_, _ = io.Copy(io.Discard, rc)
			rc.Close()
		}
		done <- err
	}()
	<-entered

	before := len(f.seen())
	_, _, _, err := c.ChatStreamContext(context.Background(), upAuth(), body, "", meta)
	if !errors.Is(err, errChatSwitchInFlight) {
		t.Fatalf("第二个同 ID 调用 err = %v，应为 errChatSwitchInFlight", err)
	}
	if n := len(f.seen()); n != before {
		t.Fatalf("第二个调用发了 %d 个请求，应一个都不发", n-before)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("第一个调用: %v", err)
	}
}
