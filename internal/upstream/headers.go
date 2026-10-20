// Package headers 构造三类上游请求头（common / chat / billing / refresh）。
// 规则来自 docs/api-reference.md §0/§4/§6。
package upstream

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
)

// 官方取值依据：WorkBuddy 桌面端安装包（国内版 WorkBuddy-darwin-arm64-5.8.3、国际版 5.8.4，
// 都内置 CLI @tencent-ai/codebuddy-code 2.161.0）。对话、刷新令牌、模型目录由内置 CLI 发；积分、
// 签到、成长任务、旅行、上报由桌面端后台服务发。两者用同一套请求框架，UA 段序不同。
const (
	// defaultClientVersionCN / defaultClientVersionGlobal 两域桌面端版本（UA 与 X-IDE-Version）。
	// config upstream.client_version 可覆盖（两域共用一个值，空 = 内置默认）。
	defaultClientVersionCN     = "5.8.3"
	defaultClientVersionGlobal = "5.8.4"
	// defaultCliVersion 桌面端内置 CLI 的版本（UA 的 `CLI/<ver>` 段），两域相同。
	// config upstream.cli_version 可覆盖（空 = 内置默认）。
	defaultCliVersion = "2.161.0"
	// deploymentType 两域 product.json 的 deploymentType。官方请求框架的 ProductEndpointHttpInterceptor
	// 把它作为 X-Product 加到经过框架的每个请求上（对话、刷新令牌、模型目录、积分等都在内）。
	deploymentType = "SaaS"

	originRefererCN     = "https://www.codebuddy.cn"
	originRefererGlobal = "https://www.workbuddy.ai"
)

// originRefererFor 按账号 realm 返回 Origin/Referer 基础域：
// global → https://www.workbuddy.ai；cn（含全局开关未开）→ https://www.codebuddy.cn。
func originRefererFor(a *auth.Auth) string {
	if a != nil && a.IsGlobal() {
		return originRefererGlobal
	}
	return originRefererCN
}

// clientVersion 生效的桌面端版本：Client.ClientVersion 非空则取之，否则按账号域取内置默认。
func (c *Client) clientVersion(a *auth.Auth) string {
	if c != nil && c.ClientVersion != "" {
		return c.ClientVersion
	}
	if a != nil && a.IsGlobal() {
		return defaultClientVersionGlobal
	}
	return defaultClientVersionCN
}

// cliVersion 生效的 CLI 版本：Client.CliVersion 非空则取之，否则内置默认 defaultCliVersion。
func (c *Client) cliVersion() string {
	if c != nil && c.CliVersion != "" {
		return c.CliVersion
	}
	return defaultCliVersion
}

// applicationNameFor 两域桌面端 product.json 的 applicationName：国内版 "WorkBuddy"，国际版
// "workbuddy-ai"（productName 是 "WorkBuddy AI"，UA 里用的是 applicationName）。
func applicationNameFor(a *auth.Auth) string {
	if a != nil && a.IsGlobal() {
		return "workbuddy-ai"
	}
	return "WorkBuddy"
}

// defaultCLIUA 内置 CLI 的 UA（UserAgentHttpInterceptor）：
// `<platform>/<ver> <applicationName>/<ver> CLI/<cli>`，platform 由桌面端固定传 "WorkBuddy"。
// 国内版 `WorkBuddy/5.8.3 WorkBuddy/5.8.3 CLI/2.161.0`，国际版 `WorkBuddy/5.8.4 workbuddy-ai/5.8.4 CLI/2.161.0`。
func (c *Client) defaultCLIUA(a *auth.Auth) string {
	v := c.clientVersion(a)
	return "WorkBuddy/" + v + " " + applicationNameFor(a) + "/" + v + " CLI/" + c.cliVersion()
}

// defaultDaemonUA 桌面端后台服务的 UA（WorkbuddyUserAgentHttpInterceptor）：applicationName 段在前，
// `<applicationName>/<ver> WorkBuddy/<ver> CLI/<cli>`。国内版两段同名，与 CLI 的 UA 一样。
func (c *Client) defaultDaemonUA(a *auth.Auth) string {
	v := c.clientVersion(a)
	return applicationNameFor(a) + "/" + v + " WorkBuddy/" + v + " CLI/" + c.cliVersion()
}

