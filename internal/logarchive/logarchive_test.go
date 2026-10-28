// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package logarchive

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// entry 测试用的日志条目。
type entry struct {
	Time time.Time `json:"time"`
	ID   string    `json:"id"`
}

func entryTime(e entry) time.Time { return e.Time }

func ids(rows []entry) []string {
	out := make([]string, 0, len(rows))
	for _, e := range rows {
		out = append(out, e.ID)
	}
	return out
}

// fastTicks 把刷盘和清理周期缩到毫秒级，免得等满一秒、一分钟。
func fastTicks(t *testing.T) {
	t.Helper()
	oldFlush, oldPrune := archiveTick, pruneTick
	archiveTick, pruneTick = time.Millisecond, time.Millisecond
	t.Cleanup(func() { archiveTick, pruneTick = oldFlush, oldPrune })
}

// eventually 每 2 毫秒看一次 cond，5 秒内成立才算过。
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("5 秒内没等到：%s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func readAll(t *testing.T, s *Store) []entry {
	t.Helper()
	rows, _, err := Read(s, Query[entry]{Limit: 1000, Time: entryTime})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func writeFile(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// 默认参数：单文件 16 MiB、队列 1024、不按天数删、总量 100 MiB；Dir 为空的归档什么都不做。
func TestNewDefaultsAndEmptyDir(t *testing.T) {
	s := New(Config{Dir: t.TempDir(), Prefix: "requests", Enabled: true})
	defer s.Close()
	if days, max := s.limits(); s.fileMax != defaultFileMax || cap(s.ch) != defaultQueue || days != 0 || max != defaultMaxBytes {
		t.Fatalf("默认参数不对: fileMax=%d queue=%d days=%d max=%d", s.fileMax, cap(s.ch), days, max)
	}

	none := New(Config{Prefix: "task", Enabled: true, RetentionDays: 3})
	none.Append(time.Now(), entry{ID: "x"})
	none.Reconfigure(true, 5, 1<<20)
	if none.Enabled() {
		t.Fatal("Dir 为空的归档不算开着")
	}
	if err := none.Clear(); err != nil {
		t.Fatal(err)
	}
	if rows, more, err := Read(none, Query[entry]{Time: entryTime}); rows != nil || more || err != nil {
		t.Fatalf("Dir 为空读出 %v %v %v", rows, more, err)
	}
	if st := none.Stats(); st.Enabled || st.Files != 0 || st.RetentionDays != 5 {
		t.Fatalf("Dir 为空的状态不对: %+v", st)
	}
	none.Close()

	var nilStore *Store
	nilStore.Append(time.Now(), entry{})
	nilStore.Reconfigure(true, 1, 1)
	nilStore.Close()
	if nilStore.Enabled() || nilStore.Clear() != nil || nilStore.Stats() != (Stats{}) {
		t.Fatal("nil 归档的方法应是空操作")
	}
	if rows, _, err := Read(nilStore, Query[entry]{Time: entryTime}); rows != nil || err != nil {
		t.Fatal("nil 归档读出来应为空")
	}
}

// 几类日志放同一个目录：各写各的前缀，读、统计、清空都只管自己的文件。
func TestPrefixIsolation(t *testing.T) {
	dir := t.TempDir()
	task := New(Config{Dir: dir, Prefix: "task", Enabled: true})
	system := New(Config{Dir: dir, Prefix: "system", Enabled: true})
	now := time.Now()
	task.Append(now, entry{Time: now, ID: "t1"})
	system.Append(now, entry{Time: now, ID: "s1"})
	system.Append(now.Add(time.Second), entry{Time: now.Add(time.Second), ID: "s2"})
	task.Close()
	system.Close()

	if got := ids(readAll(t, task)); len(got) != 1 || got[0] != "t1" {
		t.Fatalf("任务日志只应读到自己的: %v", got)
	}
	if got := ids(readAll(t, system)); len(got) != 2 || got[0] != "s2" {
		t.Fatalf("系统日志只应读到自己的、最新的在前: %v", got)
	}
	if st := task.Stats(); st.Files != 1 || st.OldestDay != now.Format(dayLayout) {
		t.Fatalf("任务日志的统计不对: %+v", st)
	}

	// 关闭后清空：后台协程已停，回报已关闭，文件不动。
	if err := task.Clear(); err != errClosed {
		t.Fatalf("关闭后清空应回报已关闭: %v", err)
	}
	reopened := New(Config{Dir: dir, Prefix: "task", Enabled: true})
	defer reopened.Close()
	if err := reopened.Clear(); err != nil {
		t.Fatal(err)
	}
	if got := names(t, dir); len(got) != 1 || !strings.HasPrefix(got[0], "system-") {
		t.Fatalf("清空任务日志不应碰系统日志: %v", got)
	}
}

// 运行中改开关：开着写、关掉不写且放开最后那份文件（到期能删）、再打开接着写。
func TestReconfigureEnabled(t *testing.T) {
	fastTicks(t)
	dir := t.TempDir()
	s := New(Config{Dir: dir, Prefix: "task"})
	defer s.Close()
	s.Append(time.Now(), entry{ID: "off"})
	if st := s.Stats(); st.Enabled || st.Files != 0 {
		t.Fatalf("没开留存时不写: %+v", st)
	}

	s.Reconfigure(true, 7, 1<<20)
	now := time.Now()
	s.Append(now, entry{Time: now, ID: "on"})
	eventually(t, "打开后写盘", func() bool { return len(readAll(t, s)) == 1 })

	s.Reconfigure(false, 1, 1<<20)
	path := filepath.Join(dir, "task-"+now.Format(dayLayout)+".jsonl")
	eventually(t, "关掉后放开最后那份文件", func() bool {
		old := time.Now().AddDate(0, 0, -3)
		_ = os.Chtimes(path, old, old)
		return s.Stats().Files == 0
	})
	s.Append(time.Now(), entry{ID: "off-again"})
	if st := s.Stats(); st.Enabled || st.Files != 0 || st.RetentionDays != 1 {
		t.Fatalf("关掉后不写、按新天数清: %+v", st)
	}
}

// 保留天数：超期的整份删；0 = 不按天数删，只按容量删最旧的。
func TestPruneDaysAndSize(t *testing.T) {
	t.Run("只清理过期归档文件", func(t *testing.T) {
		dir := t.TempDir()
		old := time.Unix(1700000000, 0)
		for _, name := range []string{"requests-2026-01-01.jsonl", "requests-2026-01-02.jsonl"} {
			writeFile(t, filepath.Join(dir, name), "x", old) // mtime 相同，覆盖排序时按路径定先后的分支
		}
		writeFile(t, filepath.Join(dir, "notes.txt"), "keep", old)
		writeFile(t, filepath.Join(dir, "task-2026-01-01.jsonl"), "x", old) // 别的前缀不归它管
		if err := os.Mkdir(filepath.Join(dir, "requests-dir.jsonl"), 0o700); err != nil {
			t.Fatal(err)
		}
		s := &Store{dir: dir, prefix: "requests"}
		s.setLimits(7, 1<<20)
		s.prune()
		if got := names(t, dir); strings.Join(got, ",") != "notes.txt,requests-dir.jsonl,task-2026-01-01.jsonl" {
			t.Fatalf("只应删过期的 requests 文件: %v", got)
		}
	})

	t.Run("天数为 0 永久保留，超容量删最旧的", func(t *testing.T) {
		dir := t.TempDir()
		for i, name := range []string{"requests-2026-09-20.jsonl", "requests-2026-09-21.jsonl", "requests-2026-09-22.jsonl"} {
			writeFile(t, filepath.Join(dir, name), strings.Repeat("x", 40), time.Now().AddDate(0, 0, -400+i))
		}
		s := &Store{dir: dir, prefix: "requests"}
		s.setLimits(0, 1<<20)
		s.prune()
		if got := names(t, dir); len(got) != 3 {
			t.Fatalf("天数为 0 时一年前的也留着: %v", got)
		}
		s.setLimits(0, 50)
		s.prune()
		if got := names(t, dir); len(got) != 1 || got[0] != "requests-2026-09-22.jsonl" {
			t.Fatalf("超容量应只剩最新的: %v", got)
		}
	})

	t.Run("正在写的那份不删", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "requests-2026-01-01.jsonl")
		writeFile(t, path, "x", time.Unix(1700000000, 0))
		s := &Store{dir: dir, prefix: "requests", path: path}
		s.setLimits(1, 1)
		s.prune()
		if got := names(t, dir); len(got) != 1 {
			t.Fatalf("正在写的不应删: %v", got)
		}
	})

	t.Run("目录不存在不算错，目录是文件算错", func(t *testing.T) {
		s := &Store{dir: filepath.Join(t.TempDir(), "nope"), prefix: "requests"}
		s.setLimits(7, 1<<20)
		s.prune()
		if s.errString() != "" {
			t.Fatalf("目录不存在不应记错: %s", s.errString())
		}
		file := filepath.Join(t.TempDir(), "file")
		writeFile(t, file, "x", time.Time{})
		s = &Store{dir: file, prefix: "requests"}
		s.prune()
		if s.errString() == "" {
			t.Fatal("目录是文件应记错")
		}
	})
}

