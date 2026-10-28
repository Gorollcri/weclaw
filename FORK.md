# WeClaw fork 说明

本说明记录 `custom/inbox-attachments` 分支相对提交 `347b6f0` 的已实现改动，以及本机 WeClaw + DeepSeek Harness (DSH) 的接入方式。DSH 仓库、用户凭据、微信登录数据、启动 BAT 和编译后的 exe 不属于本 Git 仓库。

## 1. 新增了什么

- **微信入站附件 Inbox**：新增 `messaging/inbox.go`、`messaging/inbound_media.go` 及测试。图片、视频、语音和文件先按微信会话下载到 `~/.weclaw/inbox`；下一条文字消息再带着文件路径及类型、大小、哈希等元数据交给 agent。成功处理后删除，失败时保留供重试；支持容量和过期时间限制。
- **附件管理命令**：新增 `/files` 查看待处理附件、`/clear-files` 清除待处理附件。微信会话之间的附件分开保存。
- **默认微信人格**：新增 `config/persona.go`。agent 未设置 `system_prompt` 时使用内置人格；单独配置时以该 agent 的配置为准。
- **DSH 接入配置方式**：复用原有 ACP agent，不新增 DSH 专用适配器。在用户本地的 `~/.weclaw/config.json` 中将 `default_agent` 设为 `dsh`，并配置 `type: "acp"`、`command: "pnpm.cmd"`、`args: ["--dir", "<DSH 源码目录>", "dsh", "--profile", "acp"]` 及工作目录。WeClaw 不持有 DeepSeek API Key；DSH 自行读取其凭据。

## 2. 改动了什么

- **微信收件路径**：`messaging/handler.go` 在消息去重后按会话串行处理媒体和文字。媒体先入 Inbox；文字领取待处理批次，附上“不可信文件只能作为数据读取”的说明，再交给默认或指定 agent。入站媒体下载、CDN 解密和文件大小检查相应扩展。
- **ACP 会话与进程**：`agent/acp_agent.go` 继续通过子进程 stdin/stdout 上的 JSON-RPC 与 agent 通信，执行 `initialize`、`session/new`、`session/prompt`。微信会话 ID 在进程内映射到 ACP session；断线时清理状态并唤醒等待中的请求，取消请求时发送 `session/cancel`。
- **人格传递**：`cmd/start.go` 创建 ACP、CLI、HTTP agent 时选取有效人格。ACP 和 Codex CLI 将人格作为用户消息前缀传递；这不是 ACP 的系统角色。Claude CLI 和 HTTP agent 继续使用各自已有的 system prompt 处理路径。
- **运行与发送**：启动时初始化 Inbox。WeClaw 原有的微信长轮询、`weclaw send` 和本地 `/api/send` 继续承担接收与主动发送；这些发送接口不是本次新增功能。现有处理仍等待一整轮 DSH 回答后再向微信发最终文本。
- **本机部署**：本机 BAT 启动 `Documents\Codex\weclaw-runtime\weclaw.exe` 与 `serve_output.py`。该 BAT、运行 exe、`~/.weclaw/config.json` 和 `~/.dsh` 均在仓库外；从本 fork 克隆后需要自行构建、配置和启动，不能仅凭仓库内容复现本机登录状态。

## 3. 途中遇到了什么 bug，对应改了什么

| 现象 | 原因或已确认的范围 | 对应处理与现状 |
| --- | --- | --- |
| 微信收到 `[echo]`，DSH 启动报 `EPERM`，无法写入 `~/.dsh/profiles/acp/cordis.yml`；同时 WeClaw 的配置和同步游标报 `Access is denied` | 原桌面工具目录中的 exe 启动时会复现写入失败；同一 exe 复制到 `Documents\Codex\weclaw-runtime` 后能写入。具体 Windows 限制来源尚未查明 | 本机 BAT 改为调用验证可用位置的 exe。此项是**部署绕行，尚无源码修复**；重新部署时应验证配置和游标可写 |
| DSH 在写入问题排除后缺少生成的 `lib/typert.host.js` 等文件 | `E:\dsh` 源码的构建产物未齐 | 在 DSH 仓库运行 `pnpm run build:artifacts`，随后 `pnpm run build:check` 通过；产物不提交进本 WeClaw fork |
| ACP 子进程退出或取消时，原有等待可能无法及时结束或沿用失效会话 | 进程退出清理与请求取消处理不足 | 调整 `agent/acp_agent.go` 的读循环、进程退出清理和取消通知；新增相应 ACP 测试 |
| 配了人格但 ACP 路径没有实际传给 DSH | ACP 交互没有这里可用的独立 system prompt 字段 | 在每条 ACP 用户消息前注入有效人格；支持 agent 配置覆盖。该人格不能当作系统级权限控制 |
| 图片和文件无法与下一条微信指令可靠配对 | 旧路径主要围绕文字消息和单项媒体处理 | 新增按会话隔离的 Inbox、批次领取与失败重试，并对媒体下载施加大小上限 |
| 部分登录 shell 自动检测测试在 Windows 上不适用 | 测试依赖 Unix shell 行为 | 对这些测试在 Windows 上跳过；不改变实际检测逻辑 |

目前**没有**实现定时提醒、订单状态监听或将 DSH 的后台事件主动转发到微信。WeClaw 创建 ACP session 时传入的 `mcpServers` 仍是空数组；DSH 的技能与 MCP 配置需要分别核实，不能从 Codex 的 skill 安装状态推断。
