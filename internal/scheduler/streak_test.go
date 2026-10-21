// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package scheduler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
)

// streakReply 构造 streak 域成功信封。
func streakReply(data string) string { return `{"code":0,"msg":"ok","data":` + data + `}` }

// readReqBody 读取请求体（假上游断言 target_date/tier 用）。
func readReqBody(t *testing.T, r *http.Request) string {
	t.Helper()
	raw, _ := io.ReadAll(r.Body)
	return string(raw)
}

// TestCompactJSON 奖品载荷裁剪：短串原样，超 220 字节截断并加省略号，不切开多字节字符。
func TestCompactJSON(t *testing.T) {
	short := json.RawMessage(`{"prize":"50c"}`)
	if got := compactJSON(short); got != string(short) {
		t.Errorf("短载荷 = %q，应原样返回", got)
	}
	long := json.RawMessage(strings.Repeat("x", 300))
	got := compactJSON(long)
	if len(got) != 220+len("…") || !strings.HasSuffix(got, "…") || got[:220] != strings.Repeat("x", 220) {
		t.Errorf("长载荷长度 = %d，应为 220 字节 + 省略号", len(got))
	}
	// 每个汉字 3 字节，第 220 字节落在字中间：退回到 219，不留半个字
	cn := json.RawMessage(strings.Repeat("中", 100))
	got = compactJSON(cn)
	if !utf8.ValidString(got) || got != strings.Repeat("中", 73)+"…" {
		t.Errorf("中文载荷截断 = %q，应在字符边界截断", got)
	}
}

// TestStreakBonusFullFlow 单账号连登管家：只在可领档位兑换、按 chances 抽完；
// 锁定/已领档位跳过。
func TestStreakBonusFullFlow(t *testing.T) {
	var mu sync.Mutex
	var redeemTiers []string
	c := newSchedCounter()
	c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
		switch {
		case strings.HasSuffix(path, "/activity/growth/heatmap"):
			w.Write([]byte(streakReply(`{"cells":[]}`)))
			return true
		case strings.HasSuffix(path, "/billing/meter/check-gift-claimed"):
			// 网页先查礼包状态：开放且未领才允许领取。
			w.Write([]byte(streakReply(`{"active":true,"claimed":false}`)))
			return true
		case strings.HasSuffix(path, "/activity/growth/streak"):
			w.Write([]byte(streakReply(`{"makeup_cards":{"balance":0},"redemption_status":{
				"tier_7d_status":"available","tier_14d_status":"locked","tier_28d_status":"claimed",
				"tiers":[{"tier":"7d","credit":100,"energy":5,"cards":1,"chances":2},
					{"tier":"14d","credit":200},{"tier":"28d","credit":300}]}}`)))
			return true
		case strings.HasSuffix(path, "/activity/growth/redeem"):
			var body map[string]any
			json.Unmarshal([]byte(readReqBody(t, r)), &body)
			mu.Lock()
			redeemTiers = append(redeemTiers, body["tier"].(string))
			mu.Unlock()
			return false // 默认成功
		case strings.HasSuffix(path, "/lottery/summary"):
			w.Write([]byte(streakReply(`{"chances":2}`)))
			return true
		case strings.HasSuffix(path, "/lottery/draw"):
			w.Write([]byte(streakReply(`"` + strings.Repeat("p", 300) + `"`)))
			return true
		}
		return false
	}
	srv := c.server(t)
	s := schedWithPool(srv, pool.New(""), Config{})

	s.streakBonusAccount(&auth.Auth{UID: "u1", AccessToken: "at", Nickname: "n"})

	if len(redeemTiers) != 1 || redeemTiers[0] != "7d" {
		t.Errorf("兑换档位 = %v，应只有 7d", redeemTiers)
	}
	if got := c.count("/lottery/draw"); got != 2 {
		t.Errorf("抽奖 = %d 次，应为 2", got)
	}
	if got := c.count("/billing/meter/claim-gift"); got != 1 {
		t.Errorf("新手礼包应调用 1 次，got %d", got)
	}
}

