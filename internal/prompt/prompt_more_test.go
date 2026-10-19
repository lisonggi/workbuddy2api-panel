package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestLoadEveryMode 每种 mode 在 file 为空时都拿到内置默认提示词（Load 只取文本，
// 路由语义不在本函数）。
func TestLoadEveryMode(t *testing.T) {
	for _, mode := range []string{"custom", "passthrough", "", "unknown-mode"} {
		got, err := Load(mode, "")
		if err != nil || got != defaultPrompt {
			t.Errorf("Load(%q, \"\") = %d 字节/%v，应为内置默认", mode, len(got), err)
		}
	}
}

// TestLoadEmptyFile 文件存在但为空：返回空串且不报错（调用方自行决定语义）。
func TestLoadEmptyFile(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(fp, nil, 0o600); err != nil {
		t.Fatalf("建空文件: %v", err)
	}
	got, err := Load("custom", fp)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != "" {
		t.Errorf("空文件 = %q，应为空串", got)
	}
}

func decodeBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("解码: %v (%s)", err, raw)
	}
	return obj
}

// TestRewriteNoMessagesField 无 messages 字段：插入仅含 system 的 messages，其余字段保留。
func TestRewriteNoMessagesField(t *testing.T) {
	in := []byte(`{"model":"glm-5.2","stream":true}`)
	out := Rewrite(in, "SYS")
	obj := decodeBody(t, out)
	if obj["model"] != "glm-5.2" || obj["stream"] != true {
		t.Errorf("其他字段应保留: %v", obj)
	}
	msgs := obj["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", msgs)
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "SYS" {
		t.Errorf("插入的 system = %v", first)
	}
}

// TestRewriteMessagesWrongType messages 类型不符：整体替换为仅 system。
func TestRewriteMessagesWrongType(t *testing.T) {
	in := []byte(`{"messages":"not-an-array","keep":1}`)
	obj := decodeBody(t, Rewrite(in, "SYS"))
	msgs, ok := obj["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages = %v", obj["messages"])
	}
	if obj["keep"] != float64(1) {
		t.Errorf("其他字段应保留: %v", obj)
	}
}

// TestRewriteKeepsNonMapMessage 非对象消息原样保留在 system 之后。
func TestRewriteKeepsNonMapMessage(t *testing.T) {
	in := []byte(`{"messages":[42,{"role":"user","content":"hi"}]}`)
	out := Rewrite(in, "SYS")
	obj := decodeBody(t, out)
	msgs := obj["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %v", msgs)
	}
	if msgs[1] != float64(42) {
		t.Errorf("非对象消息应原样保留: %v", msgs[1])
	}
	if msgs[2].(map[string]any)["role"] != "user" {
		t.Errorf("user 消息应保留在后: %v", msgs[2])
	}
}

// TestAppendGuards 空 body、空提示词、坏 JSON、无 messages、messages 非数组的守卫
// 与 Rewrite 一致。
func TestAppendGuards(t *testing.T) {
	in := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	if out := Append(nil, "SYS"); len(out) != 0 {
		t.Errorf("空 body 应原样返回: %s", out)
	}
	if out := Append(in, ""); string(out) != string(in) {
		t.Errorf("空提示词应原样返回: %s", out)
	}
	bad := []byte(`{not json`)
	if out := Append(bad, "SYS"); string(out) != string(bad) {
		t.Errorf("坏 JSON 应原样返回: %s", out)
	}

	obj := decodeBody(t, Append([]byte(`{"model":"m"}`), "SYS"))
	msgs, ok := obj["messages"].([]any)
	if !ok || len(msgs) != 1 || obj["model"] != "m" {
		t.Errorf("无 messages 应插入单条 system 并保留其他字段: %v", obj)
	}
	obj = decodeBody(t, Append([]byte(`{"messages":{"x":1}}`), "SYS"))
	if msgs, ok := obj["messages"].([]any); !ok || len(msgs) != 1 {
		t.Errorf("messages 非数组应替换: %v", obj["messages"])
	}
}

// TestAppendAfterLeadingBlock 在开头连续 system/developer 块之后插入，既有消息逐字不动。
func TestAppendAfterLeadingBlock(t *testing.T) {
	in := []byte(`{"messages":[
		{"role":"system","content":"项目规范"},
		{"role":"developer","content":"工具约定"},
		{"role":"system","content":"第二条规范"},
		{"role":"user","content":"你好"},
		{"role":"system","content":"中途 system"}
	]}`)
	out := Append(in, "网关提示词")
	obj := decodeBody(t, out)
	msgs := obj["messages"].([]any)
	if len(msgs) != 6 {
		t.Fatalf("messages = %d，应为 6", len(msgs))
	}
	wantRoles := []string{"system", "developer", "system", "system", "user", "system"}
	for i, want := range wantRoles {
		mm := msgs[i].(map[string]any)
		if mm["role"] != want {
			t.Errorf("msgs[%d].role = %v，应为 %v", i, mm["role"], want)
		}
	}
	gw := msgs[3].(map[string]any)
	if gw["content"] != "网关提示词" {
		t.Errorf("网关消息 = %v", gw)
	}
	// 既有消息内容不动。
	if msgs[0].(map[string]any)["content"] != "项目规范" ||
		msgs[1].(map[string]any)["content"] != "工具约定" ||
		msgs[4].(map[string]any)["content"] != "你好" ||
		msgs[5].(map[string]any)["content"] != "中途 system" {
		t.Errorf("既有消息被改动: %v", msgs)
	}
}

// TestAppendNoLeadingBlock 首条不是 system/developer 时插到最前。
func TestAppendNoLeadingBlock(t *testing.T) {
	out := Append([]byte(`{"messages":[{"role":"user","content":"hi"}]}`), "SYS")
	if roles := systemRoles(t, out); !reflect.DeepEqual(roles, []string{"system", "user"}) {
		t.Errorf("roles = %v", roles)
	}
}

// TestAppendStopsAtNonMapOrNoRole 开头块遇到非对象消息或无 role 消息即停。
func TestAppendStopsAtNonMapOrNoRole(t *testing.T) {
	out := Append([]byte(`{"messages":[42,{"role":"user","content":"hi"}]}`), "SYS")
	obj := decodeBody(t, out)
	msgs := obj["messages"].([]any)
	if len(msgs) != 3 || msgs[0].(map[string]any)["content"] != "SYS" || msgs[1] != float64(42) {
		t.Errorf("非对象首条应停在 0: %v", msgs)
	}

	out = Append([]byte(`{"messages":[{"content":"no-role"},{"role":"system","content":"s"}]}`), "SYS")
	if roles := systemRoles(t, out); !reflect.DeepEqual(roles, []string{"system", "", "system"}) {
		t.Errorf("无 role 首条应停在 0（新 system 在最前），roles = %v", roles)
	}

	// system 之后遇到非对象消息：插入点止于 system 块。
	out = Append([]byte(`{"messages":[{"role":"system","content":"s"},42]}`), "SYS")
	obj = decodeBody(t, out)
	msgs = obj["messages"].([]any)
	if len(msgs) != 3 || msgs[1].(map[string]any)["content"] != "SYS" || msgs[2] != float64(42) {
		t.Errorf("块尾插入 = %v", msgs)
	}
}
