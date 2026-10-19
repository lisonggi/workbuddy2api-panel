package session

import (
	"fmt"
	"testing"
	"time"
)

const (
	openerBody = `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"你好"}]}`
	// stepBody 第一轮的工具调用续轮：assistant 回复带工具调用 callID、参数 args。
	stepTmpl = `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"你好"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"%s","type":"function","function":{"name":"bash","arguments":%s}}]},` +
		`{"role":"tool","tool_call_id":"%s","content":"ok"}%s]}`
)

func stepBody(callID, args, more string) []byte {
	return []byte(fmt.Sprintf(stepTmpl, callID, args, callID, more))
}

// TestParseTurn 开轮 / 轮内后续请求的区分、工具调用 id 提取、回复摘要在轮内稳定。
func TestParseTurn(t *testing.T) {
	op := ParseTurn([]byte(openerBody))
	if !op.Opening || op.Key != "u1:你好" || len(op.CallIDs) != 0 {
		t.Fatalf("开轮请求: %+v", op)
	}
	s1 := ParseTurn(stepBody("call_a", `"{\"cmd\":\"ls\"}"`, ""))
	if s1.Opening || s1.Key != op.Key || len(s1.CallIDs) != 1 || s1.CallIDs[0] != "call_a" || s1.reply == "" {
		t.Fatalf("续轮: %+v", s1)
	}
	// 轮内再往后追加消息：第一条回复不变 → 摘要不变。
	s2 := ParseTurn(stepBody("call_a", `"{\"cmd\":\"ls\"}"`,
		`,{"role":"assistant","tool_calls":[{"id":"call_b","function":{"name":"bash","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_b","content":"ok"}`))
	if s2.Key != s1.Key || s2.reply != s1.reply || s2.CallIDs[0] != "call_a" {
		t.Errorf("同一轮的后续请求应保持同一锚点: %+v vs %+v", s2, s1)
	}
	// arguments 是对象（有的客户端这么发）也不能让整个 body 解析失败。
	obj := ParseTurn(stepBody("call_a", `{"cmd":"ls"}`, ""))
	if obj.Key != op.Key || len(obj.CallIDs) != 1 {
		t.Errorf("对象形态的 arguments: %+v", obj)
	}
	if other := ParseTurn(stepBody("call_a", `"{\"cmd\":\"pwd\"}"`, "")); other.reply == s1.reply {
		t.Error("参数不同的回复摘要应不同")
	}
	if z := ParseTurn([]byte(`{"messages":[{"role":"system","content":"s"}]}`)); z.Key != "" || z.Opening {
		t.Errorf("没有 user 消息应返回零值: %+v", z)
	}
}

// TestTurnIDsIdenticalOpeners 两个逐字相同的开轮请求拿到不同的轮 ID，各自的续轮凭工具调用 id
// 找回自己那一轮；记录丢了（重启）就按回复摘要派生，仍互不相同、轮内稳定。
func TestTurnIDsIdenticalOpeners(t *testing.T) {
	ids := NewTurnIDs()
	op := ParseTurn([]byte(openerBody))
	a, b := ids.ForRequest("d-x", op), ids.ForRequest("d-x", op)
	if a == "" || a == b {
		t.Fatalf("逐字相同的开轮请求应各拿一个 ID: %q %q", a, b)
	}
	ids.Remember("d-x", op, a, []string{"call_a"})
	ids.Remember("d-x", op, b, []string{"call_b"})
	stepA := ParseTurn(stepBody("call_a", `"{}"`, ""))
	stepB := ParseTurn(stepBody("call_b", `"{}"`, ""))
	if got := ids.ForRequest("d-x", stepA); got != a {
		t.Errorf("A 的续轮应找回 A 的轮 ID: got %q want %q", got, a)
	}
	if got := ids.ForRequest("d-x", stepB); got != b {
		t.Errorf("B 的续轮应找回 B 的轮 ID: got %q want %q", got, b)
	}
	// 别的会话键下同一个工具调用 id 找不回（命名空间隔离），走派生。
	if got := ids.ForRequest("d-y", stepA); got == a {
		t.Error("不同会话键不应共享记录")
	}

	fresh := NewTurnIDs() // 网关重启
	da, db := fresh.ForRequest("d-x", stepA), fresh.ForRequest("d-x", stepB)
	if da == db || da == a {
		t.Errorf("找不回时按回复派生，应互不相同: %q %q", da, db)
	}
	if again := fresh.ForRequest("d-x", stepA); again != da {
		t.Error("派生值在轮内应稳定")
	}
	var nilIDs *TurnIDs
	if got := nilIDs.ForRequest("d-x", stepA); got != da {
		t.Error("nil 表只做派生")
	}
	nilIDs.Remember("d-x", op, a, []string{"call_a"}) // 不 panic
}

