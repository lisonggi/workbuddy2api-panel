package upstream

import "testing"

// TestContextWindowListing 三级查找：上游动态值权威；种子表命中用表值；
// 未收录回落 1M。
func TestContextWindowListing(t *testing.T) {
	cases := []struct {
		name   string
		model  string
		remote int64
		want   int64
	}{
		{"remote>0 原样返回", "glm-5.2", 424242, 424242},
		{"种子表 glm-5.1", "glm-5.1", 0, 200000},
		{"种子表 kimi-k2.5", "kimi-k2.5", 0, 164000},
		{"种子表 auto", "auto", 0, 168000},
		{"未知模型兜底 1M", "no-such-model", 0, DefaultContextWindow},
		{"空模型名兜底", "", 0, DefaultContextWindow},
		{"remote 为负按未提供处理", "glm-5.1", -1, 200000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContextWindowListing(tc.model, tc.remote); got != tc.want {
				t.Errorf("ContextWindowListing(%q, %d) = %d，应为 %d", tc.model, tc.remote, got, tc.want)
			}
		})
	}
}

// TestMaxOutputTokensListing 输出上限：remote 权威；种子表命中；未知或表内
// 输出上限为 0 时省略（ok=false）。
func TestMaxOutputTokensListing(t *testing.T) {
	cases := []struct {
		name   string
		model  string
		remote int64
		want   int64
		wantOK bool
	}{
		{"remote>0 原样返回", "glm-5.2", 999, 999, true},
		{"种子表 glm-5.2", "glm-5.2", 0, 131072, true},
		{"种子表 deepseek-v4-pro", "deepseek-v4-pro", 0, 384000, true},
		{"表内输出未知省略", "kimi-k2.8-preview", 0, 0, false},
		{"表内 auto 输出未知", "auto", 0, 0, false},
		{"未知模型省略", "no-such-model", 0, 0, false},
		{"空模型名省略", "", 0, 0, false},
		{"remote 为负按未提供处理", "glm-5.1", -1, 131072, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := MaxOutputTokensListing(tc.model, tc.remote)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("MaxOutputTokensListing(%q, %d) = %d/%v，应为 %d/%v", tc.model, tc.remote, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
