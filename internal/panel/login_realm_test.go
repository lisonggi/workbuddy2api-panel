package panel

import (
	"testing"
)

// TestLoginEndpoints realm→端点映射：global 三端点全在 workbuddy.ai 域，
// cn/空/非法值 → www.workbuddy.cn（零回归兜底）。
func TestLoginEndpoints(t *testing.T) {
	cases := []struct {
		realm      string
		wantState  string
		wantOrigin string
	}{
		{"global", "https://www.workbuddy.ai/v2/plugin/auth/state?platform=CLI", "https://www.workbuddy.ai"},
		{"cn", "https://www.workbuddy.cn/v2/plugin/auth/state?platform=CLI", "https://www.codebuddy.cn"},
		{"", "https://www.workbuddy.cn/v2/plugin/auth/state?platform=CLI", "https://www.codebuddy.cn"},
		{"weird", "https://www.workbuddy.cn/v2/plugin/auth/state?platform=CLI", "https://www.codebuddy.cn"},
	}
	for _, c := range cases {
		t.Run(c.realm, func(t *testing.T) {
			st, tok, acct, origin := loginEndpoints(c.realm)
			if st != c.wantState {
				t.Errorf("realm=%q state=%q want %q", c.realm, st, c.wantState)
			}
			if origin != c.wantOrigin {
				t.Errorf("realm=%q origin=%q want %q", c.realm, origin, c.wantOrigin)
			}
			// token/account 端点必与 state 同 base。
			if c.realm == "global" {
				if tok[:len("https://www.workbuddy.ai")] != "https://www.workbuddy.ai" ||
					acct[:len("https://www.workbuddy.ai")] != "https://www.workbuddy.ai" {
					t.Errorf("global token/account endpoints should be workbuddy.ai: %q %q", tok, acct)
				}
			} else {
				if tok[:len("https://www.workbuddy.cn")] != "https://www.workbuddy.cn" ||
					acct[:len("https://www.workbuddy.cn")] != "https://www.workbuddy.cn" {
					t.Errorf("cn token/account endpoints should be www.workbuddy.cn: %q %q", tok, acct)
				}
			}
		})
	}
}
