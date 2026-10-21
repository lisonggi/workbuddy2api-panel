// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"net/http"
	"strings"
	"testing"
)

// scenesFixture 首页模板列表：id 有数字、数字字符串、带小数、缺失的，名字有空的，模式有工作、编程，
// 交互模式有只在 plan 下出现的。
const scenesFixture = `[
	{"id":47,"name":"创作文档","mode":"working","interactionModes":["craft","plan"]},
	{"id":"52","name":" 金融服务 ","mode":"working"},
	{"id":53.5,"name":"小数","mode":"working"},
	{"name":"没 id","mode":"working"},
	{"id":60,"name":"","mode":"working"},
	{"id":61,"name":7,"mode":"working"},
	{"id":" ","name":"空串 id","mode":"working"},
	{"id":true,"name":"布尔 id","mode":"working"},
	{"id":54,"name":"日常开发","mode":"coding"},
	{"id":62,"name":"只在规划","mode":"working","interactionModes":["plan"]},
	{"id":48,"name":"分析数据","mode":"working","interactionModes":[]}
]`

// TestHomeScenes 照官方 normalizeEntry 只留 id 是整数（数字或数字字符串）、名字不为空的模板，顺序不变。
func TestHomeScenes(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, "/plugin-marketplace/scenes-v2.json", 200, scenesFixture)
	got, err := upClient(f).HomeScenes(upAuth())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	var names []string
	for _, sc := range got {
		names = append(names, sc.Name)
	}
	if strings.Join(names, ",") != "创作文档,金融服务,日常开发,只在规划,分析数据" {
		t.Fatalf("模板 = %v", names)
	}
	if got[1].ID != 52 || got[0].Mode != "working" || len(got[0].InteractionModes) != 2 {
		t.Fatalf("字段 = %+v", got)
	}
	r := f.seen()[0]
	if !strings.HasPrefix(r.url, defaultScenesBase+"/") || r.header.Get("User-Agent") != browserUA {
		t.Fatalf("请求 = %s %v", r.url, r.header)
	}
}

// TestPickWorkingScenes 只取工作模式、craft 下能看到的模板，按顺序取够就停；不够报错。
func TestPickWorkingScenes(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, "/sc/scenes-v2.json", 200, scenesFixture)
	c := upClient(f)
	c.ScenesBase = "https://static.test/sc"
	got, err := c.PickWorkingScenes(upAuth(), 2)
	if err != nil || len(got) != 2 || got[0].ID != 47 || got[1].ID != 52 {
		t.Fatalf("got = %+v err = %v", got, err)
	}
	got, err = c.PickWorkingScenes(upAuth(), 3)
	if err != nil || got[2].ID != 48 {
		t.Fatalf("交互模式为空应不限: %+v err = %v", got, err)
	}
	if _, err := c.PickWorkingScenes(upAuth(), 4); err == nil || !strings.Contains(err.Error(), "只有 3 个") {
		t.Fatalf("不够时 err = %v", err)
	}
}

// TestHomeScenesErrors 列表拉不到、形状不对都报错。
func TestHomeScenesErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"拉不到":  {404, `x`, "http 404"},
		"形状不对": {200, `{"scenes":[]}`, "parse"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newUpFake(t)
			f.on(http.MethodGet, "/plugin-marketplace/scenes-v2.json", tc.status, tc.body)
			if _, err := upClient(f).PickWorkingScenes(upAuth(), 1); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v，应含 %q", err, tc.want)
			}
		})
	}
}
