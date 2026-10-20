// Package upstreamtest 给其他包的测试用：官方 id 等请求形状的公共断言，免得每个包各写一遍。
// 只在测试里导入。
package upstreamtest

import "regexp"

// UUIDv7 / UUIDv7Hex 官方会话 id（带连字符的 UUID v7）与轮次 id、消息 id（去掉连字符的 UUID v7）的形状。
var (
	UUIDv7    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	UUIDv7Hex = regexp.MustCompile(`^[0-9a-f]{12}7[0-9a-f]{3}[89ab][0-9a-f]{15}$`)
)
