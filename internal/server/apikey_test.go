// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/apikeys"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/usage"
)

// sseCredit 带积分的流式回复：10 个 token、0.5 积分。
const sseCredit = "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"你好\"}}]}\n\n" +
	"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10,\"credit\":0.5}}\n\n" +
	"data: [DONE]\n\n"

const chatBody = `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":true}`

// keyRig 一个能对话的网关，带客户端密钥库；admin 为管理员密钥（可空）。
func keyRig(t *testing.T, admin string) (*Handler, *apikeys.Store, *int) {
	t.Helper()
	h, calls := bridgeHandler(t, 200, sseCredit, true)
	store, err := apikeys.Open("")
	if err != nil {
		t.Fatal(err)
	}
	h.cfg.Keys = store
	h.cfg.Live = livecfg.New(livecfg.Snapshot{APIKey: admin})
	return h, store, calls
}

func createKey(t *testing.T, s *apikeys.Store, in apikeys.Input) apikeys.Key {
	t.Helper()
	k, err := s.Create(in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func openAIErr(t *testing.T, rec *httptest.ResponseRecorder) (code, msg string) {
	t.Helper()
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("不是 OpenAI 错误格式: %s", rec.Body)
	}
	return e.Error.Code, e.Error.Message
}

// TestKeyAuth 管理员密钥、客户端密钥都认；停用、过期的 401 并说明原因；建过客户端密钥后，
// 管理员密钥为空也必须带密钥。
func TestKeyAuth(t *testing.T) {
	t.Run("什么都没设：不鉴权", func(t *testing.T) {
		h, _, _ := keyRig(t, "")
		if rec := post(h, "/v1/chat/completions", chatBody); rec.Code != 200 {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
		}
	})

	h, store, _ := keyRig(t, "sk-admin")
	on := createKey(t, store, apikeys.Input{Name: "张三", Enabled: true})
	off := createKey(t, store, apikeys.Input{Name: "停用的"})
	old := createKey(t, store, apikeys.Input{Name: "过期的", Enabled: true, ExpiresAt: time.Now().Add(-time.Minute)})
	for name, tc := range map[string]struct {
		path, hdr, val string
		code           int
		want           string
	}{
		"管理员密钥":                   {"/v1/chat/completions", "Authorization", "Bearer sk-admin", 200, ""},
		"客户端密钥":                   {"/v1/chat/completions", "Authorization", "Bearer " + on.Secret, 200, ""},
		"Claude Code 用 x-api-key": {"/v1/messages", "X-Api-Key", on.Secret, 200, ""},
		"聊天接口不认 x-api-key":        {"/v1/chat/completions", "X-Api-Key", on.Secret, 401, "missing or invalid API key"},
		"错的密钥":                    {"/v1/chat/completions", "Authorization", "Bearer sk-nope", 401, "missing or invalid API key"},
		"停用":                      {"/v1/chat/completions", "Authorization", "Bearer " + off.Secret, 401, "「停用的」已停用"},
		"过期":                      {"/v1/messages", "X-Api-Key", old.Secret, 401, "「过期的」已于北京时间"},
	} {
		t.Run(name, func(t *testing.T) {
			body := chatBody
			if tc.path == "/v1/messages" {
				body = strings.Replace(claudeBody, "%s", "false", 1)
			}
			rec := post(h, tc.path, body, tc.hdr, tc.val)
			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
			}
		})
	}

	t.Run("管理员密钥为空但建过客户端密钥：必须带", func(t *testing.T) {
		h, store, calls := keyRig(t, "")
		k := createKey(t, store, apikeys.Input{Name: "a", Enabled: true})
		if rec := post(h, "/v1/chat/completions", chatBody); rec.Code != 401 {
			t.Fatalf("不带密钥应 401：code=%d", rec.Code)
		}
		if rec := post(h, "/v1/messages", strings.Replace(claudeBody, "%s", "false", 1)); rec.Code != 401 {
			t.Fatalf("不带密钥应 401：code=%d", rec.Code)
		}
		if rec := post(h, "/v1/chat/completions", chatBody, "Authorization", "Bearer "+k.Secret); rec.Code != 200 {
			t.Fatalf("带客户端密钥应放行：code=%d body=%s", rec.Code, rec.Body)
		}
		if *calls != 1 {
			t.Fatalf("calls=%d", *calls)
		}
	})

	t.Run("/status 只认管理员密钥", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/status", nil)
		req.Header.Set("Authorization", "Bearer "+on.Secret)
		h.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("客户端密钥不该能看 /status：code=%d", rec.Code)
		}
	})
}

