// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// routing.go 选号相关的两处装配，单独成函数便于测试：漏接或接错时编译照过，
// 只有请求分错账号才会暴露（跨域负载均衡下国际版号分不到会话就是这么来的）。
package main

import (
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/redisstore"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// sessionConfig 会话粘性路由的配置。按 handler 定好的域过滤可用账号（跨 realm 不泄漏；
// 跨域合池时域为 "" 即两域都算）；返回列表对快过期账号重复同一 UID 作虚拟实例权重，
// 会话哈希分配无需感知。新会话分配时再按成本分层只留最便宜那一层。
func sessionConfig(ttl, gcInterval time.Duration, store redisstore.Store, p *pool.Pool) session.Config {
	return session.Config{
		TTL:                ttl,
		GCInterval:         gcInterval,
		Store:              store,
		Available:          p.AvailableUIDs,
		AvailableForModel:  p.WeightedAvailableUIDsForModelRealm,
		AssignableForModel: p.CheapestWeightedUIDsForModelRealm,
	}
}

// wireModelCatalog 让选号读上游模型目录：
//   - 积分倍率表作积分保底的「收费」兜底判据。本地实测台账无观测时用它判收费——否则
//     「没学过」恒等于「放行」，高价新模型会把触底号一笔打穿（kimi-k3-1 实案：全池无观测
//     → 保底全放行 → 两笔打穿并硬冷却到次日 04:00）；
//   - 按目录选号：企业号只接自己目录里的模型，企业专属模型不派给个人号（见 upstream/account_catalog.go）。
func wireModelCatalog(p *pool.Pool, up *upstream.Client) {
	p.SetModelRateOf(func(realm, model string) string { return up.ModelRate(realm, model) })
	p.SetModelServes(up.ServesModel)
}