// userAgent 内置 CLI 发的请求（对话、刷新令牌、模型目录）的 UA。
// config user_agent 显式覆盖优先（两类请求共用这一个值）。
func (c *Client) userAgent(a *auth.Auth) string {
	if c != nil && c.UserAgent != "" {
		return c.UserAgent
	}
	return c.defaultCLIUA(a)
}

// daemonUserAgent 桌面端后台服务发的请求（积分、签到、成长任务、旅行、上报）的 UA。
// config user_agent 显式覆盖优先；显式 client_name="SaaS"（旧行为）时不设 UA。
func (c *Client) daemonUserAgent(a *auth.Auth) string {
	if c != nil && c.UserAgent != "" {
		return c.UserAgent
	}
	if c.attributionClientName() == "SaaS" {
		return ""
	}
	return c.defaultDaemonUA(a)
}

// resolveDeviceToken 解析本次请求的 X-Device-Token 取值。
// 优先级：auth.Auth.DeviceToken（每号）> Client.DeviceToken（config 全局）> 文件兜底。
// 三者皆空/读失败则返回空串（调用方不注入该头，优雅降级）。
func (c *Client) resolveDeviceToken(a *auth.Auth) string {
	if a != nil && a.DeviceToken != "" {
		return a.DeviceToken
	}
	if c != nil && c.DeviceToken != "" {
		return c.DeviceToken
	}
	if c != nil && c.DeviceTokenFile != "" {
		return readDeviceTokenFile(c.DeviceTokenFile)
	}
	return ""
}

// injectDeviceToken 在 req 注入 X-Device-Token 头（仅当取到非空 token）。官方只在签到
// （getCheckinRequestHeaders）和登录前的 auth/state 上带它，对话和其他请求都不带。
func (c *Client) injectDeviceToken(req *http.Request, a *auth.Auth) {
	if tok := c.resolveDeviceToken(a); tok != "" {
		req.Header.Set("X-Device-Token", tok)
	}
}

// deriveAccountStableID 按 uid + 用途盐稳定派生 36 hex 设备/会话标识。
// 跨重启稳定（固定盐 "wb2a:"，不随进程换——这是与 session 包派生盐的本质差异：
// 那是会话键维度的进程级随机盐，重启换新；本函数是账号维度，必须跨重启恒定）、
// 账号间互异（uid 不同则不同）、同 uid 同用途恒同值（幂等）。用 sha256 与项目
// 既有派生（session/ids.go、cache_key.go）保持一致；截 36 hex 提供更长熵。
//
// 两个用途：
//   - purpose="machine" → X-Machine-ID（设备级，跨会话稳定）
//   - purpose="session" → X-Session-ID（账号固定会话，跨重启稳定）
//
// 与 injectDeviceToken 的 X-Device-Token 并存不冲突：那是登录时上游签发的
// 真实设备令牌（有则发，权威）；本对头是「每账号一台固定虚拟设备」的稳定指纹，
// 防多号被上游按设备指纹缺失/漂移关联风控。两者是不同头族，官方桌面端都发。
func deriveAccountStableID(uid, purpose string) string {
	sum := sha256.Sum256([]byte("wb2a:" + purpose + ":" + uid))
	return hex.EncodeToString(sum[:18]) // 36 hex chars
}

// injectAccountStableHeaders 在 req 注入 X-Machine-ID / X-Session-ID：按 uid 稳定
// 派生，跨重启固定、账号间互异。uid 为空时不注入（匿名请求无设备标识，上游不要求）。
func (c *Client) injectAccountStableHeaders(req *http.Request, a *auth.Auth) {
	if a == nil || a.UID == "" {
		return
	}
	req.Header.Set("X-Machine-ID", deriveAccountStableID(a.UID, "machine"))
	req.Header.Set("X-Session-ID", deriveAccountStableID(a.UID, "session"))
}

