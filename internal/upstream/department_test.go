package upstream

// department_test.go 企业号的部门全名：登录取账号时读进来、刷新后照官方 mergeCurrentAccountDetails
// 合并、部门全名变了算刷新失败；有值时桌面端请求带 X-Department-Info。

import (
	"net/http"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestDepartmentHeader 有部门全名时对话、后台服务、刷新令牌的请求都带 X-Department-Info，值原样；
// 没有时一个都不带。
func TestDepartmentHeader(t *testing.T) {
	c := &Client{}
	for _, tc := range []struct{ name, dept string }{{"企业号", "研发部/平台组"}, {"个人号", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			a := cnAuth()
			a.EnterpriseID = "corp-cn"
			a.DepartmentFullName = tc.dept
			reqs := map[string]*http.Request{}
			for _, kind := range []string{"对话", "后台服务", "刷新令牌"} {
				req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
				switch kind {
				case "对话":
					c.ChatHeaders(req, a, "", ChatMeta{})
				case "后台服务":
					c.BillingHeaders(req, a)
				default:
					c.RefreshHeaders(req, a)
				}
				reqs[kind] = req
			}
			for kind, req := range reqs {
				got, sent := req.Header.Get("X-Department-Info"), len(req.Header.Values("X-Department-Info")) > 0
				if got != tc.dept || sent != (tc.dept != "") {
					t.Errorf("%s: X-Department-Info = %q（发了=%v），应为 %q", kind, got, sent, tc.dept)
				}
			}
		})
	}
}

// TestLoginFetchAccountDepartment 登录取账号时读出 departmentFullName。
func TestLoginFetchAccountDepartment(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, loginTestAccountCN, 200, `{"code":0,"data":{"uid":"u-1","enterpriseId":"ent-1","departmentFullName":"研发部"}}`)
	acct, err := loginTestClient(f).LoginFetchAccount("cn", "st-1", LoginToken{AccessToken: "at-1"}, LoginTrace{})
	if err != nil || derefStr(acct.DepartmentFullName) != "研发部" {
		t.Fatalf("取账号: %+v %v", acct, err)
	}
}

// TestRefreshMergesAccount 刷新后照官方合并取到的账号（mergeCurrentAccountDetails）。这一组的本地
// 凭证没有账号类型（1.1.2 以前的网关写的）：挑账号不看类型，合并后只补字段、不比对身份。
func TestRefreshMergesAccount(t *testing.T) {
	for _, tc := range []struct {
		name, acctBody     string
		global             bool
		ent, dept          string
		wantNick, wantDept string
		wantType           string
	}{
		{name: "补上部门全名、类型并更新昵称", acctBody: `{"code":0,"data":{"uid":"u-1","type":"enterprise","enterpriseId":"e1","nickname":"新名","departmentFullName":"研发部","idp":"","oneidAccountId":""}}`,
			ent: "e1", wantNick: "新名", wantDept: "研发部", wantType: "enterprise"},
		{name: "部门全名不同只补不判失败", acctBody: `{"code":0,"data":{"uid":"u-1","enterpriseId":"e1","departmentFullName":"市场部"}}`,
			ent: "e1", dept: "研发部", wantNick: "旧名", wantDept: "市场部"},
		{name: "响应里没有部门全名", acctBody: `{"code":0,"data":{"uid":"u-1","enterpriseId":"e1"}}`,
			ent: "e1", dept: "研发部", wantNick: "旧名", wantDept: "研发部"},
		{name: "空昵称不覆盖", acctBody: `{"code":0,"data":{"uid":"u-1","nickname":""}}`, wantNick: "旧名"},
		{name: "国内版 uid 对不上", acctBody: `{"code":0,"data":{"uid":"u-other","nickname":"别人","departmentFullName":"别处"}}`,
			wantNick: "旧名"},
		{name: "国内版企业 id 对不上", acctBody: `{"code":0,"data":{"uid":"u-1","enterpriseId":"e2","nickname":"别处"}}`,
			ent: "e1", wantNick: "旧名"},
		{name: "国际版按 uid 与企业 id 挑", acctBody: `{"code":0,"data":{"accounts":[{"uid":"u-1","enterpriseId":"e9","nickname":"企业"},{"uid":"u-1","nickname":"个人","type":"personal"}]}}`,
			global: true, wantNick: "个人", wantType: "personal"},
		{name: "国际版个人号退到 lastLogin", acctBody: `{"code":0,"data":{"accounts":[{"uid":"u-1","nickname":"甲"},{"uid":"u-1","nickname":"乙","lastLogin":true}]}}`,
			global: true, wantNick: "乙"},
		{name: "国际版挑不出唯一一个", acctBody: `{"code":0,"data":{"accounts":[{"uid":"u-1","nickname":"甲"},{"uid":"u-1","nickname":"乙"}]}}`,
			global: true, wantNick: "旧名"},
		{name: "国际版列表里没有", acctBody: `{"code":0,"data":{"accounts":[{"uid":"u-other","nickname":"别人"}]}}`,
			global: true, wantNick: "旧名"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "/v2/plugin/account"
			if tc.global {
				path = "/v2/plugin/accounts"
			}
			f := newUpFake(t)
			f.on(http.MethodPost, refreshTestPath, 200, refreshTestNew)
			f.on(http.MethodGet, path, 200, tc.acctBody)
			a := refreshTestAuth(tc.global)
			a.Nickname, a.EnterpriseID, a.DepartmentFullName = "旧名", tc.ent, tc.dept
			if err := refreshTestClient(f).RefreshToken(a); err != nil || a.AccessToken != "at-new" {
				t.Fatalf("刷新应成功并写回: %v %s", err, a.AccessToken)
			}
			if a.Nickname != tc.wantNick || a.DepartmentFullName != tc.wantDept || derefStr(a.AccountType) != tc.wantType {
				t.Errorf("昵称 / 部门全名 / 类型 = %q / %q / %q，应为 %q / %q / %q", a.Nickname, a.DepartmentFullName,
					derefStr(a.AccountType), tc.wantNick, tc.wantDept, tc.wantType)
			}
			if tc.dept != "" {
				for _, r := range f.seen() {
					if r.header.Get("X-Department-Info") != tc.dept {
						t.Errorf("%s 应带本地的部门全名 %q，实际 %q", r.path, tc.dept, r.header.Get("X-Department-Info"))
					}
				}
			}
		})
	}
}