// TestStreakBonusRedeemError 兑换失败只记日志，继续抽奖。
func TestStreakBonusRedeemError(t *testing.T) {
	c := newSchedCounter()
	c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
		switch {
		case strings.HasSuffix(path, "/activity/growth/streak"):
			w.Write([]byte(streakReply(`{"redemption_status":{"tiers":[{"tier":"7d","chances":0}]}}`)))
			return true
		case strings.HasSuffix(path, "/activity/growth/redeem"):
			w.WriteHeader(403)
			w.Write([]byte(`{"code":403,"msg":"连续登录天数不足"}`))
			return true
		case strings.HasSuffix(path, "/lottery/summary"):
			w.Write([]byte(streakReply(`{"chances":0}`)))
			return true
		}
		return false
	}
	srv := c.server(t)
	s := schedWithPool(srv, pool.New(""), Config{})
	s.streakBonusAccount(&auth.Auth{UID: "u1", AccessToken: "at"})

	if got := c.count("/activity/growth/redeem"); got != 1 {
		t.Errorf("应尝试兑换 1 次，got %d", got)
	}
	if got := c.count("/lottery/summary"); got != 1 {
		t.Errorf("兑换失败后应继续查抽奖次数，got %d", got)
	}
}

// TestStreakBonusEarlyReturns streak 状态/抽奖次数/抽奖失败时按源码提前返回。
func TestStreakBonusEarlyReturns(t *testing.T) {
	t.Run("连登状态查询失败", func(t *testing.T) {
		c := newSchedCounter()
		c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
			if strings.HasSuffix(path, "/activity/growth/streak") {
				w.WriteHeader(400)
				w.Write([]byte(`{"code":400,"msg":"boom"}`))
				return true
			}
			return false
		}
		s := schedWithPool(c.server(t), pool.New(""), Config{})
		s.streakBonusAccount(&auth.Auth{UID: "u1", AccessToken: "at"})
		if c.count("/lottery/summary") != 0 || c.count("/activity/growth/redeem") != 0 {
			t.Error("连登状态失败后应直接返回")
		}
	})

	t.Run("抽奖次数查询失败", func(t *testing.T) {
		c := newSchedCounter()
		c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
			switch {
			case strings.HasSuffix(path, "/activity/growth/streak"):
				w.Write([]byte(streakReply(`{"redemption_status":{}}`)))
				return true
			case strings.HasSuffix(path, "/lottery/summary"):
				w.WriteHeader(400)
				w.Write([]byte(`{"code":400,"msg":"summary boom"}`))
				return true
			}
			return false
		}
		s := schedWithPool(c.server(t), pool.New(""), Config{})
		s.streakBonusAccount(&auth.Auth{UID: "u1", AccessToken: "at"})
		if c.count("/lottery/draw") != 0 {
			t.Error("次数查询失败不应抽奖")
		}
	})

	t.Run("抽奖失败停止", func(t *testing.T) {
		c := newSchedCounter()
		c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
			switch {
			case strings.HasSuffix(path, "/activity/growth/streak"):
				w.Write([]byte(streakReply(`{"redemption_status":{}}`)))
				return true
			case strings.HasSuffix(path, "/lottery/summary"):
				w.Write([]byte(streakReply(`{"chances":3}`)))
				return true
			case strings.HasSuffix(path, "/lottery/draw"):
				w.WriteHeader(400)
				w.Write([]byte(`{"code":400,"msg":"draw boom"}`))
				return true
			}
			return false
		}
		s := schedWithPool(c.server(t), pool.New(""), Config{})
		s.streakBonusAccount(&auth.Auth{UID: "u1", AccessToken: "at"})
		if got := c.count("/lottery/draw"); got != 1 {
			t.Errorf("首次抽奖失败后应停止，draw=%d", got)
		}
	})
}

