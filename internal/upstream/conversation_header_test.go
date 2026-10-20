package upstream

import (
	"net/http"
	"testing"
)

// TestChatHeadersConversationID 国际版上游不带 X-Conversation-ID 一律 400 code=11128（10-19 生产实测）：
// 客户端没给会话 id 时用本轮的轮次 id 补上，给了就原样透传。国内版仍是客户端没给就不发。
func TestChatHeadersConversationID(t *testing.T) {
	cases := []struct {
		name     string
		cn       bool
		meta     ChatMeta
		wantConv string // "=turn" 表示等于本轮的轮次 id
	}{
		{"国际版没给会话 id，用轮次 id 补上", false, ChatMeta{ConversationRequestID: "0123456789abcdef0123456789abcdef"}, "=turn"},
		{"国际版连轮次 id 也没有，补一个新的两头同值", false, ChatMeta{}, "=turn"},
		{"国际版客户端给了会话 id，原样透传", false, ChatMeta{ConversationID: "conv-client", ConversationRequestID: "0123456789abcdef0123456789abcdef"}, "conv-client"},
		{"国内版没给会话 id，不发", true, ChatMeta{ConversationRequestID: "0123456789abcdef0123456789abcdef"}, ""},
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
			want := tc.wantConv
			if want == "=turn" {
				want = turn
			}
			got, present := req.Header.Get("X-Conversation-ID"), len(req.Header.Values("X-Conversation-ID")) > 0
			if got != want || present != (want != "") {
				t.Fatalf("X-Conversation-ID = %q（发了=%v）, want %q", got, present, want)
			}
		})
	}
}