// TestKeyModelAllowlist 白名单按模型路由改写后的模型判：Claude Code 发 claude-* 被改写成 glm-5.2，
// 白名单里有 glm-* 就放行；不在白名单里的 403，Claude Code 入口是 permission_error。
// /v1/models 只列白名单里的模型。
func TestKeyModelAllowlist(t *testing.T) {
	h, _ := routeRig(t, true, testRoutes...)
	store, _ := apikeys.Open("")
	rl := reqlog.New(reqlog.Config{})
	t.Cleanup(rl.Close)
	h.cfg.Keys, h.cfg.RequestLog = store, rl
	k := createKey(t, store, apikeys.Input{Name: "只用 glm", Enabled: true, Models: []string{"glm-*"}})
	auth := func(path, body string) *httptest.ResponseRecorder {
		return post(h, path, body, "Authorization", "Bearer "+k.Secret)
	}
	claude := func(model string) string {
		return strings.Replace(strings.Replace(claudeBody, "%s", "false", 1), `"glm-5.2"`, `"`+model+`"`, 1)
	}
	if rec := auth("/v1/messages", claude("claude-sonnet-4-5")); rec.Code != 200 {
		t.Fatalf("改写成 glm-5.2 应放行：code=%d body=%s", rec.Code, rec.Body)
	}
	if rec := auth("/v1/chat/completions", `{"model":"cn:glm-5.2","messages":[{"role":"user","content":"hi"}]}`); rec.Code != 200 {
		t.Fatalf("带前缀按裸名判：code=%d body=%s", rec.Code, rec.Body)
	}
	rec := auth("/v1/messages", claude("claude-haiku-4-5")) // 改写成 global:gpt-5.5
	if typ, msg := anthropicErr(t, rec); rec.Code != 403 || typ != "permission_error" || !strings.Contains(msg, "不能用模型 gpt-5.5") || !strings.Contains(msg, "glm-*") {
		t.Fatalf("code=%d type=%s msg=%s", rec.Code, typ, msg)
	}
	rec = auth("/v1/chat/completions", `{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	if code, _ := openAIErr(t, rec); rec.Code != 403 || code != "model_not_allowed" {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if got, _ := store.Get(k.ID, time.Now()); got.Used.Requests != 2 {
		t.Fatalf("被白名单拦下的不计请求：%+v", got.Used)
	}
	// 被拦下的也记请求日志，带模型名和密钥名
	if e := rl.Snapshot().Recent[0]; e.Status != 403 || e.Outcome != reqlog.OutcomeHTTPError || e.Model != "gpt-5.5" || e.APIKey != "只用 glm" {
		t.Fatalf("请求日志 = %+v", e)
	}

	list := func(hdr string) []string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", hdr)
		h.ServeHTTP(rec, req)
		var out struct {
			Data []struct{ ID string } `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		ids := make([]string, 0, len(out.Data))
		for _, m := range out.Data {
			ids = append(ids, m.ID)
		}
		return ids
	}
	ids := list("Bearer " + k.Secret)
	for _, id := range ids {
		if _, bare := resolveModel(id); !strings.HasPrefix(bare, "glm-") {
			t.Fatalf("白名单密钥的模型列表不该有 %s: %v", id, ids)
		}
	}
	if len(ids) == 0 {
		t.Fatal("白名单密钥应能看到 glm 模型")
	}
	all := createKey(t, store, apikeys.Input{Name: "不限", Enabled: true})
	if ids := list("Bearer " + all.Secret); len(ids) < 2 {
		t.Fatalf("不限的密钥应看到全部模型: %v", ids)
	}
}

// TestKeyQuota 上限：每天的请求数用完回 429，带 Retry-After 和 X-Should-Retry: false；总量的不带
// Retry-After；不打上游。Claude Code 入口是 rate_limit_error。
func TestKeyQuota(t *testing.T) {
	h, store, calls := keyRig(t, "sk-admin")
	k := createKey(t, store, apikeys.Input{Name: "每天一次", Enabled: true, DailyLimit: apikeys.Limits{Requests: 1}})
	if rec := post(h, "/v1/chat/completions", chatBody, "Authorization", "Bearer "+k.Secret); rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	rec := post(h, "/v1/chat/completions", chatBody, "Authorization", "Bearer "+k.Secret)
	code, msg := openAIErr(t, rec)
	if rec.Code != 429 || code != "key_quota_exceeded" || !strings.Contains(msg, "今天的用量已到上限（请求次数 1）") ||
		rec.Header().Get("X-Should-Retry") != "false" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("code=%d hdr=%v body=%s", rec.Code, rec.Header(), rec.Body)
	}
	rec = post(h, "/v1/messages", strings.Replace(claudeBody, "%s", "false", 1), "X-Api-Key", k.Secret)
	if typ, _ := anthropicErr(t, rec); rec.Code != 429 || typ != "rate_limit_error" || rec.Header().Get("X-Should-Retry") != "false" {
		t.Fatalf("code=%d type=%s", rec.Code, typ)
	}
	// 管理员密钥不受限
	if rec := post(h, "/v1/chat/completions", chatBody, "Authorization", "Bearer sk-admin"); rec.Code != 200 {
		t.Fatalf("管理员密钥不受上限约束：code=%d", rec.Code)
	}

	tot := createKey(t, store, apikeys.Input{Name: "总量 10 token", Enabled: true, TotalLimit: apikeys.Limits{Tokens: 10}})
	if rec := post(h, "/v1/chat/completions", chatBody, "Authorization", "Bearer "+tot.Secret); rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	rec = post(h, "/v1/chat/completions", chatBody, "Authorization", "Bearer "+tot.Secret)
	if _, msg := openAIErr(t, rec); rec.Code != 429 || !strings.Contains(msg, "总用量已到上限（token 10）") || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("code=%d hdr=%v body=%s", rec.Code, rec.Header(), rec.Body)
	}
	if *calls != 3 {
		t.Fatalf("被上限拦下的不该打上游：calls=%d", *calls)
	}
}

