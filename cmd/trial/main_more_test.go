// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

// main_more_test.go 覆盖 main 的退出码：CN 账号不适用 trial，正常返回 0；
// 没有凭证文件时返回 1。

import (
	"os"
	"testing"
)

// trialExit 捕获 exitFunc 的退出码。
type trialExit int

// stubTrialExit 把 exitFunc 换成 panic，测完还原。
func stubTrialExit(t *testing.T) {
	t.Helper()
	old := exitFunc
	exitFunc = func(code int) { panic(trialExit(code)) }
	t.Cleanup(func() { exitFunc = old })
}

// callTrialMain 调 main 并返回退出码。
func callTrialMain(t *testing.T, args ...string) int {
	t.Helper()
	oldArgs := os.Args
	os.Args = append([]string{"trial"}, args...)
	t.Cleanup(func() { os.Args = oldArgs })

	code := -1
	func() {
		defer func() {
			if r := recover(); r != nil {
				p, ok := r.(trialExit)
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
	return code
}

func TestTrialMainExitCodes(t *testing.T) {
	stubTrialExit(t)

	// 空目录：没有凭证文件 → 1。
	if code := callTrialMain(t, t.TempDir()); code != 1 {
		t.Fatalf("空目录退出码 = %d want 1", code)
	}

	// 只有 CN 账号：不适用 trial，不发请求，正常返回 0。
	dir := t.TempDir()
	writeTrialAuth(t, dir, "c-cn", "国内号", "cn")
	if code := callTrialMain(t, dir); code != 0 {
		t.Fatalf("CN 账号退出码 = %d want 0", code)
	}
}
