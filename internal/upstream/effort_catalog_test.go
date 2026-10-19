package upstream

import (
	"reflect"
	"testing"
)

// TestStaticEffortCap 兜底表按 realm 分开：同一模型在两个面档位不同，
// realm 为空按 cn 归一化，未收录返回零值。
func TestStaticEffortCap(t *testing.T) {
	cases := []struct {
		name        string
		realm       string
		model       string
		wantEfforts []string
		wantDefault string
	}{
		{name: "空 realm 按国内版", realm: "", model: "deepseek-v4.1-flash", wantEfforts: []string{"low", "high", "max"}, wantDefault: "high"},
		{name: "国内版三档", realm: "cn", model: "deepseek-v4.1-flash", wantEfforts: []string{"low", "high", "max"}, wantDefault: "high"},
		{name: "国际版同模型只有 high", realm: "global", model: "deepseek-v4.1-flash", wantEfforts: []string{"high"}},
		{name: "国际版带默认档", realm: "global", model: "hy4-preview-f", wantEfforts: []string{"high"}, wantDefault: "high"},
		{name: "国内版仅单档", realm: "cn", model: "kimi-k2.6", wantEfforts: []string{"medium"}},
		{name: "国际版单档", realm: "global", model: "kimi-k2.6", wantEfforts: []string{"medium"}},
		{name: "国内版独有模型在国际版未收录", realm: "global", model: "deepseek-v4-flash"},
		{name: "国际版独有模型在国内版未收录", realm: "cn", model: "primary-model"},
		{name: "兜底表里也没有", realm: "cn", model: "no-such-model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := staticEffortCap(tc.realm, tc.model)
			if !reflect.DeepEqual(got.efforts, tc.wantEfforts) {
				t.Errorf("efforts = %v，应为 %v", got.efforts, tc.wantEfforts)
			}
			if got.defaultEffort != tc.wantDefault {
				t.Errorf("defaultEffort = %q，应为 %q", got.defaultEffort, tc.wantDefault)
			}
		})
	}
}

// TestEffortListingRemoteWins 上游给了档位时以上游为权威：即使与静态兜底表冲突
// 也不回落；默认档必须命中档位列表才返回。
func TestEffortListingRemoteWins(t *testing.T) {
	cases := []struct {
		name        string
		realm       string
		model       string
		remote      []string
		remoteDef   string
		wantEfforts []string
		wantDefault string
	}{
		{
			name: "上游值与静态表冲突以远端为准（国内版）", realm: "cn", model: "deepseek-v4.1-flash",
			remote: []string{"medium"}, remoteDef: "medium",
			wantEfforts: []string{"medium"}, wantDefault: "medium",
		},
		{
			name: "上游值与静态表冲突以远端为准（国际版）", realm: "global", model: "deepseek-v4.1-flash",
			remote:      []string{"low", "max"},
			wantEfforts: []string{"low", "max"},
		},
		{
			name: "远端默认档不在档位里则省略", realm: "cn", model: "deepseek-v4.1-flash",
			remote: []string{"low"}, remoteDef: "high",
			wantEfforts: []string{"low"},
		},
		{
			name: "远端默认档为空", realm: "global", model: "primary-model",
			remote:      []string{"high"},
			wantEfforts: []string{"high"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			efforts, def := EffortListing(tc.realm, tc.model, tc.remote, tc.remoteDef)
			if !reflect.DeepEqual(efforts, tc.wantEfforts) {
				t.Errorf("efforts = %v，应为 %v", efforts, tc.wantEfforts)
			}
			if def != tc.wantDefault {
				t.Errorf("defaultEffort = %q，应为 %q", def, tc.wantDefault)
			}
		})
	}
}

// TestEffortListingFallback 上游没给值时按 realm 走静态兜底表；两边都没有则
// 返回 nil（调用方省略字段，不输出空数组）。
func TestEffortListingFallback(t *testing.T) {
	cases := []struct {
		name        string
		realm       string
		model       string
		wantEfforts []string
		wantDefault string
	}{
		{name: "国内版三档", realm: "cn", model: "deepseek-v4.1-flash", wantEfforts: []string{"low", "high", "max"}, wantDefault: "high"},
		{name: "国际版只暴露 high", realm: "global", model: "deepseek-v4.1-flash", wantEfforts: []string{"high"}},
		{name: "国际版多档", realm: "global", model: "gpt-5.5", wantEfforts: []string{"low", "medium", "high", "xhigh"}, wantDefault: "high"},
		{name: "国内版多档带默认", realm: "cn", model: "hy3", wantEfforts: []string{"low", "high"}, wantDefault: "high"},
		{name: "空 realm 按国内版", realm: "", model: "glm-5.2", wantEfforts: []string{"high", "xhigh"}, wantDefault: "high"},
		{name: "兜底表里也没有", realm: "cn", model: "unknown-model"},
		{name: "国际版兜底表里也没有", realm: "global", model: "unknown-model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			efforts, def := EffortListing(tc.realm, tc.model, nil, "")
			if !reflect.DeepEqual(efforts, tc.wantEfforts) {
				t.Errorf("efforts = %v，应为 %v", efforts, tc.wantEfforts)
			}
			if def != tc.wantDefault {
				t.Errorf("defaultEffort = %q，应为 %q", def, tc.wantDefault)
			}
			if tc.wantEfforts == nil && efforts != nil {
				t.Errorf("无档位时应返回 nil，got %#v", efforts)
			}
		})
	}
}

