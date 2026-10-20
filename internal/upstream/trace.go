// trace.go 官方后台服务刷新令牌请求的链路头。
//
// 依据（10-20 读官方桌面端 5.8.3 / 5.8.4 安装包）：两个安装包 product.json 的
// telemetry.tracing.enabled 都是 true，刷新令牌把当前 span 的 requestHeaders 展开进请求头
// （OpenTelemetrySpan.requestHeaders）：
//
//	X-Trace-ID: <traceId>
//	b3: <traceId>-<spanId>-<traceFlags>-<父 spanId>
//	X-B3-TraceId、X-B3-ParentSpanId、X-B3-SpanId、X-B3-Sampled
//
// 刷新用的是 traceService.startTrace 新开的根 span，没有父 span，所以 b3 以空的父
// span 结尾（…-1-），X-B3-ParentSpanId 是空值。
package upstream

import (
	"encoding/hex"
	"net/http"

	"github.com/yreidev/workbuddy2api-panel/internal/session"
)

// daemonTrace 一个根 span：traceID 32 位小写 hex，spanID 16 位小写 hex。
type daemonTrace struct {
	traceID string
	spanID  string
}

// newDaemonTrace 新开一条 trace（刷新令牌每次一条）。
func newDaemonTrace() daemonTrace {
	return daemonTrace{traceID: session.NewMessageID(), spanID: session.NewMessageID()[:16]}
}

// setRoot 设根 span 的链路头（OpenTelemetrySpan.requestHeaders，traceFlags 为 1）。
func (t daemonTrace) setRoot(req *http.Request) {
	req.Header.Set("X-Trace-ID", t.traceID)
	req.Header.Set("b3", t.traceID+"-"+t.spanID+"-1-")
	req.Header.Set("X-B3-TraceId", t.traceID)
	req.Header.Set("X-B3-ParentSpanId", "")
	req.Header.Set("X-B3-SpanId", t.spanID)
	req.Header.Set("X-B3-Sampled", "1")
}

// isHex 判断 s 是否为 n 位十六进制。
func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
