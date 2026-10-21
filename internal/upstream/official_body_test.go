// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestInjectOfficialBodyFields 带了思考档位才补 reasoning_summary="auto"，从不补 cache_retention；
// 客户端带了任何值都原样保留；空 body / 坏 body 原样返回。
func TestInjectOfficialBodyFields(t *testing.T) {
	decode := func(b []byte) map[string]any {
		t.Helper()
		var obj map[string]any
		if err := json.Unmarshal(b, &obj); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		return obj
	}
	for _, tc := range []struct {
		name, in       string
		cache, summary any
	}{
		{"都没带、没有思考档位", `{"model":"m"}`, nil, nil},
		{"reasoning_effort", `{"model":"m","reasoning_effort":"high"}`, nil, "auto"},
		{"reasoning.effort", `{"model":"m","reasoning":{"effort":"low"}}`, nil, "auto"},
		{"reasoning 不是对象", `{"model":"m","reasoning":true}`, nil, nil},
		{"客户端都带了", `{"model":"m","reasoning_effort":"high","cache_retention":"short","reasoning_summary":"none"}`, "short", "none"},
	} {
		obj := decode(InjectOfficialBodyFields([]byte(tc.in)))
		if obj["cache_retention"] != tc.cache || obj["reasoning_summary"] != tc.summary || obj["model"] != "m" {
			t.Errorf("%s: cache_retention=%v reasoning_summary=%v（%v）", tc.name, obj["cache_retention"], obj["reasoning_summary"], obj)
		}
	}
	keep := `{"model":"m","cache_retention":"short","reasoning_effort":"high","reasoning_summary":"none"}`
	if got := string(InjectOfficialBodyFields([]byte(keep))); got != keep {
		t.Fatalf("不需要补时应原样返回: %s", got)
	}
	for _, in := range []string{"", "not json"} {
		if got := string(InjectOfficialBodyFields([]byte(in))); got != in {
			t.Fatalf("%q 应原样返回: %q", in, got)
		}
	}
}

// TestPrepareBodyInjectsOfficialFields chat 出站 body 经 prepareBody 后不带 cache_retention，两个域都一样；
// deepseek 模型被补了默认思考档位，也就跟着带 reasoning_summary。
func TestPrepareBodyInjectsOfficialFields(t *testing.T) {
	for _, realm := range []string{"cn", "global"} {
		out := string((&Client{}).prepareBody([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), realm))
		if strings.Contains(out, "cache_retention") || strings.Contains(out, "reasoning_summary") {
			t.Fatalf("%s: 普通模型两个字段都不该补: %s", realm, out)
		}
		out = string((&Client{}).prepareBody([]byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`), realm))
		if !strings.Contains(out, `"reasoning_effort"`) || !strings.Contains(out, `"reasoning_summary":"auto"`) {
			t.Fatalf("%s: deepseek 补了思考档位就应带 reasoning_summary: %s", realm, out)
		}
	}
}
