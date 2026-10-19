package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLockedAccessors 并发访问器：加解锁可用；nil 接收者返回空串；非 nil 返回字段值。
func TestLockedAccessors(t *testing.T) {
	a := &Auth{AccessToken: "at", RefreshToken: "rt", Domain: "www.workbuddy.cn"}
	a.Lock()
	a.Nickname = "加锁后写入"
	a.Unlock()

	if got := a.AccessTokenValue(); got != "at" {
		t.Errorf("AccessTokenValue = %q", got)
	}
	if got := a.RefreshTokenValue(); got != "rt" {
		t.Errorf("RefreshTokenValue = %q", got)
	}
	if got := a.DomainValue(); got != "www.workbuddy.cn" {
		t.Errorf("DomainValue = %q", got)
	}

	var nilAuth *Auth
	if nilAuth.AccessTokenValue() != "" || nilAuth.DomainValue() != "" || nilAuth.RefreshTokenValue() != "" {
		t.Error("nil 接收者应返回空串")
	}
}

// TestGlobalEnabledToggle 全局开关的读取与关闭后的逃生门行为（关闭后 realm 恒 cn）。
func TestGlobalEnabledToggle(t *testing.T) {
	if !GlobalEnabled() {
		t.Fatal("默认应为开启")
	}
	SetGlobalEnabled(false)
	t.Cleanup(func() { SetGlobalEnabled(true) })
	if GlobalEnabled() {
		t.Error("SetGlobalEnabled(false) 后应为关闭")
	}
	a := &Auth{Domain: "www.workbuddy.ai"}
	if got := a.Realm(); got != "cn" {
		t.Errorf("逃生门关闭后 Realm = %q，应为 cn", got)
	}
	// ResolveRealm 不受逃生门影响（标识判定不受路由锁影响）。
	if got := ResolveRealm("", "www.workbuddy.ai"); got != "global" {
		t.Errorf("ResolveRealm = %q，应为 global", got)
	}
}

// TestBackfillRealmForExplicit 显式写入 realm：nil/非法值报错，相同不变更，不同写入（去掉两端空白）。
func TestBackfillRealmForExplicit(t *testing.T) {
	if _, err := BackfillRealmFor(nil, "cn"); err == nil {
		t.Error("nil auth 应返回错误")
	}
	a := &Auth{Domain: "www.codebuddy.cn"}
	if _, err := BackfillRealmFor(a, "us"); err == nil || !strings.Contains(err.Error(), "cn/global") {
		t.Errorf("非法 realm 应报错，got %v", err)
	}
	changed, err := BackfillRealmFor(a, "global")
	if err != nil || !changed || a.RealmStored() != "global" {
		t.Errorf("写入 global：changed=%v err=%v stored=%q", changed, err, a.RealmStored())
	}
	changed, err = BackfillRealmFor(a, "global")
	if err != nil || changed {
		t.Errorf("相同值不应变更：changed=%v err=%v", changed, err)
	}
	changed, err = BackfillRealmFor(a, " cn ")
	if err != nil || !changed {
		t.Errorf("写入 cn：changed=%v err=%v", changed, err)
	}
	if got := a.RealmStored(); got != "cn" {
		t.Errorf("写入前应去掉两端空白，stored=%q", got)
	}
	changed, err = BackfillRealmFor(a, "cn ")
	if err != nil || changed {
		t.Errorf("去掉空白后相同不应变更：changed=%v err=%v", changed, err)
	}
}

