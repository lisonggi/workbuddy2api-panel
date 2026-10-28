// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package session

import (
	"testing"
	"time"
)

// testConvStore 时钟可拨的会话 id 记录。
func testConvStore() (*convStore, *time.Time) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	s := newConvStore()
	s.now = func() time.Time { return now }
	return s, &now
}

// 一段对话第一轮记下的 id 之后各轮接着用，换了号也不变。
func TestConvIDsKeepFirstTurnID(t *testing.T) {
	s, _ := testConvStore()
	turn1 := Conversation{Opener: "open-1"}
	turn2 := Conversation{Opener: "open-1", Seed: "seed-A"}
	id := s.id("u1", turn1)
	if !uuidV7Dashed.MatchString(id) {
		t.Fatalf("应是带连字符的 UUID v7: %q", id)
	}
	if got := s.id("u1", turn1); got != id {
		t.Fatalf("第一轮重发应沿用同一个 id: %q / %q", id, got)
	}
	if got := s.id("u1", turn2); got != id {
		t.Fatalf("第二轮应认领第一轮的 id: %q / %q", id, got)
	}
	if got := s.id("u1", turn2); got != id {
		t.Fatalf("之后各轮不变: %q / %q", id, got)
	}
	if got := s.id("u2", turn2); got != id {
		t.Fatalf("换了号也沿用同一个 id: %q / %q", id, got)
	}
	if got := s.id("u3", Conversation{Opener: "open-2"}); got == id {
		t.Fatal("别的对话应另起一个 id")
	}
	a, b := OutboundConversationID("u1", Conversation{}), OutboundConversationID("u1", Conversation{})
	if a == b || !uuidV7Dashed.MatchString(a) {
		t.Fatalf("认不出对话时每次新生成: %q / %q", a, b)
	}
}

// 1.6.0 按号记的旧记录：对话第一次来时从这个号上的旧记录挪过来接着用，之后换号也不变；别的号
// 上的旧记录不认，过期的旧记录不认。
func TestConvIDsAdoptLegacy(t *testing.T) {
	s, now := testConvStore()
	put := func(kind, uid, digest, id string, last time.Time, claimed bool) {
		s.m[legacyKey(kind, uid, digest)] = convRecord{id: id, last: last, claimed: claimed}
	}
	put("conv", "u1", "seed-A", "id-A-on-u1", *now, false)
	put("conv", "u2", "seed-A", "id-A-on-u2", *now, false)
	put("open", "u1", "open-B", "id-B", *now, false)
	put("open", "u1", "open-C", "id-C", *now, true)
	put("conv", "u1", "seed-D", "id-D", now.Add(-convIDTTL), false)
	s.takeDirty()

	if got := s.id("u2", Conversation{Opener: "o", Seed: "seed-A"}); got != "id-A-on-u2" {
		t.Fatalf("应接着用这个号上的旧 id，got %q", got)
	}
	if !s.takeDirty() {
		t.Fatal("挪旧记录算改动")
	}
	if got := s.id("u1", Conversation{Opener: "o", Seed: "seed-A"}); got != "id-A-on-u2" {
		t.Fatalf("挪过来以后换号也不变，got %q", got)
	}
	if _, ok := s.m[legacyKey("conv", "u2", "seed-A")]; ok {
		t.Fatal("挪过的旧记录应删掉")
	}
	if got := s.id("u1", Conversation{Opener: "open-B"}); got != "id-B" {
		t.Fatalf("第一轮的旧记录也认，got %q", got)
	}
	if got := s.id("u1", Conversation{Opener: "open-B", Seed: "seed-B"}); got != "id-B" {
		t.Fatalf("第二轮认领挪过来的第一轮记录，got %q", got)
	}
	if got := s.id("u1", Conversation{Opener: "open-C"}); got == "id-C" {
		t.Fatal("已被认领的第一轮旧记录不能再给新对话用")
	}
	if got := s.id("u3", Conversation{Opener: "o", Seed: "seed-X"}); got == "id-A-on-u2" || got == "id-A-on-u1" {
		t.Fatal("别的对话不受旧记录影响")
	}
	if got := s.id("u1", Conversation{Opener: "o", Seed: "seed-D"}); got == "id-D" {
		t.Fatal("过期的旧记录不认")
	}
}

