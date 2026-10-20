// Package upstreamtest 给其他包的测试用：官方 id 等请求形状的公共断言，以及按官方接口的形状
// 造积分与签到状态的假响应，免得每个包各写一遍。只在测试里导入。
package upstreamtest

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// UUIDv7 / UUIDv7Hex 官方会话 id（带连字符的 UUID v7）与轮次 id、消息 id（去掉连字符的 UUID v7）的形状。
var (
	UUIDv7    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	UUIDv7Hex = regexp.MustCompile(`^[0-9a-f]{12}7[0-9a-f]{3}[89ab][0-9a-f]{15}$`)
)

// 官方桌面端查积分与签到用的接口（与 internal/upstream 的常量相同，upstream 包里有测试核对）。
const (
	SummaryPath       = "/billing/meter/get-user-resource-summary"
	PaidPath          = "/billing/meter/get-user-resource-paid-packages"
	FreePath          = "/billing/meter/get-user-resource-free-packages"
	CheckinStatusPath = "/v2/billing/meter/checkin-activity-status"
	CheckinPath       = "/v2/billing/meter/daily-checkin"
)

// Package 一个按周期发放的包：额度 size、剩余 remain，extra 是追加的 JSON 字段（可空）。
func Package(size, remain int64, extra string) string {
	s := fmt.Sprintf(`{"CycleCapacitySizePrecise":"%d","CycleCapacityRemainPrecise":"%d"`, size, remain)
	if extra != "" {
		s += "," + extra
	}
	return s + "}"
}

// List 逐包接口的响应：data.Accounts + TotalCount。
func List(packages ...string) string {
	return fmt.Sprintf(`{"code":0,"data":{"Accounts":[%s],"TotalCount":%d}}`, strings.Join(packages, ","), len(packages))
}

// Respond 给积分三个接口和签到状态的默认响应：汇总 404（网关改用逐包合计）、付费包为空、
// 免费包为 packages、签到状态为活动进行中且今天未签。别的路径 ok=false，由调用方处理。
func Respond(path string, packages ...string) (status int, body string, ok bool) {
	switch path {
	case SummaryPath:
		return http.StatusNotFound, `{"code":404,"msg":"not found"}`, true
	case PaidPath:
		return http.StatusOK, List(), true
	case FreePath:
		return http.StatusOK, List(packages...), true
	case CheckinStatusPath:
		return http.StatusOK, `{"code":0,"data":{"active":true,"today_checked_in":false}}`, true
	}
	return 0, "", false
}

// Write 把 Respond 的结果写进 w；路径不认识时返回 false，什么都不写。
func Write(w http.ResponseWriter, path string, packages ...string) bool {
	status, body, ok := Respond(path, packages...)
	if !ok {
		return false
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
	return true
}
