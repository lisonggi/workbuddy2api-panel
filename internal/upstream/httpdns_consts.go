// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// httpdns_consts.go 国内版地址层（HTTPDNS 与备用 IP）用到的官方常量。
//
// 依据（10-20 读官方桌面端 5.8.3 安装包）：内置 CLI 的 HTTPDNS 模块常量
// （DEFAULT_HTTPDNS_TOKEN、DEFAULT_HTTPDNS_BOOTSTRAP_IPS、DEFAULT_HTTPDNS_SERVICE_IPS、
// DEFAULT_HTTPDNS_CONF_PATH、DEFAULT_ENTERPRISE_PATH），以及国内版产品配置
// productFeaturesConfig.NetworkFailover.routes["core-api"].fallbackAddresses。
// 产品配置里也有 httpDnsToken、httpDnsBootstrapIps，值与内置常量相同，但官方代码不读它们。
package upstream

const (
	// httpDNSToken 腾讯云 HTTPDNS 的 token（conf 与解析请求的 token 参数）。
	httpDNSToken = "719980335"
	// httpDNSConfPath 取服务 IP 列表的路径。
	httpDNSConfPath = "/conf"
	// httpDNSResolvePath 解析域名的路径。
	httpDNSResolvePath = "/d"
)

var (
	// httpDNSBootstrapIPs 取服务 IP 列表（conf）时依次连的 IP。
	httpDNSBootstrapIPs = []string{"119.29.29.99", "119.28.28.99"}
	// httpDNSDefaultServiceIPs conf 拿不到列表、也没有缓存时用的服务 IP。
	httpDNSDefaultServiceIPs = []string{"119.29.29.99", "119.28.28.99"}
	// failoverFallbackIPs 备用入口 wb.tencentbuddy.com 的备用 IP（只对备用入口生效）。
	failoverFallbackIPs = []string{"162.14.3.241"}
)
