package upstream

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

const (
	gmV3Path     = "/v3/config"
	gmEntV2Path  = "/v2/enterprises/personal/models"
	gmEntConPath = "/console/enterprises/personal/models"
)

// upGlobalClient global 账号客户端：所有探测都落到假上游。
func upGlobalClient(f *upFake) *Client {
	c := upClient(f)
	c.ChatBaseGlobal = upBase
	return c
}

func v3ModelsJSON(models string) string {
	return `{"code":0,"data":{"models":[` + models + `]}}`
}

func entModelsJSON(models string) string {
	return `{"code":0,"data":{"models":[` + models + `]}}`
}

// sameStringSet 比较两个名字集合（顺序不敏感）。
func sameStringSet(got, want []string) bool {
	a := append([]string(nil), got...)
	b := append([]string(nil), want...)
	sort.Strings(a)
	sort.Strings(b)
	return reflect.DeepEqual(a, b)
}

func gmFindInfo(infos []ModelInfo, id string) (ModelInfo, bool) {
	for _, mi := range infos {
		if mi.ID == id {
			return mi, true
		}
	}
	return ModelInfo{}, false
}

// TestFetchGlobalModelsNonGlobal 非 global 账号或逃生门关闭时不探测、零请求。
func TestFetchGlobalModelsNonGlobal(t *testing.T) {
	f := newUpFake(t)
	c := upGlobalClient(f)
	cn := &auth.Auth{UID: "u-cn", AccessToken: "at", Domain: "www.codebuddy.cn"}
	if names := c.FetchGlobalModels(cn); names != nil {
		t.Errorf("国内版账号应返回 nil，got %v", names)
	}
	if infos := c.FetchGlobalModelInfos(cn); infos != nil {
		t.Errorf("国内版账号 infos 应为 nil，got %v", infos)
	}
	c.GlobalEnabled = false
	if names := c.FetchGlobalModels(globalAuth()); names != nil {
		t.Errorf("逃生门关闭应返回 nil，got %v", names)
	}
	if n := len(f.seen()); n != 0 {
		t.Errorf("不应发请求，实际 %d 次", n)
	}
}

// TestFetchGlobalModelsMergesRoutes 三路 /v3/config + 企业端点并发探测：
// 主路字段权威、副路只补缺失 id；结果缓存 1h（二次调用零请求）；
// effort 桶 / 倍率 / 域模型集合同步落缓存。
func TestFetchGlobalModelsMergesRoutes(t *testing.T) {
	f := newUpFake(t)
	f.onFunc(http.MethodGet, gmV3Path, func(r *upReq) (*http.Response, error) {
		switch r.header.Get("User-Agent") {
		case codeBuddyIDEUA:
			return jsonResp(200, v3ModelsJSON(`{"id":"shared","maxInputTokens":999},{"id":"ide-only","maxInputTokens":200}`)), nil
		case codeBuddyCLIUA:
			return jsonResp(200, v3ModelsJSON(`{"id":"cli-only","maxInputTokens":300}`)), nil
		default: // 桌面端主路
			return jsonResp(200, v3ModelsJSON(`{"id":"shared","maxInputTokens":111,"maxOutputTokens":1000,"credits":"x0.5",
				"reasoning":{"supportedEfforts":["low","high"],"defaultEffort":"high"}},
				{"id":"desktop-only","maxInputTokens":222},
				{"id":"nonchat","tags":["text-to-video"]}`)), nil
		}
	})
	f.on(http.MethodGet, gmEntV2Path, 200, entModelsJSON(`{"id":"ent-only","maxInputTokens":333},{"id":"desktop-only","maxInputTokens":444}`))

	c := upGlobalClient(f)
	names := c.FetchGlobalModels(globalAuth())
	if want := []string{"shared", "desktop-only", "ide-only", "cli-only", "ent-only"}; !sameStringSet(names, want) {
		t.Fatalf("names = %v，应为集合 %v", names, want)
	}
	if n := f.count(http.MethodGet, gmV3Path); n != 3 {
		t.Errorf("v3/config 应探测 3 个 UA，实际 %d 次", n)
	}
	if n := f.count(http.MethodGet, gmEntV2Path); n != 1 {
		t.Errorf("企业端点应探测 1 次，实际 %d", n)
	}

	infos := c.FetchGlobalModelInfos(globalAuth())
	if len(infos) == 0 {
		t.Fatal("对象形态探测应返回 infos")
	}
	shared, ok := gmFindInfo(infos, "shared")
	if !ok || shared.ContextWindow != 111 || shared.MaxTokens != 1000 || shared.Credits != "x0.5" {
		t.Errorf("shared 字段应取自主路: %+v/%v", shared, ok)
	}
	desktopOnly, ok := gmFindInfo(infos, "desktop-only")
	if !ok || desktopOnly.ContextWindow != 222 {
		t.Errorf("desktop-only 字段应取自主路（非企业端点）: %+v/%v", desktopOnly, ok)
	}
	entOnly, ok := gmFindInfo(infos, "ent-only")
	if !ok || entOnly.ContextWindow != 333 {
		t.Errorf("ent-only 应来自企业端点: %+v/%v", entOnly, ok)
	}

	efforts, defaults := c.GlobalEffortSnapshot()
	if !reflect.DeepEqual(efforts["shared"], []string{"low", "high"}) || defaults["shared"] != "high" {
		t.Errorf("effort 桶 = %v/%v", efforts["shared"], defaults["shared"])
	}
	if got := c.ModelRate("global", "shared"); got != "0.5" {
		t.Errorf("ModelRate = %q，应为 0.5", got)
	}
	if !c.RealmHasModel("global", "shared") || !c.RealmHasModel("global", "ent-only") {
		t.Error("域模型集合应包含并集模型")
	}

	// 缓存命中：1h 内再查零请求。
	before := len(f.seen())
	c.FetchGlobalModels(globalAuth())
	c.FetchGlobalModelInfos(globalAuth())
	if after := len(f.seen()); after != before {
		t.Errorf("缓存期内不应再请求，%d → %d", before, after)
	}
}

