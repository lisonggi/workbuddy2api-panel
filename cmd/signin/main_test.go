package main

// signin/main_test.go 批量签到工具：用假 Transport 覆盖无凭证、多账号各状态、
// token 刷新（成功/会话失效/其他失败）与余额查询成败。凭证只写 t.TempDir()。

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
)

type signinRT func(*http.Request) (*http.Response, error)

func (f signinRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type signinRec struct{ method, host, path, uid string }

type signinFake struct {
	mu      sync.Mutex
	recs    []signinRec
	handler func(*http.Request, string) (int, string)
}

func (f *signinFake) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	f.mu.Lock()
	f.recs = append(f.recs, signinRec{r.Method, r.URL.Host, r.URL.Path, r.Header.Get("X-User-Id")})
	f.mu.Unlock()
	status, b := f.handler(r, string(body))
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(b)),
	}, nil
}

func (f *signinFake) requests() []signinRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]signinRec(nil), f.recs...)
}

// signinDefaultResponse 汇总 / 付费包 / 签到状态走 upstreamtest 的默认响应，
// 免费包（余额本体）与签到响应由各用例显式登记。
func signinDefaultResponse(path string) (int, string, bool) {
	switch path {
	case upstreamtest.SummaryPath, upstreamtest.PaidPath, upstreamtest.CheckinStatusPath:
		status, body, _ := upstreamtest.Respond(path)
		return status, body, true
	}
	return 0, "", false
}

func (f *signinFake) count(uid, path string) int {
	n := 0
	for _, r := range f.requests() {
		if r.uid == uid && r.path == path {
			n++
		}
	}
	return n
}

