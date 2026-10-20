package upstream

import (
	"net/http"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestClaimTrialOnlyGlobal 非 global 账号（含 nil）直接报错且不发请求。
func TestClaimTrialOnlyGlobal(t *testing.T) {
	f := newUpFake(t)
	c := upGlobalClient(f)
	for _, a := range []*auth.Auth{nil, &auth.Auth{UID: "u-cn", AccessToken: "at", Domain: "www.codebuddy.cn"}} {
		if claimed, err := c.ClaimTrial(a); err == nil || claimed {
			t.Errorf("非 global 账号应返回错误，got claimed=%v err=%v", claimed, err)
		}
	}
	if n := len(f.seen()); n != 0 {
		t.Errorf("不应发请求，实际 %d 次", n)
	}
}

// TestClaimTrialSuccess 领取成功：POST billingBase /billing/ide/trial，带鉴权头。
func TestClaimTrialSuccess(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, trialPath, 200, `{"code":0,"msg":"OK","data":{}}`)
	c := upGlobalClient(f)
	claimed, err := c.ClaimTrial(globalAuth())
	if err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v，应为 true/nil", claimed, err)
	}
	r := f.seen()[0]
	if r.method != http.MethodPost || r.path != trialPath {
		t.Errorf("请求 = %s %s", r.method, r.path)
	}
	if r.header.Get("Authorization") != "Bearer "+globalAuth().AccessTokenValue() {
		t.Errorf("Authorization = %q", r.header.Get("Authorization"))
	}
	if r.header.Get("Content-Type") != "application/json" || r.header.Get("Accept") != "application/json, text/plain, */*" {
		t.Errorf("内容头 = %v", r.header)
	}
}

// TestClaimTrialAlready 已领过（幂等码 14051）：不算错误，claimed=false。
// 两种指纹都要覆盖：HTTP 200 业务码（code=14051）与 HTTP 400 原始 body（"code":14051）。
func TestClaimTrialAlready(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"HTTP 200 业务码", 200, `{"code":14051,"msg":"already claimed"}`},
		{"HTTP 400 原始 body", 400, `{"code":14051,"msg":"already claimed"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.on(http.MethodPost, trialPath, tc.status, tc.body)
			claimed, err := upGlobalClient(f).ClaimTrial(globalAuth())
			if err != nil || claimed {
				t.Fatalf("已领过应返回 false/nil，got %v/%v", claimed, err)
			}
		})
	}

	if !trialAlreadyErr("code=14051 msg=x") || !trialAlreadyErr(`{"code":14051}`) {
		t.Error("两种幂等指纹都应识别")
	}
	if trialAlreadyErr("code=14050") || trialAlreadyErr("") {
		t.Error("其他消息不应识别为已领过")
	}
}

// [高熵密钥] 其他失败（非 14051）原样返回错误。
func TestClaimTrialBusinessFailure(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, trialPath, 400, `{"code":500,"msg":"trial boom"}`)
	claimed, err := upGlobalClient(f).ClaimTrial(globalAuth())
	if claimed || err == nil || !strings.Contains(err.Error(), "trial boom") {
		t.Fatalf("claimed=%v err=%v，应返回带 msg 的错误", claimed, err)
	}
}

// TestClaimTrialBadResponse 响应不是 JSON → doJSON 解析错误；200 + 空 data → 照常成功。
func TestClaimTrialBadResponse(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, trialPath, 200, `oops`)
	if claimed, err := upGlobalClient(f).ClaimTrial(globalAuth()); err == nil || claimed {
		t.Fatalf("非 JSON 应返回错误，got %v/%v", claimed, err)
	}

	f2 := newUpFake(t)
	f2.on(http.MethodPost, trialPath, 200, `{"code":0,"data":null}`)
	if claimed, err := upGlobalClient(f2).ClaimTrial(globalAuth()); err != nil || !claimed {
		t.Fatalf("空 data 应视为成功，got %v/%v", claimed, err)
	}
}
