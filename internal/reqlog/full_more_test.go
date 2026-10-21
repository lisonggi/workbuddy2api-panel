// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package reqlog

// full_more_test.go 补齐随机数失败回退、归档写缓冲/刷盘/轮转/Stat/刷盘失败、
// ticker 周期刷盘、latest 文件命中、零时间事件、prune 的 Info 失败与 Account 过滤。

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 随机数失败时回退到时间戳，格式仍是 req-<十进制>。
func TestNewRequestIDFallback(t *testing.T) {
	old := randRead
	randRead = func([]byte) (int, error) { return 0, errors.New("rand boom") }
	t.Cleanup(func() { randRead = old })

	id := NewRequestID()
	rest := strings.TrimPrefix(id, "req-")
	if rest == id || rest == "" {
		t.Fatalf("格式不对: %q", id)
	}
	if _, err := strconv.ParseInt(rest, 10, 64); err != nil {
		t.Fatalf("回退 ID 应为十进制时间戳: %q", id)
	}
}

// 直接构造 archiveWriter（绕开 New 的预置默认值）时补齐默认参数。
func TestNewArchiveWriterDefaults(t *testing.T) {
	w := newArchiveWriter(Config{Enabled: true, Dir: t.TempDir()})
	defer w.close()
	if w.cfg.FileMaxBytes != defaultFileMax || w.cfg.QueueSize != defaultQueue {
		t.Fatalf("默认参数不对: FileMaxBytes=%d QueueSize=%d", w.cfg.FileMaxBytes, w.cfg.QueueSize)
	}
	if w.cfg.RetentionDays != 7 || w.cfg.MaxBytes != 100<<20 {
		t.Fatalf("保留参数不对: %+v", w.cfg)
	}
}

// ticker 周期刷盘：事件先在缓冲里，等 tick 后落盘（不等 Close）。
func TestArchiveTickerFlush(t *testing.T) {
	old := archiveTick
	archiveTick = time.Millisecond
	t.Cleanup(func() { archiveTick = old })

	dir := t.TempDir()
	w := newArchiveWriter(Config{Enabled: true, Dir: dir, FileMaxBytes: 1 << 20, MaxBytes: 1 << 20, RetentionDays: 7})
	w.enqueue(Event{Time: time.Now(), RequestID: "tick-1", Status: 200, OK: true, Outcome: OutcomeSuccess})

	path := filepath.Join(dir, "requests-"+time.Now().Format("2006-01-02")+".jsonl")
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(raw), "tick-1") {
			break
		}
		if time.Now().After(deadline) {
			w.close()
			t.Fatalf("tick 后事件未落盘: %q err=%v", raw, err)
		}
		<-time.After(2 * time.Millisecond)
	}
	w.close()
}

