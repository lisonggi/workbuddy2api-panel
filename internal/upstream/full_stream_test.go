// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// 第七轮补齐：sse.go、dsml.go、tool_pairing.go、sanitize.go 的剩余分支。

// fullWriter 可编程的 ResponseWriter：failAt 为第几次 Write 失败（1 起；0 表示永不失败）。
type fullWriter struct {
	header http.Header
	buf    bytes.Buffer
	failAt int
	writes int
}

func (w *fullWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *fullWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.failAt > 0 && w.writes == w.failAt {
		return 0, errors.New("write boom")
	}
	return w.buf.Write(p)
}

func (w *fullWriter) WriteHeader(int) {}
func (w *fullWriter) Flush()          {}

func fullDataFrame(t *testing.T, obj any) string {
	t.Helper()
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return "data: " + string(raw) + "\n\n"
}

func fullAggregate(t *testing.T, input string) map[string]any {
	t.Helper()
	resp, err := Aggregate(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	return resp
}

func fullMessage(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	choices, ok := resp["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("choices = %#v", resp["choices"])
	}
	ch, ok := choices[0].(map[string]any)
	if !ok {
		t.Fatalf("choice = %#v", choices[0])
	}
	msg, ok := ch["message"].(map[string]any)
	if !ok {
		t.Fatalf("message = %#v", ch["message"])
	}
	return msg
}

func TestFull7AggregateNonMapToolCall(t *testing.T) {
	resp := fullAggregate(t, fullDataFrame(t, map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{1}}}},
	})+"data: [DONE]\n\n")
	msg := fullMessage(t, resp)
	if _, has := msg["tool_calls"]; has {
		t.Fatalf("非对象 tool_call 不应进入结果: %+v", msg)
	}
}

func TestFull7AggregateIndexlessToolCallSkipsUsedSlot(t *testing.T) {
	input := fullDataFrame(t, map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
			map[string]any{"index": float64(0), "id": "call-a", "function": map[string]any{"name": "f", "arguments": "{}"}},
		}}}},
	}) + fullDataFrame(t, map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
			map[string]any{"id": "call-b", "function": map[string]any{"name": "g", "arguments": "{}"}},
		}}}},
	}) + "data: [DONE]\n\n"
	resp := fullAggregate(t, input)
	msg := fullMessage(t, resp)
	calls, ok := msg["tool_calls"].([]map[string]any)
	if !ok || len(calls) != 2 {
		t.Fatalf("tool_calls = %#v", msg["tool_calls"])
	}
	second := calls[1]["index"]
	if second != 1 && second != float64(1) {
		t.Fatalf("缺 index 的第二个调用应落到 1，实际 %v", calls[1]["index"])
	}
}

func TestFull7AggregateIndexlessNoID(t *testing.T) {
	input := fullDataFrame(t, map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
			map[string]any{"function": map[string]any{"name": "f", "arguments": "{}"}},
		}}}},
	}) + "data: [DONE]\n\n"
	resp := fullAggregate(t, input)
	msg := fullMessage(t, resp)
	calls, ok := msg["tool_calls"].([]map[string]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("tool_calls = %#v", msg["tool_calls"])
	}
	if idx := calls[0]["index"]; idx != 0 && idx != float64(0) {
		t.Fatalf("首个无 index 调用应落到 0，实际 %v", idx)
	}
}

func TestFull7AggregateReadError(t *testing.T) {
	if _, err := Aggregate(errReader{}); err == nil {
		t.Fatal("读流错误应原样返回")
	}
}

func TestFull7AggregateNonMapChoice(t *testing.T) {
	resp := fullAggregate(t, fullDataFrame(t, map[string]any{"choices": []any{1}})+"data: [DONE]\n\n")
	msg := fullMessage(t, resp)
	if got, _ := msg["content"].(string); got != "" {
		t.Fatalf("content = %q", got)
	}
}

func TestFull7AggregateDeltaReasoning(t *testing.T) {
	resp := fullAggregate(t, fullDataFrame(t, map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": "想一下"}}},
	})+"data: [DONE]\n\n")
	msg := fullMessage(t, resp)
	if got, _ := msg["reasoning_content"].(string); got != "想一下" {
		t.Fatalf("reasoning_content = %q", got)
	}
}

