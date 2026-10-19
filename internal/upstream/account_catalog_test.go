package upstream

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// identitySplitUpstream 假上游：和真上游一样按请求里的企业身份下发名单——
// X-User-Id 与 X-Enterprise-Id 都带才给企业名单，否则给个人名单（dsh-workbuddy-connect #123 实测口径）。
// 同时记下每个目录请求的头。
type identitySplitUpstream struct {
	mu      sync.Mutex
	headers map[string]http.Header // 路径后缀 → 最近一次请求头
}

func catalogJSON(ids ...string) string {
	var models, cli []string
	for _, id := range ids {
		models = append(models, `{"id":"`+id+`"}`)
		cli = append(cli, `"`+id+`"`)
	}
	return `{"code":0,"data":{"models":[` + strings.Join(models, ",") + `],"agents":[{"name":"cli","models":[` + strings.Join(cli, ",") + `]}]}}`
}

func (u *identitySplitUpstream) client() *Client {
	u.headers = map[string]http.Header{}
	return testClient(func(r *http.Request) (*http.Response, error) {
		u.mu.Lock()
		for _, p := range []string{"/v3/config", "/console/enterprises/personal/models"} {
			if strings.HasSuffix(r.URL.Path, p) {
				u.headers[p] = r.Header.Clone()
			}
		}
		u.mu.Unlock()
		if r.Header.Get("X-User-Id") != "" && r.Header.Get("X-Enterprise-Id") != "" {
			return jsonResp(200, catalogJSON("shared", "claude-x")), nil
		}
		return jsonResp(200, catalogJSON("shared", "personal-only")), nil
	})
}

func (u *identitySplitUpstream) header(path, key string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.headers[path].Get(key)
}