// TestFetchGlobalModelsCooldown 全部路径失败：返回 nil、记负缓存；冷却期内
// 不再请求；冷却到期后重新探测。
func TestFetchGlobalModelsCooldown(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, gmV3Path, 500, `{"code":500,"msg":"boom"}`)
	f.on(http.MethodGet, gmEntV2Path, 500, `{"code":500,"msg":"boom"}`)
	f.on(http.MethodGet, gmEntConPath, 500, `{"code":500,"msg":"boom"}`)
	c := upGlobalClient(f)

	if names := c.FetchGlobalModels(globalAuth()); names != nil {
		t.Fatalf("全失败应返回 nil，got %v", names)
	}
	if c.globalModels.lastFail.IsZero() {
		t.Error("失败应记负缓存时间")
	}
	first := len(f.seen())
	if first != 5 {
		t.Errorf("首次探测请求数 = %d，应为 5（3 v3 + 2 企业路径）", first)
	}

	c.FetchGlobalModels(globalAuth())
	if n := len(f.seen()); n != first {
		t.Errorf("冷却期内不应再请求，%d → %d", first, n)
	}

	c.globalModels.lastFail = time.Now().Add(-globalModelsFailCooldown - time.Minute)
	c.FetchGlobalModels(globalAuth())
	if n := len(f.seen()); n != first*2 {
		t.Errorf("冷却到期应重新探测，%d → %d", first, n)
	}
}

