package main

// main_more_test.go 覆盖 main 的退出码、企业身份头与 fetchUserResource 的
// 传输/解析/业务错误分支、size 推导 used 的修正。

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// creditExit 捕获 exitFunc 的退出码。
type creditExit int

func stubCreditExit(t *testing.T) {
	t.Helper()
	old := exitFunc
	exitFunc = func(code int) { panic(creditExit(code)) }
	t.Cleanup(func() { exitFunc = old })
}

// 正常路径（空目录）main 以 0 退出。
func TestCreditMainExitZero(t *testing.T) {
	stubCreditExit(t)
	setAccountGap0(t)
	t.Setenv("WB2A_AUTH_DIR", t.TempDir())
	oldArgs := os.Args
	os.Args = []string{"credit"}
	t.Cleanup(func() { os.Args = oldArgs })

	code := -1
	func() {
		defer func() {
			if r := recover(); r != nil {
				p, ok := r.(creditExit)
				if !ok {
					panic(r)
				}
				code = int(p)
				return
			}
			t.Fatal("main 应调用 exitFunc")
		}()
		main()
	}()
	if code != 0 {
		t.Fatalf("退出码 = %d want 0", code)
	}
}

// fakeBodyClient 返回固定状态码与响应体的 client。
func fakeBodyClient(status int, body string) *http.Client {
	return &http.Client{Transport: creditRT(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

// 企业账号会带 X-Enterprise-Id / X-Tenant-Id；两个头都按企业 ID 填写。
func TestCreditEnterpriseHeaders(t *testing.T) {
	var got map[string]string
	client := &http.Client{Transport: creditRT(func(r *http.Request) (*http.Response, error) {
		got = map[string]string{
			"X-Enterprise-Id": r.Header.Get("X-Enterprise-Id"),
			"X-Tenant-Id":     r.Header.Get("X-Tenant-Id"),
			"X-User-Id":       r.Header.Get("X-User-Id"),
			"X-Domain":        r.Header.Get("X-Domain"),
		}
		return &http.Response{StatusCode: 200, Header: http.Header{},
			Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"Response":{"Data":{"Accounts":[]}}}}`))}, nil
	})}
	var af authFile
	af.Auth.AccessToken = "at"
	af.Auth.Domain = "www.codebuddy.cn"
	af.Account.UID = "u-ent"
	af.Account.EnterpriseID = "ent-9"
	if _, _, _, _, err := fetchUserResource(&af, client); err != nil {
		t.Fatalf("fetchUserResource: %v", err)
	}
	want := map[string]string{
		"X-Enterprise-Id": "ent-9",
		"X-Tenant-Id":     "ent-9",
		"X-User-Id":       "u-ent",
		"X-Domain":        "www.codebuddy.cn",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q want %q（全部头 %v）", k, got[k], v, got)
		}
	}
}

// fetchUserResource 的错误分支：传输失败、非 JSON、code 非 0。
func TestCreditFetchErrors(t *testing.T) {
	var af authFile
	af.Auth.AccessToken = "at"

	t.Run("传输错误", func(t *testing.T) {
		client := &http.Client{Transport: creditRT(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial boom")
		})}
		_, _, _, _, err := fetchUserResource(&af, client)
		if err == nil || !strings.Contains(err.Error(), "dial boom") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("响应不是 JSON", func(t *testing.T) {
		_, _, _, _, err := fetchUserResource(&af, fakeBodyClient(200, "not-json"))
		if err == nil || !strings.Contains(err.Error(), "invalid character") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("业务 code 非 0", func(t *testing.T) {
		_, _, _, _, err := fetchUserResource(&af, fakeBodyClient(200, `{"code":7,"msg":"nope"}`))
		if err == nil || err.Error() != "code=7 nope" {
			t.Fatalf("err=%v", err)
		}
	})
}

// size 与 used 不一致时按 size-remain 修正 used。
func TestCreditUsedDerivedFromSize(t *testing.T) {
	client := fakeBodyClient(200, `{"code":0,"data":{"Response":{"Data":{"Accounts":[`+
		`{"CapacityRemain":30,"CapacityUsed":10,"CapacitySize":100}]}}}}`)
	var af authFile
	af.Auth.AccessToken = "at"
	remain, used, size, packs, err := fetchUserResource(&af, client)
	if err != nil {
		t.Fatal(err)
	}
	if remain != 30 || used != 70 || size != 100 || packs != 1 {
		t.Fatalf("=(%d,%d,%d,%d) want (30,70,100,1)", remain, used, size, packs)
	}
}

// run 的 JSON 汇总能容纳一个企业账号（覆盖 writeCreditAuth 之外的字段组合）。
func TestCreditEnterpriseAccountRun(t *testing.T) {
	setAccountGap0(t)
	dir := t.TempDir()
	raw := `{"auth":{"accessToken":"at-e","domain":"www.codebuddy.cn","realm":"cn"},` +
		`"account":{"uid":"u-e","enterpriseId":"ent-1","nickname":"企业号"}}`
	if err := os.WriteFile(dir+"/workbuddy-u-e.json", []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB2A_AUTH_DIR", dir)
	var out bytes.Buffer
	if code := run(nil, &out, fakeBodyClient(200, `{"code":0,"data":{"Response":{"Data":{"Accounts":[]}}}}`)); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out.String(), `"uid":"u-e"`) {
		t.Fatalf("输出缺少企业账号: %s", out.String())
	}
}