// refreshIdentityAuth 有账号类型的本地凭证（1.1.2 起登录写的），身份字段都有值。
func refreshIdentityAuth(ent, dept string) *auth.Auth {
	a := refreshTestAuth(false)
	typ, empty, bearer := "personal", "", "bearer"
	if ent != "" {
		typ = "enterprise"
	}
	a.Nickname, a.EnterpriseID = "旧名", ent
	a.AccountType, a.OneIDAccountID, a.IDP, a.TokenType = &typ, &empty, &empty, &bearer
	if ent != "" {
		a.DepartmentFullName, a.DepartmentSet = dept, true
	}
	return a
}

// TestRefreshIdentityCheck 本地凭证有账号类型时照官方 normalizeAndAssertRefreshedAuthority 核对：
// type、oneidAccountId、departmentFullName、idp 与令牌的 tokenType、domain 有一项变了（含有这个键
// 变成没有）就当刷新失败、不写回新令牌；本地有 idp 而合并后为空时沿用本地的，不算变。
func TestRefreshIdentityCheck(t *testing.T) {
	const tokOK = `{"code":0,"data":{"accessToken":"at-new","refreshToken":"rt-new","expiresIn":2592000,"domain":"dom.example","tokenType":"bearer"}}`
	personal := `{"code":0,"data":{"uid":"u-1","type":"personal","idp":"","oneidAccountId":"","nickname":"新名","lastLogin":true}}`
	for _, tc := range []struct {
		name, tokBody, acctBody string
		ent, dept, idp          string
		wantErr                 bool
		wantNick                string
	}{
		{name: "个人号身份没变", tokBody: tokOK, acctBody: personal, wantNick: "新名"},
		{name: "企业号身份没变", tokBody: tokOK, ent: "e1", dept: "研发部",
			acctBody: `{"code":0,"data":{"uid":"u-1","type":"enterprise","enterpriseId":"e1","departmentFullName":"研发部","idp":"","oneidAccountId":""}}`, wantNick: "旧名"},
		{name: "部门全名变了", tokBody: tokOK, ent: "e1", dept: "研发部", wantErr: true,
			acctBody: `{"code":0,"data":{"uid":"u-1","type":"enterprise","enterpriseId":"e1","departmentFullName":"市场部","idp":"","oneidAccountId":""}}`},
		{name: "响应里没有部门全名沿用本地", tokBody: tokOK, ent: "e1", dept: "研发部",
			acctBody: `{"code":0,"data":{"uid":"u-1","type":"enterprise","enterpriseId":"e1","idp":"","oneidAccountId":""}}`, wantNick: "旧名"},
		{name: "个人号多出部门全名", tokBody: tokOK, wantErr: true,
			acctBody: `{"code":0,"data":{"uid":"u-1","type":"personal","idp":"","oneidAccountId":"","departmentFullName":""}}`},
		{name: "oneidAccountId 变了", tokBody: tokOK, wantErr: true,
			acctBody: `{"code":0,"data":{"uid":"u-1","type":"personal","idp":"","oneidAccountId":"oid-1"}}`},
		{name: "idp 变了", tokBody: tokOK, wantErr: true,
			acctBody: `{"code":0,"data":{"uid":"u-1","type":"personal","idp":"wechat","oneidAccountId":""}}`},
		{name: "idp 变空沿用本地", tokBody: tokOK, idp: "wechat", wantNick: "旧名",
			acctBody: `{"code":0,"data":{"uid":"u-1","type":"personal","idp":"","oneidAccountId":""}}`},
		{name: "tokenType 变了", tokBody: `{"code":0,"data":{"accessToken":"at-new","refreshToken":"rt-new","domain":"dom.example","tokenType":"mac"}}`,
			acctBody: personal, wantErr: true},
		{name: "响应没有 tokenType", tokBody: `{"code":0,"data":{"accessToken":"at-new","refreshToken":"rt-new","domain":"dom.example"}}`,
			acctBody: personal, wantErr: true},
		{name: "登录域变了", tokBody: `{"code":0,"data":{"accessToken":"at-new","refreshToken":"rt-new","domain":"other.example","tokenType":"bearer"}}`,
			acctBody: personal, wantErr: true},
		{name: "响应没有登录域", tokBody: `{"code":0,"data":{"accessToken":"at-new","refreshToken":"rt-new","tokenType":"bearer"}}`,
			acctBody: personal, wantErr: true},
		{name: "类型对不上就不合并", tokBody: tokOK, wantNick: "旧名",
			acctBody: `{"code":0,"data":{"uid":"u-1","type":"ultimate","idp":"x","oneidAccountId":"y","nickname":"别处"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.on(http.MethodPost, refreshTestPath, 200, tc.tokBody)
			f.on(http.MethodGet, "/v2/plugin/account", 200, tc.acctBody)
			a := refreshIdentityAuth(tc.ent, tc.dept)
			if tc.idp != "" {
				idp := tc.idp
				a.IDP = &idp
			}
			err := refreshTestClient(f).RefreshToken(a)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "changed authority context") {
					t.Fatalf("err = %v，应为身份变了", err)
				}
				if a.AccessToken != "at-old" || a.RefreshToken != "rt-old" {
					t.Errorf("失败时不应写回令牌: %s / %s", a.AccessToken, a.RefreshToken)
				}
				return
			}
			if err != nil || a.AccessToken != "at-new" {
				t.Fatalf("刷新应成功并写回: %v %s", err, a.AccessToken)
			}
			if a.Nickname != tc.wantNick {
				t.Errorf("昵称 = %q，应为 %q", a.Nickname, tc.wantNick)
			}
			if tc.idp != "" && derefStr(a.IDP) != tc.idp {
				t.Errorf("idp = %q，应沿用本地的 %q", derefStr(a.IDP), tc.idp)
			}
		})
	}
}

// TestMatchRefreshedAccountByType 本地有账号类型时先挑类型与企业 id 都相同的那个。
func TestMatchRefreshedAccountByType(t *testing.T) {
	typ := "personal"
	old := &auth.Auth{UID: "u-1", AccountType: &typ}
	ultimate, personal := "ultimate", "Personal "
	hit := matchRefreshedAccount([]refreshAccountJSON{{UID: "u-1", Type: &ultimate}, {UID: "u-1", Type: &personal}}, old)
	if hit == nil || hit.Type != &personal {
		t.Fatalf("应挑类型相同（不分大小写、去空白）的那个: %+v", hit)
	}
	if !isPersonalEdition("", "") || isPersonalEdition("e1", "personal") || isPersonalEdition("", "Exclusive") {
		t.Error("isPersonalEdition 与官方不一致")
	}
}

// TestJSONTruthy lastLogin 按 JS 真值判断。
func TestJSONTruthy(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "null": false, "false": false, "0": false, `""`: false,
		"true": true, "1": true, "1760000000000": true, `"2026-10-20"`: true} {
		if got := jsonTruthy([]byte(raw)); got != want {
			t.Errorf("jsonTruthy(%s) = %v，应为 %v", raw, got, want)
		}
	}
	if matchRefreshedAccount([]refreshAccountJSON{{UID: ""}}, &auth.Auth{}) != nil {
		t.Error("本地没有 uid 时不应对上任何账号")
	}
}
