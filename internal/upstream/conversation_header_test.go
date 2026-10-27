// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"net/http"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
)

// TestChatHeadersConversationID 官方对话请求每条都带 X-Conversation-ID，国际版上游不带一律
// 400 code=11128（10-19 生产实测）。调用方给了就原样发（入站对话由 handler 按账号定好，见
// session/convids.go）；没给就新生成一个（官方新会话的形状）。两域相同。
func TestChatHeadersConversationID(t *testing.T) {
	cases := []struct {
		name     string
		cn       bool
		meta     ChatMeta
		wantConv string // "=new" 表示新生成的会话 id（带连字符的 UUID v7）
	}{
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
			if want == "=new" {
				if !upstreamtest.UUIDv7.MatchString(got) || (tc.meta.ConversationRequestID == "" && !upstreamtest.UUIDv7Hex.MatchString(turn)) {
					t.Fatalf("X-Conversation-ID = %q、轮次 id = %q，应是官方新会话的形状", got, turn)
				}
				return
			}
			if got != want || present != (want != "") {
				t.Fatalf("X-Conversation-ID = %q（发了=%v）, want %q", got, present, want)
			}
		})
	}
}
