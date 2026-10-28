// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// Package logarchive 按天切分的 JSONL 日志归档，请求记录、任务日志、系统日志共用。
//
// 每类日志一个文件名前缀：<前缀>-<日期>.jsonl，单个文件写满后轮转成 <前缀>-<日期>.N.jsonl。
// 几类日志可以放在同一个目录，各自只认自己前缀的文件。写入走有界队列、由后台协程落盘，
// 队列满了丢弃并计数，不让写盘拖慢调用方。后台定期清理：最后写入超过保留天数的、总量超过
// 上限时最旧的整份文件删掉，正在写的那份不删。开关、保留天数、容量上限都能在运行中改。
package logarchive

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultFileMax  = int64(16 << 20)
	defaultMaxBytes = int64(100 << 20)
	defaultQueue    = 1024
	defaultLimit    = 200
	dayLayout       = "2006-01-02"
)

// 测试可替换：刷盘周期、清理周期、写缓冲、文件 Stat。默认值就是直接调用/常量。
var (
	archiveTick     = time.Second
	pruneTick       = time.Minute
	newArchiveBuf   = bufio.NewWriterSize
	statArchiveFile = (*os.File).Stat
)

// errClosed 归档已经关闭。
var errClosed = errors.New("日志归档已关闭")

// Config 一类日志的归档参数。Dir 为空时什么都不写，读出来也是空的。
type Config struct {
	Dir string
	// Prefix 文件名前缀，如 requests、task、system。
	Prefix string
	// Enabled 是否写新的日志。关掉后已有的文件照样按天数和容量清理、照样能读。
	Enabled bool
	// RetentionDays 保留天数：文件最后一次写入超过这么多天就删；0 = 不按天数删（永久保留）。
	RetentionDays int
	// MaxBytes 这一类日志的总容量上限，超过先删最旧的；<=0 回落 100 MiB。
	MaxBytes     int64
	FileMaxBytes int64 // 单个文件上限，<=0 回落 16 MiB
	QueueSize    int   // 写入队列长度，<=0 回落 1024
}

// Stats 一类日志的存储状态。
type Stats struct {
	Enabled       bool   `json:"enabled"`
	Dir           string `json:"dir,omitempty"`
	RetentionDays int    `json:"retention_days"`
	MaxBytes      int64  `json:"max_bytes"`
	Files         int    `json:"files"`
	Bytes         int64  `json:"bytes"`
	// OldestDay 最早那份文件的日期（YYYY-MM-DD），没有文件时为空。
	OldestDay     string `json:"oldest_day,omitempty"`
	DroppedWrites uint64 `json:"dropped_writes"`
	LastError     string `json:"last_error,omitempty"`
}

type record struct {
	t time.Time
	v any
}

// Store 一类日志的归档，并发安全。nil 接收者的方法都是空操作。
type Store struct {
	dir     string
	prefix  string
	fileMax int64

	enabled  atomic.Bool
	closed   atomic.Bool
	limitMu  sync.Mutex
	days     int
	maxBytes int64

	ch        chan record
	clearReq  chan chan error
	pruneReq  chan struct{}
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	dropped   atomic.Uint64
	lastErrMu sync.Mutex
	lastErr   string

	// 以下只由后台协程访问（测试里直接调用时是单协程）。
	file *os.File
	buf  *bufio.Writer
	path string
	day  string
	size int64
}

// New 建一类日志的归档；Dir 不为空时启动后台协程（没开留存也启动：要按天数清理旧文件，
// 运行中打开留存时接着写）。
func New(cfg Config) *Store {
	s := &Store{dir: strings.TrimSpace(cfg.Dir), prefix: cfg.Prefix, fileMax: cfg.FileMaxBytes}
	if s.fileMax <= 0 {
		s.fileMax = defaultFileMax
	}
	s.setLimits(cfg.RetentionDays, cfg.MaxBytes)
	if s.dir == "" {
		return s
	}
	queue := cfg.QueueSize
	if queue <= 0 {
		queue = defaultQueue
	}
	s.enabled.Store(cfg.Enabled)
	s.ch = make(chan record, queue)
	s.clearReq = make(chan chan error)
	s.pruneReq = make(chan struct{}, 1)
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go s.run()
	return s
}

