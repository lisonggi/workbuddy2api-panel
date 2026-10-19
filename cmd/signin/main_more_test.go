package main

// main_more_test.go 补齐 main 的退出码与刷新成功后写回失败的日志分支。

import (
	"bytes"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// signinExit 捕获 exitFunc 的退出码。
type signinExit int

// stubSigninExit 把 exitFunc 换成 panic，测完还原。
func stubSigninExit(t *testing.T) {
	t.Helper()
	old := exitFunc
	exitFunc = func(code int) { panic(signinExit(code)) }
	t.Cleanup(func() { exitFunc = old })
}

// 没有凭证文件时 main 以 1 退出。
func TestSigninMainExitCode(t *testing.T) {
	stubSigninExit(t)
	oldArgs := os.Args
	os.Args = []string{"signin", t.TempDir()}
	t.Cleanup(func() { os.Args = oldArgs })

	code := 0
	func() {
		defer func() {
			if r := recover(); r != nil {
				p, ok := r.(signinExit)
				if !ok {
					panic(r)
				}
				code = int(p)
				return
			}
			t.Fatal("没有凭证文件时 main 应退出")
		}()
		main()
	}()
	if code != 1 {
		t.Fatalf("退出码 = %d want 1", code)
	}
}

// 刷新成功后写回文件失败：必须打日志暴露，随后照常签到并查余额。
func TestSigninSaveFailureLog(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下权限位不生效")
	}
	dir := t.TempDir()
	soon := time.Now().Add(30 * time.Minute).Unix()
	writeSigninAuth(t, dir, "r-save", "写回失败", "old-at", soon)
	// 目录只读：文件仍可读，写 tmp 时 EACCES。
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	fake := &signinFake{handler: func(r *http.Request, body string) (int, string) {
		switch r.URL.Path {
		case "/v2/plugin/auth/token/refresh":
			return 200, `{"code":0,"data":{"accessToken":"new-at","refreshToken":"new-rt","expiresIn":7200,"domain":"www.codebuddy.cn"}}`
		case "/v2/billing/meter/daily-checkin":
			return 200, `{"code":0,"data":{}}`
		case "/v2/billing/meter/get-user-resource":
			return 200, `{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CapacityRemain":5,"CapacitySize":10}]}}}}`
		default:
			t.Errorf("未登记的请求: %s %s", r.Method, r.URL.Path)
			return 400, `{"code":400}`
		}
	}}
	up := &upstream.Client{HTTP: &http.Client{Transport: fake}, ChatBaseCN: "https://chat.fake", BillingBaseCN: "https://billing.fake"}

	var logs bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldLog) })

	var out, errb bytes.Buffer
	if code := run([]string{dir}, &out, &errb, up); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
	if !strings.Contains(logs.String(), "signin r-save save:") {
		t.Fatalf("写回失败应打日志，实际日志=%q", logs.String())
	}
	if !strings.Contains(out.String(), "| OK ") {
		t.Fatalf("写回失败不应影响签到结果:\n%s", out.String())
	}
	if got := fake.count("r-save", "/v2/billing/meter/get-user-resource"); got != 1 {
		t.Fatalf("应照常查余额，实际 %d 次", got)
	}
}