// 开头逐字相同的两段对话：第一轮共用一个 id，第二轮看到回复不同就分开；之后再来的新对话换一个新 id。
func TestConvIDsSameOpenerSplitAtSecondTurn(t *testing.T) {
	s, _ := testConvStore()
	x := s.id("u1", Conversation{Opener: "o"})
	if got := s.id("u1", Conversation{Opener: "o"}); got != x {
		t.Fatalf("开头相同的第一轮共用 id: %q / %q", x, got)
	}
	if got := s.id("u1", Conversation{Opener: "o", Seed: "A"}); got != x {
		t.Fatalf("A 认领第一轮的 id: %q / %q", x, got)
	}
	y := s.id("u1", Conversation{Opener: "o", Seed: "B"})
	if y == x {
		t.Fatal("B 的第二轮应换成自己的 id")
	}
	z := s.id("u1", Conversation{Opener: "o"})
	if z == x || z == y {
		t.Fatalf("第一轮的 id 已被认领，新对话应另起一个: %q", z)
	}
	if got := s.id("u1", Conversation{Opener: "o", Seed: "C"}); got != z {
		t.Fatalf("C 认领自己第一轮的 id: %q / %q", z, got)
	}
	if s.id("u1", Conversation{Opener: "o", Seed: "A"}) != x || s.id("u1", Conversation{Opener: "o", Seed: "B"}) != y {
		t.Fatal("已有的对话不受影响")
	}
}

// 记录在最后一次使用后保留 7 天：用着就一直不变，闲置超过 7 天换新的；过期的第一轮记录不能认领。
func TestConvIDsExpire(t *testing.T) {
	s, now := testConvStore()
	turn2 := Conversation{Opener: "o", Seed: "A"}
	id := s.id("u1", turn2)
	for i := 0; i < 3; i++ {
		*now = now.Add(convIDTTL - time.Minute)
		if got := s.id("u1", turn2); got != id {
			t.Fatalf("第 %d 次：保留期内应不变", i)
		}
	}
	*now = now.Add(convIDTTL)
	if got := s.id("u1", turn2); got == id {
		t.Fatal("闲置超过保留期应换新 id")
	}
	first := s.id("u1", Conversation{Opener: "p"})
	*now = now.Add(convIDTTL)
	if got := s.id("u1", Conversation{Opener: "p", Seed: "B"}); got == first {
		t.Fatal("过期的第一轮记录不应被认领")
	}
	if got := s.id("u1", Conversation{Opener: "p"}); got == first {
		t.Fatal("过期的第一轮记录不应沿用")
	}
}

// 改动标记：新增、认领算改动；同一小时内再用不算，跨了整点才算。
func TestConvIDsDirty(t *testing.T) {
	s, now := testConvStore()
	if s.takeDirty() {
		t.Fatal("空的不应有改动")
	}
	s.id("u1", Conversation{Opener: "o"})
	if !s.takeDirty() || s.takeDirty() {
		t.Fatal("新增算改动，取过就清掉")
	}
	s.id("u1", Conversation{Opener: "o", Seed: "A"})
	if !s.takeDirty() {
		t.Fatal("认领算改动")
	}
	*now = now.Add(10 * time.Minute)
	s.id("u1", Conversation{Opener: "o", Seed: "A"})
	if s.takeDirty() {
		t.Fatal("同一小时内再用不算改动")
	}
	*now = now.Add(time.Hour)
	s.id("u1", Conversation{Opener: "o", Seed: "A"})
	if !s.takeDirty() {
		t.Fatal("跨了整点算改动")
	}
	s.markDirty()
	if !s.takeDirty() {
		t.Fatal("markDirty 后应有改动")
	}
}