// TestEffortListingRemoteEmptyFallsBack 远端给的是空切片（不是 nil）也按没给处理，落静态表。
func TestEffortListingRemoteEmptyFallsBack(t *testing.T) {
	efforts, def := EffortListing("cn", "deepseek-v4.1-flash", []string{}, "")
	if !reflect.DeepEqual(efforts, []string{"low", "high", "max"}) || def != "high" {
		t.Errorf("空远端档位应落静态表，got %v/%q", efforts, def)
	}
}

// TestEffortListingReturnsCopy 返回的档位是副本：调用方改动它不会污染静态兜底表，
// 也不会改到传入的远端切片。
func TestEffortListingReturnsCopy(t *testing.T) {
	remote := []string{"low", "high"}
	efforts, _ := EffortListing("global", "x", remote, "")
	efforts[0] = "mutated"
	if remote[0] != "low" {
		t.Errorf("返回切片与远端入参共享底层数组：remote=%v", remote)
	}

	efforts, _ = EffortListing("global", "deepseek-v4.1-flash", nil, "")
	efforts[0] = "mutated"
	if got := staticEffortCap("global", "deepseek-v4.1-flash").efforts; !reflect.DeepEqual(got, []string{"high"}) {
		t.Errorf("返回切片与静态表共享底层数组：静态表 = %v", got)
	}
}

// TestContainsEffort 档位成员判定是精确匹配。
func TestContainsEffort(t *testing.T) {
	list := []string{"low", "high", "max"}
	for _, m := range list {
		if !containsEffort(list, m) {
			t.Errorf("%q 应在 %v 里", m, list)
		}
	}
	for _, m := range []string{"", "hig", "medium", "High"} {
		if containsEffort(list, m) {
			t.Errorf("%q 不应在 %v 里", m, list)
		}
	}
	if containsEffort(nil, "high") {
		t.Error("空列表不应命中")
	}
}

// TestGlobalEffortMap 国际域降级表：静态兜底为基，远端仅在有值时覆盖；
// 默认档同理，空值不覆盖。返回的切片是副本。
func TestGlobalEffortMap(t *testing.T) {
	t.Run("静态表为基", func(t *testing.T) {
		efforts, defs := globalEffortMap(nil, nil)
		if got := efforts["deepseek-v4.1-flash"]; !reflect.DeepEqual(got, []string{"high"}) {
			t.Errorf("国际版静态档位 = %v", got)
		}
		if got := efforts["primary-model"]; !reflect.DeepEqual(got, []string{"high"}) {
			t.Errorf("单档模型 = %v", got)
		}
		if got := defs["hy4-preview-f"]; got != "high" {
			t.Errorf("静态默认档 = %q，应为 high", got)
		}
		if _, ok := defs["primary-model"]; ok {
			t.Errorf("静态条目没有默认档时不应凭空造，got %q", defs["primary-model"])
		}
		if _, ok := efforts["deepseek-v4-flash"]; ok {
			t.Error("国内版独有模型不应出现在国际域降级表")
		}
	})

	t.Run("远端有值覆盖静态表", func(t *testing.T) {
		efforts, defs := globalEffortMap(
			map[string][]string{"deepseek-v4.1-flash": {"medium", "max"}},
			map[string]string{"deepseek-v4.1-flash": "max"},
		)
		if got := efforts["deepseek-v4.1-flash"]; !reflect.DeepEqual(got, []string{"medium", "max"}) {
			t.Errorf("远端档位应覆盖静态表，got %v", got)
		}
		if got := defs["deepseek-v4.1-flash"]; got != "max" {
			t.Errorf("远端默认档应覆盖，got %q", got)
		}
	})

	t.Run("远端空值不覆盖", func(t *testing.T) {
		efforts, defs := globalEffortMap(
			map[string][]string{"deepseek-v4.1-flash": {}},
			map[string]string{"deepseek-v4.1-flash": ""},
		)
		if got := efforts["deepseek-v4.1-flash"]; !reflect.DeepEqual(got, []string{"high"}) {
			t.Errorf("空远端档位不应覆盖静态表，got %v", got)
		}
		if got, ok := defs["deepseek-v4.1-flash"]; ok {
			t.Errorf("空远端默认档不应写入，got %q", got)
		}
	})

	t.Run("远端独有模型与默认档", func(t *testing.T) {
		efforts, defs := globalEffortMap(
			map[string][]string{"remote-only": {"low", "high"}},
			map[string]string{"remote-only": "low"},
		)
		if got := efforts["remote-only"]; !reflect.DeepEqual(got, []string{"low", "high"}) {
			t.Errorf("远端独有模型应加入，got %v", got)
		}
		if got := defs["remote-only"]; got != "low" {
			t.Errorf("远端独有默认档 = %q，应为 low", got)
		}
	})

	t.Run("返回副本", func(t *testing.T) {
		efforts, _ := globalEffortMap(nil, nil)
		efforts["deepseek-v4.1-flash"][0] = "mutated"
		if got := staticEffortCap("global", "deepseek-v4.1-flash").efforts; !reflect.DeepEqual(got, []string{"high"}) {
			t.Errorf("返回切片与静态表共享底层数组：静态表 = %v", got)
		}
	})
}