// 清空：删掉全部文件（含正在写的），队列里没落盘的一并丢掉；清空后照样能写。
func TestClear(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{Dir: dir, Prefix: "system", Enabled: true})
	defer s.Close()
	writeFile(t, filepath.Join(dir, "system-2026-01-01.jsonl"), `{"id":"old"}`+"\n", time.Time{})
	now := time.Now()
	for i := 0; i < 50; i++ {
		s.Append(now, entry{Time: now, ID: "queued"})
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if st := s.Stats(); st.Files != 0 || st.OldestDay != "" {
		t.Fatalf("清空后不应再有文件: %+v", st)
	}
	s.Append(now, entry{Time: now, ID: "after"})
	s.Close()
	if got := ids(readAll(t, s)); len(got) != 1 || got[0] != "after" {
		t.Fatalf("清空后应接着写: %v", got)
	}

	file := filepath.Join(t.TempDir(), "file")
	writeFile(t, file, "x", time.Time{})
	bad := New(Config{Dir: file, Prefix: "system", Enabled: true})
	defer bad.Close()
	if err := bad.Clear(); err == nil {
		t.Fatal("目录是文件时清空应报错")
	}
}

// 删不掉的文件：清空回报错误。
func TestClearRemoveError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下权限位不生效")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "task-2026-01-01.jsonl"), "x", time.Time{})
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	s := &Store{dir: dir, prefix: "task"}
	if err := s.removeAll(); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("删不掉应报错: %v", err)
	}
}