// CommonHeaders 设置所有 API 共享的请求头。
func (c *Client) CommonHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Content-Type", "application/json")
	// Accept 非流式默认 application/json（D6：去掉宽松的 text/plain, */*）。
	// chat 流式路径在 ChatHeaders 覆盖为 event-stream。
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	origin := originRefererFor(a)
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("User-Agent", c.userAgent(a))
	// X-CodeBuddy-Request: 1（官方客户端风控闸门头，所有 API 请求必带，D1）。
	req.Header.Set("X-CodeBuddy-Request", "1")
	// Accept-Language 按 realm 切（D5）：CN zh-CN，global en-US。官方客户端按账号域
	// 发对应语言标识，对齐避免上游风控按语言缺失误判。
	req.Header.Set("Accept-Language", acceptLanguageFor(a))
	// X-Machine-ID / X-Session-ID：按 uid 稳定派生的账号级设备头（见
	// injectAccountStableHeaders）。注入在 CommonHeaders——chat 经 ChatHeaders
	// 叠加 CommonHeaders 天然继承；billing 域另行注入，全出站覆盖。
	c.injectAccountStableHeaders(req, a)
}

// acceptLanguageFor 按账号 realm 返回 Accept-Language：global → en-US，cn → zh-CN。
func acceptLanguageFor(a *auth.Auth) string {
	if a != nil && a.IsGlobal() {
		return "en-US"
	}
	return "zh-CN"
}

// injectCodeBuddyRequest 在 req 注入 X-CodeBuddy-Request: 1。
// billing 域未走 CommonHeaders，单独注入保证全出站覆盖（D1）。
func (c *Client) injectCodeBuddyRequest(req *http.Request) {
	req.Header.Set("X-CodeBuddy-Request", "1")
}

// frameworkHeaders 官方请求框架（restOperations）给非对话请求加的头：axios 的 Accept、框架默认的
// X-Requested-With，以及各拦截器补的头——CommonHeaderHttpInterceptor 的 X-Request-ID（32 位
// 十六进制，每个请求新生成）、ProductEndpointHttpInterceptor 的 X-Product、UA、
// AuthenticationHttpInterceptor 的账号头（Authorization、X-User-Id、企业号的 X-Enterprise-Id 与同值的
// X-Tenant-Id、X-Domain）。
// Content-Type 只在 GET 以外的请求上设（官方 GET 不带，POST 都带 JSON 体；网关几个不带体的 POST
// 不是桌面端的接口，保持以前也带的样子）。官方不发的 Origin / Referer / Accept-Language /
// X-CodeBuddy-Request / X-Machine-ID / X-Session-ID 一概不发；需要的个别请求（积分的
// Accept-Language、签到的 X-Device-Token、刷新的 X-Refresh-Token）由调用方另加。ua 为空不设 UA。
func (c *Client) frameworkHeaders(req *http.Request, a *auth.Auth, ua string) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if req.Header.Get("X-Request-ID") == "" {
		req.Header.Set("X-Request-ID", session.NewMessageID())
	}
	req.Header.Set("X-Product", deploymentType)
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if at := a.AccessTokenValue(); at != "" {
		req.Header.Set("Authorization", "Bearer "+at)
	}
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	if a.IsEnterprise() {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		req.Header.Set("X-Tenant-Id", a.EnterpriseID)
	}
	if d := chatDomain(a); d != "" {
		req.Header.Set("X-Domain", d)
	}
}

// billingAcceptLanguage 积分包查询的 Accept-Language：官方按界面语言只发 "zh" 或 "en"
// （resolveBillingAcceptLanguage），国内版取 zh、国际版取 en。
func billingAcceptLanguage(a *auth.Auth) string {
	if a != nil && a.IsGlobal() {
		return "en"
	}
	return "zh"
}

// ChatMeta 一次 chat 出站的会话头族元数据（issue #35：后台按 X-Conversation-Request-ID
// 聚合请求，官方客户端一次 user send 内所有 tool call/重试/换号复用同一个 ID）。
// handler 在轮转循环外生成 conversationID / conversationRequestID，循环内每次出站
// 复用同值；TraceID 透传入站值（空 = 回落 conversationRequestID）。
// messageID（消息级，每条独立）由 ChatHeaders 内部生成，无需外部可见。
type ChatMeta struct {
	ConversationID        string // X-Conversation-ID：客户端给的会话 id；空则由 ChatHeaders 按 ConversationSeed 或轮次 id 补
	ConversationSeed      string // 客户端没给会话 id 时的派生源（session.ConversationSeed）；空 = 对话第一个请求
	ConversationRequestID string // X-Conversation-Request-ID：聚合主键，必发
	TraceID               string // X-Trace-ID：入站透传值，空则回落 conversationRequestID
}