// TestParseShapes 凭证文件解析的边界形态：空/坏 JSON/嵌套类型错误/完整字段。
func TestParseShapes(t *testing.T) {
	if _, err := Parse(nil); err == nil || !strings.Contains(err.Error(), "empty auth storage") {
		t.Errorf("空输入应报错，got %v", err)
	}
	if _, err := Parse([]byte(`not-json`)); err == nil || !strings.Contains(err.Error(), "storage_parse_error") {
		t.Errorf("坏 JSON 应报错，got %v", err)
	}
	if _, err := Parse([]byte(`{"auth":"bad"}`)); err == nil || !strings.Contains(err.Error(), "storage_parse_error") {
		t.Errorf("嵌套 auth 类型错误应报错，got %v", err)
	}
	if _, err := Parse([]byte(`{"accessToken":123}`)); err == nil || !strings.Contains(err.Error(), "storage_parse_error") {
		t.Errorf("扁平形字段类型错误应报错，got %v", err)
	}
	if _, err := Parse([]byte(`{}`)); err == nil || !strings.Contains(err.Error(), "missing accessToken") {
		t.Errorf("缺 accessToken 应报错，got %v", err)
	}
	if _, err := Parse([]byte(`{"accessToken":"  "}`)); err == nil {
		t.Error("空白 accessToken 应报错")
	}

	nested := []byte(`{"auth":{"accessToken":"at","refreshToken":"rt","expiresAt":123,"domain":"www.workbuddy.ai","realm":"global"},
		"account":{"uid":"u1","enterpriseId":"e1","nickname":"n1"},"device_token":"dt"}`)
	a, err := Parse(nested)
	if err != nil {
		t.Fatalf("嵌套形解析: %v", err)
	}
	if a.AccessToken != "at" || a.RefreshToken != "rt" || a.ExpiresAt != 123 || a.Domain != "www.workbuddy.ai" ||
		a.RealmStored() != "global" || a.UID != "u1" || a.EnterpriseID != "e1" || a.Nickname != "n1" || a.DeviceToken != "dt" {
		t.Errorf("嵌套形 = %+v", a)
	}

	flat := []byte(`{"accessToken":"at","refreshToken":"rt","expiresAt":9,"domain":"d","realm":"cn",
		"uid":"u2","enterpriseId":"e2","nickname":"n2","device_token":"dt2","unknown":true}`)
	b, err := Parse(flat)
	if err != nil {
		t.Fatalf("扁平形解析: %v", err)
	}
	if b.AccessToken != "at" || b.ExpiresAt != 9 || b.Domain != "d" || b.RealmStored() != "cn" ||
		b.UID != "u2" || b.EnterpriseID != "e2" || b.Nickname != "n2" || b.DeviceToken != "dt2" {
		t.Errorf("扁平形 = %+v", b)
	}
}

// TestDocumentShape Document 与 SaveAtomic 写盘内容一致；device_token 仅非空才写。
func TestDocumentShape(t *testing.T) {
	a := &Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 5, Domain: "d", realm: "global",
		UID: "u1", EnterpriseID: "e1", Nickname: "n1", DeviceToken: "dt"}
	doc := a.Document()
	inner, ok := doc["auth"].(map[string]any)
	if !ok {
		t.Fatalf("auth 段 = %v", doc["auth"])
	}
	if inner["accessToken"] != "at" || inner["refreshToken"] != "rt" || inner["expiresAt"] != int64(5) ||
		inner["domain"] != "d" || inner["realm"] != "global" {
		t.Errorf("auth 段 = %v", inner)
	}
	acct := doc["account"].(map[string]any)
	if acct["uid"] != "u1" || acct["enterpriseId"] != "e1" || acct["nickname"] != "n1" {
		t.Errorf("account 段 = %v", acct)
	}
	if doc["device_token"] != "dt" {
		t.Errorf("device_token = %v", doc["device_token"])
	}

	a.DeviceToken = ""
	if _, ok := a.Document()["device_token"]; ok {
		t.Error("空 device_token 不应写回")
	}

	// SaveAtomic 写出的内容可被 Parse 读回同样的字段。
	fp := filepath.Join(t.TempDir(), "workbuddy-u1.json")
	a.FilePath = fp
	if err := a.SaveAtomic(); err != nil {
		t.Fatalf("SaveAtomic: %v", err)
	}
	raw, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	back, err := Parse(raw)
	if err != nil {
		t.Fatalf("重新解析: %v", err)
	}
	if back.AccessToken != "at" || back.UID != "u1" || back.RealmStored() != "global" || back.DeviceToken != "" {
		t.Errorf("读回 = %+v", back)
	}
}