// TestStreakBonusCompensation 补偿新领成功后立即刷新余额；刷新失败不影响后续。
func TestStreakBonusCompensation(t *testing.T) {
	newFake := func(balanceFails bool) (*schedCounter, *Scheduler) {
		c := newSchedCounter()
		c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
			switch {
			case strings.HasSuffix(path, "/compensation-status"):
				w.Write([]byte(streakReply(`{"active":true,"claimed":false,"credit_num":50}`)))
				return true
			case strings.HasSuffix(path, "/claim-compensation"):
				w.Write([]byte(streakReply(`{"credit":50}`)))
				return true
			case path == upstreamtest.FreePath:
				if balanceFails {
					w.WriteHeader(400)
					w.Write([]byte(`{"code":400,"msg":"balance boom"}`))
					return true
				}
				w.Write([]byte(upstreamtest.List(upstreamtest.Package(1000, 66, ""))))
				return true
			case strings.HasSuffix(path, "/activity/growth/streak"):
				w.Write([]byte(streakReply(`{"redemption_status":{}}`)))
				return true
			}
			return false
		}
		srv := c.server(t)
		p := pool.New("")
		p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
		return c, schedWithPool(srv, p, Config{})
	}

	t.Run("补偿到账后刷新余额", func(t *testing.T) {
		c, s := newFake(false)
		s.streakBonusAccount(&auth.Auth{UID: "u1", AccessToken: "at"})
		if c.count("/claim-compensation") != 1 {
			t.Error("应领取补偿")
		}
		st, _ := s.cfg.Pool.Status("u1")
		if st.Credits != 66 {
			t.Errorf("补偿后余额 = %d，应为 66", st.Credits)
		}
	})

	t.Run("补偿后余额刷新失败", func(t *testing.T) {
		c, s := newFake(true)
		s.streakBonusAccount(&auth.Auth{UID: "u1", AccessToken: "at"})
		if c.count("/claim-compensation") != 1 {
			t.Error("应领取补偿")
		}
		if c.count("/activity/growth/streak") != 1 {
			t.Error("余额刷新失败不应影响后续连登检查")
		}
	})
}

