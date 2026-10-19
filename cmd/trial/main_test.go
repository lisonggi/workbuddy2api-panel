package main

// trial/main_test.go 批量领取 global trial：假 Transport 覆盖成功/已领/失败/
// 凭证损坏与 CN 账号不适用。global 请求必须打到国际版 base。

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

	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

type trialRT func(*http.Request) (*http.Response, error)

func (f trialRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type trialFake struct {
	mu   sync.Mutex
	recs []string // host+path
}

func (f *trialFake) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.recs = append(f.recs, r.URL.Host+r.URL.Path)
	f.mu.Unlock()
	status, body := 200, ""
	switch {
	case r.URL.Host == "global.fake" && r.URL.Path == "/billing/ide/trial":
		switch r.Header.Get("X-User-Id") {
		case "g-ok":
			body = `{"code":0,"data":{}}`
		case "g-already":
			status, body = 400, `{"code":14051,"msg":"already claimed"}`
		default:
			status, body = 400, `{"code":1,"msg":"trial boom"}`
		}
	default:
		status, body = 400, `{"code":400}`
	}
	return &http.Response{StatusCode: status,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   io.NopCloser(strings.NewReader(body))}, nil
}

func (f *trialFake) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.recs...)
}

// writeTrialAuth 写嵌套形凭证；realm 为空时用 CN 域。
func writeTrialAuth(t *testing.T, dir, uid, nick, realm string) {
	t.Helper()
	domain := "www.codebuddy.cn"
	if realm == "global" {
		domain = "www.workbuddy.ai"
	}
	raw := fmt.Sprintf(`{"auth":{"accessToken":"at-%s","refreshToken":"rt-%s","expiresAt":9999999999,"domain":%q,"realm":%q},`+
		`"account":{"uid":%q,"nickname":%q}}`, uid, uid, domain, realm, uid, nick)
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-"+uid+".json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 没有凭证文件：stderr 提示、返回 1、不发请求。
func TestTrialNoAuthFiles(t *testing.T) {
	up := &upstream.Client{HTTP: &http.Client{Transport: trialRT(func(r *http.Request) (*http.Response, error) {
		t.Errorf("不应发请求: %s%s", r.URL.Host, r.URL.Path)
		return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}}
	var out, errb bytes.Buffer
	if code := run([]string{t.TempDir()}, &out, &errb, up); code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(errb.String(), "no auth files in ") {
		t.Fatalf("stderr=%q", errb.String())
	}
}

// 多账号：global 成功/已领/失败，CN 不适用，坏文件与目录记 FAIL。
func TestTrialAccounts(t *testing.T) {
	dir := t.TempDir()
	writeTrialAuth(t, dir, "g-ok", "国际成功", "global")
	writeTrialAuth(t, dir, "g-already", "国际已领", "global")
	writeTrialAuth(t, dir, "g-fail", "国际失败", "global")
	writeTrialAuth(t, dir, "c-cn", "国内号", "cn")
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-broken.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "workbuddy-dir.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	fake := &trialFake{}
	up := &upstream.Client{
		HTTP:              &http.Client{Transport: fake},
		ChatBaseCN:        "https://chat.fake",
		BillingBaseCN:     "https://cn.fake",
		BillingBaseGlobal: "https://global.fake",
	}
	var out, errb bytes.Buffer
	if code := run([]string{dir}, &out, &errb, up); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
	text := out.String()
	for _, want := range []string{"| OK ", "| ALREADY ", "| N/A "} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺少状态 %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "| FAIL ") != 3 {
		t.Fatalf("FAIL 行数不对:\n%s", text)
	}
	if !strings.Contains(text, "total=6 ok=1 already=1 na=1 fail=3") {
		t.Fatalf("末行计数不对:\n%s", text)
	}

	reqs := fake.requests()
	if len(reqs) != 3 {
		t.Fatalf("只有 3 个有效 global 账号应发请求，实际 %v", reqs)
	}
	for _, r := range reqs {
		if r != "global.fake/billing/ide/trial" {
			t.Fatalf("trial 请求应打到国际版 base：%s", r)
		}
	}
}
