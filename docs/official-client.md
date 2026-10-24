# 官方客户端对照

网关的出站请求按官方 WorkBuddy 桌面端的取值发送。这里记下依据从哪里来、对齐到了哪一步，以及官方升级后怎么重新核对。

## 依据

| 项 | 国内版 | 国际版 |
|---|---|---|
| 桌面端安装包 | WorkBuddy 5.8.3（macOS arm64） | WorkBuddy AI 5.8.4（macOS arm64） |
| 内置 CLI（`@tencent-ai/codebuddy-code`） | 2.161.0 | 2.161.0 |
| `applicationName`（product.json） | `WorkBuddy` | `workbuddy-ai` |

2026-10-20 从官网下载两个安装包，只解包阅读，没有运行。要看的文件有三处：

- `app.asar.unpacked/cli/dist/codebuddy-lite-wb.mjs`：内置 CLI，对话、刷新令牌、模型目录、备用入口切换都由它做；
- `app.asar.unpacked/cli/product.json`：版本、`applicationName`、产品开关与备用入口配置；
- `app.asar` 里的 `main/`：桌面端后台服务，积分、签到、登录、上报、外观、专家列表由它发。

成长中心、积分补偿、新手礼包、试用只在官网网页上，同日读了 `www.workbuddy.cn` 成长中心页的前端代码。

npm 上更新的 CLI 只作对照，取值以安装包内置的版本为准：用户实际跑的是安装包里的 CLI，网关在 UA 里报的也是它的版本号。

## 几类请求

| | 对话 | 积分、签到、上报、外观、专家列表、登录 | 刷新令牌 | 成长中心、积分补偿、新手礼包、试用、账号资料 |
|---|---|---|---|---|
| 谁发 | 内置 CLI | 后台服务 | 内置 CLI | 官网网页（桌面端在浏览器里打开） |
| UA | `WorkBuddy/<版本> <应用名>/<版本> CLI/<cli>` | `<应用名>/<版本> WorkBuddy/<版本> CLI/<cli>` | 同对话 | 浏览器的 UA |
| 来源 | `UserAgentHttpInterceptor` | `WorkbuddyUserAgentHttpInterceptor` | `refreshSession` | 官网前端的 axios 实例，靠 Cookie 认人 |
| 网关实现 | `ChatHeaders` | `BillingHeaders`、`frameworkHeaders` | `RefreshHeaders`、`RefreshToken` | `webHeaders`、`webJSON`、`websession.go` |

前三类请求都经过官方请求框架（`restOperations`）。内置 CLI 和后台服务各有一套框架，挂的拦截器相同，所以共用这些头：

- `Accept: application/json, text/plain, */*`，有请求体的请求带 `Content-Type: application/json`（桌面端的 axios 对对象请求体只发这个值）；
- `X-Requested-With: XMLHttpRequest`；
- `X-Request-ID`：32 位十六进制，每个请求新生成（`CommonHeaderHttpInterceptor`；对话请求另用消息 id）；
- `X-Product: SaaS`：产品配置里的 `deploymentType`，两个安装包都是 `SaaS`（`ProductEndpointHttpInterceptor`）；
- `Authorization`、`X-User-Id`，企业号再带 `X-Enterprise-Id` 与同值的 `X-Tenant-Id`；
- `X-Domain`：登录域；
- 账号有部门全名（`account.departmentFullName`，企业号才有）时带 `X-Department-Info`，值原样（`AuthenticationHttpInterceptor`）。网关从 1.1.2 起把它存进凭证文件的 `account.departmentFullName`：登录时取，刷新令牌后随取账号更新。官方用 Node 发请求，值里有超出 Latin-1 的字符（比如中文）时 Node 会直接报错，所以上游返回的大概是 ASCII 或编码过的值；没有企业号实测过，网关照原样发；
- `Accept-Encoding: gzip, compress, deflate, br`：axios 的 http 适配器自动加的（内置 CLI 的 `advertiseZstdAcceptEncoding` 默认关，不带 zstd），官方没有在哪个请求上改写。网关按 `Content-Encoding` 解压（`encoding.go`）。

官方这三类请求都不发 `Origin`、`Referer`、`X-CodeBuddy-Request`、`X-Machine-ID`、`X-Session-ID`。个别请求另带的头：

| 头 | 只出现在 | 官方来源 |
|---|---|---|
| `Accept-Language: zh` / `en` | 积分包查询 | `resolveBillingAcceptLanguage` |
| `X-Device-Token` | 签到状态与签到、登录的发起授权 | `getCheckinRequestHeaders`、`WorkbuddyAuthStateHeaderProvider` |
| `X-Refresh-Token`、`X-Auth-Refresh-Source: plugin` | 刷新令牌 | `refreshSession` |
| `Content-Type: application/json;charset=UTF-8` | 上报 `/v2/report` | `StandardEventService.send` |

对话请求另有这些头和字段：

