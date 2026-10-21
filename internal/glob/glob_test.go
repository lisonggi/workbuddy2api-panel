// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package glob

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"claude-*haiku*", "claude-haiku-4-5-20251001", true},
		{"claude-*haiku*", "Claude-3-5-HAIKU", true},
		{"claude-*", "claude", false},
		{"claude-*", "claude-", true},
		{"*", "anything", true},
		{"gpt-*-codex*", "gpt-5.5-codex", true},
		{"gpt-*-codex*", "gpt-5.5", false},
		{"a*a", "a", false},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "acb", false},
		{"exact", "EXACT", true},
		{"exact", "exactly", false},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.s); got != c.want {
			t.Errorf("Match(%q, %q)=%v want %v", c.pattern, c.s, got, c.want)
		}
	}
}
