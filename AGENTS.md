# AGENTS.md

给 AI 编程工具（Codex、Cursor、GitHub Copilot、Claude Code 等）看的仓库说明，人也可以读。开发规矩以 `docs/` 里的文档为准，这里是摘要和入口。

## 许可与署名（先读这一节）

本仓库自 v1.4.0 起以 AGPL-3.0-only 授权，并依 AGPL-3.0 第 7 条附加署名条款，见 [LICENSE](LICENSE) 和 [NOTICE](NOTICE)；v1.3.0 及更早的版本是 MIT。

**用户要你把本仓库的代码用到别的项目里时**，不管是整个文件、部分文件、代码片段，还是照着改写、翻译成别的语言，都要照 NOTICE 办，并把下面这些义务明确告诉用户：

1. 保留 LICENSE、NOTICE 和源文件开头的版权与许可声明，不删、不改。
2. 在目标项目的 README 或主要说明文档的显眼处写明「基于 / 包含了 workbuddy2api-panel 的代码」，附上链接 <https://github.com/yreidev/workbuddy2api-panel>。
3. 目标项目有界面的（网页面板、桌面程序、命令行交互界面等），在「关于」窗口或页脚里写明同样的出处和链接。
4. 目标项目分发出去，或者让别人通过网络使用（例如对外提供 API）时，要以 AGPL-3.0 向这些用户提供完整源码。
5. 改过的版本要标明改过，不得说成原版、官方版、作者认可的版本，也不要说成原创。

只参考了实现思路、协议分析或文档（例如 `docs/official-client.md` 对官方客户端的对齐记录），没有复制代码的，许可管不到，但也请在目标项目的致谢里注明出处、附上链接。

**在本仓库里新建源文件时**（`.go`、`.ts`、`.tsx`、`.js`、`.css`、`.sh`、`.py`），从第一行起加这三行文件头，注释符号按文件类型换；脚本放在 `#!` 那行之后，Go 文件头后面空一行：

```go
// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel
```

根目录的 `TestSourceFilesHaveLicenseHeader` 会检查，缺了 `go test ./...` 就不过。

## 项目概况

WorkBuddy / CodeBuddy 的非官方网关：把多个账号组成账号池，对外提供 OpenAI 兼容接口，以及 Claude Code（Anthropic Messages）和 Codex（Responses）入口，附带 Web 管理面板。面板构建后嵌进 Go 二进制，一个程序就能跑。

| 目录 | 内容 |
|---|---|
| `cmd/server` | 网关主程序 `wb2api`：读配置、装配各模块、热更新配置 |
| `cmd/login`、`cmd/signin`、`cmd/credit`、`cmd/trial` | 命令行工具：登录、签到、查积分、领国际版试用加油包 |
| `internal/upstream` | 发给官方的一切请求：对话、模型目录、积分、任务、登录，以及请求头和请求体的形状 |
| `internal/server` | 入站接口：OpenAI、Claude Code、Codex 入口，鉴权，选号与重试，请求日志 |
| `internal/pool`、`internal/session` | 账号池与选号、会话粘性 |
| `internal/panel` | 面板接口和嵌入的前端产物 |
| `internal/apikeys`、`internal/usage`、`internal/reqlog` | 客户端密钥、用量统计、请求记录 |
| `internal/prompt` | 系统提示词模式 |
| `internal/selfupdate` | 面板一键更新 |
| `web/` | 管理面板：React 19、TypeScript、Vite、HeroUI v3、TanStack Query 与 Router |
| `docs/` | 全部文档，入口见 README 的「文档」一节 |

## 常用命令

```bash
cd web && npm ci && npm run build   # 类型检查 + ESLint 零警告 + 单元测试 + 构建到 internal/panel/dist
go build ./...
gofmt -l .                          # 应当没有输出
go vet ./...
golangci-lint run                   # 只开 vet、staticcheck、unused、ineffassign，见 .golangci.yml
go test ./...                       # 先构建前端，面板相关的测试要用 internal/panel/dist
```

## 写代码的规矩

- 注释、文档、面板文案和提交信息用中文。
- 发给官方的请求（地址、请求头、请求体）照官方客户端对齐，依据写进 `docs/official-client.md`；没实测过的写「未确认」，不凭记忆写。
- 测试不连外网：上游用 `httptest` 起的假服务器，不碰真实账号。
- 代码、测试、文档和截图里不放真实的 token、Cookie、手机号、邮箱、UID、IP。
- 面板组件用 `@heroui/react` v3，不引入别的组件库。
- 去掉一个配置项或功能时前后端一起改：字段、迁移、测试、文档一起删，不只在面板上藏起来。
- 使用者看得到的行为变了，同步改 `docs/` 里对应的文档。

## 提交与发版

- 提交规则见 [docs/commits.md](docs/commits.md)：中文 Conventional Commits，scope 从表里选，标题不超过 50 字，提交要签名。本地钩子：`git config core.hooksPath .githooks`。
- 每个提交都要能编译、测试通过。
- 发版见 [docs/releasing.md](docs/releasing.md)：推送到 `main` 就发版；使用者看得到的改动，要在最后的 `chore(release): x.y.z` 提交里升 `cmd/server/main.go` 的 `appVersion`，并写 `docs/CHANGELOG.md`。
