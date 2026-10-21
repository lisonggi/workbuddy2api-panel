// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

const playbookTestPath = "/workbuddy/playbook"

// playbookRegistry 索引：前两个案例带专家 / 是 Genie，第三个才是 plain；第一个和它同属 business。
const playbookRegistry = `{"version":"1","cases":[
	{"id":"with-expert","title":"带专家","categories":["business"],"experts":[{"id":"x"}]},
	{"id":"genie","title":"Genie","categories":["data"],"tags":["Genie"]},
	{"id":"with-scene","title":"带模板","categories":["business"],"scenario_id":"s1"},
	{"id":"no-cat","title":"没分类","categories":[]},
	{"id":"pm-plan","title":"产品计划","categories":["business","office"],"artifact_type":"html","skills":[],"experts":[],"mcps":[],"scenario_id":null}]}`

const playbookCategories = `{"categories":[{"id":"office","name":"办公协同"},{"id":"business","name":"商业分析"}]}`

// TestPickPlaybookCase 照官方发现页的顺序拉索引、分类、详情，挑第一个 plain 案例：字段取详情里的值，
// 提示词长度按 JS 长度（中文对象取 zh，emoji 记 2），位置是同分类里排在它前面的案例数。
func TestPickPlaybookCase(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, playbookTestPath+"/registry.json", 200, playbookRegistry)
	f.on(http.MethodGet, playbookTestPath+"/categories.json", 200, playbookCategories)
	f.on(http.MethodGet, playbookTestPath+"/cases/pm-plan/case.json", 200,
		`{"id":"pm-plan","title":"产品计划","categories":["business","office"],"artifact_type":"html",
		"prompt":{"zh":"写一份计划🚀","en":"Write a plan"}}`)
	pc, err := upClient(f).PickPlaybookCase(upAuth())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := PlaybookCase{ID: "pm-plan", Title: "产品计划", Type: "html", CategoryID: "business",
		CategoryName: "商业分析", PromptLength: 7, Position: 2}
	if pc != want {
		t.Fatalf("pc = %+v\nwant %+v", pc, want)
	}
	seen := f.seen()
	if len(seen) != 3 || !strings.HasSuffix(seen[0].path, "/registry.json") ||
		!strings.HasSuffix(seen[1].path, "/categories.json") || !strings.HasSuffix(seen[2].path, "/case.json") {
		t.Fatalf("请求顺序不对: %v", seen)
	}
	r := seen[0]
	if !strings.HasPrefix(r.url, defaultPlaybookBase) {
		t.Fatalf("默认站点 = %s", r.url)
	}
	if r.header.Get("User-Agent") != browserUA || r.header.Get("Accept") != "*/*" ||
		r.header.Get("Accept-Encoding") != acceptEncodingChrome || r.header.Get("Authorization") != "" {
		t.Fatalf("请求头 = %v", r.header)
	}
}

// TestPickPlaybookCaseFallback 详情没有 id 时用索引里的条目；没有类型记 other；分类不在分类表里时名字为空；
// 换了 PlaybookBase 就拉那个站点。
func TestPickPlaybookCaseFallback(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, "/pb/registry.json", 200, `{"cases":[{"id":"c1","title":"案例一","categories":["misc"],"prompt":"做一个"}]}`)
	f.on(http.MethodGet, "/pb/categories.json", 200, playbookCategories)
	f.on(http.MethodGet, "/pb/cases/c1/case.json", 200, `{}`)
	c := upClient(f)
	c.PlaybookBase = "https://static.test/pb"
	pc, err := c.PickPlaybookCase(upAuth())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := PlaybookCase{ID: "c1", Title: "案例一", Type: "other", CategoryID: "misc", PromptLength: 3}
	if pc != want {
		t.Fatalf("pc = %+v\nwant %+v", pc, want)
	}
	if u := f.seen()[0].url; u != "https://static.test/pb/registry.json" {
		t.Fatalf("url = %s", u)
	}
}