func TestFull7AggregateDropsTruncatedCallsWithoutDone(t *testing.T) {
	input := fullDataFrame(t, map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
			map[string]any{"index": float64(0), "id": "call-a", "function": map[string]any{"name": "f", "arguments": `{"x":`}},
		}}}},
	})
	resp := fullAggregate(t, input)
	msg := fullMessage(t, resp)
	if _, has := msg["tool_calls"]; has {
		t.Fatalf("无 [DONE] 的残缺参数应被丢弃: %+v", msg)
	}
}

func TestFull7StreamRefusalDelta(t *testing.T) {
	w := &fullWriter{}
	input := fullDataFrame(t, map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"refusal": "拒绝"}}},
	}) + "data: [DONE]\n\n"
	if err := Stream(w, strings.NewReader(input)); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !strings.Contains(w.buf.String(), `"refusal":"拒绝"`) {
		t.Fatalf("输出 = %q", w.buf.String())
	}
}

func TestFull7ToolCallIDObserverOption(t *testing.T) {
	var o streamOptions
	WithToolCallIDObserver(func(string) {})(&o)
	if o.onToolCallID == nil {
		t.Fatal("WithToolCallIDObserver 未写入回调")
	}
}

func TestFull7StreamWriteErrors(t *testing.T) {
	input := fullDataFrame(t, map[string]any{"choices": []any{}})
	t.Run("普通帧写失败", func(t *testing.T) {
		w := &fullWriter{failAt: 1}
		if err := Stream(w, strings.NewReader(input)); err == nil {
			t.Fatal("写失败应返回错误")
		}
	})
	t.Run("error 帧写失败", func(t *testing.T) {
		w := &fullWriter{failAt: 1}
		errInput := fullDataFrame(t, map[string]any{"error": map[string]any{"message": "x"}})
		if err := Stream(w, strings.NewReader(errInput)); err == nil {
			t.Fatal("写失败应返回错误")
		}
	})
	t.Run("非 JSON 帧写失败", func(t *testing.T) {
		w := &fullWriter{failAt: 1}
		if err := Stream(w, strings.NewReader("data: not-json\n\n")); err == nil {
			t.Fatal("写失败应返回错误")
		}
	})
	t.Run("注释行写失败", func(t *testing.T) {
		w := &fullWriter{failAt: 1}
		if err := Stream(w, strings.NewReader(": ping\n")); err == nil {
			t.Fatal("写失败应返回错误")
		}
	})
	t.Run("尾部 [DONE] 写失败", func(t *testing.T) {
		w := &fullWriter{failAt: 2}
		if err := Stream(w, strings.NewReader(input)); err == nil {
			t.Fatal("写失败应返回错误")
		}
	})
}

func TestFull7StreamHintAttachesHint(t *testing.T) {
	w := &fullWriter{}
	input := fullDataFrame(t, map[string]any{"error": map[string]any{"message": "x"}})
	hint := func(string) string { return "看看余额" }
	if err := StreamHint(w, strings.NewReader(input), hint); err != nil {
		t.Fatalf("StreamHint: %v", err)
	}
	if !strings.Contains(w.buf.String(), `"gateway_hint":"看看余额"`) {
		t.Fatalf("输出 = %q", w.buf.String())
	}
}

func TestFull7StreamNonJSONFramePassthrough(t *testing.T) {
	w := &fullWriter{}
	input := "data: not-json\n\n" + fullDataFrame(t, map[string]any{"choices": []any{}}) + "data: [DONE]\n\n"
	if err := Stream(w, strings.NewReader(input)); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !strings.Contains(w.buf.String(), "data: not-json") {
		t.Fatalf("非 JSON 帧应原样透传: %q", w.buf.String())
	}
}

func TestFull7StreamChunkShapeEdges(t *testing.T) {
	cases := []struct {
		name string
		obj  any
	}{
		{"choice 非对象", map[string]any{"choices": []any{1}}},
		{"delta 缺失", map[string]any{"choices": []any{map[string]any{}}}},
		{"tool_call 非对象", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{1}}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &fullWriter{}
			if err := Stream(w, strings.NewReader(fullDataFrame(t, tc.obj)+"data: [DONE]\n\n")); err != nil {
				t.Fatalf("Stream: %v", err)
			}
			if !strings.Contains(w.buf.String(), "data: ") {
				t.Fatalf("输出 = %q", w.buf.String())
			}
		})
	}
}