// 超过上限：先清过期的，再清最久没用的，降到九成。
func TestConvIDsEvict(t *testing.T) {
	s, now := testConvStore()
	for i := 0; i < convIDMax; i++ {
		var k [32]byte
		k[0], k[1], k[2] = byte(i), byte(i>>8), byte(i>>16)
		s.m[k] = convRecord{id: "x", last: now.Add(-time.Duration(i) * time.Second)}
	}
	keep := s.id("u1", Conversation{Opener: "new"})
	if n := len(s.m); n != convIDMax*9/10+1 {
		t.Fatalf("应降到九成再加上新的一条，剩 %d", n)
	}
	if got := s.id("u1", Conversation{Opener: "new"}); got != keep {
		t.Fatal("新加的一条不应被清掉")
	}
	var oldest [32]byte
	last := convIDMax - 1
	oldest[0], oldest[1] = byte(last), byte(last>>8)
	if _, ok := s.m[oldest]; ok {
		t.Fatal("最久没用的应先清")
	}
	var newest [32]byte
	if _, ok := s.m[newest]; !ok {
		t.Fatal("最近用过的应留下")
	}

	m := map[[32]byte]time.Time{{1}: now.Add(-convIDTTL), {2}: *now, {3}: now.Add(-time.Second)}
	evictStale(m, func(t time.Time) time.Time { return t }, *now, convIDTTL, 10)
	if len(m) != 2 {
		t.Fatalf("过期的应清掉: %v", m)
	}
}

// 存盘再读回：id、认领状态都在；过期的不存，读回时跳过过期、格式不对的，内存里已有的以内存为准。
func TestConvIDsSnapshotRestore(t *testing.T) {
	s, now := testConvStore()
	a := s.id("u1", Conversation{Opener: "o"})
	s.id("u1", Conversation{Opener: "o", Seed: "A"}) // 认领
	b := s.id("u2", Conversation{Opener: "p"})
	stale := s.id("u3", Conversation{Opener: "q"})
	*now = now.Add(time.Hour)
	for k, r := range s.m {
		if r.id == stale {
			r.last = now.Add(-convIDTTL)
			s.m[k] = r
		}
	}
	snap := s.snapshot()
	if len(snap) != 3 || len(s.m) != 3 {
		t.Fatalf("应存 3 条并清掉过期的: snap=%d m=%d", len(snap), len(s.m))
	}

	r, rnow := testConvStore()
	*rnow = *now
	snap = append(snap,
		convSnap{K: "!!", ID: "bad", T: now.Unix()},
		convSnap{K: snap[0].K, ID: "dup", T: now.Unix()},
		convSnap{K: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ID: "old", T: now.Add(-convIDTTL).Unix()},
		convSnap{K: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ID: "", T: now.Unix()},
	)
	if n := r.restore(snap); n != 3 {
		t.Fatalf("应读回 3 条，got %d", n)
	}
	if got := r.id("u1", Conversation{Opener: "o", Seed: "A"}); got != a {
		t.Fatalf("读回后同一段对话应沿用原来的 id: %q / %q", a, got)
	}
	if got := r.id("u1", Conversation{Opener: "o"}); got == a {
		t.Fatal("认领状态应读回：已认领的第一轮记录不再沿用")
	}
	if got := r.id("u2", Conversation{Opener: "p"}); got != b {
		t.Fatalf("未认领的第一轮记录读回后照旧: %q / %q", b, got)
	}

	full, _ := testConvStore()
	for i := 0; i < convIDMax; i++ {
		var k [32]byte
		k[0], k[1] = byte(i), byte(i>>8)
		k[31] = 0xff
		full.m[k] = convRecord{id: "x", last: *now}
	}
	if n := full.restore(snap); n != 0 {
		t.Fatalf("满了就不再读回，got %d", n)
	}
}

func TestDecodeKey(t *testing.T) {
	for _, in := range []string{"", "!!", "AAAA"} {
		if _, ok := decodeKey(in); ok {
			t.Errorf("%q 不是 32 字节的键", in)
		}
	}
	if _, ok := decodeKey("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); !ok {
		t.Fatal("43 个字符的 base64url 是 32 字节")
	}
}
