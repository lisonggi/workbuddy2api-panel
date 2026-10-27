// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// imagePartSig 计算 part 原文摘要（sha256 前 8 hex），供期望值精确构造——
// 签名算法变更时测试期望值随此 helper 单点同步。
func imagePartSig(t *testing.T, part string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(part))
	return hex.EncodeToString(sum[:4])
}

// TestResolveConversationID 覆盖 conversationId 提取的 snake/camel/缺失三态：
//   - metadata.conversation_id / metadata.conversationId → 取值
//   - 顶层 conversation_id / conversationId → 取值（snake 优先同 ExtractKey）
//   - 缺失 / 只有 user_id → ""（会话头族语义只认对话 ID，绝不回落 user_id）
func TestResolveConversationID(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"metadata snake_case", `{"metadata":{"conversation_id":"conv-1"}}`, "conv-1"},
		{"metadata camelCase", `{"metadata":{"conversationId":"conv-2"}}`, "conv-2"},
		{"top-level snake_case", `{"conversation_id":"conv-3"}`, "conv-3"},
		{"top-level camelCase", `{"conversationId":"conv-4"}`, "conv-4"},
		{"snake wins over camel", `{"conversation_id":"conv-s","conversationId":"conv-c"}`, "conv-s"},
		{"missing", `{"model":"glm-5.2"}`, ""},
		{"empty body", ``, ""},
		{"broken json", `{broken`, ""},
		{"metadata user_id only", `{"metadata":{"user_id":"u1"}}`, ""},
		{"top-level user_id only", `{"user_id":"u1"}`, ""},
		{"empty string value", `{"conversationId":""}`, ""},
		{"non-string value", `{"conversationId":123}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ResolveConversationID([]byte(c.body)); got != c.want {
				t.Errorf("ResolveConversationID(%q) = %q want %q", c.body, got, c.want)
			}
		})
	}
}

// TestNewMessageIDFormat 32 位 hex 且不为空、两次调用大概率不同（随机性冒烟）。
func TestNewMessageIDFormat(t *testing.T) {
	for i := 0; i < 50; i++ {
		id := NewMessageID()
		if len(id) != 32 {
			t.Fatalf("NewMessageID() = %q len=%d want 32", id, len(id))
		}
		for _, ch := range id {
			if !strings.ContainsRune("0123456789abcdef", ch) {
				t.Fatalf("NewMessageID() = %q has non-hex char %q", id, ch)
			}
		}
	}
}

// TestRequestIDForKeyStability 同 key 恒稳定、异 key 各不同、空 key 每次新值。
func TestRequestIDForKeyStability(t *testing.T) {
	// 预热清空包级缓存，避免其他测试污染 key（测试隔离）。
	k1a := RequestIDForKey("conv-a")
	k1b := RequestIDForKey("conv-a")
	if k1a != k1b {
		t.Errorf("same key should be stable: %q vs %q", k1a, k1b)
	}
	k2 := RequestIDForKey("conv-b")
	if k1a == k2 {
		t.Errorf("different keys should differ: %q", k1a)
	}
	// 空 key：每次调用生成新值（无会话则无"会话内稳定"语义）。
	e1 := RequestIDForKey("")
	e2 := RequestIDForKey("")
	if e1 == e2 {
		t.Errorf("empty key should yield fresh values each call: %q", e1)
	}
	// 稳定值自身也须是 32 hex（可作 B3 TraceId 直接使用）。
	for _, id := range []string{k1a, k2, e1} {
		if len(id) != 32 {
			t.Errorf("RequestIDForKey value %q len=%d want 32", id, len(id))
		}
	}
}

// TestTurnKeyExtraction 轮级兜底键的提取：取**最后一条** user 消息的「序号+文本」，
// 轮内追加 assistant/tool 消息不改变键；无 user / 无文本 / 坏 JSON 一律空串。
func TestTurnKeyExtraction(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"single user", `{"messages":[{"role":"user","content":"你好"}]}`, "u0:你好"},
		{"last user wins", `{"messages":[{"role":"user","content":"第一问"},{"role":"assistant","content":"答"},{"role":"user","content":"第二问"}]}`, "u2:" + historySig(t, "user", "第一问", "assistant", "答") + "\x00第二问"},
		// 首轮之前只有 system：没有可入键的历史，键形态与引入历史摘要前一致。
		{"system only before user", `{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"你好"}]}`, "u1:你好"},
		// agent 多步：轮内追加 assistant/tool 消息，末条 user 位置与内容不变 → 同键。
		{"agent step keeps same key", `{"messages":[{"role":"user","content":"任务"},{"role":"assistant","tool_calls":[{"id":"c1"}]},{"role":"tool","content":"结果"}]}`, "u0:任务"},
		{"multimodal parts text joined", `{"messages":[{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"data:x"}}]}]}`, "u0:看图\n[image_url:" + imagePartSig(t, `{"type":"image_url","image_url":{"url":"data:x"}}`) + "]"},
		{"no user message", `{"messages":[{"role":"system","content":"sys"}]}`, ""},
		{"empty messages", `{"messages":[]}`, ""},
		{"messages key absent", `{"model":"glm-5.2"}`, ""},
		{"broken json", `{broken`, ""},
		{"empty body", ``, ""},
		{"empty content", `{"messages":[{"role":"user","content":""}]}`, ""},
		{"null content", `{"messages":[{"role":"user","content":null}]}`, ""},
		// 纯图片 content 按内容签名派生非空轮级键（G1 修复，原为 ""）；键含 image part 摘要。
		{"image only content", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`, "u0:[image_url:" + imagePartSig(t, `{"type":"image_url","image_url":{"url":"x"}}`) + "]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TurnKey([]byte(c.body)); got != c.want {
				t.Errorf("TurnKey(%q) = %q want %q", c.body, got, c.want)
			}
		})
	}
}

