// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package reqlog

// full_more_test.go 补齐随机数失败回退与 Account 过滤。归档文件本身的写入、轮转、清理在 logarchive 里测。

import (
	"errors"
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

// Filter 的时间区间：闭区间，零值那一侧不设界（读归档时 logarchive 已先按区间筛过，这里直接测）。
func TestFilterTimeBounds(t *testing.T) {
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.Local)
	e := Event{Time: at}
	if !(Filter{From: at, To: at}).match(e) {
		t.Fatal("闭区间应包含端点")
	}
	if (Filter{From: at.Add(time.Second)}).match(e) || (Filter{To: at.Add(-time.Second)}).match(e) {
		t.Fatal("区间外的应筛掉")
	}
}

// ArchiveStats 只回归档状态；nil 记录器回零值。
func TestArchiveStats(t *testing.T) {
	var nilRec *Recorder
	if nilRec.ArchiveStats() != (ArchiveStats{}) {
		t.Fatal("nil 记录器应回零值")
	}
	r := New(Config{Enabled: true, Dir: t.TempDir(), RetentionDays: 7, MaxBytes: 1 << 20})
	r.Record(Event{Status: 200, OK: true})
	r.Close()
	if a := r.ArchiveStats(); !a.Enabled || a.Files != 1 || a.RetentionDays != 7 || a.MaxBytes != 1<<20 {
		t.Fatalf("归档状态不对: %+v", a)
	}
}
