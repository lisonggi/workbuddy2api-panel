// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// ids.go 会话头族 ID 的解析与生成（issue #35 后台聚合）。
//
// 官方 CodeBuddy CLI 出站头族（X-Conversation-ID / X-Conversation-Request-ID /
// X-Request-ID / X-B3-*），后台按 X-Conversation-Request-ID（对话轮）聚合请求；
// 本文件提供 conversationId 提取、消息级 32 hex messageID、以及"同一会话键
// 稳定复用"的 conversationRequestID 惰性缓存，供 handler 轮转循环外生成、循环内
// 复用（换号/重试/降级全部同 ID → 后台不再碎片化）。
package session

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

// ResolveConversationID 从请求体提取会话头族的 conversationId（snake/camel 双形态，
// 复用 ExtractKey 的识别顺序：metadata 优先、snake 优先于 camel）。
// 与 ExtractKey 的差异：**只认 conversationId，绝不回落 user_id**——X-Conversation-ID
// 语义是"对话 ID"，user_id 回落会污染后台按对话聚合的判据。
// 缺失返回 ""（不伪造：透传客户端原值优先，客户端没给就不发）。
func ResolveConversationID(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return ""
	}
	if meta, ok := obj["metadata"].(map[string]any); ok {
		if v := strOrEmpty(meta["conversation_id"]); v != "" {
			return v
		}
		if v := strOrEmpty(meta["conversationId"]); v != "" {
			return v
		}
	}
	if v := strOrEmpty(obj["conversation_id"]); v != "" {
		return v
	}
	return strOrEmpty(obj["conversationId"])
}

// cryptoRandRead 供测试替换（默认 crypto/rand.Read），用于覆盖熵源故障回退。
var cryptoRandRead = cryptorand.Read

// NewMessageID 生成消息级 ID：32 位 hex（UUID v4 去横线的长度形态），对齐官方
// X-Request-ID / X-Conversation-Message-ID。crypto/rand 失败（理论上不可能）时回落
// math/rand/v2 双 uint64 拼 32 hex——恒 32 hex、恒合法，可安全用作 B3 TraceId。
func NewMessageID() string {
	b := make([]byte, 16)
	if _, err := cryptoRandRead(b); err == nil {
		return hex.EncodeToString(b)
	}
	// 熵源故障的极端兜底：仍保证 32 hex（fallbackID 不 panic、不空串）。
	return fmt.Sprintf("%016x%016x", uint64(rand.Uint64())|1, rand.Uint64())
}

// NewUUIDv7 官方内置 CLI 的 uuidv7()（RFC 9562 版本 7：前 48 位是毫秒时间戳，其余随机），带连字符
// 的 36 位形状。官方新会话的 id（X-Conversation-ID、事件里的 conversationId）就是它。
func NewUUIDv7() string { return dashUUID(NewUUIDv7Hex()) }