// historySig 按 historyDigest 的口径算期望摘要（参数为 角色, 文本, 角色, 文本…）。
func historySig(t *testing.T, roleText ...string) string {
	t.Helper()
	var msgs []turnMessage
	for i := 0; i+1 < len(roleText); i += 2 {
		raw, _ := json.Marshal(roleText[i+1])
		msgs = append(msgs, turnMessage{Role: roleText[i], Content: raw})
	}
	return historyDigest(msgs)
}

// TestTurnKeyHistory 历史摘要：开头相同、中途走向不同的两段对话问同一句话不再撞键；
// 同一轮内追加 assistant/tool、历史里图片 URL 换签都不改变键。
func TestTurnKeyHistory(t *testing.T) {
	a := TurnKey([]byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"写个脚本"},{"role":"assistant","content":"用 Python"},{"role":"user","content":"继续"}]}`))
	b := TurnKey([]byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"写个脚本"},{"role":"assistant","content":"用 Go"},{"role":"user","content":"继续"}]}`))
	if a == "" || a == b {
		t.Fatalf("diverged histories should differ: a=%q b=%q", a, b)
	}
	step := TurnKey([]byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"写个脚本"},{"role":"assistant","content":"用 Python"},{"role":"user","content":"继续"},{"role":"assistant","tool_calls":[{"id":"c1"}]},{"role":"tool","content":"ok"}]}`))
	if step != a {
		t.Errorf("agent step within the turn should keep the key: %q vs %q", step, a)
	}
	img := func(url string) string {
		return TurnKey([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"` + url + `"}}]},{"role":"assistant","content":"是猫"},{"role":"user","content":"什么颜色"}]}`))
	}
	if img("https://x/a.png?sig=1") != img("https://x/a.png?sig=2") {
		t.Error("re-signed image url in history should not change the key")
	}
}

// TestTurnRequestIDDerivation 轮级 ID：同键恒同值、异键异值、空键每次新值、形态恒 32 hex。
func TestTurnRequestIDDerivation(t *testing.T) {
	a1 := TurnRequestID("u0:同一个问题")
	a2 := TurnRequestID("u0:同一个问题")
	if a1 != a2 {
		t.Errorf("same turn key should derive same id: %q vs %q", a1, a2)
	}
	if b := TurnRequestID("u0:另一个问题"); b == a1 {
		t.Errorf("different turn keys should derive different ids: %q", b)
	}
	// 同文本但序号不同（不同轮里内容相同的提问）也要分开。
	if c := TurnRequestID("u2:同一个问题"); c == a1 {
		t.Errorf("same text at different position should differ: %q", c)
	}
	// 空键：无轮可聚合 → 每次新值（保持原有请求级独立行为）。
	if e1, e2 := TurnRequestID(""), TurnRequestID(""); e1 == e2 {
		t.Errorf("empty turn key should yield fresh values each call: %q", e1)
	}
	for _, id := range []string{a1, TurnRequestID(""), TurnRequestID("x")} {
		if len(id) != 32 {
			t.Errorf("TurnRequestID value %q len=%d want 32", id, len(id))
		}
	}
}

