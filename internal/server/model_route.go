package server

import (
	"context"
	"log"
	"strings"
)

// routedKey 请求上下文标记：模型名已经在入口路由过（Claude Code / Codex 入口先路由再翻译），
// chatCompletions 不再路由第二遍——规则目标写错、不在目录里时，第二遍可能又命中别的规则。
type routedKey struct{}

// routeModel 模型路由：不带域前缀、模型目录里也没有的名字，按规则表从上往下找第一条
// 匹配的，改写成它的目标。没有规则、目录判断不了或没命中时原样返回，ok=false。
//
// 只改目录里没有的名字：客户端发的是上游真实模型名就直通，规则里写 * 兜底也不会动到它。
// 带 cn: / global: 前缀的名字是明确指定，不改。
func (h *Handler) routeModel(ctx context.Context, model string) (string, bool) {
	routes := h.loadLive().ModelRoutes
	if len(routes) == 0 || model == "" || h.cfg.Upstream == nil || ctx.Value(routedKey{}) != nil {
		return model, false
	}
	if _, bare := resolveModel(model); bare != model {
		return model, false
	}
	if has, known := h.catalogHas(model); has || !known {
		return model, false
	}
	for _, rt := range routes {
		if globMatch(rt.Match, model) {
			log.Printf("INFO: [server] model route %s -> %s (rule %q)", model, rt.Target, rt.Match)
			return rt.Target, true
		}
	}
	return model, false
}

// catalogHas 模型目录里有没有这个名字：国内版域目录与企业号目录，开了国际版再加国际版目录。
// known=false 表示国内版目录拉不到，判断不了。目录按需拉取，各自有缓存与失败冷却。
func (h *Handler) catalogHas(model string) (has, known bool) {
	if len(h.fetchDynamicModels()) == 0 {
		return false, false
	}
	if h.cfg.Upstream.RealmHasModel("cn", model) {
		return true, true
	}
	if h.cfg.GlobalEnabled {
		h.fetchGlobalModels()
		return h.cfg.Upstream.RealmHasModel("global", model), true
	}
	return false, true
}

// globMatch 只认 * 通配（任意长度，含空串），不区分大小写；其余字符按字面比较。
func globMatch(pattern, s string) bool {
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