func TestFull7StreamToolCallIDObserver(t *testing.T) {
	var got []string
	w := &fullWriter{}
	input := fullDataFrame(t, map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
			map[string]any{"index": float64(0), "id": "call_1"},
		}}}},
	}) + "data: [DONE]\n\n"
	err := StreamHint(w, strings.NewReader(input), nil, WithToolCallIDObserver(func(id string) { got = append(got, id) }))
	if err != nil {
		t.Fatalf("StreamHint: %v", err)
	}
	if len(got) != 1 || got[0] != "call_1" {
		t.Fatalf("观察到的 id = %v", got)
	}
}

func TestFull7StreamReadError(t *testing.T) {
	if err := Stream(&fullWriter{}, errReader{}); err == nil {
		t.Fatal("读流错误应返回")
	}
}

func TestFull7StreamRepairShapeEdges(t *testing.T) {
	repair := func() StreamOption { return WithMarkupRepair(NewMarkupRepair(testTools, false)) }
	t.Run("无 choices", func(t *testing.T) {
		w := &fullWriter{}
		if err := StreamHint(w, strings.NewReader(fullDataFrame(t, map[string]any{"foo": "bar"})+"data: [DONE]\n\n"), nil, repair()); err != nil {
			t.Fatalf("StreamHint: %v", err)
		}
	})
	t.Run("choice 非对象", func(t *testing.T) {
		w := &fullWriter{}
		if err := StreamHint(w, strings.NewReader(fullDataFrame(t, map[string]any{"choices": []any{1}})+"data: [DONE]\n\n"), nil, repair()); err != nil {
			t.Fatalf("StreamHint: %v", err)
		}
	})
	t.Run("delta 非对象", func(t *testing.T) {
		w := &fullWriter{}
		if err := StreamHint(w, strings.NewReader(fullDataFrame(t, map[string]any{"choices": []any{map[string]any{"delta": "x"}}})+"data: [DONE]\n\n"), nil, repair()); err != nil {
			t.Fatalf("StreamHint: %v", err)
		}
	})
}

func TestFull7StreamRepairFinishStop(t *testing.T) {
	block := mkBlock(mkInvoke("exec_command", mkParam("cmd", "pwd")))
	w := &fullWriter{}
	input := fullDataFrame(t, map[string]any{
		"choices": []any{map[string]any{
			"delta":         map[string]any{"content": "调用：" + block},
			"finish_reason": "stop",
		}},
	}) + "data: [DONE]\n\n"
	if err := StreamHint(w, strings.NewReader(input), nil, WithMarkupRepair(NewMarkupRepair(testTools, false))); err != nil {
		t.Fatalf("StreamHint: %v", err)
	}
	out := w.buf.String()
	if !strings.Contains(out, `"tool_calls"`) {
		t.Fatalf("应还原出 tool_calls: %q", out)
	}
	if !strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Fatalf("stop 应改写成 tool_calls: %q", out)
	}
}

func TestFull7StreamRepairTailFinish(t *testing.T) {
	block := mkBlock(mkInvoke("exec_command", mkParam("cmd", "pwd")))
	w := &fullWriter{}
	input := fullDataFrame(t, map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": block}}},
	}) + "data: [DONE]\n\n"
	if err := StreamHint(w, strings.NewReader(input), nil, WithMarkupRepair(NewMarkupRepair(testTools, false))); err != nil {
		t.Fatalf("StreamHint: %v", err)
	}
	if !strings.Contains(w.buf.String(), `"finish_reason":"tool_calls"`) {
		t.Fatalf("流尾应补 tool_calls 收尾: %q", w.buf.String())
	}
}

func TestFull7StreamRepairTailFinishWriteError(t *testing.T) {
	// 正文帧 + 还原出的调用帧各写一次，尾部补的收尾帧是第三次写（失败）。
	w := &fullWriter{failAt: 3}
	block := mkBlock(mkInvoke("exec_command", mkParam("cmd", "pwd")))
	input := fullDataFrame(t, map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": block}}},
	}) + "data: [DONE]\n\n"
	if err := StreamHint(w, strings.NewReader(input), nil, WithMarkupRepair(NewMarkupRepair(testTools, false))); err == nil {
		t.Fatal("尾部收尾帧写失败应返回错误")
	}
}

func TestFull7StreamRepairTailWriteError(t *testing.T) {
	// 正文 "<" 被修复器扣住，流尾 Flush 回吐；第二次写失败。
	w := &fullWriter{failAt: 2}
	input := fullDataFrame(t, map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": "<"}}},
	}) + "data: [DONE]\n\n"
	if err := StreamHint(w, strings.NewReader(input), nil, WithMarkupRepair(NewMarkupRepair(testTools, false))); err == nil {
		t.Fatal("尾部写失败应返回错误")
	}
}