// TestFetchGlobalModelsPartialFailure 单路失败降级：
// v3 全失败时用企业端点；企业端点失败时用 v3。
func TestFetchGlobalModelsPartialFailure(t *testing.T) {
	t.Run("v3 失败用企业端点", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, gmV3Path, 500, `{"code":500,"msg":"v3 boom"}`)
		f.on(http.MethodGet, gmEntV2Path, 200, entModelsJSON(`{"id":"ent-only","maxInputTokens":333}`))
		c := upGlobalClient(f)
		names := c.FetchGlobalModels(globalAuth())
		if !sameStringSet(names, []string{"ent-only"}) {
			t.Fatalf("names = %v，应为 [ent-only]", names)
		}
		if infos := c.FetchGlobalModelInfos(globalAuth()); len(infos) != 1 || infos[0].ID != "ent-only" {
			t.Errorf("infos = %+v", infos)
		}
	})

	t.Run("企业端点失败用 v3", func(t *testing.T) {
		f := newUpFake(t)
		f.onFunc(http.MethodGet, gmV3Path, func(r *upReq) (*http.Response, error) {
			switch r.header.Get("User-Agent") {
			case codeBuddyIDEUA:
				return jsonResp(200, v3ModelsJSON(`{"id":"ide-only"}`)), nil
			case codeBuddyCLIUA:
				return jsonResp(200, v3ModelsJSON(`{"id":"cli-only"}`)), nil
			default:
				return jsonResp(200, v3ModelsJSON(`{"id":"desktop-only"}`)), nil
			}
		})
		f.on(http.MethodGet, gmEntV2Path, 500, `{"code":500,"msg":"ent boom"}`)
		f.on(http.MethodGet, gmEntConPath, 500, `{"code":500,"msg":"ent boom"}`)
		c := upGlobalClient(f)
		names := c.FetchGlobalModels(globalAuth())
		if !sameStringSet(names, []string{"desktop-only", "ide-only", "cli-only"}) {
			t.Fatalf("names = %v", names)
		}
	})

	t.Run("企业端点 v2 失败降级 console", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, gmV3Path, 500, `{"code":500,"msg":"v3 boom"}`)
		f.on(http.MethodGet, gmEntV2Path, 400, `{"code":400,"msg":"no"}`)
		f.on(http.MethodGet, gmEntConPath, 200, entModelsJSON(`{"id":"console-only"}`))
		c := upGlobalClient(f)
		names := c.FetchGlobalModels(globalAuth())
		if !sameStringSet(names, []string{"console-only"}) {
			t.Fatalf("names = %v，应为 [console-only]", names)
		}
		if n := f.count(http.MethodGet, gmEntV2Path); n != 1 {
			t.Errorf("/v2 应尝试 1 次，实际 %d", n)
		}
		if n := f.count(http.MethodGet, gmEntConPath); n != 1 {
			t.Errorf("/console 应尝试 1 次，实际 %d", n)
		}
	})
}

// TestGlobalModelsOnce 单端点探测：请求头、成功解析、非 200/空列表/非 JSON/
// 读失败/传输层错误/URL 非法。
func TestGlobalModelsOnce(t *testing.T) {
	t.Run("成功与请求头", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, gmEntV2Path, 200, entModelsJSON(`{"id":"m","maxInputTokens":10}`))
		c := upGlobalClient(f)
		names, infos, err := c.globalModelsOnce(globalAuth(), gmEntV2Path)
		if err != nil || !sameStringSet(names, []string{"m"}) || len(infos) != 1 {
			t.Fatalf("names=%v infos=%v err=%v", names, infos, err)
		}
		r := f.seen()[0]
		if want := "Bearer " + globalAuth().AccessTokenValue(); r.header.Get("Authorization") != want {
			t.Errorf("Authorization = %q，应为 %q", r.header.Get("Authorization"), want)
		}
		if !strings.Contains(r.header.Get("Origin"), "workbuddy.ai") {
			t.Errorf("global Origin = %q", r.header.Get("Origin"))
		}
		if r.header.Get("User-Agent") == "" || r.header.Get("X-CodeBuddy-Request") != "1" {
			t.Errorf("公共头不全: %v", r.header)
		}
	})

	cases := []struct {
		name string
		fn   upRoute
	}{
		{"非 200", func(*upReq) (*http.Response, error) {
			return jsonResp(400, `{"code":400,"msg":"models boom"}`), nil
		}},
		{"空列表", func(*upReq) (*http.Response, error) {
			return jsonResp(200, `{"code":0,"data":[]}`), nil
		}},
		{"非 JSON", func(*upReq) (*http.Response, error) {
			return jsonResp(200, `oops`), nil
		}},
		{"读 body 失败", func(*upReq) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(errReader{})}, nil
		}},
		{"传输层错误", func(*upReq) (*http.Response, error) {
			return nil, errors.New("dial boom")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.onFunc(http.MethodGet, gmEntV2Path, tc.fn)
			c := upGlobalClient(f)
			if _, _, err := c.globalModelsOnce(globalAuth(), gmEntV2Path); err == nil {
				t.Fatal("应返回错误")
			}
		})
	}

	t.Run("URL 非法", func(t *testing.T) {
		c := &Client{HTTP: newUpFake(t).client(), GlobalEnabled: true, ChatBaseGlobal: "://bad"}
		if _, _, err := c.globalModelsOnce(globalAuth(), gmEntV2Path); err == nil {
			t.Fatal("非法 base 应返回错误")
		}
	})
}

