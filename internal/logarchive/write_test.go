// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package logarchive

// write_test.go 写盘这一侧：轮转与命名、写缓冲/刷盘/Stat 失败、ticker 周期刷盘、队列满丢弃计数、
// 清理时 Info 失败。原先在 reqlog 里测，随归档代码一起挪过来。

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// bare 不起后台协程的归档，测试直接调写盘的方法。
func bare(dir, prefix string, fileMax int64) *Store {
	s := &Store{dir: dir, prefix: prefix, fileMax: fileMax}
	s.setLimits(7, 1<<20)
	return s
}

// 关闭时队列里还没写的记录照样写完：直接测 drain，不靠关闭那一刻队列里正好有记录。
func TestDrainWritesQueued(t *testing.T) {
	dir := t.TempDir()
	s := bare(dir, "task", 1<<20)
	s.ch = make(chan record, 4)
	now := time.Now()
	for _, id := range []string{"q1", "q2", "q3"} {
		s.ch <- record{t: now, v: entry{Time: now, ID: id}}
	}
	s.drain()
	s.flush()
	s.closeFile()
	rows, _, err := Read(s, Query[entry]{Limit: 10, Time: entryTime})
	if err != nil || len(s.ch) != 0 || strings.Join(ids(rows), ",") != "q1,q2,q3" {
		t.Fatalf("应写完队列里的 3 条: %v 剩 %d %v", ids(rows), len(s.ch), err)
	}
}

// latestArchiveForDay / nextArchivePath / fileExists 的命名与边界。
func TestArchiveFileNaming(t *testing.T) {
	dir := t.TempDir()
	day := "2026-10-19"
	if got := latestArchiveForDay(dir, "requests", day); got != "" {
		t.Fatalf("空目录应返回空串: %q", got)
	}
	if got := nextArchivePath(dir, "requests", day); got != filepath.Join(dir, "requests-"+day+".jsonl") {
		t.Fatalf("首个文件路径不对: %q", got)
	}

	for _, name := range []string{
		"requests-" + day + ".jsonl",
		"requests-" + day + ".1.jsonl",
		"requests-" + day + ".2.jsonl",
		"requests-" + day + ".bad.jsonl", // 非数字后缀：忽略
		"task-" + day + ".5.jsonl",       // 别的前缀
		"notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "requests-"+day+".9.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := latestArchiveForDay(dir, "requests", day); got != "requests-"+day+".2.jsonl" {
		t.Fatalf("latestArchiveForDay = %q", got)
	}
	if got := nextArchivePath(dir, "requests", day); got != filepath.Join(dir, "requests-"+day+".3.jsonl") {
		t.Fatalf("nextArchivePath = %q", got)
	}
	if got := latestArchiveForDay(dir, "task", day); got != "task-"+day+".5.jsonl" {
		t.Fatalf("各前缀各算各的: %q", got)
	}
	if !fileExists(filepath.Join(dir, "requests-"+day+".jsonl")) {
		t.Fatal("普通文件应存在")
	}
	if fileExists(filepath.Join(dir, "requests-"+day+".9.jsonl")) {
		t.Fatal("目录不应被当作归档文件")
	}
	if got := latestArchiveForDay(filepath.Join(dir, "missing"), "requests", day); got != "" {
		t.Fatalf("目录不存在应返回空串: %q", got)
	}
}

// 单个文件写满轮转成 .N；同一天已有基础文件时沿用编号最大的那份。
func TestWriteRotatesAndReusesLatest(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	s := bare(dir, "requests", 60)
	for i := 0; i < 3; i++ {
		s.write(record{t: now, v: entry{Time: now, ID: strings.Repeat("x", 20)}})
	}
	s.closeFile()
	day := now.Format(dayLayout)
	if got := names(t, dir); strings.Join(got, ",") != "requests-"+day+".1.jsonl,requests-"+day+".2.jsonl,requests-"+day+".jsonl" {
		t.Fatalf("每条都超过一半上限，应各占一份: %v", got)
	}

	s = bare(dir, "requests", 1<<20)
	s.write(record{t: now, v: entry{ID: "latest"}})
	if s.path != filepath.Join(dir, "requests-"+day+".2.jsonl") {
		t.Fatalf("应沿用编号最大的那份，实际 %s", s.path)
	}
	s.closeFile()
}

// 零时间的条目按当前时间选文件，条目本身原样写。
func TestWriteZeroTime(t *testing.T) {
	dir := t.TempDir()
	s := bare(dir, "requests", 1<<20)
	s.write(record{v: entry{ID: "zero-time"}})
	s.closeFile()
	raw, err := os.ReadFile(filepath.Join(dir, "requests-"+time.Now().Format(dayLayout)+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "zero-time") {
		t.Fatalf("没写进去: %s", raw)
	}
}

// 序列化不了的值记错、不写。
func TestWriteMarshalError(t *testing.T) {
	s := bare(t.TempDir(), "requests", 1<<20)
	s.write(record{t: time.Now(), v: func() {}})
	if s.errString() == "" || s.file != nil {
		t.Fatalf("序列化失败应记错、不开文件: %q", s.errString())
	}
}

// openFile 失败：目录被换成文件、目标文件名被目录占用时记录错误。
func TestArchiveOpenFileErrors(t *testing.T) {
	t.Run("目录变成文件", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "arch")
		if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		s := bare(dir, "requests", 1<<20)
		s.write(record{t: time.Now(), v: entry{}})
		if s.errString() == "" {
			t.Fatal("目录不可建时应记录错误")
		}
	})

	t.Run("目标文件名被目录占用", func(t *testing.T) {
		dir := t.TempDir()
		now := time.Now()
		if err := os.Mkdir(filepath.Join(dir, "requests-"+now.Format(dayLayout)+".jsonl"), 0o700); err != nil {
			t.Fatal(err)
		}
		s := bare(dir, "requests", 1<<20)
		s.write(record{t: now, v: entry{}})
		if s.errString() == "" {
			t.Fatal("OpenFile 失败时应记录错误")
		}
	})

	// setErr(nil) 是空操作。
	s := &Store{}
	s.setErr(nil)
	if s.errString() != "" {
		t.Fatal("setErr(nil) 不应记录错误")
	}
}

