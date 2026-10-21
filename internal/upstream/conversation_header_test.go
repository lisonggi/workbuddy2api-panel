// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"net/http"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/session"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
)

// TestChatHeadersConversationID 官方对话请求每条都带 X-Conversation-ID，国际版上游不带一律
// 400 code=11128（10-19 生产实测）。客户端给了就原样透传；没给时有派生源（历史里已有助手回复）就按
// 账号 + 派生源算出固定值，对话第一个请求没有派生源，新生成一个（官方新会话的形状）。两域相同。
func TestChatHeadersConversationID(t *testing.T) {
	const seed = "0123456789abcdef0123456789abcdef"
	cases := []struct {
		name     string
		cn       bool
		meta     ChatMeta
		wantConv string // "=new" 表示新生成的会话 id（带连字符的 UUID v7）；"=seed" 表示按账号 + 派生源算
	}{
		{"国内版有派生源，按账号算固定值", true, ChatMeta{ConversationSeed: seed, ConversationRequestID: "0123456789abcdef0123456789abcdef"}, "=seed"},
		{"国际版有派生源，按账号算固定值", false, ChatMeta{ConversationSeed: seed}, "=seed"},
		{"客户端给了会话 id 时不看派生源", true, ChatMeta{ConversationID: "conv-client", ConversationSeed: seed}, "conv-client"},
		{"国际版没给会话 id，新生成一个", false, ChatMeta{ConversationRequestID: "0123456789abcdef0123456789abcdef"}, "=new"},
		{"国际版连轮次 id 也没有，两个都新生成", false, ChatMeta{}, "=new"},
		{"国际版客户端给了会话 id，原样透传", false, ChatMeta{ConversationID: "conv-client", ConversationRequestID: "0123456789abcdef0123456789abcdef"}, "conv-client"},
		{"国内版没给会话 id，同样新生成", true, ChatMeta{ConversationRequestID: "0123456789abcdef0123456789abcdef"}, "=new"},
		{"国内版客户端给了会话 id，原样透传", true, ChatMeta{ConversationID: "conv-cn"}, "conv-cn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := globalAuth()
			if tc.cn {
				a = cnAuth()
			}
			req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
			(&Client{}).ChatHeaders(req, a, "", tc.meta)
			turn := req.Header.Get("X-Conversation-Request-ID")
			if turn == "" {
				t.Fatal("X-Conversation-Request-ID 必发")
			}
			if tc.meta.ConversationRequestID != "" && turn != tc.meta.ConversationRequestID {
				t.Fatalf("X-Conversation-Request-ID = %q, want %q", turn, tc.meta.ConversationRequestID)
			}
			got, present := req.Header.Get("X-Conversation-ID"), len(req.Header.Values("X-Conversation-ID")) > 0
			want := tc.wantConv
			switch want {
			case "=new":
				if !upstreamtest.UUIDv7.MatchString(got) || (tc.meta.ConversationRequestID == "" && !upstreamtest.UUIDv7Hex.MatchString(turn)) {
					t.Fatalf("X-Conversation-ID = %q、轮次 id = %q，应是官方新会话的形状", got, turn)
				}
				return
			case "=seed":
				want = session.AccountConversationID(a.UID, seed)
			}
			if got != want || present != (want != "") {
				t.Fatalf("X-Conversation-ID = %q（发了=%v）, want %q", got, present, want)
			}
		})
	}
}

// TestChatHeadersConversationIDPerAccount 同一派生源在同一个号上每次都一样（上游据此命中前缀缓存），
// 换了号就不同。
func TestChatHeadersConversationIDPerAccount(t *testing.T) {
	conv := func(a *ChatMeta, uid string) string {
		req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
		au := cnAuth()
		au.UID = uid
		(&Client{}).ChatHeaders(req, au, "", *a)
		return req.Header.Get("X-Conversation-ID")
	}
	meta := &ChatMeta{ConversationSeed: "seed-1"}
	first, again, other := conv(meta, "u-1"), conv(meta, "u-1"), conv(meta, "u-2")
	if first == "" || first != again {
		t.Fatalf("同号同派生源应得同一个 id：%q / %q", first, again)
	}
	if first == other {
		t.Fatalf("换号后 id 应不同：%q", other)
	}
}
