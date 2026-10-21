// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// turns.go 客户端没给会话 id 时，给每个对话轮分配 X-Conversation-Request-ID。
//
// 轮级键（TurnKey）只看请求内容。两段不相干的对话到某一轮为止逐字相同时（最常见的是
// 首轮：同样的系统提示词、同样的第一句话；中转站的渠道测试请求也是这样），轮级键相同，
// 从它派生的 ID 也相同，上游会把两段对话的这一轮并成一条记录。逐字相同的请求从内容上
// 分不开，所以开轮请求的 ID 不再从内容派生：
//   - 开轮请求（最后一条是 user 消息）每次新生成随机 ID。网关把回复转给客户端时，
//     记下回复里每个工具调用 id 对应这个 ID（Remember）。
//   - 轮内后续请求（user 之后已经有这一轮的回复）按这条回复里的工具调用 id 找回开轮时的
//     ID（ForRequest）。工具调用 id 是上游（或网关还原工具调用标记时）为这次回复生成的，
//     两段对话一般不会相同。
//   - 找不回时（网关重启、记录过期、回复里没有工具调用 id、两个开轮请求的回复里出现了
//     同一个 id）按轮级键 + 这条回复的摘要派生：仍与别的对话不同，只是和开轮请求分成两条记录。
//
// 客户端给了 conversation_id 时不走这里：会话 id 已经区分了对话，沿用 TurnRequestID 纯派生。
package session

import (
	"sync"
	"time"
)

const (
	// defaultTurnLinkTTL 一条记录在最后一次被找回后保留多久，要盖住一轮里最长的工具调用间隔。
	defaultTurnLinkTTL = 2 * time.Hour
	// defaultTurnLinkMax 记录条数上限（每条约百来字节），超了先清过期的，再随机清掉一成。
	defaultTurnLinkMax = 50000
)

// TurnIDs 开轮请求的回复里的工具调用 id → 这一轮的 ID。并发安全；nil 时只做派生。
type TurnIDs struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	links map[string]turnLink
	now   func() time.Time
}

// turnLink id 为空表示作废：两个开轮请求的回复里出现了同一个工具调用 id，分不清后续请求属于哪一轮。
type turnLink struct {
	id  string
	exp time.Time
}

// NewTurnIDs 用默认的保留时长与条数上限建表。
func NewTurnIDs() *TurnIDs {
	return &TurnIDs{ttl: defaultTurnLinkTTL, max: defaultTurnLinkMax, links: map[string]turnLink{}, now: time.Now}
}

func turnLinkKey(scope, key, callID string) string {
	return scope + "\x00" + key + "\x00" + callID
}

// ForRequest 返回这个请求该用的轮 ID。scope 是会话键（派生的或空），只作命名空间；
// turn.Key 为空时返回 ""，由调用方兜底。
func (t *TurnIDs) ForRequest(scope string, turn Turn) string {
	if turn.Key == "" {
		return ""
	}
	if turn.Opening {
		return NewUUIDv7Hex() // 同官方开轮时 generateConversationRequestId
	}
	if t != nil && len(turn.CallIDs) > 0 {
		now := t.now()
		t.mu.Lock()
		for _, cid := range turn.CallIDs {
			k := turnLinkKey(scope, turn.Key, cid)
			if l, ok := t.links[k]; ok && l.id != "" && now.Before(l.exp) {
				l.exp = now.Add(t.ttl)
				t.links[k] = l
				t.mu.Unlock()
				return l.id
			}
		}
		t.mu.Unlock()
	}
	return TurnRequestID(scope + ":" + turn.Key + "\x00" + turn.reply)
}

// Remember 记下开轮请求的回复里出现的工具调用 id 对应的轮 ID。非开轮请求、没有 id 时不记。
func (t *TurnIDs) Remember(scope string, turn Turn, id string, callIDs []string) {
	if t == nil || turn.Key == "" || !turn.Opening || id == "" || len(callIDs) == 0 {
		return
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.links)+len(callIDs) > t.max {
		t.evictLocked(now)
	}
	for _, cid := range callIDs {
		if cid == "" {
			continue
		}
		k := turnLinkKey(scope, turn.Key, cid)
		if l, ok := t.links[k]; ok && now.Before(l.exp) && l.id != id {
			// 有的模型按序号给工具调用命名（如 "functions.bash:0"），两段对话的回复可能撞 id：
			// 作废这个 id，后续请求改走派生，不把别人的轮 ID 安到这段对话上。
			t.links[k] = turnLink{exp: now.Add(t.ttl)}
			continue
		}
		t.links[k] = turnLink{id: id, exp: now.Add(t.ttl)}
	}
}

// evictLocked 清掉过期记录；仍然超限就随机清掉一部分（map 遍历顺序随机），降到上限的九成。
func (t *TurnIDs) evictLocked(now time.Time) {
	for k, l := range t.links {
		if !now.Before(l.exp) {
			delete(t.links, k)
		}
	}
	for k := range t.links {
		if len(t.links) <= t.max*9/10 {
			break
		}
		delete(t.links, k)
	}
}