// Enabled 是否在写新的日志。
func (s *Store) Enabled() bool {
	return s != nil && s.ch != nil && s.enabled.Load()
}

// Append 把一条日志排进写入队列，t 决定写进哪一天的文件。没开留存或已关闭时丢掉；队列满了丢弃并计数。
func (s *Store) Append(t time.Time, v any) {
	if !s.Enabled() || s.closed.Load() {
		return
	}
	select {
	case s.ch <- record{t: t, v: v}:
	default:
		s.dropped.Add(1)
	}
}

// Reconfigure 运行中改开关、保留天数和容量上限，并马上按新规则清理一次。
func (s *Store) Reconfigure(enabled bool, days int, maxBytes int64) {
	if s == nil {
		return
	}
	s.setLimits(days, maxBytes)
	if s.ch == nil {
		return
	}
	s.enabled.Store(enabled)
	select {
	case s.pruneReq <- struct{}{}:
	default:
	}
}

// Clear 删掉这一类日志的全部文件（含正在写的那份），队列里还没落盘的一并丢掉。
func (s *Store) Clear() error {
	if s == nil || s.ch == nil {
		return nil
	}
	reply := make(chan error, 1)
	select {
	case s.clearReq <- reply:
	case <-s.done:
		return errClosed
	}
	return <-reply
}

// Close 写完队列里的日志、刷盘并停止后台协程。
func (s *Store) Close() {
	if s == nil || s.ch == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.stop)
		<-s.done
	})
}

func (s *Store) setLimits(days int, maxBytes int64) {
	if days < 0 {
		days = 0
	}
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	s.limitMu.Lock()
	s.days, s.maxBytes = days, maxBytes
	s.limitMu.Unlock()
}

func (s *Store) limits() (int, int64) {
	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	return s.days, s.maxBytes
}

func (s *Store) run() {
	defer close(s.done)
	flushT := time.NewTicker(archiveTick)
	defer flushT.Stop()
	pruneT := time.NewTicker(pruneTick)
	defer pruneT.Stop()
	s.prune()
	for {
		select {
		case r := <-s.ch:
			s.write(r)
		case <-flushT.C:
			s.flush()
			s.releaseIfDisabled()
		case <-pruneT.C:
			s.releaseIfDisabled()
			s.prune()
		case <-s.pruneReq:
			s.releaseIfDisabled()
			s.prune()
		case reply := <-s.clearReq:
			s.discardQueued()
			s.closeFile()
			reply <- s.removeAll()
		case <-s.stop:
			s.drain()
			s.flush()
			s.closeFile()
			return
		}
	}
}

// drain 关闭时把队列里还没写的记录写完。
func (s *Store) drain() {
	for {
		select {
		case r := <-s.ch:
			s.write(r)
		default:
			return
		}
	}
}

// releaseIfDisabled 关掉留存后不再占着最后那份文件：它算「正在写的」就永远不会被清理。
func (s *Store) releaseIfDisabled() {
	if !s.enabled.Load() {
		s.closeFile()
	}
}

func (s *Store) discardQueued() {
	for {
		select {
		case <-s.ch:
		default:
			return
		}
	}
}

func (s *Store) write(r record) {
	raw, err := json.Marshal(r.v)
	if err != nil {
		s.setErr(err)
		return
	}
	now := r.t
	if now.IsZero() {
		now = time.Now()
	}
	day := now.Format(dayLayout)
	if s.file == nil || s.day != day {
		if err := s.openFile(now, false); err != nil {
			s.setErr(err)
			return
		}
	}
	if s.size > 0 && s.size+int64(len(raw))+1 > s.fileMax {
		if err := s.openFile(now, true); err != nil {
			s.setErr(err)
			return
		}
	}
	if _, err := s.buf.Write(raw); err != nil {
		s.setErr(err)
		return
	}
	if err := s.buf.WriteByte('\n'); err != nil {
		s.setErr(err)
		return
	}
	s.size += int64(len(raw)) + 1
}

