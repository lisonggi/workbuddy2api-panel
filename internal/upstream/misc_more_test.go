package upstream

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestInjectPromptCacheKeyGuards 坏 body / 已有键 / 空键的守卫语义。
func TestInjectPromptCacheKeyGuards(t *testing.T) {
	if out := InjectPromptCacheKey(nil, "u1", "c1"); len(out) != 0 {
		t.Errorf("空 body 应原样返回，got %q", out)
	}
	bad := []byte(`{not-json`)
	if out := InjectPromptCacheKey(bad, "u1", "c1"); string(out) != string(bad) {
		t.Errorf("坏 JSON 应原样返回，got %q", out)
	}

	emptyKey := []byte(`{"model":"m","prompt_cache_key":""}`)
	if got := extractCacheKey(t, InjectPromptCacheKey(emptyKey, "u1", "c1")); got == "" {
		t.Error("空字符串键应被替换为新键")
	}
	nonString := []byte(`{"model":"m","prompt_cache_key":123}`)
	if got := extractCacheKey(t, InjectPromptCacheKey(nonString, "u1", "c1")); got == "" {
		t.Error("非字符串键应被替换")
	}
}

// TestInjectPromptCacheKeySources 会话哈希源优先级：body conversation_id >
// conversationId > 入参 conversationID > 空会话。
func TestInjectPromptCacheKeySources(t *testing.T) {
	uid := "user-123456789"
	cases := []struct {
		name string
		body string
		conv string
		want string
	}{
		{"conversation_id 优先", `{"model":"m","conversation_id":"a","conversationId":"b"}`, "c", buildCacheKey(uid, "a")},
		{"camelCase 兜底", `{"model":"m","conversationId":"b"}`, "c", buildCacheKey(uid, "b")},
		{"入参兜底", `{"model":"m"}`, "c", buildCacheKey(uid, "c")},
		{"全空", `{"model":"m"}`, "", buildCacheKey(uid, "")},
		{"空白字段忽略", `{"model":"m","conversation_id":"   ","conversationId":"x"}`, "", buildCacheKey(uid, "x")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractCacheKey(t, InjectPromptCacheKey([]byte(tc.body), uid, tc.conv))
			if got != tc.want {
				t.Errorf("key = %q，应为 %q", got, tc.want)
			}
		})
	}
}

// TestBuildCacheKey 稳定键格式：uid8 隔离段、同源稳定、不同源不同、空 uid 用 "-"。
func TestBuildCacheKey(t *testing.T) {
	long := buildCacheKey("1234567890", "conv")
	if !strings.HasPrefix(long, "wb2a-12345678-") {
		t.Errorf("uid 应截 8 位: %q", long)
	}
	if buildCacheKey("1234567890", "conv") != long {
		t.Error("同账号同会话应稳定")
	}
	if buildCacheKey("1234567890", "other") == long {
		t.Error("不同会话应不同")
	}
	if buildCacheKey("other", "conv") == long {
		t.Error("不同账号应不同")
	}
	if !strings.HasPrefix(buildCacheKey("", ""), "wb2a--") {
		t.Errorf("空 uid 段应为 -: %q", buildCacheKey("", ""))
	}
	if buildCacheKey("u", "") == buildCacheKey("u", "x") {
		t.Error("空会话与显式会话应不同")
	}
}

// TestStrField 只取字符串并去空白。
func TestStrField(t *testing.T) {
	obj := map[string]any{"s": "  v  ", "n": 1, "nil": nil}
	if got := strField(obj, "s"); got != "v" {
		t.Errorf("strField = %q", got)
	}
	for _, k := range []string{"n", "nil", "missing"} {
		if got := strField(obj, k); got != "" {
			t.Errorf("strField(%q) = %q，应为空", k, got)
		}
	}
}