// ChatHeaders 设置 chat 出站请求头。形状对齐桌面端内置 CLI 2.161.0 的对话请求：官方对话请求
// 不带 Origin / Referer / Accept-Language / X-CodeBuddy-Request / X-Machine-ID / X-Session-ID，
// 也不带 X-No-*（那几个只在登录前的 auth/state、auth/token 上发）。
//   - 账号头：Authorization、X-User-Id；企业号加 X-Enterprise-Id 与同值的 X-Tenant-Id，个人号
//     两个都不发；X-Domain 取登录域（国际版固定 www.workbuddy.ai），没有就不发。
//   - X-Agent-Intent=craft、X-Agent-Type=main（主对话）。
//   - UA、X-IDE-* 取桌面端传给内置 CLI 的值（见 defaultCLIUA、injectAttribution）；X-Product 是
//     请求框架给每个请求加的 deploymentType；不带 X-Device-Token。
//
// clientIP 为本次请求的客户端 IP（按参数传递，不读共享字段——避免并发串扰）；
// PassthroughIP=false 或 clientIP 为空时不注入 IP 头。
// meta 为会话头族元数据（纯新增，不改既有头），见 injectConversationHeaders。
func (c *Client) ChatHeaders(req *http.Request, a *auth.Auth, clientIP string, meta ChatMeta) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", c.userAgent(a))
	// AccessToken 加锁快照：keepalive 定时刷新会在 a.mu 内改写它，锁外直读构成数据竞争
	// （见 auth.AccessTokenValue 注释）。
	if at := a.AccessTokenValue(); at != "" {
		req.Header.Set("Authorization", "Bearer "+at)
	}
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	// 安全红线：绝不在 chat 请求里携带 X-Refresh-Token。
	// 企业号带企业 ID 与 cli2api、dsh-workbuddy-connect 的对话头一致；国际版企业号本仓库未实测。
	if a.IsEnterprise() {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		req.Header.Set("X-Tenant-Id", a.EnterpriseID)
	}
	if d := chatDomain(a); d != "" {
		req.Header.Set("X-Domain", d)
	}
	req.Header.Set("X-Agent-Intent", "craft")
	req.Header.Set("X-Agent-Type", "main")
	req.Header.Set("X-Product", deploymentType)
	// 用量归属头：默认同官方桌面端（client_name="SaaS" 还原旧行为）。
	c.injectAttribution(req, a)
	// 客户端 IP 透传（仅 PassthroughIP=true 且本次请求带 IP）。
	c.injectClientIP(req, clientIP)
	// 不带 X-Device-Token：官方只在签到和登录前的 auth/state 上带。
	// X-Conversation-ID 官方每条都发（值是整段对话不变的会话 id）。国际版不带一律 400
	// code=11128 "request illegal"（10-19 生产实测），国内版不带也能用，同样补上对齐官方。
	// 客户端没给会话 id 时：历史里已有助手回复就按派生源 + 账号算出固定值（同一段对话在同一个号上
	// 不变，上游据此命中前缀缓存，见 session.ConversationSeed）；对话第一个请求还没有回复可区分，
	// 新生成一个，形状同官方新会话的 id（带连字符的 UUID v7）。
	if meta.ConversationID == "" {
		switch {
		case meta.ConversationSeed != "":
			meta.ConversationID = session.AccountConversationID(a.UID, meta.ConversationSeed)
		default:
			meta.ConversationID = session.NewUUIDv7()
		}
	}
	// 会话头族（对话/请求/消息/B3 链路），见 injectConversationHeaders。
	c.injectConversationHeaders(req, meta)
}