// --- dsml.go ---------------------------------------------------------------

func TestFull7MarkupCountersNil(t *testing.T) {
	var r *MarkupRepair
	if r.Converted() != 0 || r.Seen() != 0 || r.Rejected() != 0 {
		t.Fatal("nil 修复器计数应为 0")
	}
}

func TestFull7MarkupOversizeBlock(t *testing.T) {
	r := NewMarkupRepair(testTools, false)
	text := markupOpen + strings.Repeat("x", markupMaxBlockBytes+1)
	emit, calls := r.Feed(text)
	if len(calls) != 0 {
		t.Fatalf("超长块不应还原调用: %+v", calls)
	}
	if emit != markupOpen+strings.Repeat("x", markupMaxBlockBytes+1) {
		t.Fatalf("超长块应原文回吐，实际长度 %d", len(emit))
	}
}

func TestFull7MarkupParseRejections(t *testing.T) {
	invokeOpen := "<" + markupPipe + "DSML" + markupPipe + ` invoke name="exec_command">`
	paramOpen := "<" + markupPipe + "DSML" + markupPipe + ` parameter name="cmd" string="true">`
	cases := []struct {
		name string
		in   string
	}{
		{"invoke 未闭合", markupOpen + invokeOpen + "x" + markupClose},
		{"块内没有 invoke", markupOpen + "  " + markupClose},
		{"invoke 后夹带正文", markupOpen + mkInvoke("exec_command", mkParam("cmd", "pwd")) + "尾巴" + markupClose},
		{"parameter 前夹带正文", markupOpen + invokeOpen + "垃圾" + paramOpen + "v" + markupParamClose + markupInvokeClose + markupClose},
		{"parameter 未闭合", markupOpen + invokeOpen + paramOpen + "v" + markupInvokeClose + markupClose},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewMarkupRepair(testTools, false)
			emit, calls := r.Feed(tc.in)
			if len(calls) != 0 {
				t.Fatalf("不应还原调用: %+v", calls)
			}
			if emit != tc.in {
				t.Fatalf("应原文回吐:\n got %q\nwant %q", emit, tc.in)
			}
		})
	}
}

func TestFull7RepairAggregatedResponseEdges(t *testing.T) {
	t.Run("choices 非数组", func(t *testing.T) {
		resp := map[string]any{"choices": "x"}
		r := RepairAggregatedResponse(resp, testTools, false)
		if r == nil || r.Converted() != 0 {
			t.Fatalf("r = %+v", r)
		}
	})
	t.Run("choice 非对象", func(t *testing.T) {
		resp := map[string]any{"choices": []any{1}}
		RepairAggregatedResponse(resp, testTools, false)
		if _, has := resp["tool_calls"]; has {
			t.Fatal("非对象 choice 不应产生 tool_calls")
		}
	})
	t.Run("message 非对象", func(t *testing.T) {
		resp := map[string]any{"choices": []any{map[string]any{"message": "x"}}}
		RepairAggregatedResponse(resp, testTools, false)
	})
	t.Run("有分隔符但无完整块", func(t *testing.T) {
		content := "讨论 " + markupPipe + " 这段语法"
		resp := map[string]any{"choices": []any{map[string]any{
			"message":       map[string]any{"content": content},
			"finish_reason": "stop",
		}}}
		RepairAggregatedResponse(resp, testTools, false)
		ch := resp["choices"].([]any)[0].(map[string]any)
		msg := ch["message"].(map[string]any)
		if msg["content"] != content {
			t.Fatalf("无完整块时正文不应改写: %q", msg["content"])
		}
		if _, has := msg["tool_calls"]; has {
			t.Fatal("无完整块不应产生 tool_calls")
		}
	})
}

// --- tool_pairing.go -------------------------------------------------------

func fullToolCallMsg(id string) map[string]any {
	return map[string]any{
		"role": "assistant",
		"tool_calls": []any{
			map[string]any{"id": id, "function": map[string]any{"name": "f"}},
		},
	}
}

