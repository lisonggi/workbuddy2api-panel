// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package reqlog

// reqlog_more_test.go reqlog 的补充用例：请求 ID、nil 接收者、空结果、归档默认参数与关闭状态、
// 运行中改开关与清空、目录不可用、跨天文件、读取边界。归档文件本身的轮转、清理在 logarchive 里测。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// NewRequestID：req- + 16 位十六进制；连续生成不重复。
func TestNewRequestIDFormatAndUniqueness(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := NewRequestID()
		if !strings.HasPrefix(id, "req-") || len(id) != len("req-")+16 {
			t.Fatalf("格式不对: %q", id)
		}
		if strings.Trim(id[len("req-"):], "0123456789abcdef") != "" {
			t.Fatalf("应为小写十六进制: %q", id)
		}
		if seen[id] {
			t.Fatalf("重复的请求 ID: %q", id)
		}
		seen[id] = true
	}
}

// nil 接收者：所有方法空操作，不 panic。
func TestNilRecorderMethods(t *testing.T) {
	var r *Recorder
	r.Begin()
	r.Record(Event{Status: 200, OK: true})
	if s := r.Snapshot(); s.Completed != 0 {
		t.Fatalf("nil 快照应为零值: %+v", s)
	}
	if rows, err := r.ReadArchive(10, Filter{}); rows != nil || err != nil {
		t.Fatalf("nil ReadArchive = %v, %v", rows, err)
	}
	r.SetArchive(true, 7, 1<<20)
	if err := r.ClearArchive(); err != nil {
		t.Fatalf("nil ClearArchive = %v", err)
	}
	r.Close()
}

// Record：空 Outcome 按状态推导；时间缺省补 now；最近列表有界 100 条。
func TestRecordDerivesOutcomeAndCapsRecent(t *testing.T) {
	r := New(Config{})
	r.Record(Event{Status: 200, OK: true})
	r.Record(Event{Status: 500})
	s := r.Snapshot()
	if len(s.Recent) != 2 {
		t.Fatalf("recent = %d", len(s.Recent))
	}
	if s.Recent[1].Outcome != OutcomeSuccess || s.Recent[1].Time.IsZero() {
		t.Fatalf("成功事件推导不对: %+v", s.Recent[1])
	}
	if s.Recent[0].Outcome != OutcomeHTTPError {
		t.Fatalf("失败事件推导不对: %+v", s.Recent[0])
	}

	for i := 0; i < 150; i++ {
		r.Record(Event{RequestID: "bulk", Status: 200, OK: true, Outcome: OutcomeSuccess})
	}
	if got := len(r.Snapshot().Recent); got != recentLimit {
		t.Fatalf("recent 应封顶 %d，实际 %d", recentLimit, got)
	}
}

// 归档默认参数与关闭状态：不填天数 = 永久保留、容量回落 100 MiB；Enabled=false 时读为空、Close 无副作用。
func TestArchiveDefaultsAndDisabled(t *testing.T) {
	r := New(Config{Enabled: true, Dir: t.TempDir()})
	if a := r.Snapshot().Archive; !a.Enabled || a.RetentionDays != 0 || a.MaxBytes != 100<<20 {
		t.Fatalf("归档默认参数不对: %+v", a)
	}
	r.Close()

	off := New(Config{})
	if rows, err := off.ReadArchive(10, Filter{}); rows != nil || err != nil {
		t.Fatalf("归档关闭时 ReadArchive = %v, %v", rows, err)
	}
	off.Record(Event{Status: 200, OK: true})
	off.Close() // 关闭状态 Close 提前返回
	off.Close() // 重复关闭不 panic
	if s := off.Snapshot().Archive; s.Enabled {
		t.Fatalf("归档应处于关闭状态: %+v", s)
	}
}

