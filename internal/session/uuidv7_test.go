// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package session

import (
	"encoding/hex"
	"errors"
	"regexp"
	"testing"
	"time"
)

var (
	uuidV7Dashed = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	uuidV7Plain  = regexp.MustCompile(`^[0-9a-f]{12}7[0-9a-f]{3}[89ab][0-9a-f]{15}$`)
)

// v7Millis 取 UUID v7 前 48 位的毫秒时间戳。
func v7Millis(t *testing.T, h string) int64 {
	t.Helper()
	b, err := hex.DecodeString(h[:12])
	if err != nil {
		t.Fatal(err)
	}
	var ms int64
	for _, x := range b {
		ms = ms<<8 | int64(x)
	}
	return ms
}

// TestNewUUIDv7 形状同官方 uuidv7()：版本 7、RFC 9562 变体，前 48 位是当前毫秒；去掉连字符的就是 32 位十六进制。
func TestNewUUIDv7(t *testing.T) {
	before := time.Now().UnixMilli()
	d, h := NewUUIDv7(), NewUUIDv7Hex()
	after := time.Now().UnixMilli()
	if !uuidV7Dashed.MatchString(d) || !uuidV7Plain.MatchString(h) {
		t.Fatalf("形状不对: %q / %q", d, h)
	}
	for _, s := range []string{d[0:8] + d[9:13], h} {
		if ms := v7Millis(t, s); ms < before || ms > after {
			t.Fatalf("时间戳 %d 不在 [%d, %d]", ms, before, after)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewUUIDv7Hex()
		if seen[id] {
			t.Fatalf("重复: %s", id)
		}
		seen[id] = true
	}
	// 固定时刻：时间戳按大端写进前 6 个字节。
	if got := uuidV7Hex(time.UnixMilli(0x0123456789ab))[:12]; got != "0123456789ab" {
		t.Fatalf("时间戳段 = %s", got)
	}
}

// TestNewUUIDv7EntropyFailure 熵源出错时仍是合法的 UUID v7。
func TestNewUUIDv7EntropyFailure(t *testing.T) {
	old := cryptoRandRead
	cryptoRandRead = func([]byte) (int, error) { return 0, errors.New("rand boom") }
	t.Cleanup(func() { cryptoRandRead = old })
	if d := NewUUIDv7(); !uuidV7Dashed.MatchString(d) {
		t.Fatalf("熵源故障时形状不对: %q", d)
	}
}

// TestDerivedMemo 派生 id：同键在有效期内取回同一个（取用时顺延），过期后换新；派生的轮次 id、会话 id
// 都是 UUID v7，时刻是第一次派生时。
func TestDerivedMemo(t *testing.T) {
	now := time.UnixMilli(0x0123456789ab)
	d := &derivedMemo{m: map[[32]byte]derivedEntry{}, now: func() time.Time { return now }}
	a := d.get("k", false)
	if !uuidV7Plain.MatchString(a) || a[:12] != "0123456789ab" {
		t.Fatalf("第一次派生 = %q", a)
	}
	now = now.Add(derivedIDTTL - time.Minute)
	if got := d.get("k", false); got != a {
		t.Fatalf("有效期内应取回同一个: %q / %q", got, a)
	}
	now = now.Add(derivedIDTTL - time.Minute) // 上次取用顺延过，仍有效
	if got := d.get("k", false); got != a {
		t.Fatalf("取用后应顺延: %q / %q", got, a)
	}
	now = now.Add(derivedIDTTL)
	if got := d.get("k", false); got == a || !uuidV7Plain.MatchString(got) {
		t.Fatalf("过期后应换新: %q", got)
	}
	if c := d.get("c", true); !uuidV7Dashed.MatchString(c) {
		t.Fatalf("会话 id 形状 = %q", c)
	}
	if RequestIDForKey("x") == TurnRequestID("x") {
		t.Fatal("会话级与轮级同名键应互不相干")
	}
}

// TestDerivedMemoEvict 超过条数上限时先清过期的，仍超就随机清到上限的九成。
func TestDerivedMemoEvict(t *testing.T) {
	now := time.Now()
	d := &derivedMemo{m: map[[32]byte]derivedEntry{}, now: func() time.Time { return now }}
	for i := 0; i < derivedIDMax; i++ {
		d.m[sha256Key(i)] = derivedEntry{id: "x", exp: now.Add(time.Hour)}
	}
	d.m[sha256Key(-1)] = derivedEntry{id: "old", exp: now.Add(-time.Second)}
	d.get("new", false)
	if _, ok := d.m[sha256Key(-1)]; ok {
		t.Fatal("过期记录应先清掉")
	}
	if n := len(d.m); n > derivedIDMax*9/10+1 {
		t.Fatalf("清理后 %d 条，应不超过上限的九成", n)
	}
}

func sha256Key(i int) [32]byte {
	var k [32]byte
	k[0], k[1], k[2], k[3] = byte(i), byte(i>>8), byte(i>>16), byte(i>>24)
	k[4] = 0xee
	return k
}
