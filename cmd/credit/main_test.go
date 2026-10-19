package main

// credit/main_test.go 积分查询工具：假 Transport 按 host 区分 CN/global 计费域，
// 覆盖成功、无 token、接口报错、国际版路径、坏文件跳过、-pretty 与 WB2A_AUTH_DIR。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type creditRT func(*http.Request) (*http.Response, error)

func (f creditRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type creditFake struct {
	mu   sync.Mutex
	recs []string
}

func (f *creditFake) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.recs = append(f.recs, r.URL.Host+r.URL.Path)
	f.mu.Unlock()
	uid := r.Header.Get("X-User-Id")
	var body string
	switch {
	case r.URL.Host == billingHostCN() && r.URL.Path == "/v2/billing/meter/get-user-resource":
		switch uid {
		case "u-cn":
			body = `{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CapacityRemain":60,"CapacityUsed":40,"CapacitySize":100}]}}}}`
		case "u-err":
			return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":400}`))}, nil
		default:
			return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":400}`))}, nil
		}
	case r.URL.Host == billingHostGlobal() && r.URL.Path == "/billing/meter/get-user-resource":
		body = `{"code":0,"data":{"Response":{"Data":{"TotalDosage":80,` +
			`"Accounts":[{"CycleCapacityRemain":30,"CycleCapacityUsed":10,"CycleCapacitySize":50}]}}}}`
	default:
		return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":400}`))}, nil
	}
	return &http.Response{StatusCode: 200,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   io.NopCloser(strings.NewReader(body))}, nil
}

func (f *creditFake) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.recs...)
}

func billingHostCN() string     { return strings.TrimPrefix(billingBaseCN, "https://") }
func billingHostGlobal() string { return strings.TrimPrefix(billingBaseGlobal, "https://") }

func writeCreditAuth(t *testing.T, dir, uid, nick, token, realm string) {
	t.Helper()
	domain := "www.codebuddy.cn"
	if realm == "global" {
		domain = "www.workbuddy.ai"
	}
	raw := fmt.Sprintf(`{"auth":{"accessToken":%q,"domain":%q,"realm":%q},`+
		`"account":{"uid":%q,"enterpriseId":"","nickname":%q}}`, token, domain, realm, uid, nick)
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-"+uid+".json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

type creditOutput struct {
	Service string `json:"service"`
	TS      int64  `json:"ts"`
	Total   struct {
		Remain   int64 `json:"remain"`
		Used     int64 `json:"used"`
		Size     int64 `json:"size"`
		Accounts int   `json:"accounts"`
		OK       int   `json:"ok"`
		Failed   int   `json:"failed"`
	} `json:"total"`
	Accounts []struct {
		UID      string `json:"uid"`
		Nickname string `json:"nickname"`
		Remain   *int64 `json:"remain"`
		Used     *int64 `json:"used"`
		Size     *int64 `json:"size"`
		Packages int    `json:"packages"`
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
	} `json:"accounts"`
}

// setAccountGap0 测试里去掉账号间的 200ms 间隔。
func setAccountGap0(t *testing.T) {
	t.Helper()
	old := accountGap
	accountGap = 0
	t.Cleanup(func() { accountGap = old })
}

// JSON 输出：字段、汇总、CN/global 域与路径、坏文件跳过。
func TestCreditRunJSON(t *testing.T) {
	setAccountGap0(t)
	dir := t.TempDir()
	writeCreditAuth(t, dir, "u-cn", "国内号", "at-cn", "cn")
	writeCreditAuth(t, dir, "u-notoken", "无令牌", "", "cn")
	writeCreditAuth(t, dir, "u-err", "接口错", "at-err", "cn")
	writeCreditAuth(t, dir, "u-glb", "国际号", "at-glb", "global")
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-broken.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "workbuddy-dir.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WB2A_AUTH_DIR", dir)
	fake := &creditFake{}
	client := &http.Client{Transport: fake}
	var out bytes.Buffer
	if code := run(nil, &out, client); code != 0 {
		t.Fatalf("code=%d", code)
	}
	var got creditOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("输出不是 JSON: %v\n%s", err, out.String())
	}
	if got.Service != "workbuddy" || got.TS == 0 {
		t.Fatalf("service/ts 不对: %+v", got)
	}
	if got.Total.Remain != 90 || got.Total.Used != 90 || got.Total.Size != 180 ||
		got.Total.Accounts != 4 || got.Total.OK != 2 || got.Total.Failed != 2 {
		t.Fatalf("total 不对: %+v", got.Total)
	}
	if len(got.Accounts) != 4 {
		t.Fatalf("accounts = %d", len(got.Accounts))
	}
	idx := map[string]int{}
	for i, a := range got.Accounts {
		idx[a.UID] = i
	}
	cn := got.Accounts[idx["u-cn"]]
	if !cn.OK || cn.Remain == nil || *cn.Remain != 60 || *cn.Used != 40 || *cn.Size != 100 || cn.Packages != 1 {
		t.Fatalf("CN 成功账号字段不对: %+v", cn)
	}
	nt := got.Accounts[idx["u-notoken"]]
	if nt.OK || nt.Error != "no accessToken" {
		t.Fatalf("无 token 账号不对: %+v", nt)
	}
	er := got.Accounts[idx["u-err"]]
	if er.OK || !strings.Contains(er.Error, "http 400") {
		t.Fatalf("接口报错账号不对: %+v", er)
	}
	glb := got.Accounts[idx["u-glb"]]
	if !glb.OK || glb.Remain == nil || *glb.Remain != 30 || *glb.Used != 50 || *glb.Size != 80 {
		t.Fatalf("global 账号聚合不对（TotalDosage 应抬到 80）: %+v", glb)
	}

	for _, r := range fake.requests() {
		switch {
		case strings.HasPrefix(r, billingHostCN()+"/v2/billing/meter/get-user-resource"):
		case strings.HasPrefix(r, billingHostGlobal()+"/billing/meter/get-user-resource"):
		default:
			t.Fatalf("请求域/路径不对: %s", r)
		}
	}
}