func TestFull7RepackToolResultBlockEdges(t *testing.T) {
	t.Run("扫描到非对象", func(t *testing.T) {
		in := []any{fullToolCallMsg("c1"), "junk", map[string]any{"role": "user", "content": "x"}}
		if out, changed := repackToolResultBlocks(in); changed || len(out) != 3 {
			t.Fatalf("out=%v changed=%v", out, changed)
		}
	})
	t.Run("工具结果不属于本批", func(t *testing.T) {
		in := []any{fullToolCallMsg("c1"), map[string]any{"role": "tool", "tool_call_id": "c2"}, map[string]any{"role": "user", "content": "x"}}
		if _, changed := repackToolResultBlocks(in); changed {
			t.Fatal("孤儿工具结果不应触发重排")
		}
	})
	t.Run("assistant 后没有结果", func(t *testing.T) {
		in := []any{fullToolCallMsg("c1"), map[string]any{"role": "user", "content": "x"}, map[string]any{"role": "user", "content": "y"}}
		if _, changed := repackToolResultBlocks(in); changed {
			t.Fatal("没有工具结果不应触发重排")
		}
	})
}

func TestFull7CleanupOrphanEdges(t *testing.T) {
	malformed := []any{
		"junk",
		map[string]any{"role": "assistant", "content": "hi"},
		map[string]any{"role": "assistant", "tool_calls": []any{
			float64(1),
			map[string]any{"id": "c1", "function": map[string]any{"name": "f"}},
		}},
		map[string]any{"role": "tool", "tool_call_id": "c1"},
		map[string]any{"role": "tool", "tool_call_id": "orphan"},
	}
	out, changed := cleanupOrphanToolCalls(malformed)
	if !changed {
		t.Fatal("应删除孤儿工具结果")
	}
	if len(out) != 4 {
		t.Fatalf("out = %#v", out)
	}
}

func TestFull7MergeReasoningContent(t *testing.T) {
	dst := map[string]any{}
	mergeReasoningContent(dst, map[string]any{"reasoning_content": "想"})
	if dst["reasoning_content"] != "想" {
		t.Fatalf("dst = %#v", dst)
	}
}

func TestFull7EmptyContentEdges(t *testing.T) {
	if emptyContent(123) {
		t.Fatal("数字不应判为空内容")
	}
	if emptyContent(map[string]any{}) {
		t.Fatal("对象不应判为空内容")
	}
}

func TestFull7ToolCallNameEmptyNonString(t *testing.T) {
	if toolCallNameEmpty(map[string]any{"function": map[string]any{"name": 5}}) {
		t.Fatal("name 非字符串不应判空")
	}
	if !toolCallNameEmpty(map[string]any{"function": map[string]any{"name": "  "}}) {
		t.Fatal("空白 name 应判空")
	}
}

// --- sanitize.go -----------------------------------------------------------

func TestFull7SanitizeContentNonMapPart(t *testing.T) {
	v := []any{float64(1), map[string]any{"text": "11128"}}
	out, changed := sanitizeContent(v)
	if !changed {
		t.Fatal("文本 part 应被净化")
	}
	parts := out.([]any)
	if parts[1].(map[string]any)["text"] != "11-128" {
		t.Fatalf("text = %q", parts[1].(map[string]any)["text"])
	}
}

func TestFull7SanitizeToolCallEdges(t *testing.T) {
	if sanitizeToolCalls("x") {
		t.Fatal("非数组应返回 false")
	}
	if sanitizeToolCalls([]any{"x"}) {
		t.Fatal("非对象调用应跳过")
	}
	if sanitizeToolCalls([]any{map[string]any{"function": "x"}}) {
		t.Fatal("function 非对象应跳过")
	}
	if sanitizeToolCalls([]any{map[string]any{"function": map[string]any{"arguments": float64(1)}}}) {
		t.Fatal("arguments 非字符串应跳过")
	}
	got := sanitizeToolCalls([]any{map[string]any{"function": map[string]any{"arguments": "11128"}}})
	if !got {
		t.Fatal("字符串 arguments 应被净化")
	}
}

func TestFull7SanitizeMessagesNonObject(t *testing.T) {
	if sanitizeMessages([]any{"x"}) {
		t.Fatal("非对象消息应跳过")
	}
}

func TestFull7SanitizeReasoningContent(t *testing.T) {
	msgs := []any{map[string]any{"reasoning_content": "11128"}}
	if !sanitizeMessages(msgs) {
		t.Fatal("reasoning_content 应被净化")
	}
	if got := msgs[0].(map[string]any)["reasoning_content"]; got != "11-128" {
		t.Fatalf("reasoning_content = %q", got)
	}
}