// TestMergeGlobalCatalog 两路合并：主列优先、去重、空 id 跳过、企业端点只补
// 缺失条目；窄表 infos 保持 nil 语义。
func TestMergeGlobalCatalog(t *testing.T) {
	t.Run("副列表为空直接返回主列表", func(t *testing.T) {
		names, infos := mergeGlobalCatalog([]string{"a"}, []ModelInfo{{ID: "a"}}, nil, nil)
		if !reflect.DeepEqual(names, []string{"a"}) || len(infos) != 1 {
			t.Errorf("names=%v infos=%v", names, infos)
		}
	})

	t.Run("去重并保持主列表顺序", func(t *testing.T) {
		primary := []string{"", "a", "b", "a"}
		primaryInfos := []ModelInfo{{ID: "a", ContextWindow: 1}, {ID: "b", ContextWindow: 2}}
		secondary := []string{"b", "c", "", "d"}
		secondaryInfos := []ModelInfo{{ID: "b", ContextWindow: 999}, {ID: "c", ContextWindow: 3}}
		names, infos := mergeGlobalCatalog(primary, primaryInfos, secondary, secondaryInfos)
		if want := []string{"a", "b", "c", "d"}; !reflect.DeepEqual(names, want) {
			t.Errorf("names = %v，应为 %v", names, want)
		}
		if len(infos) != 3 {
			t.Fatalf("infos = %+v", infos)
		}
		if infos[1].ID != "b" || infos[1].ContextWindow != 2 {
			t.Errorf("同 id 应保留主路字段: %+v", infos[1])
		}
		if infos[2].ID != "c" || infos[2].ContextWindow != 3 {
			t.Errorf("副列表缺失 id 应补其条目: %+v", infos[2])
		}
	})

	t.Run("主列表窄表时副列表对象条目按 id 匹配", func(t *testing.T) {
		names, infos := mergeGlobalCatalog(
			[]string{"a"}, nil,
			[]string{"b", "c"},
			[]ModelInfo{{ID: "c", ContextWindow: 8}, {ID: "zzz", ContextWindow: 9}},
		)
		if want := []string{"a", "b", "c"}; !reflect.DeepEqual(names, want) {
			t.Errorf("names = %v", names)
		}
		if len(infos) != 1 || infos[0].ID != "c" || infos[0].ContextWindow != 8 {
			t.Errorf("infos = %+v", infos)
		}
	})

	t.Run("两路都窄表不编造 infos", func(t *testing.T) {
		names, infos := mergeGlobalCatalog([]string{"a"}, nil, []string{"b"}, nil)
		if want := []string{"a", "b"}; !reflect.DeepEqual(names, want) {
			t.Errorf("names = %v", names)
		}
		if infos != nil {
			t.Errorf("窄表应保持 infos=nil，got %+v", infos)
		}
	})

	t.Run("副列表全重复", func(t *testing.T) {
		names, infos := mergeGlobalCatalog([]string{"a"}, []ModelInfo{{ID: "a"}}, []string{"a"}, nil)
		if !reflect.DeepEqual(names, []string{"a"}) || len(infos) != 1 {
			t.Errorf("names=%v infos=%v", names, infos)
		}
	})
}

// TestMergeEfforts effort 桶合并：主路权威，企业端点只补缺失键。
func TestMergeEfforts(t *testing.T) {
	got := mergeEffortBuckets(
		map[string][]string{"a": {"low"}},
		map[string][]string{"a": {"max"}, "b": {"high"}},
	)
	if !reflect.DeepEqual(got["a"], []string{"low"}) || !reflect.DeepEqual(got["b"], []string{"high"}) {
		t.Errorf("bucket 合并 = %v", got)
	}
	if m := mergeEffortBuckets(map[string][]string{"a": {"low"}}, nil); len(m) != 1 {
		t.Errorf("副桶为空应原样返回主桶: %v", m)
	}
	if m := mergeEffortBuckets(nil, map[string][]string{"a": {"low"}, "b": {"high"}}); len(m) != 2 {
		t.Errorf("主桶为空应复制副桶: %v", m)
	}

	gotD := mergeEffortDefaults(
		map[string]string{"a": "low"},
		map[string]string{"a": "max", "b": "high"},
	)
	if gotD["a"] != "low" || gotD["b"] != "high" {
		t.Errorf("defaults 合并 = %v", gotD)
	}
	if m := mergeEffortDefaults(map[string]string{"a": "low"}, nil); len(m) != 1 {
		t.Errorf("副表为空应原样返回主表: %v", m)
	}
	if m := mergeEffortDefaults(nil, map[string]string{"a": "low", "b": "high"}); len(m) != 2 {
		t.Errorf("主表为空应复制副表: %v", m)
	}
}

