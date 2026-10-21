// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import "testing"

// TestIsTruncatedArguments 只把「非空但无法解析」当截断：空串/纯空白、完整对象、
// 标量/数组/null 等合法 JSON 都不算。
func TestIsTruncatedArguments(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"空串（无参工具）", "", false},
		{"纯空白", "  \n\t ", false},
		{"完整对象", `{"query":"hi","limit":3}`, false},
		{"空对象", `{}`, false},
		{"null 合法", `null`, false},
		{"数字合法", `123`, false},
		{"数组合法", `[1,2]`, false},
		{"字符串合法", `"done"`, false},
		{"缺右括号", `{"query":"hi"`, true},
		{"字符串没闭合", `{"query":"hi`, true},
		{"对象中间截断", `{"a":1,"b":`, true},
		{"前后空白但完整", "  {\"a\":1}  ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTruncatedArguments(tc.raw); got != tc.want {
				t.Errorf("isTruncatedArguments(%q) = %v，应为 %v", tc.raw, got, tc.want)
			}
		})
	}
}

func toolCall(name, args string) map[string]any {
	return map[string]any{"type": "function", "function": map[string]any{"name": name, "arguments": args}}
}

// TestDropTruncatedToolCalls 过滤残缺工具调用：完整调用原样保留（含不同顺序）、
// 只去掉截断项；无 function/arguments 非字符串的调用不判定；全部截断返回空切片。
func TestDropTruncatedToolCalls(t *testing.T) {
	t.Run("全部完整原样保留", func(t *testing.T) {
		calls := []map[string]any{toolCall("a", `{"x":1}`), toolCall("b", `{}`)}
		kept := dropTruncatedToolCalls(calls)
		if len(kept) != 2 ||
			kept[0]["function"].(map[string]any)["name"] != "a" ||
			kept[1]["function"].(map[string]any)["name"] != "b" {
			t.Errorf("完整调用应原样保留: %v", kept)
		}
		if kept[0]["function"].(map[string]any)["arguments"] != `{"x":1}` {
			t.Errorf("保留项不应改动: %v", kept[0])
		}
	})

	t.Run("部分截断只去截断项且顺序不变", func(t *testing.T) {
		calls := []map[string]any{
			toolCall("a", `{"x":1}`),
			toolCall("broken", `{"x":`),
			toolCall("b", `{"y":2}`),
			toolCall("broken2", `{"y":"未闭合`),
		}
		kept := dropTruncatedToolCalls(calls)
		if len(kept) != 2 || kept[0]["function"].(map[string]any)["name"] != "a" ||
			kept[1]["function"].(map[string]any)["name"] != "b" {
			t.Errorf("保留项 = %v", kept)
		}
	})

	t.Run("无 function 字段保留", func(t *testing.T) {
		calls := []map[string]any{{"type": "function"}}
		if kept := dropTruncatedToolCalls(calls); len(kept) != 1 {
			t.Errorf("无 function 的调用应保留: %v", kept)
		}
	})

	t.Run("arguments 不是字符串保留", func(t *testing.T) {
		calls := []map[string]any{{"function": map[string]any{"arguments": 42}}}
		if kept := dropTruncatedToolCalls(calls); len(kept) != 1 {
			t.Errorf("非字符串 arguments 应保留: %v", kept)
		}
	})

	t.Run("全部截断返回空切片", func(t *testing.T) {
		calls := []map[string]any{toolCall("a", `{`), toolCall("b", `{"x"`)}
		kept := dropTruncatedToolCalls(calls)
		if kept == nil || len(kept) != 0 {
			t.Errorf("全部截断应返回非 nil 空切片，got %#v", kept)
		}
	})

	t.Run("nil 输入返回空切片", func(t *testing.T) {
		kept := dropTruncatedToolCalls(nil)
		if kept == nil || len(kept) != 0 {
			t.Errorf("nil 输入应返回非 nil 空切片，got %#v", kept)
		}
	})
}
