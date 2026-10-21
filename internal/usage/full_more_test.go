// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package usage

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 第八轮补齐：usage.go 剩余分支。

// captureUsageLog 捕获标准 logger 输出（New/flush 的错误日志断言用）。
func captureUsageLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

func waitUsageFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待落盘超时: %s", path)
}

func TestFull8UsageLoadErrors(t *testing.T) {
	t.Run("文件损坏", func(t *testing.T) {
		buf := captureUsageLog(t)
		path := filepath.Join(t.TempDir(), "usage.json")
		if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
			t.Fatalf("写文件: %v", err)
		}
		r := New(path)
		if len(r.buckets) != 0 {
			t.Fatalf("损坏文件应从头开始，实际 %d 桶", len(r.buckets))
		}
		if !strings.Contains(buf.String(), "读取") {
			t.Fatalf("应记录读取失败日志，实际 %q", buf.String())
		}
	})
	t.Run("路径是目录", func(t *testing.T) {
		buf := captureUsageLog(t)
		r := New(t.TempDir())
		if len(r.buckets) != 0 {
			t.Fatalf("目录路径应从头开始")
		}
		if !strings.Contains(buf.String(), "读取") {
			t.Fatalf("应记录读取失败日志，实际 %q", buf.String())
		}
	})
}

func TestFull8UsageStartTicker(t *testing.T) {
	oldFlush, oldMax := flushInterval, maxBuckets
	flushInterval = 5 * time.Millisecond
	maxBuckets = 0 // 任意桶数都触发 Rollup 分支
	path := filepath.Join(t.TempDir(), "usage.json")
	r := New(path)
	r.Start()
	r.Add(time.Now(), "cn", "u", "m", Delta{PromptTokens: 1, HasPromptTokens: true}, true)
	waitUsageFile(t, path)
	r.Stop()
	flushInterval, maxBuckets = oldFlush, oldMax

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读落盘文件: %v", err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("落盘内容不是 JSON: %v", err)
	}
	if f.Version != fileVersion || len(f.Buckets) != 1 {
		t.Fatalf("落盘 = %+v", f)
	}
}

func TestFull8UsageAddEdges(t *testing.T) {
	var nilRec *Recorder
	nilRec.Add(time.Now(), "cn", "u", "m", Delta{}, true) // nil 接收者不 panic

	r := New("")
	r.Add(time.Now(), "", "", "", Delta{}, false)
	snap := r.Snapshot(0, nil)
	if len(snap.ByRealm) != 1 || snap.ByRealm[0].Key != "cn" {
		t.Fatalf("空 realm 应归 cn: %+v", snap.ByRealm)
	}
	if len(snap.ByModel) != 1 || snap.ByModel[0].Key != "(unknown)" {
		t.Fatalf("空模型应归 (unknown): %+v", snap.ByModel)
	}

	r2 := New("")
	r2.Add(time.Now(), "cn", "u", "m", Delta{TokensPerSecond: 42.5, HasTPS: true}, true)
	if got := r2.Snapshot(0, nil).Totals.AvgTPS; got != 42.5 {
		t.Fatalf("AvgTPS = %v", got)
	}
}

func TestFull8UsageRollupNil(t *testing.T) {
	var nilRec *Recorder
	nilRec.Rollup(time.Now()) // nil 接收者直接返回
}

func TestFull8UsageFlushBranches(t *testing.T) {
	t.Run("nil 与空路径", func(t *testing.T) {
		(*Recorder)(nil).Save()
		New("").Save()
	})
	t.Run("未脏非强制", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "usage.json")
		r := New(path)
		r.flush(false)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("未脏且非强制不应落盘: %v", err)
		}
	})
	t.Run("建目录失败", func(t *testing.T) {
		buf := captureUsageLog(t)
		dir := t.TempDir()
		blocker := filepath.Join(dir, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatalf("造阻断文件: %v", err)
		}
		path := filepath.Join(blocker, "usage.json")
		r := New(path)
		r.Add(time.Now(), "cn", "u", "m", Delta{}, true)
		r.Save()
		if !strings.Contains(buf.String(), "建目录失败") {
			t.Fatalf("应记录建目录失败，实际 %q", buf.String())
		}
	})
	t.Run("写临时文件失败", func(t *testing.T) {
		buf := captureUsageLog(t)
		dir := t.TempDir()
		path := filepath.Join(dir, "usage.json")
		if err := os.Mkdir(path+".tmp", 0o755); err != nil {
			t.Fatalf("造临时目录: %v", err)
		}
		r := New(path)
		r.Add(time.Now(), "cn", "u", "m", Delta{}, true)
		r.Save()
		if !strings.Contains(buf.String(), "写临时文件失败") {
			t.Fatalf("应记录写临时文件失败，实际 %q", buf.String())
		}
	})
	t.Run("原子替换失败", func(t *testing.T) {
		buf := captureUsageLog(t)
		dir := t.TempDir()
		path := filepath.Join(dir, "usage.json")
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatalf("造目标目录: %v", err)
		}
		r := New(path)
		r.Add(time.Now(), "cn", "u", "m", Delta{}, true)
		r.Save()
		if !strings.Contains(buf.String(), "原子替换失败") {
			t.Fatalf("应记录原子替换失败，实际 %q", buf.String())
		}
		if _, err := os.Stat(path + ".tmp"); err != nil {
			t.Fatalf("临时文件应保留: %v", err)
		}
	})
}