func (s *Store) openFile(now time.Time, rotate bool) error {
	s.closeFile()
	day := now.Format(dayLayout)
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(s.dir, s.prefix+"-"+day+".jsonl")
	if rotate {
		path = nextArchivePath(s.dir, s.prefix, day)
	} else if latest := latestArchiveForDay(s.dir, s.prefix, day); latest != "" {
		path = filepath.Join(s.dir, latest)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := statArchiveFile(f)
	if err != nil {
		_ = f.Close()
		return err
	}
	s.file = f
	s.buf = newArchiveBuf(f, 64<<10)
	s.path = path
	s.day = day
	s.size = info.Size()
	return nil
}

// latestArchiveForDay 某天编号最大的那份文件名（没有返回空串）。
func latestArchiveForDay(dir, prefix, day string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	base := prefix + "-" + day
	best := ""
	bestIndex := -1
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, base) || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		idx := 0
		if name[len(base):len(name)-len(".jsonl")] != "" {
			part := strings.TrimSuffix(strings.TrimPrefix(name[len(base):], "."), ".jsonl")
			n, err := strconv.Atoi(part)
			if err != nil {
				continue
			}
			idx = n
		}
		if idx > bestIndex {
			bestIndex = idx
			best = name
		}
	}
	return best
}

func nextArchivePath(dir, prefix, day string) string {
	base := filepath.Join(dir, prefix+"-"+day)
	path := base + ".jsonl"
	for i := 1; fileExists(path); i++ {
		path = fmt.Sprintf("%s.%d.jsonl", base, i)
	}
	return path
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func (s *Store) flush() {
	if s.buf == nil {
		return
	}
	if err := s.buf.Flush(); err != nil {
		s.setErr(err)
	}
}

func (s *Store) closeFile() {
	if s.buf != nil {
		_ = s.buf.Flush()
	}
	if s.file != nil {
		_ = s.file.Close()
	}
	s.file = nil
	s.buf = nil
	s.path = ""
	s.day = ""
	s.size = 0
}

// archiveFile 目录里属于这一类日志的一份文件。
type archiveFile struct {
	name  string
	path  string
	size  int64
	mtime time.Time
}

// list 目录里属于这一类日志的文件；目录不存在算没有文件。
func (s *Store) list() ([]archiveFile, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []archiveFile
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !s.owns(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, archiveFile{name: name, path: filepath.Join(s.dir, name), size: info.Size(), mtime: info.ModTime()})
	}
	return out, nil
}

func (s *Store) owns(name string) bool {
	return strings.HasPrefix(name, s.prefix+"-") && strings.HasSuffix(name, ".jsonl")
}

// dayOf 从文件名取日期（<前缀>-YYYY-MM-DD[.N].jsonl）；取不到返回空串。
func (s *Store) dayOf(name string) string {
	rest := strings.TrimPrefix(name, s.prefix+"-")
	if len(rest) < len(dayLayout) {
		return ""
	}
	day := rest[:len(dayLayout)]
	if _, err := time.Parse(dayLayout, day); err != nil {
		return ""
	}
	return day
}

func (s *Store) prune() {
	items, err := s.list()
	if err != nil {
		s.setErr(err)
		return
	}
	days, maxBytes := s.limits()
	var total int64
	for _, it := range items {
		total += it.size
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].mtime.Equal(items[j].mtime) {
			return items[i].path < items[j].path
		}
		return items[i].mtime.Before(items[j].mtime)
	})
	cutoff := time.Now().AddDate(0, 0, -days)
	for _, it := range items {
		expired := days > 0 && it.mtime.Before(cutoff)
		if it.path != s.path && (expired || total > maxBytes) {
			if err := os.Remove(it.path); err == nil {
				total -= it.size
			}
		}
	}
}