// TestExtractEfforts 从条目抽 effort 桶并生成有序 names；空 id 跳过。
func TestExtractEfforts(t *testing.T) {
	infos := []ModelInfo{
		{ID: ""},
		{ID: "a", Efforts: []string{"low", "high"}, DefaultEffort: "high"},
		{ID: "b", DefaultEffort: "medium"},
		{ID: "c"},
	}
	names, out, efforts, defaults := extractEfforts(infos)
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v，应为 %v", names, want)
	}
	if len(out) != 3 || out[0].ID != "a" {
		t.Errorf("out = %+v", out)
	}
	if !reflect.DeepEqual(efforts, map[string][]string{"a": {"low", "high"}}) {
		t.Errorf("efforts = %v", efforts)
	}
	if !reflect.DeepEqual(defaults, map[string]string{"a": "high", "b": "medium"}) {
		t.Errorf("defaults = %v", defaults)
	}

	names, out, efforts, defaults = extractEfforts(nil)
	if len(names) != 0 || len(out) != 0 || efforts != nil || defaults != nil {
		t.Errorf("空输入 = %v/%v/%v/%v", names, out, efforts, defaults)
	}
}

// TestMergeV3RoutesEmptyUnion 全路成功但并集为空：保留空结果、不返回错误。
func TestMergeV3RoutesEmptyUnion(t *testing.T) {
	got := mergeV3Routes([]struct {
		label string
		res   probeResult
	}{
		{"desktop-UA", probeResult{}},
		{"IDE-UA", probeResult{}},
	})
	if got.err != nil || len(got.names) != 0 || len(got.infos) != 0 {
		t.Errorf("空并集应保留空结果: %+v", got)
	}
}