// TestParseConversation 认对话用的摘要：开头只看第一条 system 与第一条 user；没有助手回复时
// Seed 为空；有了之后多轮不变；第一条回复不同则不同；工具调用只看 id 与函数名，参数重新序列化
// 不影响；空回复往后找。
func TestParseConversation(t *testing.T) {
	seed := func(s string) string { return ParseConversation([]byte(s)).Seed }
	first := `{"messages":[{"role":"system","content":"S"},{"role":"user","content":"你好"}]}`
	turn2 := `{"messages":[{"role":"system","content":"S"},{"role":"user","content":"你好"},{"role":"assistant","content":"回复A"},{"role":"user","content":"继续"}]}`
	turn3 := `{"messages":[{"role":"system","content":"S"},{"role":"user","content":"你好"},{"role":"assistant","content":"回复A"},{"role":"user","content":"继续"},{"role":"assistant","content":"x"},{"role":"user","content":"再继续"}]}`
	other := `{"messages":[{"role":"system","content":"S"},{"role":"user","content":"你好"},{"role":"assistant","content":"回复B"},{"role":"user","content":"继续"}]}`
	c1 := ParseConversation([]byte(first))
	if c1.Seed != "" || c1.Opener == "" || c1.Messages != 2 {
		t.Fatalf("第一轮应只有开头: %+v", c1)
	}
	c2, c3, co := ParseConversation([]byte(turn2)), ParseConversation([]byte(turn3)), ParseConversation([]byte(other))
	if c2.Seed == "" || c2.Seed != c3.Seed || c2.Messages != 4 || c3.Messages != 6 {
		t.Fatalf("同一段对话多轮应不变: %+v / %+v", c2, c3)
	}
	if c1.Opener != c2.Opener || c2.Opener != co.Opener {
		t.Fatal("开头相同时 Opener 应相同，不管后面的回复")
	}
	if co.Seed == c2.Seed {
		t.Fatal("第一条回复不同应得到不同的 Seed")
	}
	if ParseConversation([]byte(`{"messages":[{"role":"system","content":"T"},{"role":"user","content":"你好"}]}`)).Opener == c1.Opener {
		t.Fatal("系统提示词不同时开头应不同")
	}
	toolA := `{"messages":[{"role":"user","content":"查天气"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"北京\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"晴"}]}`
	toolB := `{"messages":[{"role":"user","content":"查天气"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{ \"city\": \"北京\" }"}}]},{"role":"tool","tool_call_id":"call_1","content":"晴"}]}`
	toolC := `{"messages":[{"role":"user","content":"查天气"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_2","type":"function","function":{"name":"weather","arguments":"{}"}}]}]}`
	if a, b := seed(toolA), seed(toolB); a == "" || a != b {
		t.Fatalf("工具参数重新序列化不应改变 Seed: %q / %q", a, b)
	}
	if seed(toolA) == seed(toolC) {
		t.Fatal("工具调用 id 不同应得到不同的 Seed")
	}
	emptyReply := `{"messages":[{"role":"user","content":"你好"},{"role":"assistant","content":""},{"role":"user","content":"在吗"},{"role":"assistant","content":"在"}]}`
	if seed(emptyReply) == "" {
		t.Fatal("空回复应往后找有内容的那条")
	}
	for _, in := range []string{`{"messages":[{"role":"assistant","content":"先回复"},{"role":"user","content":"再提问"}]}`, `{"messages":[{"role":"user","content":"a"},{"role":"assistant","tool_calls":[{"id":"","function":{"name":""}}]}]}`} {
		if c := ParseConversation([]byte(in)); c.Seed != "" || c.Opener == "" {
			t.Errorf("%q 应只有开头，got %+v", in, c)
		}
	}
	for _, in := range []string{"", "not json", `{"messages":[{"role":"system","content":"S"}]}`, `{"messages":[{"role":"user","content":""}]}`} {
		if c := ParseConversation([]byte(in)); c.Opener != "" || c.Seed != "" {
			t.Errorf("%q 认不出对话，应为空，got %+v", in, c)
		}
	}
}