// TestMakeupYesterday 补签判定：漏签且有卡才补，日期用昨天；各失败分支静默。
func TestMakeupYesterday(t *testing.T) {
	fixed := time.Now()
	old := streakNow
	streakNow = func() time.Time { return fixed }
	t.Cleanup(func() { streakNow = old })
	yesterday := fixed.AddDate(0, 0, -1).Format("2006-01-02")
	newFake := func(cells, cards string, heatmapFails, fullFails, useFails bool) (*schedCounter, *[]string) {
		var mu sync.Mutex
		var dates []string
		c := newSchedCounter()
		c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
			switch {
			case strings.HasSuffix(path, "/activity/growth/heatmap"):
				if heatmapFails {
					w.WriteHeader(400)
					w.Write([]byte(`{"code":400,"msg":"heatmap boom"}`))
					return true
				}
				w.Write([]byte(streakReply(cells)))
				return true
			case strings.HasSuffix(path, "/activity/growth/streak"):
				if fullFails {
					w.WriteHeader(400)
					w.Write([]byte(`{"code":400,"msg":"streak boom"}`))
					return true
				}
				w.Write([]byte(streakReply(`{"makeup_cards":{"balance":` + cards + `},"redemption_status":{}}`)))
				return true
			case strings.HasSuffix(path, "/makeup-cards/use"):
				var body map[string]any
				json.Unmarshal([]byte(readReqBody(t, r)), &body)
				mu.Lock()
				if v, ok := body["target_date"].(string); ok {
					dates = append(dates, v)
				}
				mu.Unlock()
				if useFails {
					w.WriteHeader(400)
					w.Write([]byte(`{"code":400,"msg":"use boom"}`))
				}
				return true
			}
			return false
		}
		return c, &dates
	}
	// 上游包自己用 time.Now 算“昨天”，测试跨零点时它算出的是 fixed 的今天：两天都给同样的分数。
	today := fixed.Format("2006-01-02")
	missed := `{"cells":[{"date":"` + yesterday + `","score":0},{"date":"` + today + `","score":0}]}`
	notMissed := `{"cells":[{"date":"` + yesterday + `","score":3},{"date":"` + today + `","score":3}]}`

	t.Run("漏签且有卡", func(t *testing.T) {
		c, dates := newFake(missed, "2", false, false, false)
		s := schedWithPool(c.server(t), pool.New(""), Config{})
		s.makeupYesterday(&auth.Auth{UID: "u1", AccessToken: "at"})
		if len(*dates) != 1 || (*dates)[0] != yesterday {
			t.Errorf("补签日期 = %v，应为 %s", *dates, yesterday)
		}
	})

	t.Run("没漏签", func(t *testing.T) {
		c, dates := newFake(notMissed, "2", false, false, false)
		s := schedWithPool(c.server(t), pool.New(""), Config{})
		s.makeupYesterday(&auth.Auth{UID: "u1", AccessToken: "at"})
		if len(*dates) != 0 || c.count("/activity/growth/streak") != 0 {
			t.Error("没漏签不应查连登状态/补签")
		}
	})

	t.Run("热力图查询失败", func(t *testing.T) {
		c, dates := newFake(missed, "2", true, false, false)
		s := schedWithPool(c.server(t), pool.New(""), Config{})
		s.makeupYesterday(&auth.Auth{UID: "u1", AccessToken: "at"})
		if len(*dates) != 0 {
			t.Error("查询失败不应补签")
		}
	})

	t.Run("没有补签卡", func(t *testing.T) {
		c, dates := newFake(missed, "0", false, false, false)
		s := schedWithPool(c.server(t), pool.New(""), Config{})
		s.makeupYesterday(&auth.Auth{UID: "u1", AccessToken: "at"})
		if len(*dates) != 0 {
			t.Error("无卡不应补签")
		}
	})

	t.Run("连登状态失败", func(t *testing.T) {
		c, dates := newFake(missed, "0", false, true, false)
		s := schedWithPool(c.server(t), pool.New(""), Config{})
		s.makeupYesterday(&auth.Auth{UID: "u1", AccessToken: "at"})
		if len(*dates) != 0 {
			t.Error("状态失败不应补签")
		}
	})

	t.Run("补签失败", func(t *testing.T) {
		c, dates := newFake(missed, "2", false, false, true)
		s := schedWithPool(c.server(t), pool.New(""), Config{})
		s.makeupYesterday(&auth.Auth{UID: "u1", AccessToken: "at"})
		if len(*dates) != 1 {
			t.Errorf("应尝试补签一次，got %v", *dates)
		}
	})
}

// TestRunStreakBonusNowGates 连登管家门控：禁用/无 token/global/企业号都跳过。
func TestRunStreakBonusNowGates(t *testing.T) {
	c := newSchedCounter()
	c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
		if strings.HasSuffix(path, "/activity/growth/streak") {
			w.Write([]byte(streakReply(`{"redemption_status":{}}`)))
			return true
		}
		return false
	}
	srv := c.server(t)
	p := pool.New("")
	p.Add(&auth.Auth{UID: "ok", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "dis", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "notoken", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "g", AccessToken: "at", RefreshToken: "rt", Domain: "www.workbuddy.ai", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "e", AccessToken: "at", RefreshToken: "rt", EnterpriseID: "e1", ExpiresAt: 9999999999})
	p.Disable("dis", "test")
	s := schedWithPool(srv, p, Config{})

	s.RunStreakBonusNow()
	if got := c.count("/activity/growth/streak"); got != 1 {
		t.Errorf("连登检查 = %d，应只有 ok 账号一次", got)
	}
	if got := c.count("/activity/growth/heatmap"); got != 1 {
		t.Errorf("热力检查 = %d，应只有 ok 账号一次", got)
	}
}