// chatDomain chat 出站的 X-Domain：国际版固定 www.workbuddy.ai（与国际版登录域相同，不回退
// 登录会话原值——对齐 intl 项目出站头）；国内版取登录域，没有就不发。
func chatDomain(a *auth.Auth) string {
	if a.IsGlobal() {
		return "www.workbuddy.ai"
	}
	return a.DomainValue()
}

// injectConversationHeaders 注入官方客户端会话头族（issue #35 后台聚合）。
// 头族分四层，各司其职：
//   - X-Conversation-ID：会话级（ChatHeaders 已保证非空：客户端给的会话 id、派生的或新生成的）。
//   - X-Conversation-Request-ID：**对话轮级聚合主键**，必发。一次 user send 内的
//     所有 tool call/重试/换号/降级复用同一个 → 后台按它聚合成一条（不再碎片化）。
//   - X-Conversation-Message-ID = X-Request-ID：消息级，每条独立（去掉连字符的 UUID v7）。
//   - X-Trace-ID：入站透传或 = conversationRequestID。
//   - 链路族：X-B3-TraceId / X-B3-SpanId / X-B3-Sampled，外加官方同发的 W3C traceparent
//     与单行 b3（同一组 trace/span id）。B3 规范只认 16/32 hex TraceId 与 16 hex SpanId；
//     入站 conversationRequestID 非法时 TraceId 回落 messageID（恒 32 hex），SpanId 每条消息
//     随机新生成。traceparent 只认 32 位小写 hex，16 位的左补零。
//
// 官方主对话不发 X-Root-Request-ID（只有子代理带），这里也不发。
func (c *Client) injectConversationHeaders(req *http.Request, meta ChatMeta) {
	convReqID := meta.ConversationRequestID
	if convReqID == "" {
		// 零值 meta（直接调 injectConversationHeaders 的测试）也要保证聚合主键必发：
		// 本级补一个，ChatHeaders 已生成稳定的不走到这里。
		convReqID = session.NewUUIDv7Hex()
	}
	// 消息 id 同官方 uuidv7() 去掉连字符；span id 是链路自己的随机 16 位（UUID v7 的前 16 位是
	// 时间戳，截它当 span id 会让同一毫秒的两条消息撞上）。
	messageID := session.NewUUIDv7Hex()
	if meta.ConversationID != "" {
		req.Header.Set("X-Conversation-ID", meta.ConversationID)
	}
	req.Header.Set("X-Conversation-Request-ID", convReqID)
	req.Header.Set("X-Conversation-Message-ID", messageID)
	req.Header.Set("X-Request-ID", messageID)
	traceID := meta.TraceID
	if traceID == "" {
		traceID = convReqID
	}
	req.Header.Set("X-Trace-ID", traceID)
	b3Trace := convReqID
	if !validTraceID(b3Trace) {
		b3Trace = messageID // 非法 B3 TraceId → 回落恒 32 hex 的消息级 ID
	}
	spanID := session.NewMessageID()[:16]
	req.Header.Set("X-B3-TraceId", b3Trace)
	req.Header.Set("X-B3-SpanId", spanID)
	req.Header.Set("X-B3-Sampled", "1")
	w3cTrace := strings.ToLower(b3Trace)
	if len(w3cTrace) == 16 {
		w3cTrace = strings.Repeat("0", 16) + w3cTrace
	}
	req.Header.Set("traceparent", "00-"+w3cTrace+"-"+spanID+"-01")
	req.Header.Set("b3", strings.ToLower(b3Trace)+"-"+spanID+"-1")
}

// validTraceID 判断 B3 TraceId 是否合法：16 或 32 位 hex（大小写均可）。
// 官方客户端生成的 conversationRequestId 是 32 位 hex（UUID 去横线），入站透传值
// 可能是任意形状（含横线/超长/非 hex），直接塞进 B3 头会破坏链路关联（issue #35）。
func validTraceID(s string) bool {
	if len(s) != 16 && len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}

// attributionClientName 生效的用量归属名：ClientName 非空取之；
// 空默认 "WorkBuddy"（伪造官方桌面端指纹；显式配 "SaaS" 可还原旧行为）。
func (c *Client) attributionClientName() string {
	if c != nil && c.ClientName != "" {
		return c.ClientName
	}
	return "WorkBuddy"
}