// writeEvent 收到零时间事件时用当前时间。
func TestWriteEventZeroTime(t *testing.T) {
	dir := t.TempDir()
	w := &archiveWriter{cfg: Config{Enabled: true, Dir: dir, FileMaxBytes: 1 << 20}}
	w.writeEvent(Event{RequestID: "zero-time"})
	w.closeFile()

	raw, err := os.ReadFile(filepath.Join(dir, "requests-"+time.Now().Format("2006-01-02")+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// writeEvent 只用 now 决定文件名；事件本身的 Time 保持原样（Record 入口已补零时间）。
	if !strings.Contains(string(raw), "zero-time") {
		t.Fatalf("事件未写入: %s", raw)
	}
}

// 当天已有基础归档文件时沿用 latest 文件，不新建。
func TestWriteEventUsesLatestFile(t *testing.T) {
	dir := t.TempDir()
	day := time.Now().Format("2006-01-02")
	base := filepath.Join(dir, "requests-"+day+".jsonl")
	if err := os.WriteFile(base, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	w := &archiveWriter{cfg: Config{Enabled: true, Dir: dir, FileMaxBytes: 1 << 20}}
	w.writeEvent(Event{Time: time.Now(), RequestID: "latest"})
	if w.path != base {
		t.Fatalf("应沿用 %s，实际 %s", base, w.path)
	}
	w.closeFile()
}

// 轮转时目录只读：openFile 失败并记录错误。
func TestWriteEventRotateError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下权限位不生效")
	}
	dir := t.TempDir()
	w := &archiveWriter{cfg: Config{Enabled: true, Dir: dir, FileMaxBytes: 32}}
	w.writeEvent(Event{Time: time.Now(), RequestID: "first-event-long-enough", Status: 200})
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	w.writeEvent(Event{Time: time.Now(), RequestID: "second", Status: 200})
	if err := w.errString(); !strings.Contains(err, "permission denied") {
		t.Fatalf("应记录轮转失败: %q", err)
	}
}

// failWriter 写总是失败。
type failWriter struct{ err error }

func (f failWriter) Write([]byte) (int, error) { return 0, f.err }

// 写缓冲：大事件直接写底层失败。
func TestWriteEventBufWriteError(t *testing.T) {
	old := newArchiveBuf
	newArchiveBuf = func(io.Writer, int) *bufio.Writer {
		return bufio.NewWriterSize(failWriter{err: errors.New("write boom")}, 1)
	}
	t.Cleanup(func() { newArchiveBuf = old })

	w := &archiveWriter{cfg: Config{Enabled: true, Dir: t.TempDir(), FileMaxBytes: 1 << 20}}
	w.writeEvent(Event{Time: time.Now(), RequestID: "big-event", Status: 200})
	if err := w.errString(); !strings.Contains(err, "write boom") {
		t.Fatalf("应记录写缓冲失败: %q", err)
	}
}

// 写换行时刷缓冲失败：缓冲正好容纳事件本身，换行触发 Flush。
func TestWriteEventNewlineError(t *testing.T) {
	evt := Event{Time: time.Now(), RequestID: "newline", Status: 200}
	raw, err := json.Marshal(evt)
	if err != nil {
		t.Fatal(err)
	}
	old := newArchiveBuf
	newArchiveBuf = func(io.Writer, int) *bufio.Writer {
		return bufio.NewWriterSize(failWriter{err: errors.New("newline boom")}, len(raw))
	}
	t.Cleanup(func() { newArchiveBuf = old })

	w := &archiveWriter{cfg: Config{Enabled: true, Dir: t.TempDir(), FileMaxBytes: 1 << 20}}
	w.writeEvent(evt)
	if err := w.errString(); !strings.Contains(err, "newline boom") {
		t.Fatalf("应记录换行写失败: %q", err)
	}
}

// openFile 的 Stat 失败。
func TestOpenFileStatError(t *testing.T) {
	old := statArchiveFile
	statArchiveFile = func(*os.File) (os.FileInfo, error) { return nil, errors.New("stat boom") }
	t.Cleanup(func() { statArchiveFile = old })

	w := &archiveWriter{cfg: Config{Enabled: true, Dir: t.TempDir(), FileMaxBytes: 1 << 20}}
	w.writeEvent(Event{Time: time.Now(), RequestID: "stat"})
	if err := w.errString(); !strings.Contains(err, "stat boom") {
		t.Fatalf("应记录 Stat 失败: %q", err)
	}
}

// flush 的底层写失败。
func TestFlushError(t *testing.T) {
	w := &archiveWriter{}
	w.buf = bufio.NewWriterSize(failWriter{err: errors.New("flush boom")}, 1)
	if _, err := w.buf.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	w.flush()
	if err := w.errString(); !strings.Contains(err, "flush boom") {
		t.Fatalf("应记录刷盘失败: %q", err)
	}
}

// prune：目录可读但不可进入时 DirEntry.Info 失败，跳过该文件（不删除、不记错）。
func TestPruneEntryInfoError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下权限位不生效")
	}
	dir := t.TempDir()
	oldFile := filepath.Join(dir, "requests-2026-10-19.jsonl")
	if err := os.WriteFile(oldFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	w := &archiveWriter{cfg: Config{Enabled: true, Dir: dir}, done: make(chan struct{})}
	w.prune()
	entries, err := os.ReadDir(dir) // 目录可读但不可进入：只能靠 ReadDir 观察
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Name() == filepath.Base(oldFile) {
			found = true
		}
	}
	if !found {
		t.Fatal("Info 失败时不应删除文件")
	}
	if err := w.errString(); err != "" {
		t.Fatalf("Info 失败不应记为归档错误: %q", err)
	}
}

// Filter.Account 不匹配时该事件被过滤。
func TestFilterAccountMismatch(t *testing.T) {
	f := Filter{Account: "bob"}
	if f.match(Event{Account: "alice"}) {
		t.Fatal("Account 不匹配应返回 false")
	}
	if !f.match(Event{Account: "Bob-2"}) {
		t.Fatal("Account 应大小写不敏感包含匹配")
	}
}
