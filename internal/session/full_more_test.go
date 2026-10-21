// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package session

// full_more_test.go 补齐 ID 解析/签名的坏输入分支、ExtractKey 的空体与
// prompt_cache_key、慢路径以外的会话路由分支，以及 TurnIDs 的空轮键/空 callID。

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// 熵源失败时回退成 32 位 hex（时间戳拼 math/rand）。
func TestNewMessageIDFallback(t *testing.T) {
	old := cryptoRandRead
	cryptoRandRead = func([]byte) (int, error) { return 0, errors.New("rand boom") }
	t.Cleanup(func() { cryptoRandRead = old })

	id := NewMessageID()
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		t.Fatalf("回退 ID 应为 32 位小写 hex: %q", id)
	}
	// 低 64 位的最高位被强制置 1，保证非空。
	if id[0] == '0' && id[1] == '0' && id[2] == '0' && id[3] == '0' {
		t.Fatalf("回退 ID 不应全零: %q", id)
	}
}

// contentText：字符串/数组的坏 JSON 与未知形态都返回空串。
func TestContentTextInvalidForms(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"未闭合字符串", `"abc`, ""},
		{"未闭合数组", `[{"text":`, ""},
		{"数字形态", `123`, ""},
		{"对象形态", `{"text":"x"}`, ""},
		{"字符串", `"hello"`, "hello"},
		{"多段文本", `[{"text":"a"},{"text":"b"}]`, "ab"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := contentText(json.RawMessage(c.raw)); got != c.want {
				t.Fatalf("contentText(%s) = %q want %q", c.raw, got, c.want)
			}
		})
	}
}

// contentSignature：坏 JSON、非字符串/数组、数组元素不是对象都返回空串；
// 纯文本 part 拼接后原样返回，非文本 part 以 type+摘要入键。
func TestContentSignatureInvalidForms(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"未闭合字符串", `"abc`, ""},
		{"对象形态", `{"a":1}`, ""},
		{"未闭合数组", `[{"type":`, ""},
		{"元素不是对象", `[1]`, ""},
		{"纯文本 part", `[{"type":"text","text":"hi"}]`, "hi"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := contentSignature(json.RawMessage(c.raw)); got != c.want {
				t.Fatalf("contentSignature(%s) = %q want %q", c.raw, got, c.want)
			}
		})
	}

	got := contentSignature(json.RawMessage(`[{"type":"image_url","url":"u"}]`))
	if !strings.HasPrefix(got, "[image_url:") || !strings.HasSuffix(got, "]") {
		t.Fatalf("非文本 part 应入 type+摘要: %q", got)
	}
}

// ExtractKey：空 body、prompt_cache_key、messages 里的非对象项。
func TestExtractKeyMoreBranches(t *testing.T) {
	if got := ExtractKey(nil); got != "" {
		t.Fatalf("空 body 应返回空串: %q", got)
	}
	if got := ExtractKey([]byte(`{"prompt_cache_key":"pck-1"}`)); got != "pck-1" {
		t.Fatalf("prompt_cache_key 应作为会话键: %q", got)
	}
	got := ExtractKey([]byte(`{"messages":["junk",{"role":"user","content":"hi"}]}`))
	if !strings.HasPrefix(got, derivedKeyPrefix) {
		t.Fatalf("非对象消息应跳过并继续派生: %q", got)
	}
}

// userContentSignature：不可序列化值、标量、纯图片 parts 与 nil。
func TestUserContentSignatureMoreBranches(t *testing.T) {
	if got := userContentSignature(make(chan int)); got != "" {
		t.Fatalf("不可序列化值应为空串: %q", got)
	}
	if got := userContentSignature(nil); got != "" {
		t.Fatalf("nil 应为空串: %q", got)
	}
	if got := userContentSignature(123); got != "" {
		t.Fatalf("标量应为空串: %q", got)
	}
	if got := userContentSignature("hello"); got != "hello" {
		t.Fatalf("字符串应原样返回: %q", got)
	}
	parts := []any{map[string]any{"type": "image_url", "url": "https://x/y"}}
	if got := userContentSignature(parts); got != "[image_url]" {
		t.Fatalf("非文本 part 只入 type 占位: %q", got)
	}
}

// ForRequest：空轮键直接返回空串（nil 接收者同样安全）。
func TestTurnIDsEmptyKey(t *testing.T) {
	var nilIDs *TurnIDs
	if got := nilIDs.ForRequest("scope", Turn{}); got != "" {
		t.Fatalf("nil 接收者 + 空轮键应返回空串: %q", got)
	}
	if got := NewTurnIDs().ForRequest("scope", Turn{}); got != "" {
		t.Fatalf("空轮键应返回空串: %q", got)
	}
}

// Remember：空的 callID 跳过，非空 callID 正常登记并能被找回。
func TestTurnIDsSkipsEmptyCallID(t *testing.T) {
	ids := NewTurnIDs()
	opening := Turn{Key: "k1", Opening: true}
	ids.Remember("scope", opening, "turn-id", []string{"", "call-1"})
	got := ids.ForRequest("scope", Turn{Key: "k1", CallIDs: []string{"call-1"}})
	if got != "turn-id" {
		t.Fatalf("空 callID 应跳过、非空 callID 应找回: %q", got)
	}
	// 空 callID 不应留下任何记录：用同一个空 id 找不回。
	if got := ids.ForRequest("scope", Turn{Key: "k1", CallIDs: []string{""}}); got == "turn-id" {
		t.Fatalf("空 callID 不应登记: %q", got)
	}
}