func (s *Store) removeAll() error {
	items, err := s.list()
	if err != nil {
		return err
	}
	var errs []error
	for _, it := range items {
		if err := os.Remove(it.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Query 读归档的条件。T 是落盘时的那个类型。
type Query[T any] struct {
	// Limit 最多返回几条（取最新的），<=0 回落 200。
	Limit int
	// From/To 闭区间，零值 = 这一侧不设界。也用来按文件名的日期跳过不相干的文件。
	From, To time.Time
	// Time 取条目的时间：区间判断和排序都按它。
	Time func(T) time.Time
	// Keep 其余条件，nil = 都要。
	Keep func(T) bool
}

// Read 读出符合条件的最近 Limit 条，按条目时间倒序（同一时刻保留读出的先后）；
// more 报告还有没有更早的符合条件的条目没返回。没开留存时照样读已有的文件。
// 边读边截：攒满两倍 Limit 条就排序截回 Limit 条，查「全部历史」也不会把几十 MiB 的归档整个读进内存；
// 截法和全部读完再排序截断的结果一样。
func Read[T any](s *Store, q Query[T]) (rows []T, more bool, err error) {
	if s == nil || s.dir == "" {
		return nil, false, nil
	}
	if q.Limit <= 0 {
		q.Limit = defaultLimit
	}
	items, err := s.list()
	if err != nil {
		return nil, false, err
	}
	// 文件名的日期按写入时条目时间的时区取，差不出一天：两侧各放宽一天再比。
	lo, hi := "", ""
	if !q.From.IsZero() {
		lo = q.From.AddDate(0, 0, -1).Format(dayLayout)
	}
	if !q.To.IsZero() {
		hi = q.To.AddDate(0, 0, 1).Format(dayLayout)
	}
	trim := func() {
		sort.SliceStable(rows, func(i, j int) bool { return q.Time(rows[i]).After(q.Time(rows[j])) })
		if len(rows) > q.Limit {
			rows, more = rows[:q.Limit], true
		}
	}
	add := func(v T) {
		if rows = append(rows, v); len(rows) >= 2*q.Limit {
			trim()
		}
	}
	// 文件读取顺序无关紧要：结果一律按条目时间排序。不能靠文件 mtime 还原时间顺序——同一秒内
	// 连续轮转写出的几个文件 mtime 经常完全相同，目录整体拷贝或恢复备份后 mtime 更不可信。
	for _, it := range items {
		if day := s.dayOf(it.name); day != "" && ((lo != "" && day < lo) || (hi != "" && day > hi)) {
			continue
		}
		if err := readFile(it.path, q, add); err != nil {
			return rows, false, err
		}
	}
	trim()
	return rows, more, nil
}

func readFile[T any](path string, q Query[T], add func(T)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		var v T
		if json.Unmarshal(scanner.Bytes(), &v) != nil {
			continue
		}
		t := q.Time(v)
		if (!q.From.IsZero() && t.Before(q.From)) || (!q.To.IsZero() && t.After(q.To)) {
			continue
		}
		if q.Keep != nil && !q.Keep(v) {
			continue
		}
		add(v)
	}
	return scanner.Err()
}

// Stats 存储状态：文件数、总大小、最早那份的日期，以及丢弃数和最近的错误。
func (s *Store) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	days, maxBytes := s.limits()
	st := Stats{
		Enabled: s.Enabled(), Dir: s.dir, RetentionDays: days, MaxBytes: maxBytes,
		DroppedWrites: s.dropped.Load(), LastError: s.errString(),
	}
	if s.dir == "" {
		return st
	}
	items, err := s.list()
	if err != nil {
		st.LastError = err.Error()
		return st
	}
	for _, it := range items {
		st.Files++
		st.Bytes += it.size
		if day := s.dayOf(it.name); day != "" && (st.OldestDay == "" || day < st.OldestDay) {
			st.OldestDay = day
		}
	}
	return st
}

func (s *Store) setErr(err error) {
	if err == nil {
		return
	}
	s.lastErrMu.Lock()
	s.lastErr = err.Error()
	s.lastErrMu.Unlock()
}

func (s *Store) errString() string {
	s.lastErrMu.Lock()
	defer s.lastErrMu.Unlock()
	return s.lastErr
}