// 运行中关掉归档：之后的请求不再写盘，读归档为空（面板改用内存里的最近 100 条）；再打开接着写；
// 清空删掉全部归档文件，内存指标不动。
func TestSetArchiveAndClear(t *testing.T) {
	dir := t.TempDir()
	r := New(Config{Enabled: true, Dir: dir, MaxBytes: 1 << 20, RetentionDays: 7})
	defer r.Close()
	waitRows := func(want int) []Event {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			rows, err := r.ReadArchive(10, Filter{})
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) == want || time.Now().After(deadline) {
				return rows
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	r.Record(Event{RequestID: "on-1", Status: 200, OK: true})
	if rows := waitRows(1); len(rows) != 1 {
		t.Fatalf("开着时应写盘: %+v", rows)
	}

	r.SetArchive(false, 7, 1<<20)
	r.Record(Event{RequestID: "off", Status: 200, OK: true})
	if rows, err := r.ReadArchive(10, Filter{}); rows != nil || err != nil {
		t.Fatalf("关掉后读归档应为空: %v, %v", rows, err)
	}
	if a := r.Snapshot().Archive; a.Enabled || a.Files != 1 {
		t.Fatalf("关掉后已有的文件留着: %+v", a)
	}

	r.SetArchive(true, 30, 1<<20)
	r.Record(Event{RequestID: "on-2", Status: 200, OK: true})
	rows := waitRows(2)
	if len(rows) != 2 || rows[0].RequestID != "on-2" || rows[1].RequestID != "on-1" {
		t.Fatalf("再打开应接着写，关着时的请求不写: %+v", rows)
	}
	if a := r.Snapshot().Archive; a.RetentionDays != 30 {
		t.Fatalf("保留天数应改成 30: %+v", a)
	}

	if err := r.ClearArchive(); err != nil {
		t.Fatal(err)
	}
	if a := r.Snapshot().Archive; a.Files != 0 || a.Bytes != 0 {
		t.Fatalf("清空后不应再有文件: %+v", a)
	}
	if s := r.Snapshot(); s.Completed != 3 || len(s.Recent) != 3 {
		t.Fatalf("清空归档不动内存指标: completed=%d recent=%d", s.Completed, len(s.Recent))
	}
}

// 归档目录建不出来（路径是文件）：记录错误、读归档报错、不 panic。
func TestArchiveMkdirFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(Config{Enabled: true, Dir: path})
	r.Record(Event{Status: 200, OK: true})
	r.Close()
	if s := r.Snapshot().Archive; s.LastError == "" {
		t.Fatalf("目录不可建时应记录错误: %+v", s)
	}
	if _, err := r.ReadArchive(10, Filter{}); err == nil {
		t.Fatal("目录不可用时 ReadArchive 应报错")
	}
}

// 跨天换文件：不同日期的事件落到各自的 requests-<day>.jsonl。
func TestArchiveCrossDayFiles(t *testing.T) {
	dir := t.TempDir()
	r := New(Config{Enabled: true, Dir: dir, MaxBytes: 1 << 20})
	day1 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.Local)
	day2 := day1.AddDate(0, 0, 1)
	r.Record(Event{Time: day1, RequestID: "d1", Status: 200, OK: true, Outcome: OutcomeSuccess})
	r.Record(Event{Time: day2, RequestID: "d2", Status: 200, OK: true, Outcome: OutcomeSuccess})
	r.Close()
	for _, day := range []string{"2026-03-01", "2026-03-02"} {
		if _, err := os.Stat(filepath.Join(dir, "requests-"+day+".jsonl")); err != nil {
			t.Fatalf("缺少 %s 的归档文件: %v", day, err)
		}
	}
	if s := r.Snapshot().Archive; s.OldestDay != "2026-03-01" || s.Files != 2 {
		t.Fatalf("最早那份应是 03-01: %+v", s)
	}
}

// ReadArchive 的边界：limit 回落/夹取、非归档文件跳过、目录被删后读为空、
// 归档文件读失败（符号链接指向不存在目标）报错。
func TestArchiveReadEdges(t *testing.T) {
	t.Run("limit 回落与夹取", func(t *testing.T) {
		dir := t.TempDir()
		r := New(Config{Enabled: true, Dir: dir, MaxBytes: 1 << 20, RetentionDays: 7})
		r.Record(Event{Time: time.Now(), RequestID: "x", Status: 200, OK: true, Outcome: OutcomeSuccess})
		r.Close()
		for _, limit := range []int{0, -1, 5000} {
			if _, err := r.ReadArchive(limit, Filter{}); err != nil {
				t.Fatalf("limit=%d: %v", limit, err)
			}
		}
	})

	t.Run("非归档文件跳过", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := New(Config{Enabled: true, Dir: dir, MaxBytes: 1 << 20, RetentionDays: 7})
		rows, err := r.ReadArchive(10, Filter{})
		if err != nil || len(rows) != 0 {
			t.Fatalf("非归档文件应被跳过: %v, %v", rows, err)
		}
		s := r.Snapshot().Archive
		if s.Files != 0 || s.Bytes != 0 {
			t.Fatalf("stats 不应统计非归档文件: %+v", s)
		}
		r.Close()
	})

	t.Run("目录被删后读为空", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "arch")
		r := New(Config{Enabled: true, Dir: dir, MaxBytes: 1 << 20, RetentionDays: 7})
		r.Close()
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if rows, err := r.ReadArchive(10, Filter{}); err != nil || len(rows) != 0 {
			t.Fatalf("目录不存在算没有记录: %v, %v", rows, err)
		}
		if s := r.Snapshot().Archive; s.Files != 0 || s.LastError != "" {
			t.Fatalf("目录不存在算没有文件: %+v", s)
		}
	})

	t.Run("归档文件打不开", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Symlink(filepath.Join(dir, "missing-target"), filepath.Join(dir, "requests-bad.jsonl")); err != nil {
			t.Skipf("当前环境不支持符号链接: %v", err)
		}
		r := New(Config{Enabled: true, Dir: dir, MaxBytes: 1 << 20, RetentionDays: 7})
		if _, err := r.ReadArchive(10, Filter{}); err == nil {
			t.Fatal("归档文件打不开时应返回错误")
		}
		r.Close()
	})
}
