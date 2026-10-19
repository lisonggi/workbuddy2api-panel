package upstream

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestSSEIsEmptyStreamError 空流错误哨兵可被 errors.Is 识别，其他错误不算。
func TestSSEIsEmptyStreamError(t *testing.T) {
	if _, err := Aggregate(strings.NewReader("data: [DONE]\n\n")); !errors.Is(err, errEmptyStream) {
		t.Fatalf("只有 [DONE] 应返回空流错误，got %v", err)
	}
	if !IsEmptyStreamError(errEmptyStream) {
		t.Error("IsEmptyStreamError(errEmptyStream) 应为 true")
	}
	if IsEmptyStreamError(errors.New("other")) {
		t.Error("其他错误不应识别为空流")
	}
}

// TestNum64 num64 的数值类型归一。
func TestNum64(t *testing.T) {
	cases := []struct {
		v    any
		want float64
		ok   bool
	}{
		{1.5, 1.5, true},
		{int64(2), 2, true},
		{3, 3, true},
		{"4", 0, false},
		{nil, 0, false},
	}
	for _, tc := range cases {
		got, ok := num64(tc.v)
		if got != tc.want || ok != tc.ok {
			t.Errorf("num64(%v) = %v/%v，应为 %v/%v", tc.v, got, ok, tc.want, tc.ok)
		}
	}
}

// TestEnsureUsageTotal 缺 total_tokens 时补 prompt+completion；已有/缺项/非法
// 时原样返回（同一 map）。
func TestEnsureUsageTotal(t *testing.T) {
	withTotal := map[string]any{"total_tokens": 9.0}
	if got := ensureUsageTotal(withTotal); got["total_tokens"] != 9.0 {
		t.Errorf("已有 total 应原样返回: %v", got)
	}

	usage := map[string]any{"prompt_tokens": 10.0, "completion_tokens": 5.0}
	got := ensureUsageTotal(usage)
	if got["total_tokens"] != 15.0 {
		t.Errorf("total = %v，应为 15", got["total_tokens"])
	}
	if _, mutated := usage["total_tokens"]; mutated {
		t.Error("不应修改原 usage map")
	}

	for _, u := range []map[string]any{
		{"prompt_tokens": 10.0},
		{"completion_tokens": 5.0},
		{"prompt_tokens": "10", "completion_tokens": 5.0},
	} {
		got := ensureUsageTotal(u)
		if _, ok := got["total_tokens"]; ok {
			t.Errorf("缺项/非法时不应合成 total: %v", got)
		}
	}
}

// TestMergeToolCallDelta id/type/name 覆盖语义与 arguments 拼接语义。
func TestMergeToolCallDelta(t *testing.T) {
	merged := map[string]any{"id": "old", "type": "function", "function": map[string]any{"name": "old", "arguments": "a"}}
	mergeToolCallDelta(merged, map[string]any{
		"id": "new", "type": "function",
		"function": map[string]any{"name": "tool", "arguments": "b"},
	})
	if merged["id"] != "new" || merged["type"] != "function" {
		t.Errorf("id/type 应覆盖: %v", merged)
	}
	fn := merged["function"].(map[string]any)
	if fn["name"] != "tool" || fn["arguments"] != "ab" {
		t.Errorf("function = %v，应为 name=tool arguments=ab", fn)
	}

	// 无 function 片段：只更新 id/type 即返回。
	onlyID := map[string]any{"id": "x"}
	mergeToolCallDelta(onlyID, map[string]any{"id": "y", "function": "bad"})
	if onlyID["id"] != "y" {
		t.Errorf("id 未更新: %v", onlyID)
	}

	// 累计对象没有 function 时创建。
	created := map[string]any{}
	mergeToolCallDelta(created, map[string]any{"function": map[string]any{"arguments": "{}"}})
	if created["function"].(map[string]any)["arguments"] != "{}" {
		t.Errorf("应创建 function: %v", created)
	}
}