- `X-Agent-Intent: craft`、`X-Agent-Type: main`、`X-Agent-Purpose: conversation`；
- `X-IDE-Name` / `X-IDE-Type: WorkBuddy`，`X-IDE-Version` 为桌面端版本；
- `X-Private-Data`：`settings.enableModelOptimization` 没设过、且产品开关 `DisableYuanbaoChannel` 为真（两个安装包都是）时发 `"true"`。网关的 `upstream.model_optimization` 默认关闭，与此相同；
- `X-Conversation-ID`：会话 id，整段对话不变。新会话的 id 是内置 CLI 建会话时生成的 `uuidv7()`，带连字符的 36 位，前 48 位是毫秒时间戳（`sessionManager.create` 的 `id ?? uuidv7()`；桌面端新建会话不传 id）；
- `X-Conversation-Request-ID`：轮次 id，每次用户发言开一轮时生成，是 `uuidv7()` 去掉连字符（`generateConversationRequestId`）；
- `X-Conversation-Message-ID`、`X-Request-ID`：同一个模型消息 id，每次调模型新生成，同样是 `uuidv7()` 去掉连字符（`withModelMessageCorrelation`）。上报事件里的会话 id、轮次 id、消息 id 也是这三种形状。网关客户端没给会话 id 时的做法见[会话粘性](gateway.md#会话粘性)；
- 链路头 `traceparent`、`b3` 与 `X-B3-*`；
- 请求体带了思考档位（`reasoning_effort` 或 `reasoning.effort`）时补 `reasoning_summary: "auto"`（`injectReasoningSummaryIfNeeded`）。2.161.0 不发 `cache_retention`，也不发 `prompt_cache_key`；
- 任务中心的专家对话就是一次普通对话，只多带 `X-Expert-Id`（专家插件激活时追加）；团队专家（专家列表里 `expertType` 为 `team`）另带 `X-Expert-Team-Task: true`（后台服务的 `applyPreparedExpertSettings`），网关按专家列表里的类型带。

模型目录：内置 CLI 用对话的 UA，按 GET 拉 `/v3/config`，带 `Connection: close`。企业号另拉企业模型，见[企业号模型目录](#企业号模型目录)。

## 积分与签到

积分查询照官方桌面端（`BackendProvider` 的 `fetchUserResources`）同时发三个请求，两个域路径相同，都不带 `/v2`，都带 `Accept-Language`：

| 请求 | 请求体 |
|---|---|
| `POST /billing/meter/get-user-resource-summary` | `{}` |
| `POST /billing/meter/get-user-resource-paid-packages` | 付费包代码、`Status: [0, 3]`、`NeedRenewInfo: true`，每页 200 条，最多 10 页 |
| `POST /billing/meter/get-user-resource-free-packages` | 免费包代码、`Status: [0, 3]`，加上当天（台湾时间）的 `SlicePeriodStartTime` / `SlicePeriodEndTime` |

- 两个列表按 `ResourceId` 去重；按天发放的包（`CapacityType` 4）有当天的发放明细时用明细，其余用 `CycleCapacitySizePrecise` / `CycleCapacityRemainPrecise`（字符串）。
- 余额以汇总为准；汇总失败时用逐包合计。两个列表任一失败算查询失败。
- 1.1.2 以前用 `/v2/billing/meter/get-user-resource`，官方 5.8.3 里已经没有这个接口。2026-10-20 生产对比：新旧接口给的包相同，按整数字段算出的旧余额和汇总对不上，按官方算法算的一致。
- 企业号查 `POST /v2/billing/meter/get-enterprise-user-usage`，带 `X-Enterprise-Id`（与以前相同）。

签到照官方（`DesktopAccountRepo`）先查状态再签：`POST /v2/billing/meter/checkin-activity-status`，活动没开（`active` 为假）就不签，今天已签（`today_checked_in`）也不签；状态查不到时照常签。两条都带 `X-Device-Token`。

## 登录

官方的登录由后台服务发：国内版是 `AccountScopedExternalLinkAuthenticationProvider`，国际版是 `ExternalLinkAuthenticationProvider`。两个域的路径前缀都是 `/plugin`，`platform` 国内版是 `workbuddy`，国际版是 `workbuddy-ai`。

| 请求 | 路径 | 另带的头 |
|---|---|---|
| 发起授权 | `POST /v2/plugin/auth/state?platform=<platform>`，请求体 `{}` | `X-Domain`（登录域）、`X-Custom-Channel-Id: unknown`、有设备令牌时 `X-Device-Token`、四个 `X-No-*: true` |
| 轮询令牌 | `GET /v2/plugin/auth/token?state=<state>` | 四个 `X-No-*: true` |
| 取账号（国内版） | `GET /v2/plugin/account` | `Authorization`、`X-Domain` |
| 取账号（国际版） | `GET /v2/plugin/login/account?state=<state>` | `Authorization`、`X-Domain`、`X-No-User-Id` 等三个 |

四个 `X-No-*` 是 `X-No-Authorization`、`X-No-User-Id`、`X-No-Enterprise-Id`、`X-No-Department-Info`，告诉请求框架这条请求不要补账号头，会原样发出去。国际版取到账号后，官方还会用新令牌取一次账号列表 `GET /v2/plugin/accounts`（`getAccountSnapshot`），网关照做，失败只记日志。

链路头：登录和刷新令牌都用 `traceService.startTrace` 新开的根 span，请求带 `X-Trace-ID`、`b3: <traceId>-<spanId>-1-`（父 span 为空）、`X-B3-TraceId`、`X-B3-ParentSpanId`（空值）、`X-B3-SpanId`、`X-B3-Sampled: 1`。从登录界面发起的登录另开「登录观测」：每次登录一个 `loginSessionId`（UUID，trace id 就是它去掉连字符）和一个 `login_owner_id`，每类请求再带一个子 span 的 W3C `traceparent`，同一类请求的多次轮询共用这个子 span。国际版发起授权（`ExternalLinkAuthenticationProvider.fetchAuthState`）把根 span 的头放在了请求头之外，实际不发，只有 `traceparent`。面板添加账号、命令行登录都按从登录界面发起处理，命令行登录把这条链路存进 state 文件，`poll` 时沿用。

授权页地址照 `openAuthUrl` 追加参数：两个域都加 `version=<客户端版本>`；国内版另加 `traceparent`（打开授权页这一步自己的子 span）、`loginSessionId`、`login_owner_id`。

`X-Device-Id-List`：官方拿得到设备的 qimei36 时才发（发起授权和对话请求都是）。qimei36 由腾讯闭源的设备标识 SDK 算出（`@tencent/qimei-node`，Windows 上是 `qimei.dll`，主进程起一个 `qimei-helper.js` 去取）。下载的两个 macOS 安装包里 `qimei-node` 只有 `package.json`、没有原生库，也没有打进 `qimei-helper.js`，所以 macOS 版官方客户端拿不到 qimei36，这个头一直不发，上报事件里也没有 `qimei36`。网关按 macOS 版对齐（见[设备与平台](#设备与平台)），同样不发。

2026-10-20 生产实测：两个域按上表发起授权、轮询（未登录时 `code=11217`）、取账号都正常；按上面的规则修饰过的授权页两个域都返回 200；国际版的账号列表是 `data.accounts[]`，里面有本号的 uid。同日用国内版走完了扫码登录：扫码确认后轮询拿到令牌（有效期约 28 天）和账号，按面板的方式落盘、读回，再拉模型目录、查积分包都成功。1.1.2 以前是旧版 CodeBuddy CLI 的形状（UA `CLI/2.63.2 CodeBuddy/2.63.2`、带 `Origin` 和 `Referer`、`platform=CLI`）。

## 刷新令牌

内置 CLI 与后台服务的 `refreshSession` 一样：

1. `POST /v2/plugin/auth/token/refresh`，请求体 `{}`，头是请求框架的公共头加 `X-Domain`、`X-Refresh-Token`、`X-Auth-Refresh-Source: plugin` 和根 span 的链路头（见[登录](#登录)）；
2. 用新令牌取账号：国内版 `GET /v2/plugin/account`（`fetchCurrentAccount`），国际版 `GET /v2/plugin/accounts`（`getAccountSnapshot`），带同一条链路；
3. 取到的账号照 `mergeCurrentAccountDetails` 合并进本地账号：在 uid 相同的账号里，先挑类型和企业 id 都相同的恰好一个，再挑企业 id 相同的恰好一个（本地有企业 id 时），再挑 `lastLogin` 为真、个人版与否和本地一样的恰好一个（本地没有企业 id 时）；挑出的账号的字段盖到本地，uid 和类型保留本地的。挑不出来就保留本地账号，照常用新令牌。
4. 刷新失败、新访问令牌和旧的一样、取不到账号（国内版 `data` 为空、国际版列表为空），或者身份变了，都算刷新失败，新令牌不保存。身份按 `getAuthenticationAuthorityKey` 比：uid、账号类型、企业 id、`oneidAccountId`、部门全名、`idp`、令牌的 `tokenType` 和登录域，缺这个键和空串算不同的值；本地有 `idp` 而合并后为空时沿用本地的（`normalizeAndAssertRefreshedAuthority`）。

网关从 1.1.2 起把这些字段原样存进凭证文件（账号的 `type`、`oneidAccountId`、`idp`、`departmentFullName`，令牌的 `tokenType`），登录时写，刷新后更新，照上面的规则挑账号、比对身份。1.1.2 以前的凭证文件没有账号类型（账号接口每次都返回 `type`，有没有它就能分出新旧文件），这种文件第一次刷新时挑账号不看类型、只补字段不比对，之后就照官方比对。身份变了时官方要求重新登录；网关和别的刷新失败一样记一次失败，旧访问令牌照用到过期。1.1.2 以前国内版取到的 uid 对不上算刷新失败，官方不算。

2026-10-20 生产实测，两个域各刷新一个个人号：两个令牌都换了；旧的访问令牌刷新后仍能用；`expiresIn` 国内版 2592000（30 天）、国际版 31488222（约 1 年）。两个域的响应都有 `accessToken`、`refreshToken`、`expiresIn`、`refreshExpiresIn`、`domain`、`sessionState`、`tokenType`，国际版另有 `scope`。导回网关时用的是响应里的 `domain`，之后生产上国内版 6 个号的登录域都还是 `www.codebuddy.cn`，可见经 `www.workbuddy.cn` 刷新登录域不变。同日只读取了两个域的账号：个人号有 `type: personal`、`idp`（空串）、`oneidAccountId`（空串）、`lastLogin`（布尔值），没有 `enterpriseId` 和 `departmentFullName` 这两个键。

## 官网网页的请求

成长中心（任务、猫猫旅行、连登、抽奖、补签卡、兑换）、积分补偿、新手礼包和试用只由官网网页发：桌面端点“成长中心”会在浏览器里打开 `https://www.workbuddy.cn/profile/growth-center`。网关按网页的 axios 实例发：

- `Accept: application/json, text/plain, */*`，有请求体时才带 `Content-Type: application/json`；
- `X-Client-Platform: web`；
- 非 GET 请求带 `Origin`，`Referer` 是对应页面（成长中心 `/profile/growth-center`，积分补偿和新手礼包 `/profile/usage`，试用 `/profile`，账号资料 `/profile/account-settings`）；
- 企业号只带 `X-Enterprise-Id`；不带 `X-User-Id`、`X-Domain`、`X-Tenant-Id`、`X-Product`、`X-Request-ID`、`X-Requested-With`、`Accept-Language`；
- UA 是 Mac 上的 Chrome 152（和上报的桌面端同一台 Mac，见[设备与平台](#设备与平台)；版本取任务中心抓包里的网页上报），`Sec-CH-UA-Platform: "macOS"`，`upstream.user_agent` 配了时用它；
- `Accept-Encoding: gzip, deflate, br, zstd`（Chrome 123 起的默认值；配了自定义 UA 时也发这个）。

认人照官方的流程用网页登录态（`websession.go`）：

1. 桌面端打开官网页面前，后台服务主进程用 Node 内置的 `fetch` 发 `POST <产品入口>/v2/plugin/device/auth/code`，没有请求体，头只有 `Content-Type: application/json`、`X-User-Id`、`Authorization`、企业号的 `X-Enterprise-Id` 与 `X-Tenant-Id`、`X-Domain` 和 `X-Refresh-Token`（`wechatMpCreateDeviceAuthCode`），换回一次性登录码 `deviceCode`（5 分钟有效）。不带 `X-Refresh-Token` 时回 401 `code=10034`。
2. 浏览器打开 `<官网>/console/client-login?code=<登录码>&target=<页面路径>`，网站 302 跳到目标页，同时种下 `session`（7 天，`HttpOnly`）、国内版另有 `session_2`，以及负载均衡用的 `tgw_l7_route`。网关照浏览器跟随跳转打开目标页（同一套导航头、带上 Cookie、不带 `Referer`，最多 20 跳），页面内容读完丢掉；页面里的脚本、图片和页面自己发的请求不模拟。打开目标页出错不影响登录态。
3. 之后网页的请求只靠这些 Cookie 认人，不带 `Authorization`。

网关每个账号在每个域各存一份 Cookie，只放内存；请求回 401 时清掉重取一次，再 401 就按原错误返回。浏览器请求另带 Chrome 自动加的 `Accept-Language`（国内版 `zh-CN,zh;q=0.9`、国际版 `en-US,en;q=0.9`）、`Sec-Fetch-*`，以及用内置 UA 时的 `Sec-CH-UA*`；`Sec-CH-UA` 的值按 Chromium 的 GREASE 规则由主版本 152 推算，没有抓包确认。登录码请求另带 Node `fetch` 自动加的头：`Accept: */*`、`Accept-Language: *`、`Sec-Fetch-Mode: cors`、`User-Agent: node`、`Accept-Encoding: br, gzip, deflate`，取自安装包里 undici 6.25.0 的源码，同样没有抓包确认。

2026-10-20 生产实测：两个域都按上面的流程拿到了 Cookie；只带 Cookie 时国内版的成长任务、账号资料、抽奖次数都返回 200，国际版的账号资料、抽奖次数也返回 200（国际版没有成长体系，`/v2/activity/growth/tasks` 回 401，网关对国际版不调）；什么都不带时回 401。用 1.1.2 的代码在 6 个国内版号上走了一遍：任务列表、旅行状态正常，3 个号旅行领奖成功、1 个号出发成功。

小程序限定任务：微信小程序用 web-view 打开同一份网页（`/profile/growth-center?platform=miniProgram&token=<令牌>`），网页把令牌存起来，之后发 `X-Client-Platform: miniprogram` 和 `Authorization: Bearer <令牌>`，其余和网页一样。网关照这个形状发往官网域，UA 用微信小程序 web-view 的格式（`MicroMessenger/…` 加 `miniProgram/<小程序 appid>`，appid 取自官网前端代码，iOS 和微信的版本号是挑的近期值，没有抓包），`Accept-Encoding` 用 iOS WebKit 的 `gzip, deflate, br`。生产实测任务列表正常，领不存在的任务回 `400 task not completed`。

几处请求和以前不同：

- 任务列表先查一次订阅任务状态 `GET /v2/activity/growth/subscribe-task/status`；
- 接受任务改用 `POST /activity/growth/tasks/accept`（不带 `/v2`），领奖、领养、积分补偿、新手礼包、试用都不带请求体，旅行领奖发 `{}`；
- 兑换和抽奖的 `client_token` 带上网页的前缀：`redeem-<档位>-<uuid>`、`draw-<uuid>`；
- 新手礼包先查 `POST /billing/meter/check-gift-claimed`，活动开着且没领过才领。

官网的事件上报用 `navigator.sendBeacon` 发：请求体是 JSON 字符串，`Content-Type: text/plain;charset=UTF-8`，靠 Cookie 认人。网关照这个形状发，用同一份网页登录态。事件字段照官网前端的公共参数：`os`、`osVersion` 由 UA 解析（Mac 上的 Chrome 是 `macOS`、`10.15.7`），`sessionId` 是「毫秒时间戳-9 位随机 36 进制」，`machineId` 是 32 位十六进制的浏览器指纹，`product: UserCenter`，元素点击另带 `type`、`elementId`、`elementName`。`username` 网页取账号资料，网关没有这一项，用昵称，没有确认。

## 备用入口

官方产品配置 `productFeaturesConfig.NetworkFailover` 给国内版 SaaS 配了三个可信入口：主入口 `https://www.workbuddy.cn`、备选入口 `https://copilot.tencent.com`、备用入口 `https://wb.tencentbuddy.com`。内置 CLI 和后台服务出错时换入口、换地址，网关照做（`failover.go`、`address.go`、`httpdns.go`），只对国内版；国际版官方没有这一套。`chat_base_cn` 配成备用入口时只换地址，配成别的地址时整套不生效。

对话请求：

- 换入口：主入口或备选入口返回 HTTP 408、502、503、504，出现传输错误（连不上、连接被重置、建连或响应头超时），或者 2xx 但响应体是空的，同一请求改发备用入口一次。响应体的 `retry_policy.retry` 为 `false` 时不换。
- 换地址：DNS 解析失败时先换地址、再换入口；408、502、503 和传输错误在换不了入口（已经是备用入口）时换地址，504 不换地址。地址按候选逐个钉上去重发：URL、`Host`、TLS 的 SNI 与证书校验都还是原域名，只把 TCP 连到这个 IP。
- 候选地址：这个域名的 HTTPDNS 结果，备用入口另加官方内置的备用 IP（`httpdns_consts.go`）。本机 DNS 的结果不进候选。30 秒内失败过的 IP 跳过（全都失败过时照样全试），上次成功的排第一个。
- 同一个 `X-Conversation-Request-ID` 换过的地址记住，后续重发沿用（最多记 256 个请求）；同一请求正在换入口时，新来的重发直接返回错误，不再并行切换。
- 熔断按入口记：60 秒内 502、503、504 或连接类失败满 3 次，打开 15 秒，期间直接发备用入口；打开期间再失败依次延长 30、60、120 秒；成功一次清零。

其他请求：

- 拉模型目录 `/v3/config`：5 秒总预算内依次试主入口、备用入口、备用 IP；408、502、503、传输错误，或者 200 但目录不能用（官方 `isPersistableProductConfiguration`）时换下一个，504 不换。剩余预算不到 250 毫秒就不再试，但上一步是超时或目录不能用时，下一步另给 3 秒。
- 登录和刷新令牌不换入口（`loginPreselect: false`）。发起授权和刷新令牌在 408、502、503、传输错误，或者 2xx 但内容无效时，依次换 HTTPDNS 地址重发；轮询和取账号不换。每次尝试单独计时：发起授权、刷新令牌 8 秒，轮询、取账号 4 秒。

HTTPDNS 用腾讯云的服务，照官方的做法：

- 先按 IP 向 `119.29.29.99`、`119.28.28.99` 取服务 IP 列表（`/conf?token=`），再向服务 IP 解析域名（`/d?dn=&token=`）。请求按 IP 直连，`Host` 写 IP 本身，不校验证书，只带 `Host`；全部为空时强制重取一次列表，新列表不同就再解析一次。
- 单个请求的 300 毫秒是 Node `https.request` 的 `timeout`，也就是套接字闲置超时：连接、握手、等响应期间连续 300 毫秒没有收发才算失败，不限整个请求的时长；超时不重发。
- 地址层最多等 600 毫秒（`withTimeout(lookupHttp, 600, [])`），到点按没有结果处理，查询本身继续，查完写进缓存。
- 服务 IP 列表按返回的 `ttl` 缓存（缺省 12 小时，夹在 60 秒到 12 小时），解析结果缓存 300 秒，空结果也一样；都是过了 75% 在后台刷新（每份缓存同时只刷一路），过期后同步重查。列表刷新失败保留旧的、下一次取用时再刷；解析结果刷新到空结果时照官方覆盖旧值。
- 解析结果的后台刷新照官方（`skipCache`）不读也不写服务 IP 列表缓存：现取一次列表，取不到用旧列表或内置列表。
- 缓存未命中时每次各查一路，同一域名的并发查询不合并（同官方 `lookupHttpDns`）。

2026-10-20 生产（美国机房）实测：HTTPDNS 查到 `www.workbuddy.cn`、`copilot.tencent.com` 各 1 个 IP，`wb.tencentbuddy.com` 3 个，和系统 DNS 一致；钉到这些 IP 和备用 IP 拉模型目录全部成功。这种网络下每个 HTTPDNS 请求约 0.65 秒、一次完整查询约 1.4 秒，第一次查询按没有结果返回，之后命中缓存。改成闲置超时以前，单个请求按 300 毫秒总时长算，在这台机器上从来查不到。

## 设备与平台

两个安装包都是 macOS（darwin-arm64）版，网关按 macOS 版官方客户端对齐（10-20 定）：

- 上报事件的公共字段照 5.8.3 的事件处理器补（后台服务和内置 CLI 相同，`desktop.go` 的 `eventFingerprint`）：
  - `product`、`releaseDate`（product.json 的 `date` 转毫秒）、`commit`；
  - `userId`、`username` 与 `userNickname`（官方都取昵称）、企业号的 `enterpriseId`，有部门全名时的 `enterprise`；
  - `timezone`；
  - `os`、`arch`、`osVersion`、`cpuModel`、`cpuCores`、`memorySize`；
  - `extName: workbuddy-desktop`、`extVersion`；
  - `ideName`、`ideType`、`ideVersion`、`machineId`、`sessionId`。
- 设备取值：官方从本机读（`os.platform()`、`arch()`、`release()`、`cpus()`、`totalmem()`），网关挑一台常见配置：`darwin`、`arm64`、`24.6.0`（macOS 15.6）、`Apple M2`、8 核、16 GB。所有账号用同一组。
- `machineId`：官方 macOS 版取 `ioreg` 的 `IOPlatformUUID`，是大写 UUID；网关按账号派生一个，同一账号不变。
- `sessionId`：后台服务的事件是空串（桌面端没给后台服务设会话 id）；内置 CLI 的事件（对话活跃上报）是 CLI 进程启动时随机生成的 UUID，网关按账号固定一个。
- `reportDelay`：官方攒 2 秒发一批，发送时算 `now - timestamp`；网关立即发，把 `timestamp` 往前推 2 秒。
- 官方有、网关不带的：`qimei36` 与 `X-Device-Id-List`（macOS 版拿不到，见[登录](#登录)）、`clientIp`（只有国际版去 `api.ipify.org` 查，任务中心不对国际版开放）、`downloadChannel`（安装包没配）、`buddyId` / `buddyName`（后台服务只在 Buddy 应用里才补）、`expKeys`。界面上报的事件另有两个字段，见[没对齐或没实测的](#没对齐或没实测的)里任务中心那条。
- 官网网页按同一台 Mac 上的 Chrome 发，见[官网网页的请求](#官网网页的请求)。

以前事件里是 Windows（`win32`、`x64`、`10.0.26220`）、36 位十六进制的 `machineId` 和一个派生的 `sessionId`，取自 5.5.6 的抓包；Windows 版官方会带 qimei36，网关做不出来，所以改按 macOS。2026-10-20 生产实测：按新字段发桌面端上报、对话活跃上报、网页上报都返回成功，用 Mac 版 Chrome 的 UA 拉成长任务列表正常。

## 企业号模型目录

官方内置 CLI 只对企业号（账号有 `enterpriseId`）调 `GET /console/enterprises/{企业 ID}/config/models`，超时 5 秒。`data` 是模型数组，带 `chat` 标签的模型并进各个 agent 的名单。网关两个域的企业号都这样拉，和 `/v3/config` 合并：同名以 `/v3/config` 为准，企业接口独有的补进来；企业接口失败只用 `/v3/config`。个人号只拉 `/v3/config`，和官方一样。

1.1.2 以前国内版企业号调 `/console/enterprises/personal/models`、国际版企业号调 `/v2/enterprises/personal/models`，都不是官方的接口。2026-10-20 生产用个人号请求 `/console/enterprises/personal/config/models`，返回的 `data` 是空数组，结构和官方代码读的一致；企业号没有实测。

## 国内版域名

官方的接口请求都发往产品配置里的 `endpoint`。国内版是 `https://www.workbuddy.cn`；`https://copilot.tencent.com` 是备选入口，`https://wb.tencentbuddy.com` 是出错时切换的备用入口。`www.codebuddy.cn` 在桌面端里只用于网页链接。

网关从 1.1.2 起国内版全部发往 `https://www.workbuddy.cn`。以前对话、刷新令牌、登录、模型目录、成长任务和任务中心发往 `copilot.tencent.com`，积分、签到、积分补偿、开学季活动和小程序上报发往 `www.codebuddy.cn`。

2026-10-20 在生产用国内版个人号对比了两个域名，下面这些请求的状态码、业务码和返回的数据结构都相同，模型目录的模型名单也相同：

- 模型目录 `/v3/config`、企业模型名单 `/console/enterprises/personal/models`；
- 对话 `/v2/chat/completions`；
- 积分包查询、签到状态、积分补偿状态、开学季券；
- 成长任务列表、猫猫信息、旅行状态、连登、抽奖次数；
- 任务中心的专家列表和一次空上报；
- 登录：发起授权和轮询（授权页随之变成 `www.workbuddy.cn`）。

会改动账号状态的请求不能拿真实参数对比。2026-10-20 在 `www.workbuddy.cn` 上：

- 网关本来就会自动做的，用 1.1.2 的代码真做了一次：两个域各刷新一个号的令牌（见[刷新令牌](#刷新令牌)），3 个号旅行领奖、1 个号旅行出发，都成功。
- 其余的用必然失败的参数按新形状发：签到（今天已签过）、领奖（任务没完成）、旅行出发（地点不对）、兑换（档位不存在）、补签卡（日期不在本月）、领新手礼包（已领过）、抽奖（没有次数）、领积分补偿（已领过）、小程序领奖（任务不存在）都返回业务错误；接受任务（任务码不存在）返回成功；国际版试用返回“已申请过”。说明这些路径和认证都对。
- 设置外观不校验资源键，探测时写进了一个不存在的值，已改回原值。

真正领到的那一次没有试到的：抽奖（6 个号都没有次数）、积分补偿（都已领过）、小程序任务（没有已完成的小程序任务）、签到成功（当天都已签）。

对话请求在主入口出错时切到备用入口，见[备用入口](#备用入口)。

## 产品开关

国际版 `product.json` 里有这几项，国内版都没开：

- `DisableCheckin: true`、`UserGrowth: false`：国际版没有签到和成长任务，网关对国际版账号也都跳过；
- `DisableActivityBanner: true`；
- `DefaultModelOptimizationOff: true`。

## 错误码

上游错误体里的业务码按内置 CLI 的错误码枚举归类（`internal/upstream/bizcode.go`），先于 HTTP 状态码和文案判：

| 业务码 | 官方名字 | 官方怎么处理 | 网关怎么处置 |
|---|---|---|---|
| `14003` | `RateLimitError` | 临时限流，原请求退避重发（0.5 秒起、封顶 32 秒） | 只冷却这个号的这个模型约 30 秒，当场换号 |
| `6001` `6002` `6005` `6006` | `CraftRateTPS/TPM/RPS/RPMLimit` | 同上 | 有重置时间就冷却到那时，没有就同 14003 |
| `6000` `6003` `6004` `6007` `6008` | `CraftRateLimit`、`CraftRateTPH/TPD/RPH/RPDLimit` | 不自动重发 | 按模型冷却到上游给的重置时间，换别的模型照样用 |
| `14001` `14012` `14013` `14014` `14018` `14019` | `UsageLimitExceeded*`、`UsageLimitUserExhausted`、`UsageLimitNoTokenBudget` | 额度用完（`isQuotaExhaustedError`），不重试 | 余额不足，冷却到次日 04:00 |
| `14015` | `UsageLimitLicenseExpired` | 归授权过期 | 账号软冷却 |
| `14016` `14017` | `UsageLimitEnterpriseNotActivated`、`UsageLimitUserNotActivated` | 归未激活 | 账号软冷却 |
| `10105` | `ConversationLimitExceeded` | 归同时进行的会话太多 | 账号冷却约 60 秒，当场换号 |
| `11115` | `ContextTooLong` | 归输入超长 | 不罚账号、不换号，原文返回；文案照 `isInputLengthError` 的词表与正则认 |

`14002`（`ConversationChatTooMany`）官方只在统计里归成额度用完，既不算额度用完也不算临时限流，含义没确认，网关仍按状态码处理；`15001`（联网搜索限流）网关用不到。

`11128` 是上游拦下了这一条请求，响应体是数字码 `{"code":11128,...}`（同类项目讨论时常写成「11-128」，是为了避开上游对这串数字本身的拦截，并不是上游回的形状）。文案有两种：`Illegal API invocation from an unapproved channel` 是内容里有渠道指纹，`request illegal` / `conversation id missing` 是渠道校验没过（例如国际版缺会话头，见[生产实测](#生产实测)）。网关都按内容拦截处理，不罚账号；指纹清洗的规则见[网关与选号](gateway.md#系统提示词与内容审核)。

`11140` 按文案分两种：带 `request illegal` 的是账号被上游风控限制，网关禁用该号、原因里提示去官方申诉，并计入风控波（见[网关与选号](gateway.md#错误分类与账号处置)）；其余形态是临时限流，按状态码处理。

2026-10-23 生产实测一个国际版个人号，是**只封对话、不封其他**的账号级风控：

- 每次对话都回 `403` + `code 11140` + `request illegal`，`displayMsg` 是「账号存在异常行为 / Your account has been restricted due to unusual activity」。导入后第一次对话即被封，人工解禁后再试 3 次仍是同样的报错，每次都被重新禁用；
- 同一个号的积分查询、成长任务列表、网页登录态、国际版注册激活、国际版试用领取全部正常，说明 token 有效、身份能过，限制只落在对话上；
- 该号的凭证在导入前就带着 `HTTP 403 (account restricted)` 和 `restrictedUntil`（换算后是导入后几分钟），可见是账号在导出时已被风控，不是网关或导入造成的。同一时段其余账号对话全部 200，网关侧的请求形状没有问题。

结论：这类号的 token 是有效的，重新登录一般解不开，要向官方申诉。网关 1.5.0 起把禁用原因从「`re-login required`」改成提示申诉。同类项目 workbuddy2api-hub 的 #629 反馈，10-23 下午起国际版号成批被这样限制，国内版不受影响；网关的风控波暂停照它的做法：15 分钟内同一个域有 2 个号被限，就把该域其余的号暂停 30 分钟。

## 生产实测

2026-10-19 至 10-20，用国内版、国际版各几个个人免费号在生产实测：

- 上面这些请求形状，两个域的对话、积分查询、签到状态（只读）、模型目录都返回 200；
- 各类请求按官方的 `Accept-Encoding` 发，上游目前都不压缩响应，对话流、模型目录、网页登录态和成长任务照常；
- 备用入口与 HTTPDNS 见[备用入口](#备用入口)；
- 积分的三个接口与旧接口给的包相同，按官方算法算出的余额与汇总一致；
- 登录的发起授权、轮询、取账号按官方形状都正常；
- 官网网页的请求按官方流程换到 Cookie 后只带 Cookie，成长中心、账号资料、抽奖次数都返回 200，见[官网网页的请求](#官网网页的请求)；
- 刷新令牌两个域各真刷了一个号，见[刷新令牌](#刷新令牌)；
- 前缀缓存按 `X-Conversation-ID` 命中：`glm-5.2` 会话 id 稳定时第二轮 8/8 命中，每轮换新只有 3/10；`deepseek`、`kimi`、`glm-5.3` 不论会话 id 都能命中；`prompt_cache_key` 对哪个模型都没有帮助；
- 串会话：两个域、4 个模型共 29 次，用同一个会话 id 发不相干的内容，都没有带出别的对话的内容；
- 国际版要求第一条消息是 `system`：没有，或者放在第二条，都回 400 `code 11128`；第一条是 `developer` 也是 11128。网关会把 `developer` 改成 `system`，没有 `system` 时自动补一条；
- 个人免费号调 GPT、Gemini、Grok 回 `11158`（这类用户不能用），所以这几个模型没测到。

## 没对齐或没实测的

- 真正领到的那一次没有试到：抽奖（实测的号都没有次数）、积分补偿（都已领过）、小程序任务（没有已完成的）、签到成功（当天都已签）。这几样都按新形状用必然失败的参数试过，路径和认证是对的，见[国内版域名](#国内版域名)。
- 扫码登录只走了国内版：国际版的发起授权、轮询、取账号按新形状实测正常，扫码那一步没做。
- 设备信息是挑的一台常见 Mac 配置，不是哪台真机，见[设备与平台](#设备与平台)。
- 小程序事件（开学季、小程序任务）里的会话 id、轮次 id 按桌面端的形状生成，小程序没有源码可对照，没确认。
- 部门全名（`X-Department-Info`、上报的 `enterprise`）：照官方接上了，池里没有带部门的企业号，上游返回的值什么样、刷新时会不会变都没实测。
- 传输层：网关出站一律 HTTP/1.1（有意禁了 HTTP/2，见 `transport.go`），TLS 握手是 Go 标准库的。桌面端的请求官方也是 HTTP/1.1，但 TLS 握手是 Node（Electron）的；官网网页在浏览器里是 HTTP/2 和 Chrome 的 TLS 握手，HTTP/2 才有的 `priority` 头网关也没有。服务端能从握手特征分出这几种客户端。要对齐得换成能定制握手的库 [uTLS](https://github.com/refraction-networking/utls)：支持 Go 1.22 的最后一版是 v1.6.7（内置的 Chrome 预设停在较早的版本），v1.7.0 起要求 Go 1.24；桌面端那套还要先在 Mac 上抓到官方客户端真实的握手。2026-10-20 定 1.1.2 不做，之后要做单独发一版。
- 企业号：模型目录、积分、请求头里的企业身份都照官方改了，池里没有企业号，都没有实测；国际版企业号同样。
- 签到、领奖、接受任务、兑换、补签卡、新手礼包领取、试用：只用必然失败的参数试过，真正成功的那一次要上线后看日志；上报没有实测。
- 官网网页的 UA 固定为一个 Mac Chrome 152 的值，`Sec-CH-UA` 由版本号推算；小程序 web-view 的 iOS、微信版本号是挑的近期值；网页登录码请求的头取自 Node 内置 fetch 的源码。这几样都没有抓包确认，见[官网网页的请求](#官网网页的请求)。
- 备用入口与 HTTPDNS 的切换只在生产上验证了「查得到、钉 IP 能用」，主入口真正出错时的切换没有遇到过，靠离线测试覆盖。
- 任务中心的桌面端上报：各个事件的业务字段 1.1.2 起照 5.8.3 逐个核对过，依据是安装包里各事件的上报代码和给每个事件补字段的处理器（内置 CLI 的 baggage 处理器补 `codebuddy.session_id` 等，Buddy 处理器只在有 Buddy 时补 `buddyId`）。事件走哪条路决定多不多两个字段：
  - 界面的 `reportEvent`（模板点击、灵感案例的点击和 `playbook_cta_click`、换主题、专家召唤、定时任务、Buddy 应用、`query_recommend_prompt_send`）经渲染层上报服务 `AdapterTelemetryService` 和桌面适配器：`mode` 统一盖成 `LOCAL`（建任务、推荐类事件另有改写），没有 `buddyId`、`buddyName` 时补两个空串（`injectBuddyTelemetryContext`）；
  - 首轮回复后经 `wb.metrics` 报的（建任务、带模板建任务、做同款发送、专家使用、设计画布）、界面自动埋点（`data-track-id`，如召唤按钮、模板按钮的点击）和内置 CLI 的对话事件，不补这两个字段。

  灵感案例从官方静态站点现拉（`static.workbuddy.cn/workbuddy/playbook`，发现页同一份），首页模板从 `download.codebuddy.cn/plugin-marketplace/scenes-v2.json` 现拉，事件里的 id、名字、类型、分类、提示词长度是真实值。官方是 Electron 渲染进程里的请求，网关用官网网页的那个 UA 拉，Electron 37 内置 Chrome 的确切版本没定。灵感案例索引有 6 MB 多，网关把挑出的案例和模板列表各缓存一小时（官方也是拉一次用一阵），生产服务器上两个站点都能访问（10-20 实测）。

  这些字段是运行时才有的值，网关取的典型值：
  - 对话链的输入长度（专家对话按实际发的提示词算，做同款按案例提示词算，模板任务没有真发对话，用典型值）、步数、token 数和 `presentAt`；
  - 点模板按钮的自动埋点的 `pageURL` 取首页的 hash `#/`（首页也能从 `#/home` 进，没确认）；模板任务只模拟点模板按钮，不模拟再点它下面的示例提示词（选了示例官方会再报一次 `template_used`，带 `prompt_index`）；
  - 设计画布两个事件的 `cost` 和画布文件 id；
  - `skill_info` 的 `fileCount` 按 5.8.3 安装包内置的 48 个技能加上这个技能算（49），`skillId`、`skillVersion` 取自抓包。

  小程序口径的事件和官网资料库的 `library_doc_intro_click` 不在桌面端安装包里，没有核对。
- 错误码：生产日志（10-19 至 10-23）只见过 `6004`、`14003`、`11101`、`11128`、`11140`、`11158`，其余业务码照官方枚举归类，没有真遇到过；`14002` 的含义没确认，见[错误码](#错误码)。`11140` 只见过 `request illegal` 一种文案（账号级风控，见上），限流那种文案没遇到过；`11158` 是个人免费号调 GPT、Gemini、Grok 时回「该用户类型不能用这个模型」，网关没有专门处理，按其他客户端错误换号重试，不罚号。
- 小程序的埋点事件（开学季活动）照的是一次 Windows 版 PC 微信的抓包，系统写 `windows`、`machineId` 是抓包里的固定值；小程序 web-view 的 UA 却是 iPhone 微信，开学季接口和埋点请求从 1.1.2 起也用这个 UA（以前没设，发的是 Go 默认的 `Go-http-client/1.1`）。没有小程序的包、也没有 PC 微信的 UA 抓包可对照，系统和 UA 两边都没改成一致，见[官网网页的请求](#官网网页的请求)。

## 官方升级后怎么核对

1. 从官网下载两个域的新安装包，各自解到一个空目录，只读，不运行。
2. 在 `cli/product.json` 里读版本、`applicationName`、`commit`、`date` 与 `productFeatures`。
3. 在内置 CLI 和 `main/` 里搜上文表格列出的函数名，逐项对照取值。
4. 有变化就改这几处常量，再按改动补测试、写更新日志：
   - `internal/upstream/headers.go` 的 `defaultClientVersionCN`、`defaultClientVersionGlobal`、`defaultCliVersion`；
   - `internal/upstream/desktop.go` 的 `desktopCommit`、`desktopReleaseDate`；
   - 刷新令牌后的账号合并与身份比对在 `internal/upstream/refresh_identity.go`（取账号在 `client.go` 的 `refreshAccountCheck`）：内置 CLI 里搜 `mergeCurrentAccountDetails`、`getAuthenticationAuthorityKey`、`normalizeAndAssertRefreshedAuthority`；
   - `cli/product.json` 的 `productFeaturesConfig.NetworkFailover` 变了，改 `internal/upstream/failover.go` 的入口与熔断参数；
   - HTTPDNS 的 token、启动 IP、备用 IP 在 `internal/upstream/httpdns_consts.go`（官方代码里搜 `719980335`、`119.29.29.99`），超时与缓存时长在 `internal/upstream/httpdns.go`；
   - 上报事件的公共字段在 `internal/upstream/desktop.go` 的 `eventFingerprint`：后台服务里搜 `OSEventProcessor`、`ProductInfoEventProcessor`、`UserinfoEventProcessor`，网页上报的公共参数在官网前端里搜 `UserCenter`；
   - 任务中心各事件的业务字段在 `internal/upstream/desktop.go` 和 `internal/panel/autotask.go`：界面的事件码在 `renderer/assets/adapter-telemetry-service-*.js`（形如 ``e.TaskCreated=`agent_task_created` ``），按枚举名搜调用处；对话链在内置 CLI 里搜 `reportChatRequestSendTelemetry`、`reportMessageSendTelemetry`；换主题的事件搜 `buildSkinApplyPayload`，主题列表接口是 `operation-platform/appearance/resources`，版本档搜 `getEditionDisplayType`；界面事件补的字段搜 `AdapterTelemetryService`、`injectBuddyTelemetryContext`；灵感案例站点搜 `getDiscoverBaseUrl`（`internal/upstream/playbook.go`），首页模板搜 `scenes-v2.json`、`reportTemplateUsed`（`internal/upstream/scenes.go`）；
   - 各类请求的 `Accept-Encoding` 在 `internal/upstream/encoding.go`：内置 CLI 里搜 `advertiseZstdAcceptEncoding`，Node fetch 的默认头在 `node_modules/undici/lib/web/fetch/index.js`；
   - 登录的 `platform`、路径在 `internal/upstream/login.go`，积分包代码在 `internal/upstream/resources.go`；
   - 业务码表在 `internal/upstream/bizcode.go`：内置 CLI 里搜 `CraftRateTPSLimit`（错误码枚举）、`isTransientRateLimitBusinessCode`、`isQuotaExhaustedError`、`classifyErrorDetail`、`computeRequestRetryDelayMs`；上下文超限的词表在 `internal/upstream/client.go` 的 `promptTooLongMarkers`，搜 `isInputLengthError`。
5. 官网网页的接口变化要另看 `www.workbuddy.cn` 成长中心页的前端代码，对应 `internal/upstream/web.go` 和用它的几个文件。

不想等新版本发布时，可以用 `upstream.client_version`、`upstream.cli_version` 先改版本号，见[配置](configuration.md)。