// dashUUID 32 位十六进制排成 8-4-4-4-12。
func dashUUID(h string) string {
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// NewUUIDv7Hex 去掉连字符的 UUID v7（32 位十六进制）。官方的轮次 id（generateConversationRequestId）
// 和模型消息 id（X-Conversation-Message-ID、事件里的 messageId）都是 uuidv7() 去掉连字符。
func NewUUIDv7Hex() string { return uuidV7Hex(time.Now()) }

func uuidV7Hex(t time.Time) string {
	var b [16]byte
	if _, err := cryptoRandRead(b[6:]); err != nil {
		binary.BigEndian.PutUint64(b[8:], rand.Uint64())
		binary.BigEndian.PutUint16(b[6:], uint16(rand.Uint32()))
	}
	ms := uint64(t.UnixMilli())
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	b[6] = 0x70 | b[6]&0x0f // 版本 7
	b[8] = 0x80 | b[8]&0x3f // RFC 9562 变体
	return hex.EncodeToString(b[:])
}

// RequestIDForKey 返回会话键的稳定 conversationRequestID：第一次见到这个键时生成的去掉连字符的
// UUID v7，记在 derivedIDs 里（见 derivedMemo）。
//   - 同 key：记录有效期内恒同值（一次 user send/同会话多轮聚合）；
//   - 异 key：各自独立，互不相同；
//   - 空 key：每次生成新值（无会话则无"会话内稳定"语义——调用方应在请求级
//     捕获复用，handler 在轮转循环外取一次即天然共享）。
//
// 返回值恒为 32 hex，可直接用作 B3 TraceId（16/32 hex 合法）。
func RequestIDForKey(key string) string {
	if key == "" {
		return NewUUIDv7Hex()
	}
	return derivedIDs.get("sess\x00"+key, false)
}

// Conversation 客户端没给会话 id 时，网关按内容认出同一段对话用的摘要（见 convids.go）。
type Conversation struct {
	// Opener 对话的开头：第一条 system/developer 的文本 + 第一条 user 消息的签名。没有 user 消息时为 ""。
	Opener string
	// Seed 开头再加第一条助手回复的摘要（角色、文本、各工具调用的 id 与函数名）。历史里还没有
	// 助手回复（对话的第一轮）时为 ""。
	Seed string
	// Messages 请求里的消息条数，未命中告警据此看出客户端是不是压缩或改写了历史。
	Messages int
}

// ParseConversation 从请求体取 Conversation，取不到的字段留空。
//
// 为什么还要第一条助手回复：只看开头的话，开头逐字相同的两段不相干对话（同一个应用的系统
// 提示词、首句都是「你好」）会认成同一段，上游看到的是一个会话里分叉出两条历史。第一条回复
// 几乎总不一样（工具调用 id 每次随机），有了它就能把这样的两段对话分开。工具调用只取 id 与
// 函数名、不取参数原文：参数是客户端回传的 JSON 字符串，个别客户端每轮重新序列化，入键会让
// 摘要漂移。
func ParseConversation(body []byte) Conversation {
	if len(body) == 0 {
		return Conversation{}
	}
	var obj struct {
		Messages []turnMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &obj); err != nil {
		return Conversation{}
	}
	c := Conversation{Messages: len(obj.Messages)}
	system, firstUser, reply := "", "", ""
	for _, m := range obj.Messages {
		switch m.Role {
		case "system", "developer":
			if system == "" {
				system = contentText(m.Content)
			}
		case "user":
			if firstUser == "" {
				firstUser = contentSignature(m.Content)
			}
		case "assistant":
			if firstUser != "" && reply == "" {
				reply = assistantDigest(m)
			}
		}
		if reply != "" {
			break
		}
	}
	if firstUser == "" {
		return c
	}
	sum := sha256.Sum256([]byte(system + "\x00" + firstUser))
	c.Opener = hex.EncodeToString(sum[:16])
	if reply != "" {
		sum = sha256.Sum256([]byte(system + "\x00" + firstUser + "\x00" + reply))
		c.Seed = hex.EncodeToString(sum[:16])
	}
	return c
}

// assistantDigest 助手消息的摘要：文本 + 每个工具调用的 id 与函数名。文本和工具调用都没有时返回 ""。
func assistantDigest(m turnMessage) string {
	var b strings.Builder
	b.WriteString(contentText(m.Content))
	for _, raw := range m.ToolCalls {
		var tc struct {
			ID       string `json:"id"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(raw, &tc) == nil {
			b.WriteString("\x00" + tc.ID + "\x00" + tc.Function.Name)
		}
	}
	if strings.Trim(b.String(), "\x00") == "" {
		return ""
	}
	return b.String()
}

// turnMessage ParseTurn 只需要的消息字段。tool_calls 逐条留原文，各家客户端的字段类型
// 不一（arguments 有字符串也有对象），整体按结构解析会让整个 body 解析失败。
type turnMessage struct {
	Role      string            `json:"role"`
	Content   json.RawMessage   `json:"content"`
	ToolCalls []json.RawMessage `json:"tool_calls"`
}

// Turn 一个请求在对话轮里的位置，供分配 X-Conversation-Request-ID（见 turns.go）。
type Turn struct {
	// Key 轮级键（规则见 TurnKey）；"" 表示建不出轮级键。
	Key string
	// Opening 开轮请求：最后一条 user 消息之后没有别的消息，即用户刚发出这一句。
	Opening bool
	// CallIDs 轮内后续请求：最后一条 user 消息之后第一条消息里的工具调用 id。
	CallIDs []string
	// reply 轮内后续请求：最后一条 user 消息之后第一条消息的摘要，找不回开轮 ID 时派生用。
	reply string
}

// TurnKey 派生「对话轮级」聚合键：body 里**最后一条** role=="user" 消息的
// 「序号 + 文本」。
//
// 为什么需要它：无会话键的客户端（OpenAI 兼容协议——dsh / Codex / Cherry Studio
// 等的请求体里既无 conversationId 也无 metadata 键）会让 ExtractKey 恒返回空串，
// 会话头族的聚合主键便只能逐请求新生成，agent 多轮在上游用量明细里仍是一条请求
// 一条记录。本函数给这类客户端一个**不依赖客户端配合**的轮级键：一次用户发送内的
// 所有上游调用（tool call 多轮 / 换号重试 / 降级重发）body 里最后一条 user 消息
// 恒定 → 同键；用户发下一条消息 → 换键。
//
// 为什么不取第一条 user 消息：首条在整个会话内不变，会把一次会话的所有轮并进
// 同一个聚合键（跨对话轮混并）。取最后一条才对齐官方 X-Conversation-Request-ID
// 的「对话轮」语义。序号一并入键：两次不同轮里内容相同的提问（"继续"）不会被并成
// 一轮。
//
// 这条 user 消息之前若还有历史（system/developer 之外的消息），再拼上历史摘要
// （见 historyDigest）：客户端没给会话 id 时会话键只由系统提示词 + 首条用户消息决定，
// 开头相同、中途走向不同的两段对话在同一位置问同一句话（"继续"）会撞成同一轮。
// 历史在一轮之内恒定（轮内只在末尾追加 assistant/tool），不影响轮内同键。
// 没有历史的首轮键形态不变。到某一轮为止逐字相同的两段对话（最常见的是首轮）键仍然相同，
// 客户端没给会话 id 时由 turns.go 另行区分。
//
// 无 body / 无 messages / 无 user 消息 / 该消息无文本 → ""（调用方回落请求级随机
// ID，不伪造聚合键）。
func TurnKey(body []byte) string {
	return ParseTurn(body).Key
}

// ParseTurn 解析请求所在的对话轮：轮级键（TurnKey）、是不是开轮请求，以及轮内后续请求
// 里这一轮第一条回复的工具调用 id 与摘要。建不出轮级键时返回零值。
func ParseTurn(body []byte) Turn {
	if len(body) == 0 {
		return Turn{}
	}
	var obj struct {
		Messages []turnMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &obj); err != nil {
		return Turn{}
	}
	for i := len(obj.Messages) - 1; i >= 0; i-- {
		if obj.Messages[i].Role != "user" {
			continue
		}
		sig := contentSignature(obj.Messages[i].Content)
		if sig == "" {
			// 最后一条 user 消息没有可签名内容（空/null/空 parts）→ 本轮不建立聚合键。
			// 不继续往前找：整轮内该消息位置恒定，往前找反而会让键随 step 漂移。
			return Turn{}
		}
		t := Turn{Key: fmt.Sprintf("u%d:%s", i, sig)}
		if h := historyDigest(obj.Messages[:i]); h != "" {
			t.Key = fmt.Sprintf("u%d:%s\x00%s", i, h, sig)
		}
		rest := obj.Messages[i+1:]
		if len(rest) == 0 {
			t.Opening = true
			return t
		}
		t.CallIDs, t.reply = replySignature(rest[0])
		return t
	}
	return Turn{}
}

// replySignature 取这一轮第一条回复的工具调用 id 和摘要（角色 + 文本 + 每个工具调用的原文）。
// 摘要在一轮之内恒定：轮内只在它后面追加消息。
func replySignature(m turnMessage) (callIDs []string, digest string) {
	h := sha256.New()
	h.Write([]byte(m.Role))
	h.Write([]byte{0})
	h.Write([]byte(contentText(m.Content)))
	for _, raw := range m.ToolCalls {
		h.Write([]byte{0})
		h.Write(raw)
		var tc struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &tc) == nil && tc.ID != "" {
			callIDs = append(callIDs, tc.ID)
		}
	}
	return callIDs, hex.EncodeToString(h.Sum(nil)[:8])
}

// historyDigest 末条 user 消息之前的历史摘要：逐条取「角色 + 文本」入哈希，返回前 8 字节 hex。
// 跳过 system/developer（派生的会话键已含系统提示词）；只取文本不取图片 part 的原文摘要，
// 图片 URL 逐轮重签不会让键漂移。没有可入键的历史返回 ""。
func historyDigest(msgs []turnMessage) string {
	h := sha256.New()
	n := 0
	for _, m := range msgs {
		if m.Role == "system" || m.Role == "developer" {
			continue
		}
		n++
		h.Write([]byte(m.Role))
		h.Write([]byte{0})
		h.Write([]byte(contentText(m.Content)))
		h.Write([]byte{0})
	}
	if n == 0 {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// contentText 取消息 content 的文本：字符串形态直接返回；数组形态（多模态 parts）
// 拼接各 part 的 text 字段。无文本（纯图片 / null / 未知形态）返回 ""。
func contentText(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	switch s[0] {
	case '"':
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return ""
		}
		return str
	case '[':
		var parts []struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &parts); err != nil {
			return ""
		}
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return ""
}

// contentSignature 取消息 content 的确定性签名（G1 修复——纯图片轮不再碎片化）：
//   - string 形态：返回文本，与 contentText 结果**完全一致**——纯文本路径键值
//     不变，存量会话的轮键/粘性键零漂移（向后兼容契约）；
//   - 数组形态（多模态 parts）：文本 part（type 为 "" 或 "text"，与 contentText
//     的拼接口径一致）按原文无缝拼接；非文本 part 追加 "[type:摘要]"——摘要取该
//     part 原始字节的 sha256 前 8 hex。data: base64 内联图可能超长，原文入键会
//     放大派生哈希压力（见 TurnKey 头注），只入短摘要；type + 原文摘要天然区分
//     不同内容/数量的非文本 part 集合，无需额外计数占位。
//
// 无可签名内容（空 / null / 空数组 / 纯文本 part 全为空串）返回 ""（不伪造——
// 调用方回落原有的空键语义）。TurnKey 与 firstUserText（粘性兜底）共用本函数，
// 两条链路的图片盲区一并修复。
func contentSignature(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	// 字符串形态：纯文本路径签名 == contentText 结果（键值零漂移）。
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return ""
		}
		return str
	}
	if s[0] != '[' {
		return ""
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	hasNonText := false
	for _, pr := range parts {
		var p struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(pr, &p); err != nil {
			return ""
		}
		if p.Type == "" || p.Type == "text" {
			// 文本 part：无缝拼接（与 contentText 完全同口径——全文本 part 的数组
			// 签名 == contentText 结果，存量键零漂移）。
			b.WriteString(p.Text)
			continue
		}
		// 非文本 part：type + 原文 sha256 前 8 hex（超长 data: URL 只入短摘要）。
		hasNonText = true
		sum := sha256.Sum256(pr)
		fmt.Fprintf(&b, "\n[%s:%s]\n", p.Type, hex.EncodeToString(sum[:4]))
	}
	out := b.String()
	if !hasNonText {
		return out
	}
	return strings.TrimSpace(out)
}

// TurnRequestID 返回轮级键对应的聚合 ID：第一次见到这个键时生成的去掉连字符的 UUID v7（同官方
// 开轮时的 generateConversationRequestId），记在 derivedIDs 里，可直接作 B3 TraceId。
// 空键返回新值（无轮可聚合时保持原有的「每请求独立」行为）。
func TurnRequestID(turnKey string) string {
	if turnKey == "" {
		return NewUUIDv7Hex()
	}
	return derivedIDs.get("turn\x00"+turnKey, false)
}

const (
	// derivedIDTTL 派生 id 的记录在最后一次被取用后保留多久，同轮次记录（defaultTurnLinkTTL）。
	// 只放轮次 id 一类，不存盘；出站会话 id 另有记录，见 convids.go。
	derivedIDTTL = 2 * time.Hour
	// derivedIDMax 记录条数上限（每条约百来字节），超了先清过期的，再随机清掉一成。
	derivedIDMax = 50000
)

// derivedMemo 按内容派生的键 → 第一次派生时生成的 UUID v7。官方的会话 id、轮次 id 在会话、轮次
// 开始时随机生成（前 48 位是当时的毫秒数），网关没有状态时只能按内容派生；为了 id 和官方同形、
// 时刻真实，第一次派生时现生成一个，之后同一个键取回同一个。键先取 sha256，条目大小固定。
type derivedMemo struct {
	mu  sync.Mutex
	m   map[[32]byte]derivedEntry
	now func() time.Time
}

type derivedEntry struct {
	id  string
	exp time.Time
}

var derivedIDs = &derivedMemo{m: map[[32]byte]derivedEntry{}, now: time.Now}

// get 取 key 的 id，没有（或已过期）就按当前时刻生成一个；取到就把有效期顺延。dashed 为真时是
// 带连字符的会话 id 形状，否则是 32 位十六进制。
func (d *derivedMemo) get(key string, dashed bool) string {
	k := sha256.Sum256([]byte(key))
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.m[k]; ok && now.Before(e.exp) {
		e.exp = now.Add(derivedIDTTL)
		d.m[k] = e
		return e.id
	}
	if len(d.m) >= derivedIDMax {
		d.evictLocked(now)
	}
	id := uuidV7Hex(now)
	if dashed {
		id = dashUUID(id)
	}
	d.m[k] = derivedEntry{id: id, exp: now.Add(derivedIDTTL)}
	return id
}

// evictLocked 清掉过期记录；仍然超限就随机清掉一部分（map 遍历顺序随机），降到上限的九成。
func (d *derivedMemo) evictLocked(now time.Time) {
	for k, e := range d.m {
		if !now.Before(e.exp) {
			delete(d.m, k)
		}
	}
	for k := range d.m {
		if len(d.m) <= derivedIDMax*9/10 {
			break
		}
		delete(d.m, k)
	}
}