// TestStripToolCallNames 每个 index 只在首片保留 function.name；缺 index 按 0
// 处理；非 tool_calls 形态零影响、不 panic。
func TestStripToolCallNamesEdges(t *testing.T) {
	frame := func(tcs []any) map[string]any {
		return map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
			"tool_calls": tcs,
		}}}}
	}
	name := func(tc map[string]any) any {
		return tc["function"].(map[string]any)["name"]
	}
	seen := map[int]bool{}

	first := map[string]any{"index": 0.0, "function": map[string]any{"name": "Bash"}}
	stripToolCallNames(frame([]any{first}), seen)
	if name(first) != "Bash" {
		t.Error("首片应保留 name")
	}
	if !seen[0] {
		t.Error("首片应标记 seen")
	}

	second := map[string]any{"index": 0.0, "function": map[string]any{"name": "Bash"}}
	stripToolCallNames(frame([]any{second}), seen)
	if _, ok := second["function"].(map[string]any)["name"]; ok {
		t.Error("后续分片应删除 name 键")
	}

	other := map[string]any{"index": 1.0, "function": map[string]any{"name": "Read"}}
	stripToolCallNames(frame([]any{other}), seen)
	if name(other) != "Read" {
		t.Error("不同 index 互不串扰")
	}

	// 缺 index 按 0 处理：seen[0] 已置位 → 删除 name。
	noIndex := map[string]any{"function": map[string]any{"name": "X"}}
	stripToolCallNames(frame([]any{noIndex}), map[int]bool{0: true})
	if _, ok := noIndex["function"].(map[string]any)["name"]; ok {
		t.Error("缺 index 应按 0 并删除 name")
	}

	// 非 tool_calls 形态零影响。
	stripToolCallNames(map[string]any{"choices": "bad"}, seen)
	stripToolCallNames(map[string]any{"choices": []any{42}}, seen)
	stripToolCallNames(map[string]any{"choices": []any{map[string]any{"delta": "bad"}}}, seen)
	stripToolCallNames(frame([]any{42}), seen)
	stripToolCallNames(frame([]any{map[string]any{"index": 2.0}}), seen)
}