// TestReadTrimmedFile 读文件并 trim；超限/缺失返回错误。
func TestReadTrimmedFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "tok")
	if err := os.WriteFile(fp, []byte("  tok-123 \n"), 0o600); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	got, err := readTrimmedFile(fp, 64)
	if err != nil || got != "tok-123" {
		t.Fatalf("got %q err=%v", got, err)
	}

	exact := filepath.Join(dir, "exact")
	if err := os.WriteFile(exact, []byte(strings.Repeat("x", 8)), 0o600); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	if got, err := readTrimmedFile(exact, 8); err != nil || got != "xxxxxxxx" {
		t.Errorf("正好等于上限应允许: %q/%v", got, err)
	}

	over := filepath.Join(dir, "over")
	if err := os.WriteFile(over, []byte(strings.Repeat("x", 9)), 0o600); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	if _, err := readTrimmedFile(over, 8); err != errDeviceTokenTooLarge {
		t.Errorf("超限应返回 errDeviceTokenTooLarge，got %v", err)
	}

	if _, err := readTrimmedFile(filepath.Join(dir, "missing"), 8); err == nil {
		t.Error("缺失文件应返回错误")
	}

	blank := filepath.Join(dir, "blank")
	if err := os.WriteFile(blank, []byte(" \n\t "), 0o600); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	if got, err := readTrimmedFile(blank, 8); err != nil || got != "" {
		t.Errorf("纯空白应 trim 成空串，got %q/%v", got, err)
	}
}

// TestReadDeviceTokenFileCache 文件读取缓存：路径空直接空；命中复用；过期重读；
// 读失败清空并缓存错误。
func TestReadDeviceTokenFileCache(t *testing.T) {
	if got := readDeviceTokenFile(""); got != "" {
		t.Errorf("空路径应返回空串，got %q", got)
	}

	dir := t.TempDir()
	fp := filepath.Join(dir, "device_token")
	if err := os.WriteFile(fp, []byte("tok-1"), 0o600); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	save := resetDeviceTokenFileCache(fp)
	defer save()

	if got := readDeviceTokenFile(fp); got != "tok-1" {
		t.Fatalf("首次读取 = %q", got)
	}
	// 缓存期内重写文件不生效。
	if err := os.WriteFile(fp, []byte("tok-2"), 0o600); err != nil {
		t.Fatalf("重写文件: %v", err)
	}
	if got := readDeviceTokenFile(fp); got != "tok-1" {
		t.Errorf("缓存期内应复用旧值，got %q", got)
	}
	// 过期后重读。
	dtFileCache.mu.Lock()
	dtFileCache.readAt = time.Now().Add(-deviceTokenFileTTL - time.Minute)
	dtFileCache.mu.Unlock()
	if got := readDeviceTokenFile(fp); got != "tok-2" {
		t.Errorf("过期后应重读，got %q", got)
	}

	// 换路径触发重读；文件缺失 → 空 + lastErr。
	missing := filepath.Join(dir, "missing")
	if got := readDeviceTokenFile(missing); got != "" {
		t.Errorf("缺失文件应返回空串，got %q", got)
	}
	dtFileCache.mu.Lock()
	if dtFileCache.lastErr == nil {
		t.Error("读失败应记录 lastErr")
	}
	dtFileCache.mu.Unlock()

	// 文件出现 + 缓存过期 → 读取成功并清空 lastErr。
	if err := os.WriteFile(missing, []byte("tok-3"), 0o600); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	dtFileCache.mu.Lock()
	dtFileCache.readAt = time.Now().Add(-deviceTokenFileTTL - time.Minute)
	dtFileCache.mu.Unlock()
	if got := readDeviceTokenFile(missing); got != "tok-3" {
		t.Errorf("恢复后 = %q", got)
	}
	dtFileCache.mu.Lock()
	lastErr := dtFileCache.lastErr
	dtFileCache.mu.Unlock()
	if lastErr != nil {
		t.Errorf("成功后应清空 lastErr，got %v", lastErr)
	}
}