// TestCatalogIdentityHeaders 国内版企业号拉目录时两个目录端点都带上完整企业身份；个人号不带。
func TestCatalogIdentityHeaders(t *testing.T) {
	u := &identitySplitUpstream{}
	c := u.client()
	ent := &auth.Auth{UID: "e1", EnterpriseID: "ent-1", AccessToken: "at-e", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
	infos, err := c.FetchAccountCatalog(ent)
	if err != nil {
		t.Fatalf("企业号目录: %v", err)
	}
	if ids := modelIDs(infos); !reflect.DeepEqual(ids, []string{"claude-x", "shared"}) {
		t.Errorf("企业号应拿到企业名单，got %v", ids)
	}
	for _, p := range []string{"/v3/config", "/console/enterprises/personal/models"} {
		if u.header(p, "X-User-Id") != "e1" || u.header(p, "X-Enterprise-Id") != "ent-1" || u.header(p, "X-Tenant-Id") != "ent-1" {
			t.Errorf("%s 企业身份头不全: uid=%q ent=%q tenant=%q", p,
				u.header(p, "X-User-Id"), u.header(p, "X-Enterprise-Id"), u.header(p, "X-Tenant-Id"))
		}
	}

	per := &auth.Auth{UID: "p1", AccessToken: "at-p", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
	if _, err := c.FetchModels(per); err != nil {
		t.Fatalf("个人号目录: %v", err)
	}
	for _, p := range []string{"/v3/config", "/console/enterprises/personal/models"} {
		if u.header(p, "X-Enterprise-Id") != "" || u.header(p, "X-Tenant-Id") != "" {
			t.Errorf("%s 个人号不应带企业头", p)
		}
	}
	if _, err := c.FetchAccountCatalog(per); err == nil {
		t.Error("个人号不该单独拉目录")
	}

	// 国际版企业号没有实测数据：拉目录不加企业头。
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	gl := &auth.Auth{UID: "g1", EnterpriseID: "ent-g", AccessToken: "at-g", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
	req := httptest.NewRequest("GET", "https://global.example/v3/config", nil)
	injectCatalogIdentity(req, gl)
	if req.Header.Get("X-Enterprise-Id") != "" {
		t.Error("国际版企业号拉目录不应加企业头")
	}
}

// modelIDs 排序后的模型 id（v3 目录解析出来的顺序不固定）。
func modelIDs(infos []ModelInfo) []string {
	ids := make([]string, 0, len(infos))
	for _, mi := range infos {
		ids = append(ids, mi.ID)
	}
	sort.Strings(ids)
	return ids
}

// TestServesModel 按目录判断能不能接：企业号只接自己目录里的；企业专属模型不派给个人号和其他域；
// 企业号目录没拉到时放行；域目录没拉到时不判定企业专属；目录清掉后回到放行。
func TestServesModel(t *testing.T) {
	u := &identitySplitUpstream{}
	c := u.client()
	ent := &auth.Auth{UID: "e1", EnterpriseID: "ent-1", AccessToken: "at-e", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
	per := &auth.Auth{UID: "p1", AccessToken: "at-p", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}

	if !c.ServesModel("p1", "cn", false, "claude-x") {
		t.Fatal("还没有任何目录时一律放行")
	}
	if _, err := c.FetchAccountCatalog(ent); err != nil {
		t.Fatal(err)
	}
	if !c.ServesModel("p1", "cn", false, "claude-x") || c.EnterpriseOnlyModels("cn") != nil {
		t.Fatal("域目录没拉到时不判定企业专属")
	}
	if _, err := c.FetchModels(per); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		uid, realm string
		enterprise bool
		model      string
		want       bool
	}{
		{"个人号不接企业专属模型", "p1", "cn", false, "claude-x", false},
		{"个人号照常接两边都有的模型", "p1", "cn", false, "shared", true},
		{"个人号接目录里没有的模型时放行（与现状一致）", "p1", "cn", false, "unknown", true},
		{"企业号接自己目录里的企业专属模型", "e1", "cn", true, "claude-x", true},
		{"企业号不接自己目录里没有的模型", "e1", "cn", true, "personal-only", false},
		{"哪份目录都没有的模型企业号也放行（交给上游报错）", "e1", "cn", true, "unknown", true},
		{"目录没拉到的企业号先放行", "e2", "cn", true, "claude-x", true},
		{"国际版号不受国内版企业目录影响", "g1", "global", false, "claude-x", true},
		{"空模型放行", "p1", "cn", false, "", true},
	} {
		if got := c.ServesModel(tc.uid, tc.realm, tc.enterprise, tc.model); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if !c.RealmHasModel("cn", "claude-x") {
		t.Error("企业专属模型也算国内版有（跨域选号时据此留在国内版）")
	}
	if ids := modelIDs(c.EnterpriseOnlyModels("cn")); !reflect.DeepEqual(ids, []string{"claude-x"}) {
		t.Errorf("EnterpriseOnlyModels=%v want [claude-x]", ids)
	}
	if c.EnterpriseOnlyModels("global") != nil {
		t.Error("国际版没有企业专属模型")
	}

	c.PruneAccountCatalogs(map[string]bool{})
	if !c.ServesModel("p1", "cn", false, "claude-x") || len(c.EnterpriseOnlyModels("cn")) != 0 {
		t.Error("企业号目录清掉后不再判定企业专属")
	}
	if _, ok := c.AccountCatalogFetched("e1"); ok {
		t.Error("清掉的目录不应还在")
	}
}

// TestGlobalEnterpriseChatHeader 国际版企业号对话带企业 ID，个人号照旧声明无企业。
func TestGlobalEnterpriseChatHeader(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	c := testClient(nil)
	for _, tc := range []struct {
		name       string
		a          *auth.Auth
		ent, noEnt string
	}{
		{"国际版企业号", &auth.Auth{UID: "g1", EnterpriseID: "ent-g", AccessToken: "at", Domain: "www.workbuddy.ai"}, "ent-g", ""},
		{"国际版个人号", &auth.Auth{UID: "g2", AccessToken: "at", Domain: "www.workbuddy.ai"}, "", "1"},
	} {
		req := httptest.NewRequest("POST", "https://global.example/v2/chat/completions", nil)
		c.ChatHeaders(req, tc.a, "", ChatMeta{})
		if got := req.Header.Get("X-Enterprise-Id"); got != tc.ent {
			t.Errorf("%s: X-Enterprise-Id=%q want %q", tc.name, got, tc.ent)
		}
		if got := req.Header.Get("X-No-Enterprise-Id"); got != tc.noEnt {
			t.Errorf("%s: X-No-Enterprise-Id=%q want %q", tc.name, got, tc.noEnt)
		}
		if req.Header.Get("X-Domain") != "www.workbuddy.ai" {
			t.Errorf("%s: X-Domain 应固定为国际版域", tc.name)
		}
	}
}
