// Package glob 模型名通配：模型路由的匹配规则和客户端密钥的模型白名单共用这一套。
package glob

import "strings"

// Match 只认 * 通配（任意长度，含空串），不区分大小写；其余字符按字面比较。
func Match(pattern, s string) bool {
	pattern, s = strings.ToLower(pattern), strings.ToLower(s)
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(s, part)
		if i < 0 {
			return false
		}
		s = s[i+len(part):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}