// TestParseGlobalModelNames 多信封兼容解析：data 数组/对象、models/items/list/
// data/result 键、对象数组全字段、id 回退、disabled 剔除、窄表、混合数组与各种非法形态。
func TestParseGlobalModelNames(t *testing.T) {
	t.Run("对象数组全字段与 id 回退", func(t *testing.T) {
		raw := `{"code":0,"data":{"models":[
			{"id":"m1","name":"模型一","maxInputTokens":100,"maxOutputTokens":50,"maxAllowedSize":111,
			 "supportsReasoning":true,"supportsImages":true,"tags":["craft"],"credits":"x0.5",
			 "reasoning":{"supportedEfforts":["low","high"],"defaultEffort":"high"}},
			{"modelId":"m2"},{"model":"m3"},{"name":"m4"},
			{"id":"off","disabled":true},{"name":""}
		]}}`
		names, infos, efforts, defaults, err := parseGlobalModelNames([]byte(raw))
		if err != nil {
			t.Fatalf("解析: %v", err)
		}
		if want := []string{"m1", "m2", "m3", "m4"}; !reflect.DeepEqual(names, want) {
			t.Errorf("names = %v，应为 %v", names, want)
		}
		m1, ok := gmFindInfo(infos, "m1")
		if !ok || m1.ContextWindow != 100 || m1.MaxTokens != 50 || m1.MaxAllowedSize != 111 ||
			!m1.SupportsReasoning || !m1.SupportsImages || m1.Credits != "x0.5" {
			t.Errorf("m1 = %+v/%v", m1, ok)
		}
		if !reflect.DeepEqual(efforts["m1"], []string{"low", "high"}) || defaults["m1"] != "high" {
			t.Errorf("effort 桶 = %v/%v", efforts["m1"], defaults["m1"])
		}
		if m4, ok := gmFindInfo(infos, "m4"); !ok || m4.ID != "m4" {
			t.Errorf("name 兜底 id = %+v/%v", m4, ok)
		}
	})

	t.Run("老键 effort 在条目上兜底", func(t *testing.T) {
		_, infos, _, _, err := parseGlobalModelNames([]byte(`{"data":[{"id":"m","reasoning":{"effort":"medium"}}]}`))
		if err != nil {
			t.Fatalf("解析: %v", err)
		}
		m, ok := gmFindInfo(infos, "m")
		if !ok || m.DefaultEffort != "medium" {
			t.Fatalf("条目 DefaultEffort = %q/%v，应为 medium", m.DefaultEffort, ok)
		}
	})

	t.Run("窄表字符串数组", func(t *testing.T) {
		names, infos, efforts, defaults, err := parseGlobalModelNames([]byte(`{"code":0,"data":[" a ","","b"]}`))
		if err != nil {
			t.Fatalf("解析: %v", err)
		}
		if want := []string{"a", "b"}; !reflect.DeepEqual(names, want) {
			t.Errorf("names = %v，应为 %v", names, want)
		}
		if infos != nil || efforts != nil || defaults != nil {
			t.Errorf("窄表不应产生元数据: %v/%v/%v", infos, efforts, defaults)
		}
	})

	t.Run("窄表全空报错", func(t *testing.T) {
		if _, _, _, _, err := parseGlobalModelNames([]byte(`{"code":0,"data":[""," "]}`)); err == nil {
			t.Fatal("空名单应返回错误")
		}
	})

	t.Run("业务 code 非 0", func(t *testing.T) {
		_, _, _, _, err := parseGlobalModelNames([]byte(`{"code":7,"msg":"x","data":["m"]}`))
		if err == nil || !strings.Contains(err.Error(), "code=7") {
			t.Fatalf("err = %v，应含 code=7", err)
		}
	})

	t.Run("无 code 字段放行", func(t *testing.T) {
		names, _, _, _, err := parseGlobalModelNames([]byte(`{"models":["m"]}`))
		if err != nil || !reflect.DeepEqual(names, []string{"m"}) {
			t.Fatalf("names=%v err=%v", names, err)
		}
	})

	t.Run("code 为字符串 0 放行", func(t *testing.T) {
		names, _, _, _, err := parseGlobalModelNames([]byte(`{"code":"0","data":["m"]}`))
		if err != nil || !reflect.DeepEqual(names, []string{"m"}) {
			t.Fatalf("names=%v err=%v", names, err)
		}
	})

	t.Run("data 为 null 时用整包", func(t *testing.T) {
		names, _, _, _, err := parseGlobalModelNames([]byte(`{"code":0,"data":null,"models":["m"]}`))
		if err != nil || !reflect.DeepEqual(names, []string{"m"}) {
			t.Fatalf("names=%v err=%v", names, err)
		}
	})

	t.Run("递归 data.data.list", func(t *testing.T) {
		names, _, _, _, err := parseGlobalModelNames([]byte(`{"data":{"data":{"list":["m"]}}}`))
		if err != nil || !reflect.DeepEqual(names, []string{"m"}) {
			t.Fatalf("names=%v err=%v", names, err)
		}
	})

	t.Run("result 键", func(t *testing.T) {
		names, _, _, _, err := parseGlobalModelNames([]byte(`{"result":["m"]}`))
		if err != nil || !reflect.DeepEqual(names, []string{"m"}) {
			t.Fatalf("names=%v err=%v", names, err)
		}
	})

	t.Run("字段名大小写不同", func(t *testing.T) {
		names, _, _, _, err := parseGlobalModelNames([]byte(`{"data":[{"ID":"upper"}]}`))
		if err != nil || !reflect.DeepEqual(names, []string{"upper"}) {
			t.Fatalf("names=%v err=%v", names, err)
		}
	})

	t.Run("混合数组", func(t *testing.T) {
		names, infos, _, _, err := parseGlobalModelNames([]byte(`{"data":["s1",{"modelId":"m2","maxInputTokens":9},{"no":"id"}]}`))
		if err != nil {
			t.Fatalf("解析: %v", err)
		}
		if want := []string{"s1", "m2"}; !reflect.DeepEqual(names, want) {
			t.Errorf("names = %v，应为 %v", names, want)
		}
		if len(infos) != 2 || infos[0].ID != "s1" || infos[1].ContextWindow != 9 {
			t.Errorf("infos = %+v", infos)
		}
	})

	t.Run("数字数组报空列表", func(t *testing.T) {
		_, _, _, _, err := parseGlobalModelNames([]byte(`{"data":[1,2]}`))
		if err == nil || !strings.Contains(err.Error(), "empty list") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("对象数组全部无效", func(t *testing.T) {
		_, _, _, _, err := parseGlobalModelNames([]byte(`{"data":[{"foo":"bar"},{"disabled":true,"id":"x"}]}`))
		if err == nil {
			t.Fatal("无可用条目应返回错误")
		}
	})

	t.Run("空数组报错", func(t *testing.T) {
		if _, _, _, _, err := parseGlobalModelNames([]byte(`{"data":[]}`)); err == nil {
			t.Fatal("空数组应返回错误")
		}
	})

	t.Run("非法 JSON", func(t *testing.T) {
		if _, _, _, _, err := parseGlobalModelNames([]byte(`not-json`)); err == nil {
			t.Fatal("非法 JSON 应返回错误")
		}
	})

	t.Run("顶层直接数组也报错", func(t *testing.T) {
		// 信封层要求对象：裸数组无法解成 map（resolve 支持数组，但入口先解信封）。
		if _, _, _, _, err := parseGlobalModelNames([]byte(`[{"id":"m"}]`)); err == nil {
			t.Fatal("裸数组应返回解析错误")
		}
	})
}

// TestResolveGlobalModelsArray 数组定位：直接数组、五个候选键、递归下钻、失败形态。
func TestResolveGlobalModelsArray(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		want   string
		wantOK bool
	}{
		{"直接数组带空白", `  [ "a" ]  `, `[ "a" ]`, true},
		{"models 键", `{"models":["a"]}`, `["a"]`, true},
		{"items 键", `{"items":["b"]}`, `["b"]`, true},
		{"list 键", `{"list":["c"]}`, `["c"]`, true},
		{"data 键", `{"data":["d"]}`, `["d"]`, true},
		{"result 键", `{"result":["e"]}`, `["e"]`, true},
		{"嵌套下钻", `{"data":{"items":["f"]}}`, `["f"]`, true},
		{"键优先级 models 先于 result", `{"result":["r"],"models":["m"]}`, `["m"]`, true},
		{"没有数组", `{}`, ``, false},
		{"键不是数组", `{"models":"x"}`, ``, false},
		{"不是 JSON", `not-json`, ``, false},
		{"数字不是对象也不是数组", `123`, ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolveGlobalModelsArray(json.RawMessage(tc.raw))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v，应为 %v", ok, tc.wantOK)
			}
			if ok && strings.TrimSpace(string(got)) != tc.want {
				t.Errorf("got %q，应为 %q", got, tc.want)
			}
		})
	}
}