// 关闭后 Append 不 panic、不写；重复 Close 不 panic。
func TestAppendAfterClose(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{Dir: dir, Prefix: "task", Enabled: true})
	s.Close()
	s.Close()
	s.Append(time.Now(), entry{ID: "late"})
	if got := names(t, dir); len(got) != 0 {
		t.Fatalf("关闭后不应再写: %v", got)
	}
}

// Read：按条目时间倒序、Limit 截断并回报 more、区间是闭区间、Keep 过滤、坏行跳过；
// 文件名日期在区间外（各放宽一天）的整份跳过，日期取不出的照读。
func TestRead(t *testing.T) {
	dir := t.TempDir()
	day := func(d int) time.Time { return time.Date(2026, 3, d, 12, 0, 0, 0, time.Local) }
	line := func(d int, id string) string {
		return `{"time":"` + day(d).Format(time.RFC3339) + `","id":"` + id + `"}` + "\n"
	}
	writeFile(t, filepath.Join(dir, "task-2026-03-01.jsonl"), line(1, "d1"), time.Time{})
	writeFile(t, filepath.Join(dir, "task-2026-03-05.jsonl"), line(5, "d5")+"not json\n"+line(5, "d5-keep"), time.Time{})
	writeFile(t, filepath.Join(dir, "task-2026-03-09.jsonl"), line(9, "d9"), time.Time{})
	// 名字是 03-20、内容却是 03-05：区间查 03-05 时这份按名字跳过，证明真的按文件名跳。
	writeFile(t, filepath.Join(dir, "task-2026-03-20.jsonl"), line(5, "misfiled"), time.Time{})
	writeFile(t, filepath.Join(dir, "task-bad.jsonl"), line(5, "undated"), time.Time{})
	s := &Store{dir: dir, prefix: "task"}

	all, more, err := Read(s, Query[entry]{Time: entryTime})
	if err != nil || more || len(all) != 6 {
		t.Fatalf("全读应有 6 条: %v %v %v", ids(all), more, err)
	}
	if all[0].ID != "d9" || all[5].ID != "d1" {
		t.Fatalf("应按时间倒序: %v", ids(all))
	}

	top, more, err := Read(s, Query[entry]{Limit: 2, Time: entryTime})
	if err != nil || !more || strings.Join(ids(top), ",") != "d9,d5" && strings.Join(ids(top), ",") != "d9,d5-keep" {
		t.Fatalf("Limit 2 应取最新两条并回报还有: %v %v %v", ids(top), more, err)
	}

	got, _, err := Read(s, Query[entry]{From: day(5), To: day(5), Time: entryTime,
		Keep: func(e entry) bool { return e.ID != "d5" }})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids(got), ",") != "d5-keep,undated" {
		t.Fatalf("闭区间 + Keep，区间外的文件按名字跳过: %v", ids(got))
	}

	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "task-2026-03-06.jsonl")); err == nil {
		if _, _, err := Read(s, Query[entry]{Time: entryTime}); err == nil {
			t.Fatal("文件打不开应报错")
		}
	}
}