// 轮转时目录只读：openFile 失败并记录错误。
func TestWriteRotateError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下权限位不生效")
	}
	dir := t.TempDir()
	s := bare(dir, "requests", 32)
	s.write(record{t: time.Now(), v: entry{ID: "first-event-long-enough"}})
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	s.write(record{t: time.Now(), v: entry{ID: "second"}})
	if err := s.errString(); !strings.Contains(err, "permission denied") {
		t.Fatalf("应记录轮转失败: %q", err)
	}
}

// failWriter 写总是失败。
type failWriter struct{ err error }

func (f failWriter) Write([]byte) (int, error) { return 0, f.err }

// 写缓冲：大条目直接写底层失败。
func TestWriteBufWriteError(t *testing.T) {
	old := newArchiveBuf
	newArchiveBuf = func(io.Writer, int) *bufio.Writer {
		return bufio.NewWriterSize(failWriter{err: errors.New("write boom")}, 1)
	}
	t.Cleanup(func() { newArchiveBuf = old })

	s := bare(t.TempDir(), "requests", 1<<20)
	s.write(record{t: time.Now(), v: entry{ID: "big-event"}})
	if err := s.errString(); !strings.Contains(err, "write boom") {
		t.Fatalf("应记录写缓冲失败: %q", err)
	}
}

// 写换行时刷缓冲失败：缓冲正好容纳条目本身，换行触发 Flush。
func TestWriteNewlineError(t *testing.T) {
	e := entry{Time: time.Now(), ID: "newline"}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	old := newArchiveBuf
	newArchiveBuf = func(io.Writer, int) *bufio.Writer {
		return bufio.NewWriterSize(failWriter{err: errors.New("newline boom")}, len(raw))
	}
	t.Cleanup(func() { newArchiveBuf = old })

	s := bare(t.TempDir(), "requests", 1<<20)
	s.write(record{t: e.Time, v: e})
	if err := s.errString(); !strings.Contains(err, "newline boom") {
		t.Fatalf("应记录换行写失败: %q", err)
	}
}

// openFile 的 Stat 失败。
func TestOpenFileStatError(t *testing.T) {
	old := statArchiveFile
	statArchiveFile = func(*os.File) (os.FileInfo, error) { return nil, errors.New("stat boom") }
	t.Cleanup(func() { statArchiveFile = old })

	s := bare(t.TempDir(), "requests", 1<<20)
	s.write(record{t: time.Now(), v: entry{ID: "stat"}})
	if err := s.errString(); !strings.Contains(err, "stat boom") {
		t.Fatalf("应记录 Stat 失败: %q", err)
	}
}

// flush：没有打开文件时为空操作；底层写失败记错。
func TestFlush(t *testing.T) {
	s := &Store{}
	s.flush()
	if s.errString() != "" {
		t.Fatalf("无 buffer 时 flush 不应出错: %s", s.errString())
	}
	s.buf = bufio.NewWriterSize(failWriter{err: errors.New("flush boom")}, 1)
	if _, err := s.buf.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	s.flush()
	if err := s.errString(); !strings.Contains(err, "flush boom") {
		t.Fatalf("应记录刷盘失败: %q", err)
	}
}

// ticker 周期刷盘：条目先在缓冲里，等 tick 后落盘（不等 Close）。
func TestArchiveTickerFlush(t *testing.T) {
	fastTicks(t)
	dir := t.TempDir()
	s := New(Config{Dir: dir, Prefix: "requests", Enabled: true, FileMaxBytes: 1 << 20, MaxBytes: 1 << 20, RetentionDays: 7})
	defer s.Close()
	s.Append(time.Now(), entry{ID: "tick-1"})
	path := filepath.Join(dir, "requests-"+time.Now().Format(dayLayout)+".jsonl")
	eventually(t, "tick 后落盘", func() bool {
		raw, err := os.ReadFile(path)
		return err == nil && strings.Contains(string(raw), "tick-1")
	})
}

// 队列满了丢弃并计数。
func TestArchiveQueueDropCounter(t *testing.T) {
	s := &Store{dir: t.TempDir(), prefix: "requests", ch: make(chan record, 1)}
	s.enabled.Store(true)
	s.ch <- record{v: entry{ID: "occupied"}}
	s.Append(time.Now(), entry{ID: "dropped"})
	if got := s.Stats().DroppedWrites; got != 1 {
		t.Fatalf("dropped=%d want 1", got)
	}
}

// 清理：目录可读但不可进入时 DirEntry.Info 失败，跳过该文件（不删除、不记错）。
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
	s := bare(dir, "requests", 1<<20)
	s.setLimits(1, 1)
	s.prune()
	found := false
	for _, name := range names(t, dir) { // 目录可读但不可进入：只能靠 ReadDir 观察
		if name == filepath.Base(oldFile) {
			found = true
		}
	}
	if !found {
		t.Fatal("Info 失败时不应删除文件")
	}
	if err := s.errString(); err != "" {
		t.Fatalf("Info 失败不应记为归档错误: %q", err)
	}
}