// TestNormalizeFrameWhitelist 帧白名单重建：未知字段剔除、缺省字段补默认、
// 空 delta 字段省略、function_call 占位剔除、usage 规范化与 null。
func TestNormalizeFrameWhitelist(t *testing.T) {
	obj := map[string]any{
		"id": "x1", "object": "chat.completion.chunk", "created": 1.0, "model": "m",
		"unknown_field": "drop",
		"choices": []any{
			42, // 非对象 choice 跳过
			map[string]any{
				"index": 0.0,
				"delta": map[string]any{
					"role": "assistant", "content": "", "reasoning_content": "r",
					"refusal": "", "tool_calls": []any{},
					"function_call": map[string]any{"name": "", "arguments": ""},
				},
				"finish_reason": "",
			},
			map[string]any{
				"index": 1.0,
				"delta": map[string]any{
					"content": "hi", "tool_calls": []any{map[string]any{"index": 0.0}},
					"function_call": map[string]any{"name": "f"},
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{"cached_tokens": 3.0},
	}
	out := normalizeFrame(obj)
	if _, ok := out["unknown_field"]; ok {
		t.Error("未知字段应剔除")
	}
	if out["object"] != "chat.completion.chunk" || out["id"] != "x1" {
		t.Errorf("标准字段 = %v/%v", out["object"], out["id"])
	}
	chs := out["choices"].([]any)
	if len(chs) != 2 {
		t.Fatalf("choices = %d，应为 2", len(chs))
	}
	c0 := chs[0].(map[string]any)
	d0 := c0["delta"].(map[string]any)
	if _, ok := d0["content"]; ok {
		t.Error("空 content 应省略")
	}
	if d0["role"] != "assistant" || d0["reasoning_content"] != "r" {
		t.Errorf("delta = %v", d0)
	}
	if _, ok := d0["tool_calls"]; ok {
		t.Error("空 tool_calls 应省略")
	}
	if _, ok := d0["function_call"]; ok {
		t.Error("空占位 function_call 应剔除")
	}
	if c0["finish_reason"] != nil {
		t.Errorf("空 finish_reason 应为 nil，got %v", c0["finish_reason"])
	}
	c1 := chs[1].(map[string]any)
	if c1["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v", c1["finish_reason"])
	}
	d1 := c1["delta"].(map[string]any)
	if d1["content"] != "hi" || len(d1["tool_calls"].([]any)) != 1 || d1["function_call"].(map[string]any)["name"] != "f" {
		t.Errorf("delta1 = %v", d1)
	}
	usage := out["usage"].(map[string]any)
	if usage["cached_tokens"] != 3.0 || usage["cache_read_input_tokens"] != 3.0 {
		t.Errorf("usage 应规范化: %v", usage)
	}

	// 缺省 id/object/choices/usage。
	bare := normalizeFrame(map[string]any{})
	if bare["id"] != "chatcmpl-wb2api" || bare["object"] != "chat.completion.chunk" {
		t.Errorf("缺省字段 = %v", bare)
	}
	if _, ok := bare["choices"]; ok {
		t.Error("无 choices 时不应造空数组")
	}
	if v, ok := bare["usage"]; !ok || v != nil {
		t.Errorf("缺 usage 应为 null，got %v/%v", v, ok)
	}

	// usage 非对象时原样透传；function_call 非对象时保留。
	odd := normalizeFrame(map[string]any{
		"usage": "bad",
		"choices": []any{map[string]any{"delta": map[string]any{
			"function_call": "raw",
		}}},
	})
	if odd["usage"] != "bad" {
		t.Errorf("非对象 usage 应透传: %v", odd["usage"])
	}
	if odd["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["function_call"] != "raw" {
		t.Error("非对象 function_call 应保留")
	}
}

// TestFrameGatewayHintFunc hint 函数：nil、TrimSpace、panic 隔离。
func TestFrameGatewayHintFunc(t *testing.T) {
	if got := frameGatewayHint(nil, "payload"); got != "" {
		t.Errorf("nil hintFn 应为空，got %q", got)
	}
	if got := frameGatewayHint(func(string) string { return "  hint  " }, "p"); got != "hint" {
		t.Errorf("应 TrimSpace，got %q", got)
	}
	if got := frameGatewayHint(func(string) string { panic("boom") }, "p"); got != "" {
		t.Errorf("panic 应隔离为空，got %q", got)
	}
}

// TestAttachHintToErrorFrame 只给带 error 对象的合法 JSON 附加 gateway_hint，
// 其他形态原样返回。
func TestAttachHintToErrorFrame(t *testing.T) {
	raw := `{"error":{"code":6004,"message":"rate limited","requestId":"r1"}}`
	out := attachHintToErrorFrame(raw, "retry later")
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("输出不是 JSON: %v", err)
	}
	e := obj["error"].(map[string]any)
	if e["gateway_hint"] != "retry later" || e["message"] != "rate limited" || e["requestId"] != "r1" {
		t.Errorf("error 对象 = %v", e)
	}

	for _, payload := range []string{`not-json`, `{"error":"string"}`, `{}`} {
		if got := attachHintToErrorFrame(payload, "h"); got != payload {
			t.Errorf("形态 %q 应原样返回，got %q", payload, got)
		}
	}
}

// TestTakeFinishReason 取出首个非空 finish_reason 并从所有 choice 删除。
func TestTakeFinishReason(t *testing.T) {
	obj := map[string]any{"choices": []any{
		42,
		map[string]any{"finish_reason": ""},
		map[string]any{"finish_reason": "stop"},
		map[string]any{"finish_reason": "length"},
	}}
	if got := takeFinishReason(obj); got != "stop" {
		t.Errorf("取到 %q，应为 stop", got)
	}
	for _, ci := range obj["choices"].([]any) {
		c, ok := ci.(map[string]any)
		if !ok {
			continue
		}
		if _, exists := c["finish_reason"]; exists {
			t.Errorf("finish_reason 应被删除: %v", c)
		}
	}
}

// TestRewriteFinishReason 只改写已有且为 stop 的 finish_reason。
func TestRewriteFinishReason(t *testing.T) {
	obj := map[string]any{"choices": []any{
		42,
		map[string]any{"finish_reason": "stop"},
		map[string]any{"finish_reason": "length"},
		map[string]any{},
	}}
	if !rewriteFinishReason(obj, "tool_calls") {
		t.Fatal("stop 应被改写")
	}
	chs := obj["choices"].([]any)
	if chs[1].(map[string]any)["finish_reason"] != "tool_calls" {
		t.Errorf("改写后 = %v", chs[1])
	}
	if chs[2].(map[string]any)["finish_reason"] != "length" {
		t.Error("length 不应改写")
	}
	if _, ok := chs[3].(map[string]any)["finish_reason"]; ok {
		t.Error("无 finish_reason 不应凭空造")
	}

	if rewriteFinishReason(map[string]any{"choices": []any{map[string]any{"finish_reason": "length"}}}, "stop") {
		t.Error("无 stop 时不应报告改写")
	}
}

// TestMarkupFrames 标记修复构造帧：tool_call 帧与收尾帧复制 created/model 模板字段。
func TestMarkupFrames(t *testing.T) {
	src := map[string]any{"created": 7.0, "model": "m"}
	call := markupCallFrame(src, 2, MarkupCall{Name: "Bash", Arguments: `{"cmd":"ls"}`})
	if call["object"] != "chat.completion.chunk" || call["created"] != 7.0 || call["model"] != "m" {
		t.Errorf("call frame = %v", call)
	}
	tcs := call["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["tool_calls"].([]any)
	tc := tcs[0].(map[string]any)
	if tc["index"] != float64(2) || tc["id"] == "" || tc["type"] != "function" {
		t.Errorf("tool_call = %v", tc)
	}
	fn := tc["function"].(map[string]any)
	if fn["name"] != "Bash" || fn["arguments"] != `{"cmd":"ls"}` {
		t.Errorf("function = %v", fn)
	}

	fin := finishOnlyFrame(src, "tool_calls")
	if fin["created"] != 7.0 || fin["model"] != "m" {
		t.Errorf("finish frame = %v", fin)
	}
	choice := fin["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" || len(choice["delta"].(map[string]any)) != 0 {
		t.Errorf("finish choice = %v", choice)
	}

	// 模板缺字段时不补。
	empty := finishOnlyFrame(map[string]any{}, "stop")
	if _, ok := empty["created"]; ok {
		t.Errorf("无 created 不应补: %v", empty)
	}
}

// TestAggregateMessageShape 非 delta 的整条 message 并入聚合，之后的 delta 照常追加；
// 已取到正文后再来的整条 message 不再并入；usage 缺 total 自动合成；id/created 缺省合成。
func TestAggregateMessageShape(t *testing.T) {
	raw := "data: {\"choices\":[{\"index\":0,\"message\":{" +
		`"role":"assistant","content":"hello","reasoning_content":"think",` +
		`"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"A","arguments":"{}"}}]` +
		`},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"}}]}\n\n" +
		"data: [DONE]\n\n"
	resp, err := Aggregate(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	id, _ := resp["id"].(string)
	if !strings.HasPrefix(id, "chatcmpl-") {
		t.Errorf("缺省 id = %q", id)
	}
	if created, _ := resp["created"].(int64); created <= 0 {
		t.Errorf("缺省 created = %v", resp["created"])
	}
	choice := resp["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	if msg["role"] != "assistant" || msg["reasoning_content"] != "think" {
		t.Errorf("message = %v", msg)
	}
	// latch 只拦之后的整条 message 帧，delta 帧是新内容，照常追加。
	if msg["content"] != "hello world" {
		t.Errorf("content = %v，应为 hello world", msg["content"])
	}
	if tcs, ok := msg["tool_calls"].([]map[string]any); !ok || len(tcs) != 1 || tcs[0]["id"] != "call_a" {
		t.Errorf("tool_calls = %v", msg["tool_calls"])
	}
	usage := resp["usage"].(map[string]any)
	if usage["total_tokens"] != 15.0 {
		t.Errorf("usage total = %v，应为 15", usage["total_tokens"])
	}

	// delta 已取过正文后，后续帧里的整条 message 被 latch 拒绝。
	raw2 := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"message\":{\"content\":\"b\"}}]}\n\n" +
		"data: [DONE]\n\n"
	resp2, err := Aggregate(strings.NewReader(raw2))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	msg2 := resp2["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if msg2["content"] != "a" {
		t.Errorf("delta 取过正文后 message 应被拒绝，content = %v", msg2["content"])
	}
}

// TestAggregateIndexlessToolCalls 缺 index 的 tool_call 归位：带 index 的占用
// 槽位后新 id 开新号、无 id 碎片延续最近调用；choices 非数组时不 panic。
func TestAggregateIndexlessToolCalls(t *testing.T) {
	raw := "data: {\"id\":\"r1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[" +
		`{"index":0,"id":"call_a","type":"function","function":{"name":"A","arguments":"{\"a\":1"}}` +
		`]}}]}` + "\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[" +
		`{"function":{"arguments":",\"b\":2}"}}` +
		`]}}]}` + "\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[" +
		`{"id":"call_b","function":{"name":"B","arguments":"{}"}}` +
		`]}}]}` + "\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[" +
		`{"id":"call_b","function":{"arguments":""}}` +
		`]}}]}` + "\n\n" +
		"data: {\"choices\":\"not-an-array\"}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	resp, err := Aggregate(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	msg := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	tcs, ok := msg["tool_calls"].([]map[string]any)
	if !ok || len(tcs) != 2 {
		t.Fatalf("tool_calls = %#v，应为 2 个", msg["tool_calls"])
	}
	fn0 := tcs[0]["function"].(map[string]any)
	if tcs[0]["id"] != "call_a" || fn0["arguments"] != `{"a":1,"b":2}` {
		t.Errorf("call_a = %v / %v", tcs[0], fn0)
	}
	fn1 := tcs[1]["function"].(map[string]any)
	if tcs[1]["id"] != "call_b" || fn1["name"] != "B" || fn1["arguments"] != "{}" {
		t.Errorf("call_b = %v / %v", tcs[1], fn1)
	}
	if tcs[0]["index"] != 0 || tcs[1]["index"] != 1 {
		t.Errorf("index 分配 = %v/%v", tcs[0]["index"], tcs[1]["index"])
	}
}