// TestPickPlaybookCaseErrors 任一文件拉不到、解析不了，或没有 plain 案例，都报错。
func TestPickPlaybookCaseErrors(t *testing.T) {
	const reg, cats, detail = playbookTestPath + "/registry.json", playbookTestPath + "/categories.json",
		playbookTestPath + "/cases/pm-plan/case.json"
	type route struct {
		path   string
		status int
		body   string
	}
	for name, tc := range map[string]struct {
		routes []route
		want   string
	}{
		"索引失败":    {[]route{{reg, 500, `x`}}, "http 500"},
		"索引解析失败":  {[]route{{reg, 200, `[]`}}, "parse"},
		"分类失败":    {[]route{{reg, 200, playbookRegistry}, {cats, 404, `x`}}, "http 404"},
		"没有可用案例":  {[]route{{reg, 200, `{"cases":[{"id":"a","categories":["x"],"skills":[{}]}]}`}, {cats, 200, playbookCategories}}, "没有不带专家"},
		"详情失败":    {[]route{{reg, 200, playbookRegistry}, {cats, 200, playbookCategories}, {detail, 403, `x`}}, "http 403"},
		"详情解析失败":  {[]route{{reg, 200, playbookRegistry}, {cats, 200, playbookCategories}, {detail, 200, `{"id":1}`}}, "parse"},
		"站点地址不合法": {nil, "missing protocol scheme"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newUpFake(t)
			for _, r := range tc.routes {
				f.on(http.MethodGet, r.path, r.status, r.body)
			}
			c := upClient(f)
			if tc.routes == nil {
				c.PlaybookBase = "://bad"
			}
			_, err := c.PickPlaybookCase(upAuth())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v，应含 %q", err, tc.want)
			}
		})
	}
}

// TestPlaybookPromptZH 提示词：字符串原样；对象取 zh，没有 zh 取 en；别的形状为空。
func TestPlaybookPromptZH(t *testing.T) {
	for raw, want := range map[string]string{
		`"直接"`:                  "直接",
		`{"zh":"中文","en":"en"}`: "中文",
		`{"en":"only en"}`:      "only en",
		`123`:                   "",
		``:                      "",
	} {
		if got := (playbookEntry{Prompt: json.RawMessage(raw)}).promptZH(); got != want {
			t.Errorf("promptZH(%s) = %q, want %q", raw, got, want)
		}
	}
}

// TestStaticCache 灵感案例和首页模板缓存一小时：有效期内不再拉，过期重拉；失败不缓存。
func TestStaticCache(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, playbookTestPath+"/registry.json", 200, `{"cases":[{"id":"c1","title":"案例一","categories":["misc"]}]}`)
	f.on(http.MethodGet, playbookTestPath+"/categories.json", 200, playbookCategories)
	f.on(http.MethodGet, playbookTestPath+"/cases/c1/case.json", 200, `{}`)
	f.on(http.MethodGet, "/plugin-marketplace/scenes-v2.json", 200, `[{"id":1,"name":"甲","mode":"working"}]`)
	c := upClient(f)
	for i := 0; i < 2; i++ {
		if _, err := c.PickPlaybookCase(upAuth()); err != nil {
			t.Fatal(err)
		}
		scenes, err := c.HomeScenes(upAuth())
		if err != nil {
			t.Fatal(err)
		}
		scenes[0].Name = "改坏" // 拿到的是副本，改了不影响缓存
	}
	if n := len(f.seen()); n != 4 {
		t.Fatalf("有效期内应只拉一次，实际 %d 个请求", n)
	}
	if scenes, _ := c.HomeScenes(upAuth()); scenes[0].Name != "甲" {
		t.Fatalf("缓存被调用方改了: %+v", scenes)
	}
	c.static.playbookAt = time.Now().Add(-staticCacheTTL)
	c.static.scenesAt = time.Now().Add(-staticCacheTTL)
	if _, err := c.PickPlaybookCase(upAuth()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.HomeScenes(upAuth()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.seen()); n != 8 {
		t.Fatalf("过期后应重拉，实际 %d 个请求", n)
	}

	bad := newUpFake(t)
	bad.on(http.MethodGet, playbookTestPath+"/registry.json", 500, `x`)
	bc := upClient(bad)
	for i := 0; i < 2; i++ {
		if _, err := bc.PickPlaybookCase(upAuth()); err == nil {
			t.Fatal("应报错")
		}
	}
	if n := len(bad.seen()); n != 2 {
		t.Fatalf("失败不该缓存，实际 %d 个请求", n)
	}
}