func TestFull8UsageSnapshotEdges(t *testing.T) {
	t.Run("nil 记录器", func(t *testing.T) {
		var nilRec *Recorder
		snap := nilRec.Snapshot(0, nil)
		if snap.Generated == "" {
			t.Fatal("nil 记录器也应返回 Generated 时间")
		}
	})
	t.Run("脏 scope 不进窗口", func(t *testing.T) {
		r := New("")
		r.mu.Lock()
		r.buckets["bad"] = &bucket{Scope: "x:bad", Realm: "cn", UID: "u", Model: "m", Req: 9}
		r.mu.Unlock()
		snap := r.SnapshotWindow(Window{Hours: 1}, nil, nil)
		if snap.Totals.Requests != 0 {
			t.Fatalf("脏 scope 不应计入窗口: %+v", snap.Totals)
		}
	})
	t.Run("倍率回填只查一次", func(t *testing.T) {
		r := New("")
		now := time.Now()
		for i := 0; i < 2; i++ {
			r.Add(now, "cn", "u"+strconv.Itoa(i), "m", Delta{Credit: 1, HasCredit: true, TotalTokens: 10, HasTotal: true}, true)
		}
		calls := 0
		snap := r.SnapshotWindow(Window{}, nil, func(realm, model string) string {
			calls++
			return "0.50"
		})
		if calls != 1 {
			t.Fatalf("同 (realm, model) 应只回填一次，实际 %d 次", calls)
		}
		if len(snap.CreditByModel) != 1 || snap.CreditByModel[0].Rate != "0.50" {
			t.Fatalf("CreditByModel = %+v", snap.CreditByModel)
		}
	})
}

func TestFull8UsageCanonicalModel(t *testing.T) {
	cases := map[string]string{
		"":             "(unknown)",
		"  ":           "(unknown)",
		" cn:glm-5.2 ": "glm-5.2",
		"global:m":     "m",
		"plain":        "plain",
	}
	for in, want := range cases {
		if got := canonicalUsageModel(in); got != want {
			t.Errorf("canonicalUsageModel(%q) = %q，应为 %q", in, got, want)
		}
	}
}

func TestFull8UsageCreditKeyedSort(t *testing.T) {
	m := map[string]*creditAcc{
		"a": {CreditAgg: CreditAgg{Key: "a", Credits: 1, CreditTokens: 10, Rate: "y"}},
		"b": {CreditAgg: CreditAgg{Key: "b", Credits: 1, CreditTokens: 10, Rate: "x"}},
		"c": {CreditAgg: CreditAgg{Key: "c", Credits: 1, CreditTokens: 5}},
		"d": {CreditAgg: CreditAgg{Key: "a", Credits: 1, CreditTokens: 10, Rate: "x"}},
	}
	out := creditKeyed(m)
	got := make([]string, 0, len(out))
	for _, r := range out {
		got = append(got, r.Key+"/"+r.Rate)
	}
	want := []string{"a/x", "a/y", "b/x", "c/"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("排序 = %v，应为 %v", got, want)
		}
	}
}

func TestFull8UsageKeyedSort(t *testing.T) {
	m := map[string]*aggAcc{
		"k1": {Agg: Agg{TotalTokens: 10, Requests: 1}},
		"k2": {Agg: Agg{TotalTokens: 10, Requests: 2}},
		"k3": {Agg: Agg{TotalTokens: 10, Requests: 2}},
	}
	out := keyed(m, func(k string) (string, string) { return k, "" })
	got := []string{out[0].Key, out[1].Key, out[2].Key}
	if got[0] != "k2" || got[1] != "k3" || got[2] != "k1" {
		t.Fatalf("排序 = %v，应为 [k2 k3 k1]", got)
	}
}

func TestFull8UsageDescribe(t *testing.T) {
	if got := (*Recorder)(nil).Describe(); got != "disabled" {
		t.Fatalf("nil Describe = %q", got)
	}
	if got := New("").Describe(); !strings.Contains(got, "0 buckets") {
		t.Fatalf("空记录器 Describe = %q", got)
	}
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(path, []byte(`{"version":4,"buckets":[]}`), 0o600); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := New(path).Describe(); !strings.Contains(got, "file "+strconv.FormatInt(fi.Size(), 10)+" bytes") {
		t.Fatalf("Describe = %q", got)
	}
}