// injectAttribution 注入用量归属头（X-Agent-Purpose / X-IDE-*）。仅在 chat/completions 路径生效
// （ChatHeaders 调用；X-Product 由 ChatHeaders 自己设）。
//
// 默认（ClientName 空）同官方桌面端：X-Agent-Purpose="conversation"，X-IDE-Name / X-IDE-Type
// 为桌面端传给 CLI 的 IDE_TYPE "WorkBuddy"，X-IDE-Version 为桌面端版本。显式 ClientName="SaaS"
// 还原旧行为（不设 X-Agent-Purpose 与 X-IDE-*）；配其他值则 X-IDE-Name / X-IDE-Type 跟随该值。
func (c *Client) injectAttribution(req *http.Request, a *auth.Auth) {
	name := c.attributionClientName()
	if name == "SaaS" {
		return
	}
	req.Header.Set("X-Agent-Purpose", "conversation")
	req.Header.Set("X-IDE-Name", name)
	req.Header.Set("X-IDE-Type", name)
	req.Header.Set("X-IDE-Version", c.clientVersion(a))
}

// injectClientIP 在 PassthroughIP 开启时把 clientIP 参数透传给上游（三等价头）。
func (c *Client) injectClientIP(req *http.Request, clientIP string) {
	if c == nil || !c.PassthroughIP || clientIP == "" {
		return
	}
	req.Header.Set("X-Forwarded-For", clientIP)
	req.Header.Set("X-Real-IP", clientIP)
	req.Header.Set("X-Client-IP", clientIP)
}

// ExtractClientIP 从入站请求提取客户端 IP 首段（X-Forwarded-For 首段，回落 X-Real-IP）。
func ExtractClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return strings.TrimSpace(xff[:i])
			}
		}
		return strings.TrimSpace(xff)
	}
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		return real
	}
	return ""
}

// BillingHeaders 桌面端后台服务发的请求（积分、签到、成长任务、旅行、上报、补偿）的请求头：
// 官方请求框架的形状（frameworkHeaders）+ 后台服务的 UA。
func (c *Client) BillingHeaders(req *http.Request, a *auth.Auth) {
	c.frameworkHeaders(req, a, c.daemonUserAgent(a))
}

// miniProgramHeaders 小程序限定任务（X-Client-Platform 为小程序）的请求头，保持 1.1.2 以前的
// 形状：没有小程序的包可对照，改了反而多一个没验证过的变量。
func (c *Client) miniProgramHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Authorization", "Bearer "+a.AccessTokenValue())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CodeBuddy-Request", "1")
	if a.IsGlobal() {
		req.Header.Set("Accept-Language", "en-US")
	} else {
		req.Header.Set("Accept-Language", "zh-CN")
	}
	if c != nil && c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	} else if c.attributionClientName() != "SaaS" {
		req.Header.Set("User-Agent", "WorkBuddy/"+c.clientVersion(a))
	}
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	if a.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		req.Header.Set("X-Tenant-Id", a.EnterpriseID)
	}
	if d := a.DomainValue(); d != "" {
		req.Header.Set("X-Domain", d)
	}
	c.injectDeviceToken(req, a)
	req.Header.Set("X-Client-Platform", mpPlatform)
}

// RefreshHeaders 刷新令牌的请求头（X-Refresh-Token 只允许出现在这里）。官方内置 CLI 的
// refreshSession：请求框架的头 + X-Domain（登录域，没有就用产品入口域）+ X-Refresh-Token +
// X-Auth-Refresh-Source: plugin。
func (c *Client) RefreshHeaders(req *http.Request, a *auth.Auth) {
	c.frameworkHeaders(req, a, c.userAgent(a))
	d := chatDomain(a)
	if d == "" {
		d = "www.workbuddy.cn" // 国内版产品入口 https://www.workbuddy.cn 的 authority
	}
	req.Header.Set("X-Domain", d)
	req.Header.Set("X-Refresh-Token", a.RefreshToken)
	req.Header.Set("X-Auth-Refresh-Source", "plugin")
}
