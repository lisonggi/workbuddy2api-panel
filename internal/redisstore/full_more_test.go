package redisstore

// full_more_test.go 补齐 Close 等待超时、DelBind 命令失败、LoadBinds 单键读取失败。

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// captureRedisLogs 捕获 log 输出。
func captureRedisLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	old := log.Writer()
	buf := &bytes.Buffer{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return buf
}

// Close 等待在途写超时：打 WARN 并照常返回。
func TestCloseWaitTimeout(t *testing.T) {
	old := closeWaitTimeout
	closeWaitTimeout = 10 * time.Millisecond
	t.Cleanup(func() { closeWaitTimeout = old })

	logs := captureRedisLogs(t)
	u := &Upstash{}
	u.wg.Add(1) // 模拟一笔永远不结束的写；超时后由测试补 Done 放行走查 goroutine
	if err := u.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	u.wg.Done()
	if !strings.Contains(logs.String(), "Close 等待在途写超时") {
		t.Fatalf("日志 = %q", logs.String())
	}
}

// DelBind 命令失败只记 debug 日志，不 panic。
func TestDelBindErrorLogged(t *testing.T) {
	u, mr := newUpstash(t)
	logs := captureRedisLogs(t)
	mr.SetError("boom")
	u.DelBind("k1")
	u.wg.Wait()
	if text := logs.String(); !strings.Contains(text, "debug: DelBind k1") || !strings.Contains(text, "boom") {
		t.Fatalf("日志 = %q", text)
	}
}

// LoadBinds：某个键不是字符串（GET 报 WRONGTYPE）时跳过它，其它键照常返回。
func TestLoadBindsSkipsUnreadableKey(t *testing.T) {
	u, mr := newUpstash(t)
	if err := mr.Set(bindPrefix+"ok", "u1"); err != nil {
		t.Fatal(err)
	}
	mr.HSet(bindPrefix+"bad", "field", "value")
	binds := u.LoadBinds()
	if len(binds) != 1 || binds["ok"] != "u1" {
		t.Fatalf("LoadBinds = %v", binds)
	}
	if _, ok := binds["bad"]; ok {
		t.Fatalf("读取失败的键不应出现: %v", binds)
	}
}
