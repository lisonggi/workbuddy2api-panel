// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// scenes.go 首页模板（官方叫场景 scene）的列表。官方 5.8.3 首页的模板按钮来自内置的设计模板
// 和远程列表 https://download.codebuddy.cn/plugin-marketplace/scenes-v2.json（英文界面是
// scenes-v2-en.json），公开文件，不用登录。任务中心只对国内版开放，取中文列表。
//
// 任务中心的「使用模板创建任务」只上报事件、不真发对话（用户 10-20 定），事件里的模板 id、名字
// 取这里的真实值。
package upstream

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

const defaultScenesBase = "https://download.codebuddy.cn/plugin-marketplace"

// HomeScene 首页的一个模板。
type HomeScene struct {
	ID   int    // 模板的数字 id（template_used 的 template_id）
	Name string // 按钮上的名字
	Mode string // working / coding / design：模板出现在首页哪个模式下
	// InteractionModes 能在哪些交互模式下出现（craft、plan 等），空表示不限（quick 模式除外）。
	InteractionModes []string
}

func (c *Client) scenesBase() string {
	if c.ScenesBase != "" {
		return c.ScenesBase
	}
	return defaultScenesBase
}

// HomeScenes 拉首页模板列表，照官方 normalizeEntry 只留 id 是有限数字（数字或数字字符串）、名字
// 不为空的条目，顺序同列表（首页按钮的顺序）。结果缓存 staticCacheTTL（见 playbook.go），返回的是
// 副本。账号参数留给以后按域区分。
func (c *Client) HomeScenes(_ *auth.Auth) ([]HomeScene, error) {
	c.static.mu.Lock()
	defer c.static.mu.Unlock()
	if !cacheFresh(c.static.scenesAt) {
		scenes, err := c.fetchHomeScenes()
		if err != nil {
			return nil, err
		}
		c.static.scenes, c.static.scenesAt = scenes, time.Now()
	}
	return slices.Clone(c.static.scenes), nil
}

func (c *Client) fetchHomeScenes() ([]HomeScene, error) {
	var raw []struct {
		ID               any      `json:"id"`
		Name             any      `json:"name"`
		Mode             string   `json:"mode"`
		InteractionModes []string `json:"interactionModes"`
	}
	if err := c.staticGet(c.scenesBase()+"/scenes-v2.json", "scenes", &raw); err != nil {
		return nil, err
	}
	out := make([]HomeScene, 0, len(raw))
	for _, r := range raw {
		id, ok := sceneID(r.ID)
		name, _ := r.Name.(string)
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		out = append(out, HomeScene{ID: id, Name: name, Mode: r.Mode, InteractionModes: r.InteractionModes})
	}
	return out, nil
}

// sceneID 官方 pickFiniteNumber：数字原样（JSON 数字解码到 any 是 float64），数字字符串去掉首尾
// 空白后转数字。模板 id 都是整数，带小数的不要。
func sceneID(v any) (int, bool) {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0, false
		}
		f = n
	default:
		return 0, false
	}
	if math.IsInf(f, 0) || math.IsNaN(f) || f != math.Trunc(f) {
		return 0, false
	}
	return int(f), true
}

// PickWorkingScenes 照首页默认的工作模式、craft 交互模式下能看到的模板按钮，按顺序取前 n 个
// （官方 workingActions 只留 mode 为 working 的，再按 shouldHideByInteractionMode 去掉不在
// craft 下出现的）。不够 n 个就报错。
func (c *Client) PickWorkingScenes(a *auth.Auth, n int) ([]HomeScene, error) {
	all, err := c.HomeScenes(a)
	if err != nil {
		return nil, err
	}
	var out []HomeScene
	for _, sc := range all {
		if sc.Mode != "working" || (len(sc.InteractionModes) > 0 && !slices.Contains(sc.InteractionModes, "craft")) {
			continue
		}
		if out = append(out, sc); len(out) == n {
			return out, nil
		}
	}
	return nil, fmt.Errorf("首页工作模式只有 %d 个模板，不够 %d 个", len(out), n)
}