// -pretty：四行汇总 + 失败明细，不输出 JSON；WB2A_AUTH_DIR 生效。
func TestCreditPrettyAndAuthDir(t *testing.T) {
	setAccountGap0(t)
	dir := t.TempDir()
	writeCreditAuth(t, dir, "u-cn", "国内号", "at-cn", "cn")
	writeCreditAuth(t, dir, "u-error-long", "", "at-err", "cn") // 无昵称：失败行回落 uid 前 8 位
	t.Setenv("WB2A_AUTH_DIR", dir)

	fake := &creditFake{}
	var out bytes.Buffer
	if code := run([]string{"-pretty"}, &out, &http.Client{Transport: fake}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	text := out.String()
	if strings.Contains(text, `{"service"`) {
		t.Fatalf("pretty 不应输出 JSON:\n%s", text)
	}
	for _, want := range []string{
		"📊 WorkBuddy 积分日报",
		"账号: 1/2",
		"总计: 60/100",
		"剩余: 60%",
		"⚠️ u-error- http 400",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("pretty 输出缺少 %q:\n%s", want, text)
		}
	}
}

// 没有凭证：输出空 accounts 与零汇总，仍返回 0。
func TestCreditEmpty(t *testing.T) {
	setAccountGap0(t)
	t.Setenv("WB2A_AUTH_DIR", t.TempDir())
	var out bytes.Buffer
	if code := run(nil, &out, &http.Client{Transport: &creditFake{}}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	var got creditOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("输出不是 JSON: %v", err)
	}
	if got.Total.Accounts != 0 || got.Total.OK != 0 || got.Total.Failed != 0 || len(got.Accounts) != 0 {
		t.Fatalf("空目录输出不对: %+v", got)
	}
}

// billingBaseFor：显式 realm=global、workbuddy.ai 域、CN 缺省。
func TestCreditBillingBaseFor(t *testing.T) {
	cases := []struct {
		name   string
		realm  string
		domain string
		want   string
	}{
		{"显式 global", "global", "www.codebuddy.cn", billingBaseGlobal},
		{"GLOBAL 大写", "GLOBAL", "", billingBaseGlobal},
		{"workbuddy.ai 域", "", "www.workbuddy.ai", billingBaseGlobal},
		{"裸 workbuddy.ai", "", "workbuddy.ai", billingBaseGlobal},
		{"CN 域", "", "www.codebuddy.cn", billingBaseCN},
		{"空", "", "", billingBaseCN},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var af authFile
			af.Auth.Realm, af.Auth.Domain = c.realm, c.domain
			if got := billingBaseFor(&af); got != c.want {
				t.Fatalf("billingBaseFor = %q want %q", got, c.want)
			}
		})
	}
}

// packageRemainUsed：Cycle/非 Cycle、负值钳 0、remain 超 size、used 修正。
func TestCreditPackageRemainUsed(t *testing.T) {
	cases := []struct {
		name               string
		p                  resourcePackage
		remain, used, size int64
	}{
		{"非 cycle 推导 used", resourcePackage{CapacityRemain: 30, CapacitySize: 100}, 30, 70, 100},
		{"非 cycle 显式 used", resourcePackage{CapacityRemain: 30, CapacityUsed: 10, CapacitySize: 100}, 30, 10, 100},
		{"cycle 负剩余钳 0", resourcePackage{CycleCapacityRemain: -5, CycleCapacitySize: 50}, 0, 50, 50},
		{"cycle 剩余超上限", resourcePackage{CycleCapacityRemain: 80, CycleCapacitySize: 50}, 50, 0, 50},
		{"cycle used 更大时修正", resourcePackage{CycleCapacityRemain: 40, CycleCapacityUsed: 35, CycleCapacitySize: 50}, 15, 35, 50},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, u, s := packageRemainUsed(c.p)
			if r != c.remain || u != c.used || s != c.size {
				t.Fatalf("packageRemainUsed = (%d,%d,%d) want (%d,%d,%d)", r, u, s, c.remain, c.used, c.size)
			}
		})
	}
}