// TestParseGlobalModelLoose 宽松解析：id 回退、disabled 剔除、窗口键回退、
// reasoning 新老键与 supportedEfforts 清洗。
func TestParseGlobalModelLoose(t *testing.T) {
	full := map[string]any{
		"name": " 模型 ", "maxInputTokens": float64(100), "contextWindow": float64(999),
		"maxOutputTokens": float64(50), "maxTokens": float64(77),
		"maxAllowedSize":    float64(111),
		"supportsReasoning": true, "supportsImages": true,
		"credits": "x9",
		"reasoning": map[string]any{
			"defaultEffort": "high", "effort": "low", "canDisableThinking": true,
			"supportedEfforts": []any{" low ", "", 42, "high"},
		},
	}
	mi, ok := parseGlobalModelLoose(full)
	if !ok {
		t.Fatal("应解析成功")
	}
	if mi.ID != "模型" || mi.Name != "模型" {
		t.Errorf("name 回退 id = %q/%q", mi.ID, mi.Name)
	}
	if mi.ContextWindow != 100 || mi.MaxTokens != 50 || mi.MaxAllowedSize != 111 {
		t.Errorf("窗口字段 = %+v", mi)
	}
	if !mi.SupportsReasoning || !mi.SupportsImages {
		t.Errorf("能力字段 = %+v", mi)
	}
	if mi.DefaultEffort != "high" || !mi.CanDisableThinking {
		t.Errorf("reasoning = %+v", mi)
	}
	if !reflect.DeepEqual(mi.Efforts, []string{"low", "high"}) {
		t.Errorf("efforts = %v", mi.Efforts)
	}
	if mi.Credits != "" {
		t.Errorf("credits 不应解析，got %q", mi.Credits)
	}

	t.Run("id 回退顺序", func(t *testing.T) {
		for _, k := range []string{"id", "modelId", "model", "name"} {
			mi, ok := parseGlobalModelLoose(map[string]any{k: "x"})
			if !ok || mi.ID != "x" {
				t.Errorf("键 %s 回退失败: %+v/%v", k, mi, ok)
			}
		}
	})

	t.Run("没有 id", func(t *testing.T) {
		if _, ok := parseGlobalModelLoose(map[string]any{"name": ""}); ok {
			t.Error("无 id 应返回 false")
		}
	})

	t.Run("disabled 剔除", func(t *testing.T) {
		if _, ok := parseGlobalModelLoose(map[string]any{"id": "x", "disabled": true}); ok {
			t.Error("disabled=true 应剔除")
		}
		if _, ok := parseGlobalModelLoose(map[string]any{"id": "x", "disabled": false}); !ok {
			t.Error("disabled=false 应保留")
		}
		if _, ok := parseGlobalModelLoose(map[string]any{"id": "x", "disabled": "true"}); !ok {
			t.Error("非 bool 的 disabled 应忽略")
		}
	})

	t.Run("窗口键回退与非法值", func(t *testing.T) {
		mi, _ := parseGlobalModelLoose(map[string]any{
			"id": "x", "maxInputTokens": float64(0), "contextWindow": float64(200),
			"maxOutputTokens": float64(-1), "maxTokens": float64(300),
			"maxAllowedSize": "bad",
		})
		if mi.ContextWindow != 200 || mi.MaxTokens != 300 || mi.MaxAllowedSize != 0 {
			t.Errorf("回退 = %+v", mi)
		}
	})

	t.Run("老 effort 键兜底", func(t *testing.T) {
		mi, _ := parseGlobalModelLoose(map[string]any{"id": "x", "reasoning": map[string]any{"effort": "low"}})
		if mi.DefaultEffort != "low" {
			t.Errorf("DefaultEffort = %q，应为 low", mi.DefaultEffort)
		}
	})

	t.Run("reasoning 不是对象", func(t *testing.T) {
		mi, _ := parseGlobalModelLoose(map[string]any{"id": "x", "reasoning": "bad"})
		if mi.DefaultEffort != "" || len(mi.Efforts) != 0 {
			t.Errorf("非对象 reasoning 应忽略: %+v", mi)
		}
	})
}

