package reqlog

// reqlog_more_test.go reqlog 的补充用例：请求 ID、nil 接收者、空结果、归档默认
// 参数与各类 IO 失败、跨天/轮转命名、prune 与 stats 的边界。已有用例不重复。

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

// 归档默认参数与关闭状态：Enabled=false 时读为空、Close 无副作用。
func TestArchiveDefaultsAndDisabled(t *testing.T) {
	r := New(Config{Enabled: true, Dir: t.TempDir()})
	a := r.archive
	if a.cfg.RetentionDays != 7 || a.cfg.MaxBytes != 100<<20 ||
		a.cfg.FileMaxBytes != defaultFileMax || a.cfg.QueueSize != defaultQueue {
		t.Fatalf("归档默认参数不对: %+v", a.cfg)
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

// 归档目录建不出来（路径是文件）：setErr 记录错误、降级为不可用、不 panic。
func TestArchiveMkdirFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(Config{Enabled: true, Dir: path})
	s := r.Snapshot().Archive
	if s.Enabled || s.LastError == "" {
		t.Fatalf("目录不可建时应禁用归档并记录错误: %+v", s)
	}
	r.Record(Event{Status: 200, OK: true})
	if rows, err := r.ReadArchive(10, Filter{}); rows != nil || err != nil {
		t.Fatalf("不可用归档 ReadArchive = %v, %v", rows, err)
	}
	r.Close()
}

// 跨天换文件：不同日期的事件落到各自的 requests-<day>.jsonl。
func TestArchiveCrossDayFiles(t *testing.T) {
	dir := t.TempDir()
	r := New(Config{Enabled: true, Dir: dir, MaxBytes: 1 << 20, RetentionDays: 7})
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
}

// latestArchiveForDay / nextArchivePath / fileExists 的命名与边界。
func TestArchiveFileNaming(t *testing.T) {
	dir := t.TempDir()
	day := "2026-10-19"
	if got := latestArchiveForDay(dir, day); got != "" {
		t.Fatalf("空目录应返回空串: %q", got)
	}
	if got := nextArchivePath(dir, day); got != filepath.Join(dir, "requests-"+day+".jsonl") {
		t.Fatalf("首个文件路径不对: %q", got)
	}

	for _, name := range []string{
		"requests-" + day + ".jsonl",
		"requests-" + day + ".1.jsonl",
		"requests-" + day + ".2.jsonl",
		"requests-" + day + ".bad.jsonl", // 非数字后缀：忽略
		"notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "requests-"+day+".9.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := latestArchiveForDay(dir, day); got != "requests-"+day+".2.jsonl" {
		t.Fatalf("latestArchiveForDay = %q", got)
	}
	if got := nextArchivePath(dir, day); got != filepath.Join(dir, "requests-"+day+".3.jsonl") {
		t.Fatalf("nextArchivePath = %q", got)
	}
	if !fileExists(filepath.Join(dir, "requests-"+day+".jsonl")) {
		t.Fatal("普通文件应存在")
	}
	if fileExists(filepath.Join(dir, "requests-"+day+".9.jsonl")) {
		t.Fatal("目录不应被当作归档文件")
	}

	// 目录不存在时 latestArchiveForDay 返回空串。
	if got := latestArchiveForDay(filepath.Join(dir, "missing"), day); got != "" {
		t.Fatalf("目录不存在应返回空串: %q", got)
	}
}

// openFile 失败：目录被换成文件、目标文件名被目录占用时记录错误。
func TestArchiveOpenFileErrors(t *testing.T) {
	t.Run("目录变成文件", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "arch")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		w := &archiveWriter{
			cfg:  Config{Enabled: true, Dir: dir, FileMaxBytes: 1 << 20, MaxBytes: 1 << 20, RetentionDays: 7},
			ch:   make(chan Event, 1),
			stop: make(chan struct{}),
			done: make(chan struct{}),
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		w.writeEvent(Event{Time: time.Now(), Status: 200})
		if w.errString() == "" {
			t.Fatal("目录不可建时应记录错误")
		}
	})

	t.Run("目标文件名被目录占用", func(t *testing.T) {
		dir := t.TempDir()
		now := time.Now()
		if err := os.Mkdir(filepath.Join(dir, "requests-"+now.Format("2006-01-02")+".jsonl"), 0o700); err != nil {
			t.Fatal(err)
		}
		w := &archiveWriter{
			cfg:  Config{Enabled: true, Dir: dir, FileMaxBytes: 1 << 20, MaxBytes: 1 << 20, RetentionDays: 7},
			ch:   make(chan Event, 1),
			stop: make(chan struct{}),
			done: make(chan struct{}),
		}
		w.writeEvent(Event{Time: now, Status: 200})
		if w.errString() == "" {
			t.Fatal("OpenFile 失败时应记录错误")
		}
	})

	// setErr(nil) 是空操作。
	w := &archiveWriter{}
	w.setErr(nil)
	if w.errString() != "" {
		t.Fatal("setErr(nil) 不应记录错误")
	}
}

// ReadArchive 的边界：limit 回落/夹取、非归档文件跳过、目录缺失报错、
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

	t.Run("目录被删后读取与统计报错", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "arch")
		r := New(Config{Enabled: true, Dir: dir, MaxBytes: 1 << 20, RetentionDays: 7})
		r.Close()
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if _, err := r.ReadArchive(10, Filter{}); err == nil {
			t.Fatal("目录缺失时 ReadArchive 应报错")
		}
		if s := r.Snapshot().Archive; s.LastError == "" {
			t.Fatalf("目录缺失时 stats 应记录错误: %+v", s)
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

// prune：归档关闭时空操作；目录缺失记录错误；非归档文件与同名目录不删；
// mtime 相同时按路径稳定排序（同批旧文件都被清理）。
func TestArchivePruneEdges(t *testing.T) {
	t.Run("归档关闭时为空操作", func(t *testing.T) {
		w := &archiveWriter{cfg: Config{Enabled: false, Dir: t.TempDir()}}
		w.prune()
		if w.errString() != "" {
			t.Fatal("关闭状态不应记录错误")
		}
	})

	t.Run("目录缺失记录错误", func(t *testing.T) {
		w := &archiveWriter{cfg: Config{Enabled: true, Dir: filepath.Join(t.TempDir(), "nope"), RetentionDays: 7, MaxBytes: 1 << 20}, done: make(chan struct{})}
		w.prune()
		if w.errString() == "" {
			t.Fatal("目录缺失应记录错误")
		}
	})

	t.Run("只清理过期归档文件", func(t *testing.T) {
		dir := t.TempDir()
		oldStamp := time.Unix(1700000000, 0)
		for _, name := range []string{"requests-2026-01-01.jsonl", "requests-2026-01-02.jsonl"} {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, oldStamp, oldStamp); err != nil { // mtime 相同，覆盖排序 tie 分支
				t.Fatal(err)
			}
		}
		keep := filepath.Join(dir, "notes.txt")
		if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "requests-dir.jsonl"), 0o700); err != nil {
			t.Fatal(err)
		}
		w := &archiveWriter{cfg: Config{Enabled: true, Dir: dir, RetentionDays: 7, MaxBytes: 1 << 20}, done: make(chan struct{})}
		w.prune()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if len(names) != 2 || names[0] != "notes.txt" || names[1] != "requests-dir.jsonl" {
			t.Fatalf("只应保留非归档文件与目录: %v", names)
		}
	})
}

// flush：没有打开文件时为空操作；有 buffer 时写盘（Close 后内容完整可读）。
func TestArchiveFlushNoop(t *testing.T) {
	w := &archiveWriter{cfg: Config{Enabled: true, Dir: t.TempDir()}}
	w.flush()
	if w.errString() != "" {
		t.Fatalf("无 buffer 时 flush 不应出错: %s", w.errString())
	}
}
