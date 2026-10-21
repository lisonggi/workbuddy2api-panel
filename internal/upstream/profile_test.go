// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// profile_test.go 钉住账号资料拉取（issue #94）：网页登录态 Cookie + web 平台头可访问
// /console/account；只解析 nickname，uid 不一致防串号；业务错误原样返回。
package upstream

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
)

func TestFetchAccountProfile(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if resp, ok := upstreamtest.WebSessionRoundTrip(r); ok {
			return resp, nil
		}
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/console/account") {
			return nil, errors.New("wrong method / path")
		}
		// 网页请求只靠登录态 Cookie 认人，不带 Authorization。
		if r.Header.Get("Authorization") != "" || !strings.Contains(r.Header.Get("Cookie"), "session=at") ||
			r.Header.Get("x-client-platform") != "web" {
			return nil, errors.New("missing session cookie / web platform header")
		}
		// 官网网页形状：GET 不带 Origin，Referer 是账号设置页，UA 用网页 UA。
		if got := r.Header.Get("Origin"); got != "" {
			return nil, errors.New("GET 不应带 Origin: " + got)
		}
		if got := r.Header.Get("Referer"); got != "https://web.example"+webPageAccountSettings {
			return nil, errors.New("Referer = " + got)
		}
		if got := r.Header.Get("User-Agent"); got != browserUA {
			return nil, errors.New("User-Agent = " + got)
		}
		// 响应刻意带 phoneNumber：方法必须只解析 nickname/uid，敏感字段不得进入返回值。
		return jsonResp(200, `{"code":0,"msg":"OK","data":{"uid":"u1","nickname":"新名字","phoneNumber":"13800000000"}}`), nil
	})
	c.WebBaseCN = "https://web.example"
	nick, err := c.FetchAccountProfile(&auth.Auth{UID: "u1", AccessToken: "at"})
	if err != nil || nick != "新名字" {
		t.Fatalf("nick=%q err=%v, want 新名字 nil", nick, err)
	}
}

func TestFetchAccountProfileUIDMismatch(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"code":0,"data":{"uid":"someone-else","nickname":"x"}}`), nil
	})
	c.WebBaseCN = "https://web.example"
	if _, err := c.FetchAccountProfile(&auth.Auth{UID: "u1", AccessToken: "at"}); err == nil {
		t.Fatal("uid 不一致应报错（防串号）")
	}
}

func TestFetchAccountProfileBusinessError(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(401, `{"code":1002,"msg":"unauthorized"}`), nil
	})
	c.WebBaseCN = "https://web.example"
	if _, err := c.FetchAccountProfile(&auth.Auth{UID: "u1", AccessToken: "bad"}); err == nil {
		t.Fatal("401 应返回错误")
	}
}