// TestKeyFailedRequestRefunded 请求最后失败（上游报错）不算次数：每天 1 次的密钥连发两个失败请求都放行，
// 计数退回 0；成功的那次才算上。
func TestKeyFailedRequestRefunded(t *testing.T) {
	h, _ := bridgeHandler(t, 400, `{"code":11101,"msg":"bad request"}`, false)
	store, _ := apikeys.Open("")
	h.cfg.Keys = store
	h.cfg.Live = livecfg.New(livecfg.Snapshot{APIKey: "sk-admin"})
	k := createKey(t, store, apikeys.Input{Name: "每天一次", Enabled: true, DailyLimit: apikeys.Limits{Requests: 1}})
	for i := 0; i < 2; i++ {
		if rec := post(h, "/v1/chat/completions", chatBody, "Authorization", "Bearer "+k.Secret); rec.Code == 429 || rec.Code < 400 {
			t.Fatalf("第 %d 次：失败的请求不该占次数 code=%d body=%s", i+1, rec.Code, rec.Body)
		}
	}
	got, _ := store.Get(k.ID, time.Now())
	if got.Used.Requests != 0 || got.Today.Requests != 0 || got.LastUsedAt.IsZero() {
		t.Fatalf("失败后计数应退回：%+v", got)
	}
}

// TestKeyUsageRecorded 客户端密钥的请求：密钥累计 token 和积分，用量桶和请求日志记下是哪个密钥；
// 管理员密钥记 admin、「管理员」。
func TestKeyUsageRecorded(t *testing.T) {
	h, store, _ := keyRig(t, "sk-admin")
	u := usage.New("")
	rl := reqlog.New(reqlog.Config{})
	t.Cleanup(rl.Close)
	h.cfg.Usage, h.cfg.RequestLog = u, rl
	k := createKey(t, store, apikeys.Input{Name: "张三", Enabled: true})
	if rec := post(h, "/v1/chat/completions", chatBody, "Authorization", "Bearer "+k.Secret); rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	if rec := post(h, "/v1/chat/completions", chatBody, "Authorization", "Bearer sk-admin"); rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	got, _ := store.Get(k.ID, time.Now())
	if got.Used != (apikeys.Usage{Requests: 1, Tokens: 10, Credits: 0.5}) || got.Today != got.Used || got.LastUsedAt.IsZero() {
		t.Fatalf("密钥用量 = %+v", got)
	}
	snap := u.SnapshotWindow(usage.Window{}, nil, nil)
	byKey := map[string]int64{}
	for _, row := range snap.ByKey {
		byKey[row.Key] = row.TotalTokens
	}
	if byKey[k.ID] != 10 || byKey["admin"] != 10 || len(byKey) != 2 {
		t.Fatalf("用量按密钥 = %v", byKey)
	}
	if one := u.SnapshotWindow(usage.Window{Key: k.ID, KeySet: true}, nil, nil); one.Totals.Requests != 1 || one.Totals.Credits != 0.5 {
		t.Fatalf("按密钥筛 = %+v", one.Totals)
	}
	labels := map[string]bool{}
	for _, e := range rl.Snapshot().Recent {
		labels[e.APIKey] = true
	}
	if !labels["张三"] || !labels["管理员"] {
		t.Fatalf("请求日志的密钥 = %v", labels)
	}

	t.Run("上游没给 total_tokens：按 prompt + completion 计", func(t *testing.T) {
		body := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2}}\n\n" +
			"data: [DONE]\n\n"
		h2, _ := bridgeHandler(t, 200, body, true)
		h2.cfg.Keys = store
		if rec := post(h2, "/v1/chat/completions", chatBody, "Authorization", "Bearer "+k.Secret); rec.Code != 200 {
			t.Fatalf("code=%d", rec.Code)
		}
		if got, _ := store.Get(k.ID, time.Now()); got.Used.Tokens != 16 || got.Used.Credits != 0.5 {
			t.Fatalf("密钥用量 = %+v", got.Used)
		}
	})
}
