// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

// main_more_test.go 补齐 --realm 缺值、runURL 上游失败与 main 入口。

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
)

// --realm 缺少取值：fatal，提示 requires a value。
func TestRunRealmMissingValue(t *testing.T) {
	_, errb, fatal, code := runLogin(t, []string{"--realm"}, strings.NewReader(""),
		&http.Client{Transport: loginFakeClient(t)})
	if !fatal || code != 1 || !strings.Contains(errb, "requires a value") {
		t.Fatalf("fatal=%v code=%d stderr=%q", fatal, code, errb)
	}
}

// url 子命令：请求 state 端点失败（传输层）走 fatal。
func TestRunURLTransportError(t *testing.T) {
	useStateFile(t)
	client := &http.Client{Transport: loginRT(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial boom")
	})}
	_, errb, fatal, _ := runLogin(t, []string{"url"}, strings.NewReader(""), client)
	if !fatal || !strings.Contains(errb, "auth state failed") || !strings.Contains(errb, "dial boom") {
		t.Fatalf("fatal=%v stderr=%q", fatal, errb)
	}
}

// main：没有子命令时走 fatal（退出码 1），覆盖入口本身。
func TestLoginMainEntry(t *testing.T) {
	stubExit(t)
	oldArgs := os.Args
	os.Args = []string{"login"}
	t.Cleanup(func() { os.Args = oldArgs })

	code := -1
	func() {
		defer func() {
			if r := recover(); r != nil {
				p, ok := r.(exitPanic)
				if !ok {
					panic(r)
				}
				code = p.code
				return
			}
			t.Fatal("main 应调用 exitFunc")
		}()
		main()
	}()
	if code != 1 {
		t.Fatalf("退出码 = %d want 1", code)
	}
}
