// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

//go:build !unix

package selfupdate

import "errors"

// execFn 这些平台没有 exec；unsupportedReason 已经挡住了一键更新，这里只是让编译通过。
var execFn = func(string, []string, []string) error {
	return errors.New("这个平台不支持原地重启")
}
