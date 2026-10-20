package upstream

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