// TestSaveAtomicRefusals 空 accessToken / 未设 FilePath 拒绝写盘；目录不可写报错（root 跳过）。
func TestSaveAtomicRefusals(t *testing.T) {
	if err := (&Auth{FilePath: filepath.Join(t.TempDir(), "f.json")}).SaveAtomic(); err == nil ||
		!strings.Contains(err.Error(), "empty accessToken") {
		t.Errorf("空 token 应拒绝，got %v", err)
	}
	if err := (&Auth{AccessToken: "at"}).SaveAtomic(); err == nil || !strings.Contains(err.Error(), "no FilePath") {
		t.Errorf("无 FilePath 应拒绝，got %v", err)
	}

	if os.Geteuid() == 0 {
		t.Skip("root 下目录权限不生效")
	}
	dir := t.TempDir()
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatalf("建只读目录: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	err := (&Auth{AccessToken: "at", UID: "u1", FilePath: filepath.Join(ro, "f.json")}).SaveAtomic()
	if err == nil || !strings.Contains(err.Error(), "失败") {
		t.Errorf("不可写目录应报错，got %v", err)
	}
}

// TestLoadDirReadErrorSkips 读失败的文件静默跳过；glob 模式非法返回错误。
func TestLoadDirReadErrorSkips(t *testing.T) {
	dir := t.TempDir()
	// 目录名匹配 workbuddy*.json：ReadFile 失败 → 跳过。
	if err := os.Mkdir(filepath.Join(dir, "workbuddy-dir.json"), 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	good := filepath.Join(dir, "workbuddy-u1.json")
	if err := os.WriteFile(good, []byte(`{"accessToken":"at","uid":"u1","realm":"cn"}`), 0o600); err != nil {
		t.Fatalf("写凭证: %v", err)
	}
	got, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(got) != 1 || got[0].UID != "u1" {
		t.Errorf("加载结果 = %+v", got)
	}

	if _, err := LoadDir("["); err == nil {
		t.Error("非法 glob 模式应返回错误")
	}
}

// TestNeedsRefreshBoundary 过期判断：无 expiry 恒需刷新；窗口内/外按 ExpiresAt。
func TestNeedsRefreshBoundary(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name      string
		expiresAt int64
		within    time.Duration
		want      bool
	}{
		{"无 expiry", 0, time.Minute, true},
		{"已过期", now - 10, time.Minute, true},
		{"窗口内", now + 30, time.Minute, true},
		{"窗口外", now + 2*3600, time.Minute, false},
		{"稍超窗口", now + 120, time.Minute, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Auth{ExpiresAt: tc.expiresAt}
			if got := a.NeedsRefresh(tc.within); got != tc.want {
				t.Errorf("NeedsRefresh = %v，应为 %v", got, tc.want)
			}
		})
	}
}

// TestRealmBoundary realm 判定边界：显式值去空白、域名大小写与后缀、伪后缀。
func TestRealmBoundary(t *testing.T) {
	cases := []struct {
		name string
		auth *Auth
		want string
	}{
		{"显式反复空白", &Auth{realm: " global "}, "global"},
		{"裸域", &Auth{Domain: "workbuddy.ai"}, "global"},
		{"子域大写", &Auth{Domain: "WWW.WORKBUDDY.AI"}, "global"},
		{"任意子域", &Auth{Domain: "api.workbuddy.ai"}, "global"},
		{"伪后缀不算", &Auth{Domain: "evilworkbuddy.ai"}, "cn"},
		{"显式非 global 但有 global 域", &Auth{realm: "weird", Domain: "www.workbuddy.ai"}, "global"},
		{"显式 cn 但 global 域仍判 global", &Auth{realm: "cn", Domain: "www.workbuddy.ai"}, "global"},
		{"空白域", &Auth{Domain: "  "}, "cn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.auth
			if got := a.Realm(); got != tc.want {
				t.Errorf("Realm = %q，应为 %q", got, tc.want)
			}
		})
	}

	if (&Auth{EnterpriseID: "  "}).IsEnterprise() {
		t.Error("空白 enterpriseId 不算企业号")
	}
	if !(&Auth{EnterpriseID: " e1 "}).IsEnterprise() {
		t.Error("非空 enterpriseId（含空白）应算企业号")
	}
}