// 边读边截：结果和全部读完再排序截断一样（同一时刻保留读出的先后），内存里不攒全部条目。
func TestReadTrimsWhileReading(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 3, 5, 12, 0, 0, 0, time.Local)
	var a, b strings.Builder
	for i := range 100 {
		// 两份文件交错：a 是偶数秒，b 是奇数秒；最后几条同一时刻，看并列时的先后。
		sec := min(i, 95)
		w := &a
		if i%2 == 1 {
			w = &b
		}
		fmt.Fprintf(w, `{"time":%q,"id":"%d"}`+"\n", base.Add(time.Duration(sec)*time.Second).Format(time.RFC3339), i)
	}
	writeFile(t, filepath.Join(dir, "task-2026-03-05.jsonl"), a.String(), time.Time{})
	writeFile(t, filepath.Join(dir, "task-2026-03-05.1.jsonl"), b.String(), time.Time{})
	s := &Store{dir: dir, prefix: "task"}

	all, _, err := Read(s, Query[entry]{Limit: 1000, Time: entryTime})
	if err != nil || len(all) != 100 {
		t.Fatalf("全读: %d %v", len(all), err)
	}
	top, more, err := Read(s, Query[entry]{Limit: 3, Time: entryTime})
	if err != nil || !more {
		t.Fatalf("应回报还有更早的: %v %v", more, err)
	}
	if got, want := strings.Join(ids(top), ","), strings.Join(ids(all[:3]), ","); got != want {
		t.Fatalf("边读边截应和全部读完再截一样: %s，应为 %s", got, want)
	}
	if cap(top) >= 20 {
		t.Fatalf("内存里攒了 %d 条，没有边读边截", cap(top))
	}
}

// Stats：文件数、大小、最早日期；目录是文件时记错。
func TestStats(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "task-2026-03-09.jsonl"), "12345", time.Time{})
	writeFile(t, filepath.Join(dir, "task-2026-03-01.1.jsonl"), "123", time.Time{})
	writeFile(t, filepath.Join(dir, "task-bad.jsonl"), "1", time.Time{})
	s := &Store{dir: dir, prefix: "task"}
	s.setLimits(30, 20<<20)
	st := s.Stats()
	if st.Files != 3 || st.Bytes != 9 || st.OldestDay != "2026-03-01" || st.RetentionDays != 30 || st.MaxBytes != 20<<20 {
		t.Fatalf("统计不对: %+v", st)
	}

	file := filepath.Join(t.TempDir(), "file")
	writeFile(t, file, "x", time.Time{})
	if st := (&Store{dir: file, prefix: "task"}).Stats(); st.LastError == "" {
		t.Fatal("目录是文件应记错")
	}
}

// 负数天数当 0（永久）。
func TestSetLimitsNegativeDays(t *testing.T) {
	s := &Store{}
	s.setLimits(-3, 0)
	if days, max := s.limits(); days != 0 || max != defaultMaxBytes {
		t.Fatalf("days=%d max=%d", days, max)
	}
}

// 边角：连着改两次设置只排一次清理；文件名里日期那段不是日期；目录是文件时读报错；
// 读进来的文件里区间外的条目照样按时间筛掉。
func TestEdgeCases(t *testing.T) {
	s := &Store{ch: make(chan record, 1), pruneReq: make(chan struct{}, 1)}
	s.Reconfigure(true, 1, 1)
	s.Reconfigure(false, 2, 2) // 清理请求已经排着，不再排第二个、也不阻塞
	if len(s.pruneReq) != 1 || s.Enabled() {
		t.Fatalf("pruneReq=%d enabled=%v", len(s.pruneReq), s.Enabled())
	}

	if day := (&Store{prefix: "task"}).dayOf("task-notadate1.jsonl"); day != "" {
		t.Fatalf("日期那段不是日期应取不到: %q", day)
	}

	file := filepath.Join(t.TempDir(), "file")
	writeFile(t, file, "x", time.Time{})
	if _, _, err := Read(&Store{dir: file, prefix: "task"}, Query[entry]{Time: entryTime}); err == nil {
		t.Fatal("目录是文件时读应报错")
	}

	dir := t.TempDir()
	in := time.Date(2026, 3, 5, 12, 0, 0, 0, time.Local)
	out := in.Add(3 * time.Hour)
	writeFile(t, filepath.Join(dir, "task-2026-03-05.jsonl"),
		`{"time":"`+in.Format(time.RFC3339)+`","id":"in"}`+"\n"+`{"time":"`+out.Format(time.RFC3339)+`","id":"out"}`+"\n", time.Time{})
	rows, _, err := Read(&Store{dir: dir, prefix: "task"}, Query[entry]{From: in, To: in.Add(time.Hour), Time: entryTime})
	if err != nil || strings.Join(ids(rows), ",") != "in" {
		t.Fatalf("同一份文件里区间外的条目应筛掉: %v %v", ids(rows), err)
	}
}