// writeSigninAuth 写一份嵌套形凭证（expiresAt 秒）。
func writeSigninAuth(t *testing.T, dir, uid, nick, token string, expires int64) string {
	t.Helper()
	raw := fmt.Sprintf(`{"auth":{"accessToken":%q,"refreshToken":"rt-%s","expiresAt":%d,"domain":"www.codebuddy.cn"},`+
		`"account":{"uid":%q,"nickname":%q}}`, token, uid, expires, uid, nick)
	path := filepath.Join(dir, "workbuddy-"+uid+".json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// 目录里没有凭证：stderr 提示、返回 1、不发请求；无参数时默认 auths。
func TestSigninNoAuthFiles(t *testing.T) {
	dir := t.TempDir()
	up := &upstream.Client{HTTP: &http.Client{Transport: signinRT(func(r *http.Request) (*http.Response, error) {
		t.Errorf("不应发请求: %s %s", r.Method, r.URL.Path)
		return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}}
	var out, errb bytes.Buffer
	if code := run([]string{dir}, &out, &errb, up); code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(errb.String(), "no auth files in "+dir) {
		t.Fatalf("stderr=%q", errb.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout 应为空: %q", out.String())
	}

	// 无参数：默认读 auths（切到空临时目录）。
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	errb.Reset()
	if code := run(nil, &out, &errb, up); code != 1 || !strings.Contains(errb.String(), "no auth files in auths") {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
}

// 多账号：OK / ALREADY / FAIL / LOAD_ERR 各自一行，末行计数与 remain 列正确。
func TestSigninAccounts(t *testing.T) {
	dir := t.TempDir()
	far := time.Now().Add(24 * time.Hour).Unix()
	writeSigninAuth(t, dir, "u-ok", "成功号", "at-ok", far)
	writeSigninAuth(t, dir, "u-already", "已签号", "at-already", far)
	writeSigninAuth(t, dir, "u-fail", "失败号", "at-fail", far)
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-broken.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "workbuddy-dir.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	fake := &signinFake{handler: func(r *http.Request, body string) (int, string) {
		uid := r.Header.Get("X-User-Id")
		switch r.URL.Path {
		case "/v2/billing/meter/daily-checkin":
			switch uid {
			case "u-ok":
				return 200, `{"code":0,"data":{}}`
			case "u-already":
				return 400, `{"code":10001,"msg":"今天已签到"}`
			default:
				return 400, `{"code":500,"msg":"boom"}`
			}
		case upstreamtest.FreePath:
			switch uid {
			case "u-ok":
				return 200, upstreamtest.List(upstreamtest.Package(100, 42, ""))
			case "u-fail":
				return 200, upstreamtest.List(upstreamtest.Package(10, 7, ""))
			default:
				return 400, `{"code":400,"msg":"balance boom"}`
			}
		default:
			if status, body, ok := signinDefaultResponse(r.URL.Path); ok {
				return status, body
			}
			t.Errorf("未登记的请求: %s %s", r.Method, r.URL.Path)
			return 400, `{"code":400}`
		}
	}}
	up := &upstream.Client{HTTP: &http.Client{Transport: fake}, ChatBaseCN: "https://chat.fake", BillingBaseCN: "https://billing.fake"}

	var out, errb bytes.Buffer
	if code := run([]string{dir}, &out, &errb, up); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
	text := out.String()
	if !strings.Contains(text, "| OK ") || !strings.Contains(text, "| ALREADY ") ||
		!strings.Contains(text, "| FAIL ") || strings.Count(text, "| LOAD_ERR ") != 2 {
		t.Fatalf("状态行不对:\n%s", text)
	}
	if !strings.Contains(text, "total=5 ok=1 already=1 fail=3") {
		t.Fatalf("末行计数不对:\n%s", text)
	}
	if !strings.Contains(text, "| 42 ") {
		t.Fatalf("成功账号 remain 应为 42:\n%s", text)
	}
	if !strings.Contains(text, "| 7 ") {
		t.Fatalf("签到失败账号仍应查余额:\n%s", text)
	}
	if got := fake.count("u-already", upstreamtest.FreePath); got != 1 {
		t.Fatalf("已签到账号也应查余额，实际 %d 次", got)
	}
}

// token 刷新：2 小时内过期就先刷新，成功写回文件后再签到；会话失效 AUTH_INVALID；其他错误 FAIL，都不签到。
func TestSigninRefresh(t *testing.T) {
	dir := t.TempDir()
	soon := time.Now().Add(30 * time.Minute).Unix() // 还没过期，但在 2 小时的提前刷新窗口内
	okPath := writeSigninAuth(t, dir, "r-ok", "刷新成功", "old-at", soon)
	writeSigninAuth(t, dir, "r-dead", "会话失效", "dead-at", soon)
	writeSigninAuth(t, dir, "r-err", "刷新失败", "err-at", soon)

	fake := &signinFake{handler: func(r *http.Request, body string) (int, string) {
		uid := r.Header.Get("X-User-Id")
		switch r.URL.Path {
		case "/v2/plugin/auth/token/refresh":
			// refresh 请求不带 X-User-Id，用 X-Refresh-Token（rt-<uid>）区分账号。
			switch r.Header.Get("X-Refresh-Token") {
			case "rt-r-ok":
				return 200, `{"code":0,"data":{"accessToken":"new-at","refreshToken":"new-rt","expiresIn":7200,"domain":"www.codebuddy.cn"}}`
			case "rt-r-dead":
				return 401, `{"code":12153,"msg":"Offline user session not found"}`
			default:
				return 500, `{"code":500,"msg":"refresh boom"}`
			}
		case "/v2/billing/meter/daily-checkin":
			if uid != "r-ok" {
				t.Errorf("刷新失败/会话失效不应签到: uid=%s", uid)
			}
			return 200, `{"code":0,"data":{}}`
		case upstreamtest.FreePath:
			return 200, upstreamtest.List(upstreamtest.Package(2, 1, ""))
		default:
			if status, body, ok := signinDefaultResponse(r.URL.Path); ok {
				return status, body
			}
			t.Errorf("未登记的请求: %s %s", r.Method, r.URL.Path)
			return 400, `{"code":400}`
		}
	}}
	up := &upstream.Client{HTTP: &http.Client{Transport: fake}, ChatBaseCN: "https://chat.fake", BillingBaseCN: "https://billing.fake"}

	var out, errb bytes.Buffer
	if code := run([]string{dir}, &out, &errb, up); code != 0 {
		t.Fatalf("code=%d", code)
	}
	text := out.String()
	if !strings.Contains(text, "AUTH_INVALID") || strings.Count(text, "| FAIL ") != 1 {
		t.Fatalf("刷新状态不对:\n%s", text)
	}
	if got := fake.count("r-ok", "/v2/billing/meter/daily-checkin"); got != 1 {
		t.Fatalf("刷新成功后应签到一次，实际 %d", got)
	}

	// 刷新成功要写回文件：重新读文件断言新 token。
	raw, err := os.ReadFile(okPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"accessToken": "new-at"`) || !strings.Contains(string(raw), `"refreshToken": "new-rt"`) {
		t.Fatalf("刷新后凭证未写回:\n%s", raw)
	}
}

// isAlready 的各判定分支与截断辅助函数。
func TestSigninHelpers(t *testing.T) {
	for _, msg := range []string{"今天已签到", "ALREADY checked", "checkin failed", "code=400 x"} {
		if !isAlready(msg) {
			t.Fatalf("isAlready(%q) 应为 true", msg)
		}
	}
	if isAlready("boom") {
		t.Fatal("无关错误不应判已签到")
	}
	if got := trunc("0123456789", 4); got != "0123" {
		t.Fatalf("trunc=%q", got)
	}
	if got := trunc("abc", 10); got != "abc" {
		t.Fatalf("trunc=%q", got)
	}
	if got := short("a\nb"); got != "a b" {
		t.Fatalf("short=%q", got)
	}
	long := strings.Repeat("x", 100)
	if got := short(long); len(got) != 60 {
		t.Fatalf("short 应截断到 60，实际 %d", len(got))
	}
}