// TestFetchGlobalModelsDegradedNarrowList v3 三路全失败、企业端点只回名字数组（窄表）：
// 降级用企业端点的名单，和两路都成功时对窄表的处理一致，不当成探测失败。
func TestFetchGlobalModelsDegradedNarrowList(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, gmV3Path, 400, `{"code":400,"msg":"v3 down"}`)
	f.on(http.MethodGet, gmEntV2Path, 200, `{"code":0,"data":{"models":["m-b","m-a"]}}`)
	c := upGlobalClient(f)
	if names := c.FetchGlobalModels(globalAuth()); !reflect.DeepEqual(names, []string{"m-b", "m-a"}) {
		t.Fatalf("names = %v，应为企业端点名单 [m-b m-a]", names)
	}
	if infos := c.FetchGlobalModelInfos(globalAuth()); infos != nil {
		t.Errorf("窄表不应编造 infos，got %+v", infos)
	}
}

// TestFetchGlobalModelsStableOrder 只有 v3 主路给出模型时，名单按 id 排序，
// 不随 map 遍历顺序变化（每次换新客户端重新探测，连测多次）。
func TestFetchGlobalModelsStableOrder(t *testing.T) {
	ids := []string{"m-h", "m-c", "m-f", "m-a", "m-g", "m-b", "m-e", "m-d"}
	var models []string
	for _, id := range ids {
		models = append(models, `{"id":"`+id+`","maxInputTokens":1000}`)
	}
	want := append([]string(nil), ids...)
	sort.Strings(want)
	for i := 0; i < 20; i++ {
		f := newUpFake(t)
		f.onFunc(http.MethodGet, gmV3Path, func(r *upReq) (*http.Response, error) {
			if r.header.Get("User-Agent") == codeBuddyIDEUA || r.header.Get("User-Agent") == codeBuddyCLIUA {
				return jsonResp(400, `{"code":400}`), nil
			}
			return jsonResp(200, v3ModelsJSON(strings.Join(models, ","))), nil
		})
		f.on(http.MethodGet, gmEntV2Path, 400, `{"code":400}`)
		f.on(http.MethodGet, gmEntConPath, 400, `{"code":400}`)
		if names := upGlobalClient(f).FetchGlobalModels(globalAuth()); !reflect.DeepEqual(names, want) {
			t.Fatalf("第 %d 次 names = %v，应按 id 排序为 %v", i, names, want)
		}
	}
}