// TestTurnIDsCollidingCallIDs 两段对话的回复撞了工具调用 id（按序号命名的模型）：作废这个 id，
// 续轮改走派生，不把别人的轮 ID 安上来。
func TestTurnIDsCollidingCallIDs(t *testing.T) {
	ids := NewTurnIDs()
	op := ParseTurn([]byte(openerBody))
	a, b := ids.ForRequest("", op), ids.ForRequest("", op)
	ids.Remember("", op, a, []string{"functions.bash:0"})
	ids.Remember("", op, b, []string{"functions.bash:0"})
	ids.Remember("", op, "c", []string{"functions.bash:0"}) // 作废后不会被第三个复活
	stepA := ParseTurn(stepBody("functions.bash:0", `"{\"cmd\":\"ls\"}"`, ""))
	stepB := ParseTurn(stepBody("functions.bash:0", `"{\"cmd\":\"pwd\"}"`, ""))
	ga, gb := ids.ForRequest("", stepA), ids.ForRequest("", stepB)
	if ga == a || ga == b || ga == "c" || gb == a || gb == b {
		t.Errorf("撞 id 时不应找回任何一方的轮 ID: %q %q", ga, gb)
	}
	if ga == gb {
		t.Error("回复参数不同，派生值应不同")
	}
	// 同一个开轮请求重复记同一个 ID 不算撞。
	op2 := ParseTurn([]byte(`{"messages":[{"role":"user","content":"别的"}]}`))
	ids.Remember("", op2, "x", []string{"call_x"})
	ids.Remember("", op2, "x", []string{"call_x"})
	if got := ids.ForRequest("", ParseTurn([]byte(`{"messages":[{"role":"user","content":"别的"},{"role":"assistant","tool_calls":[{"id":"call_x"}]}]}`))); got != "x" {
		t.Errorf("重复记同一 ID 后应仍能找回: %q", got)
	}
	// 非开轮请求、空 ID、空 id 列表都不记。
	step := ParseTurn(stepBody("call_n", `"{}"`, ""))
	ids.Remember("", step, "n", []string{"call_n"})
	ids.Remember("", op, "", []string{"call_m"})
	if _, ok := ids.links[turnLinkKey("", step.Key, "call_n")]; ok {
		t.Error("续轮请求的回复不应记录")
	}
	if _, ok := ids.links[turnLinkKey("", op.Key, "call_m")]; ok {
		t.Error("空 ID 不应记录")
	}
}

// TestTurnIDsExpiryAndEviction 记录按最后一次找回续期；超过条数上限先清过期的，再随机清到九成。
func TestTurnIDsExpiryAndEviction(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ids := NewTurnIDs()
	ids.now = func() time.Time { return now }
	op := ParseTurn([]byte(openerBody))
	step := ParseTurn(stepBody("call_a", `"{}"`, ""))
	ids.Remember("", op, "a", []string{"call_a"})

	now = now.Add(defaultTurnLinkTTL - time.Minute)
	if ids.ForRequest("", step) != "a" {
		t.Fatal("未过期应找回")
	}
	now = now.Add(defaultTurnLinkTTL - time.Minute) // 上次找回已续期
	if ids.ForRequest("", step) != "a" {
		t.Fatal("找回应续期")
	}
	now = now.Add(defaultTurnLinkTTL)
	if ids.ForRequest("", step) == "a" {
		t.Fatal("过期后不应找回")
	}

	ids.max = 10
	ids.Remember("", op, "a", []string{"call_a"})
	now = now.Add(defaultTurnLinkTTL) // 让 call_a 过期
	for i := 0; i < 20; i++ {
		ids.Remember("", op, fmt.Sprintf("id%d", i), []string{fmt.Sprintf("call_%d", i)})
		if len(ids.links) > ids.max {
			t.Fatalf("记录数 %d 超过上限 %d", len(ids.links), ids.max)
		}
	}
	if _, ok := ids.links[turnLinkKey("", op.Key, "call_a")]; ok {
		t.Error("过期记录应先被清掉")
	}
	if got := ids.ForRequest("", ParseTurn(stepBody("call_19", `"{}"`, ""))); got != "id19" {
		t.Errorf("刚记下的应还在: %q", got)
	}
}
