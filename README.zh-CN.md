<div align="center">

# 氪脑 · K-brain

**运行在终端里的编程 Agent，用 Go 构建，也可作为其他前端的后端底座。**

读代码、改文件、执行命令、验证结果，在同一会话中完成任务。

运行 **`kn`** 开始使用，取自“氪脑”拼音 **K**e **N**ao。

[![Go](https://img.shields.io/badge/Go-1.27%2B-00ADD8?logo=go&logoColor=white)](go.mod)
[![Platforms](https://img.shields.io/badge/Windows%20%7C%20Linux%20%7C%20macOS-x64%20%7C%20ARM64-555)](https://github.com/Stack-Cairn/K-brain/releases)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

**简体中文** · [English](README.md)

[快速开始](#快速开始) · [使用方式](#使用方式) · [源码构建](#源码构建) · [文档](#文档) · [项目结构](#项目结构)

<p align="center">
  <img src="docs/assets/k-brain-tui.png" alt="氪脑 TUI" width="900">
</p>

</div>

---

## 快速开始

### 1. 安装

**macOS / Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/Stack-Cairn/K-brain/main/install.sh | sh
```

**Windows PowerShell**

```powershell
& ([scriptblock]::Create((Invoke-WebRequest -UseBasicParsing https://raw.githubusercontent.com/Stack-Cairn/K-brain/main/install.ps1).Content))
```

安装脚本从 [GitHub Releases](https://github.com/Stack-Cairn/K-brain/releases) 下载对应平台的二进制，并校验 SHA-256。Windows 默认安装到 `%LOCALAPPDATA%\Programs\k-brain`，验证发布版本后，将安装目录置于当前进程 PATH 和用户 PATH 的首位；其他已打开的终端需要重启。如果 `kn --version` 仍显示 `dev`，运行 `Get-Command kn -All` 检查是否命中了旧程序或别名。若尚无适合的平台发布包，可[从源码构建](#源码构建)。

<details>
<summary>手动下载：Windows、Linux、macOS × x64、ARM64</summary>

发布资源使用 `k-brain-<os>-<arch>` 命名，Windows 带 `.exe`；`os` 为 `windows`、`linux` 或 `darwin`，`arch` 为 `x64` 或 `arm64`。下载后将主程序重命名为 `kn`（Windows 为 `kn.exe`），放到 PATH 中；Linux/macOS 还需赋予执行权限。

Computer-use helper 使用 `k-brain-computer-<os>-<arch>` 命名，重命名为 `k-brain-computer`（Windows 带 `.exe`）并与主程序放在同一目录。运行依赖见 [平台说明](#平台说明)。

</details>

### 2. 配置 API

编辑 `~/.k-brain/config.json`，Windows 对应 `$HOME\.k-brain\config.json`。配置支持 JSONC 注释与末尾逗号；`K_BRAIN_HOME` 可覆盖配置目录。

```json
{
  "language": "en",
  "defaultModel": "model1",
  "providers": {
    "demo": {
      "name": "demo",
      "api": "openai-completions",
      "baseUrl": "https://api.example.com/v1",
      "apiKey": "YOUR_API_KEY",
      "models": [
        {"id": "model1", "contextWindow": 128000, "maxTokens": 8192},
        {"id": "model2", "contextWindow": 128000, "maxTokens": 8192}
      ]
    }
  }
}
```

将 `baseUrl`、`apiKey`、`model1` 和 `model2` 替换为服务商提供的值。只支持第三方 API Key 接入，不提供账户登录、OAuth 或订阅认证。

`apiKey` 也支持环境变量引用（例如 `${MY_API_KEY}`）或以 `!` 开头的本地凭据命令。命令支持引号包裹的路径和参数，直接执行程序，不自动启动 Shell 或展开管道；凭据取自标准输出，去除首尾空白。单次执行限时 5 秒，标准输出最多 64 KiB，失败时不采用部分输出。MCP 请求头和环境变量中的凭据命令使用相同规则。

| `api` | 协议 |
| --- | --- |
| `openai-completions` | OpenAI Chat Completions |
| `openai-responses` | OpenAI Responses |
| `anthropic-messages` | Anthropic Messages |

- `baseUrl` 填 API 前缀，例如 `https://api.example.com/v1`，不要填写完整的 `/chat/completions` 路径。
- 模型放在各 provider 的 `models` 数组中，不使用旧版顶层 `models` 配置。
- `contextWindow` 是上下文 token 上限，`maxTokens` 是输出 token 上限。按实际模型能力填写；显式配置优先于模型目录。
- `/model refresh` 从 `baseUrl + "/models"` 获取目录，上例对应 `/v1/models`；`/model` 选择模型。
- 修改文件后重新启动。未填写 API 也可进入 TUI，但发送模型请求需要有效配置。

三种协议均按顺序发送混合图文输入。Responses 使用 `input_image`；Anthropic 使用 base64 或 URL 图片来源，并保留全部系统指令。图片能力仍取决于所选模型和服务端，Anthropic 内嵌图片须为 JPEG、PNG、GIF 或 WebP。无效图片引用会在本地报错，不再静默丢弃。会话文件保留图文内容块，ACP 加载会话时也会按顺序回放内嵌图片和文字。

浏览器与 Computer-use 截图会附在对应工具结果中，并保存在会话历史里。并行工具和子代理的图片分别归属各自调用，接收截图的模型须支持图片。TUI、CLI、ACP 共用后端的附件传递逻辑，各入口的工具启用策略保持不变。

可以为 Agent 生命周期事件配置本地 Hook。命令通过 `K_BRAIN_HOOK_EVENT` 环境变量接收 JSON 事件；`PreToolUse` Hook 返回非零状态时会拒绝本次工具调用。

```json
{
  "hooks": {
    "SessionStart": [{"command": "echo session started"}],
    "PreToolUse": [{"command": "echo $K_BRAIN_HOOK_EVENT", "shell": "bash", "timeout": 10}]
  }
}
```

支持 `SessionStart`、`UserPromptSubmit`、`PreToolUse`、`PostToolUse` 和 `Stop` 事件。Windows 可使用 `powershell`、`pwsh`、`bash`、`wsl` 或 `cmd`，其他平台使用对应系统 Shell。

CLI、TUI 和 ACP 共用配置钩子与已启用插件钩子的事件入口。事件携带会话 ID 和工作目录，命令在该目录执行并继承当前沙箱策略。启动钩子在最终会话 ID 分配后执行，每次激活只运行一次；TUI 预览和切换模型不会重复触发。ACP 初始化失败时释放 MCP 连接，并仅删除本次新建的会话；恢复失败保留已保存的历史。

### 会话文件

会话保存在 `~/.k-brain/sessions/<project-id>/<session-id>/session.jsonl`；设置 `K_BRAIN_HOME` 时位于该目录下的 `sessions`。项目 ID 是工作区路径的稳定哈希，会话按项目分组，同时保留完整 session ID。

```text
~/.k-brain/
  config.json
  brain.md
  sessions/<project-id>/
    <session-id>/
      session.jsonl
```

使用 `kn sessions` 查看按项目分组的会话，使用 `kn sessions search <query>` 搜索，或用 `archive/delete` 管理；使用 `kn --resume <session-id>` 恢复会话。长期工作指令保存在 `~/.k-brain/brain.md`，通过 `/brain` 编辑；TUI 中 `/status` 显示当前会话文件路径。JSONL 记录包含元数据、消息、任务、压缩、定时任务和回退快照引用。不再支持 SQLite 存储或旧库迁移，也不会读取或修改已有数据库文件。

项目授权使用 TOML 格式，保存于 `~/.k-brain/trusted_folders.toml`（Windows：`~\.k-brain\trusted_folders.toml`），每个目录使用 `[folders."<绝对路径>"]`，并记录 `trusted` 与 `decided_at`。

### OS 级沙箱

在 `config.json` 中配置命令隔离：

```json
{"sandbox":{"mode":"strict","backend":"auto","network":false,"writable":[],"readOnly":[]}}
```

Linux 使用 Bubblewrap namespace，macOS 使用 Seatbelt，Windows 使用 WSL2 加 Bubblewrap（`backend: "wsl"`）。严格模式缺少后端时会直接失败。Shell、插件和后台任务都会继承同一策略。

### 插件

项目插件放在 `.k-brain/plugins/<name>/plugin.json`，用户插件放在 `~/.k-brain/plugins/<name>/plugin.json`：

```json
{"name":"sample","version":"1.0.0","command":["sample-plugin"],"prompt":"附加指令","tools":[{"name":"lookup","description":"查询值","inputSchema":{"type":"object"}}]}
```

进程接收一行 `tool.invoke` JSONL 请求并返回一行 JSON-RPC 响应。插件必须显式启用，并继承沙箱策略。使用 `kn plugins list|install|enable|disable|remove|reload` 或 `/plugins` 管理。

### 3. 开始任务

在项目目录运行：

```sh
kn
```

直接输入任务，例如：

```text
阅读当前项目，定位测试失败的原因，修改代码并运行相关测试，最后总结改动。
```

TUI 使用当前终端的全屏界面，输入区固定在底部。首轮对话后会话标题显示在右下角。使用 `kn -c` 继续当前目录最近的会话，或 `kn --resume` 打开会话列表。

预览和切换模型会保留当前会话、待办、排队指导消息及后台任务。已有子任务继续使用原模型处理后续提问；新子任务继承所选模型，除非配置了 `taskModel`。当前回合或压缩尚未结束时不能切换。TUI 按模型保存和恢复用量，切换后保留原有消耗及其计价归属。

## 使用方式

### 交互会话

| 输入 | 功能 |
| --- | --- |
| `/language` · `/language zh_cn` · `/language zh_Hant` · `/language en` | 选择界面语言（简体中文 / 繁體中文 / English） |
| `/model` · `/effort` | 切换模型、调整推理强度 |
| `/context` · `/compact` | 查看上下文、手动压缩 |
| `/rewind` · `/fork` | 回退到先前轮次、创建会话分支 |
| `/forks` · `/export` · `/import` | 查看会话树、导出 Markdown/JSONL/HTML、导入 JSONL |
| `/title` · `/resume` | 修改标题、恢复会话 |
| `/diff [--staged] [--stat]` | 本地查看 Git 已跟踪文件的差异，不发起模型请求 |
| `/copy [N] [file]` | 复制倒数第 N 条助手正文，或保存到新文件 |
| `/prompts [refresh]` | 查看或重新加载 Markdown 提示词模板 |
| `/privacy on|off|toggle|status` | 开启本地隐私脱敏网关；status 显示规则数、内存映射数和请求体上限 |
| `/mcp` · `/tasks` | 管理 MCP 连接、查看后台任务 |
| `!命令` · `!!命令` | 执行 shell；前者将结果加入会话，后者仅在本地显示 |
| `/help` | 查看完整命令与快捷键 |

`Ctrl+P` 打开命令面板，`Ctrl+J` 换行，`Esc` 中断当前任务，`PgUp/PgDn` 滚动消息。使用 `kn -cautious` 启动时，执行命令或写文件前会请求确认。

<details>
<summary>Markdown 提示词模板</summary>

全局模板放在 `~/.k-brain/prompts/`，项目模板放在受信任项目的 `.k-brain/prompts/`；仅扫描当前目录，项目同名模板覆盖全局模板，内置命令优先。

例如 `audit.md`：

```markdown
---
description: 审查指定模块
argument-hint: "<模块> [关注点]"
---
审查 $1，重点关注 ${2:-正确性与测试覆盖}。
补充要求：${@:3}
```

执行 `/prompts refresh` 后输入 `/audit "internal/agent" "并发控制"`。模板先展开到输入框，再按 Enter 发送。

支持 `$1`、`$2`、`$@`、`$ARGUMENTS`、`${1:-默认值}`、`${@:-默认值}`、`${ARGUMENTS:-默认值}`、`${@:N}`、`${@:N:L}`；参数支持单双引号，不执行 shell 或环境变量替换。元数据仅支持简单单行字段，不是完整 YAML。单文件上限 1 MiB。

</details>

### 界面语言

输入 `/language` 打开选择器，↑/↓ 选择、Enter 应用、Esc 取消；也可以直接执行 `/language zh_cn`、`/language zh_Hant` 或 `/language en`。设置保存到 `~/.k-brain/config.json` 的 `language` 字段，默认英文，未知值回退为英文。

`/export` 根据扩展名生成 Markdown（`.md`）、结构化 JSONL（`.jsonl`）或 HTML（`.html`）文件；`/import <path>` 可将 JSONL 消息追加到当前会话。`/forks` 显示当前会话及其分支关系。

命令菜单说明、帮助、输入提示、主要导航标签及语言/模型选择器操作提示即时切换。命令名称、模型与提供商标识、对话内容和工具输出保持原样。此设置不控制模型回复语言；部分详细诊断与次级面板仍使用英文。

### 外部提示词编辑器

按 `Ctrl+G` 编辑当前草稿，或用 `/editor` 在外部编辑器中新建提示词。依次读取 `VISUAL`、`EDITOR`，默认 Windows 使用 `notepad.exe`，Linux/macOS 使用 `vi`。支持带引号的路径和参数（如 `code --wait`），不展开 shell 表达式。保存并关闭编辑器后回填输入框，再按 Enter 发送；任务执行中不可打开。编辑失败或内容超限时保留临时文件用于恢复。

### MCP 服务

在 `~/.k-brain/config.json` 的 `mcp` 对象中配置服务，或使用 `kn mcp add <name> -- <command...>`、`kn mcp add <name> --url <url>` 添加。通过 `kn mcp list`、`kn mcp test <name>`、`kn mcp remove <name>` 管理服务，TUI 内使用 `/mcp` 管理连接。不自动发现或导入 Claude/Codex 的外部配置；ACP 客户端仍可显式传入当前会话的服务。

旧 `mcpImport` 字段和 `kn mcp import` 命令已删除。如果已有配置含 `mcpImport`，请删去该字段；未知配置字段会被拒绝。

### 无界面运行与后端集成

```sh
kn run "解释这个项目的结构"
git diff | kn run "审查这些变更"
kn run --format json --quiet --max-turns 8 --timeout 5m "定位并修复构建失败"
```

`--format json` 输出逐行 JSON 事件流（NDJSON），不是单个 JSON 文档。

每个 `tool_start`、`tool_output` 和 `tool_end` 事件都包含服务商调用 `id`，因此可以关联并行调用。`tool_output` 是该调用截至当前时刻的累计输出快照。事件流会串行写出；如果标准输出管道断开，运行会取消并返回非零错误，不会错误地输出 `done`。

`kn run` 与 TUI 一样按项目目录保存会话；`--no-session` 关闭持久化，不能与 `--resume` 同用。存储失败会明确报错，不再静默新建对话或忽略保存。运行超时也会取消管道输入等待、凭据命令、模型目录请求和会话启动钩子。

ACP 已支持 `session/new`、`session/prompt`、`session/cancel`、`session/close`、`session/list`、`session/load` 和 `session/resume`。编辑器断开连接、显式调用 `session/cancel` 或后端请求到达截止时间时，会取消模型请求和工具。忙碌会话拒绝第二条提示，不中断当前回合；权限按会话隔离。

通过 `session/set_mode` 选择 `auto`、`ask` 或 `plan`。Plan 仅开放读取文件、提问和待办计划工具，阻止执行命令、修改文件及调用外部工具。模式切换会影响当前回合后续的工具检查，不会撤销已经执行的操作；切换到 Plan 后，尚未完成的权限确认也不能放行新的写入。新建和恢复的会话从 Auto 开始。

`session/load` 回放已保存的对话，`session/resume` 恢复对话但不回放。两者都保留用于记忆和提示缓存的会话 ID，并拒绝重复激活正在运行或加载的会话。关闭会话会等待当前回合及持久化结束，再释放 MCP 连接。存储失败会明确报错，模型请求同时失败时也会保留两项错误。

全局记忆位于 `~/.k-brain/memory.md`，会话记忆与对话记录一起存放在 `~/.k-brain/sessions/<project>/<session-id>/memory.md`，三个入口均在每轮请求前刷新。删除会话也会删除其记忆，不导入旧的平铺 `<session-id>.memory.md` 文件。子代理保持独立上下文，不注入全局或父会话记忆。ACP 按请求中的工作目录发现项目技能。

TUI、ACP 与 `kn run` 共用会话历史映射，摘要和对话记录一起写入。不同入口交替续聊时保留最新摘要和消息，不覆盖原始对话。压缩完成后，即使后续模型请求被取消，已完成的摘要仍会保存；达到 `--max-turns` 后产生的最终回答也会保存到下一轮上下文。

三个入口会将历史和累计用量一起保存，保留模型与服务商归属、子代理用量及缓存读写计数。独立压缩模型单独记录用量；Anthropic 输入计数统一为包含缓存读写的总量。TUI 使用记录中对应服务商的目录价格估算费用，缺失或无法确定价格时显示未知。

状态栏、压缩日志和回退列表共用普通输入、输出、缓存读取及缓存写入四项计价。模型目录的 `pricing` 对象支持 `prompt`、`completion`、`input_cache_read` 和 `input_cache_write`，值为十进制字符串，单位是**美元／token**。普通输入和输出单价必须同时提供；未提供的缓存单价按输入价格估算，显式 `"0"` 表示免费。价格无效或用量不一致时费用保持未知，回合缺少价格时也不会显示部分总价。目前采用固定单价，尚未计入按上下文长度分档、单独的一小时缓存写入费率和标题生成费用。

Chat Completions、Responses 和 Anthropic Messages 共用 SSE 分帧，遇到格式错误、服务商错误或缺少结束信号的断流会明确报错。失败请求保留已收到的用量，不执行待处理的工具调用。Chat 接受 `[DONE]`，或结束原因后正常关闭连接；Responses 和 Anthropic 收到终止事件后立即返回。Chat 收到生成内容后不再自动重试，即使调用方没有注册流式回调。

流式回复达到输出上限时，氪脑保留部分正文和用量、显示截断提示，并丢弃该响应中的工具调用。Responses 的 `incomplete` 原因为 `max_output_tokens` 时也按此处理，其他不完整原因仍报错。会话消息保存统一的 `stop_reason` 和服务商原始的 `raw_stop_reason`，再次请求 API 时不发送这些本地字段。ACP 返回 `max_tokens`，`kn run --format json` 的 `done` 事件包含 `stopReason: "length"`；发送后续消息即可继续。截断回复中的完成标记不会结束 TUI 持续目标。非流式请求返回部分文本和 `OutputLimitError`，因此被截断的压缩摘要不会替换原历史。

三种协议均支持成功响应前的临时 HTTP 故障（408、409、429、5xx）与连接故障重试，遵循 `x-should-retry`、`retry-after-ms` 和 `Retry-After`。服务端要求等待超过 60 秒时立即返回错误，等待期间可取消。`config.json` 的 `maxRetries` 表示包含首次请求的最大尝试数：`1` 禁止重试，`0` 或省略时默认尝试 8 次。Responses 和 Anthropic 不会重新启动已接受的响应流；Chat 收到任意用量块（包括仅缓存用量）后也不再重试。

TUI 中的 `/compact retry` 可撤销最近一次压缩；`/rewind` 和 `/fork` 通过存储消息序号定位，即使多次压缩后也能找到正确位置。分叉保留对应的摘要和工作区快照引用，回退时会保留其他会话仍在使用的快照。新建 TUI 会话会在首轮模型请求前设置会话缓存键。

`session/list` 每页最多返回 100 条会话，将返回的 `nextCursor` 作为下一次请求的 `cursor`，并保持相同的 `cwd` 过滤条件。先按项目过滤，再分页；不列出归档会话、空会话或子代理记录。结果按更新时间倒序排列，同一时间按会话 ID 排序。浏览期间新增或更新的会话需从第一页刷新查看。

子代理默认继承当前模型；可在 `config.json` 中设置 `taskModel`，并用 `taskProvider` 指定服务商。未设置 `taskModel` 时不会额外请求模型目录；TUI 中选择 **Subagent model → current model** 可清除覆盖设置。

子代理创建时复制父代理的执行工具集，包括自定义替换、MCP 和插件工具，不会恢复已移除的工具。绑定父会话的提问、任务编排、待办和记忆工具不会继承；Go 工具集成也可用 `NoInherit` 排除指定工具。计划模式切换仍会同步作用于已有子代理。

| 入口 | 用途 |
| --- | --- |
| `kn acp` | 通过标准输入/输出接入 ACP 编辑器 |
| `kn mcp serve` | 向其他客户端提供内置文件和命令工具 |
| `kn sessions` | 列出已保存会话 |
| `kn update` | 更新已安装版本 |

执行内核与 TUI 分离，可复用模型适配、工具循环、上下文压缩、后台子任务和 JSONL 会话文件。当前后端入口是 CLI、ACP 与 MCP，不是独立 HTTP 服务，也不包含 Desktop 前端。

### 平台说明

- **Windows shell**：优先 PowerShell 7，回退 Windows PowerShell。`K_BRAIN_SHELL` 可指定 `pwsh`、`powershell`、`bash`、`wsl`、`cmd` 或程序路径；原生 Windows 不支持 Unix PTY 式交互转发。
- **Computer-use**：Windows 使用 PowerShell/UIA；macOS 使用 Python/PyObjC；Linux 使用 Python/AT-SPI2。Linux X11 已通过 GTK/Xvfb 集成验证；macOS 尚待真机验证，Wayland 通用桌面输入 portal 尚未接入。运行依赖见下方说明。

Computer-use 运行依赖：Windows 使用系统 PowerShell；macOS 需为终端/解释器授予辅助功能和屏幕录制权限；Linux 需在具有 DISPLAY 与 D-Bus 的图形会话中运行。`K_BRAIN_COMPUTER_BIN` 可覆盖 helper 路径。

```sh
# macOS
python3 -m venv ~/.k-brain/computer-venv
~/.k-brain/computer-venv/bin/python -m pip install pyobjc-framework-Cocoa pyobjc-framework-Quartz pyobjc-framework-ApplicationServices
export K_BRAIN_COMPUTER_PYTHON="$HOME/.k-brain/computer-venv/bin/python"

# Debian / Ubuntu (X11)
sudo apt-get install python3-pyatspi python3-pil python3-pil.imagetk xdotool
export K_BRAIN_COMPUTER_PYTHON=/usr/bin/python3
```

## 源码构建

需要 Git 和 **Go 1.27+**，版本要求见 [`go.mod`](go.mod)。

```sh
git clone https://github.com/Stack-Cairn/K-brain.git
cd K-brain
go install ./cmd/kn ./cmd/k-brain-computer
```

确认 Go 的安装目录（通常为 `~/go/bin`）已加入 PATH。也可以在仓库中直接运行或构建：

```sh
go run ./cmd/kn
go build -o kn ./cmd/kn
```

Windows 使用 `go build -o kn.exe ./cmd/kn`，随后运行 `.\kn.exe`。需要 Computer-use 时另行构建或安装 `cmd/k-brain-computer`。

## 文档

- [构建和发布工作流](.github/workflows/build.yml)
- 命令行帮助：`kn --help`、`kn run --help`；TUI 帮助：`/help`

## 项目结构

| 路径 | 职责 |
| --- | --- |
| `cmd/kn` | 主程序，TUI、无界面运行及协议入口 |
| `cmd/k-brain-computer` | Computer-use RPC helper |
| `internal/agent` | 模型与工具执行循环、子任务协调 |
| `internal/ai` · `internal/routing` | 协议适配、流式响应、模型路由 |
| `internal/config` | 配置、模型目录和本地设置 |
| `internal/tools` | Shell、文件读写与编辑工具 |
| `internal/prompts` · `internal/skills` | 提示词、模板和技能加载 |
| `internal/session` · `internal/memory` | 会话持久化与长期记忆 |
| `internal/mcp` · `internal/acp` · `internal/lsp` | 工具、编辑器及语言服务协议 |
| `internal/browser` · `internal/computer` | 浏览器与桌面自动化 |
| `internal/tui` | 终端布局、输入和事件展示 |

## 开发

优先运行改动模块的测试，再执行更广范围的检查：

```sh
go test ./internal/computer/... -count=1
go vet ./internal/computer/...
go test ./...
```

部分集成测试需要平台工具、图形会话或显式启用环境变量，不能将编译通过视为完整测试通过。开发任务见 [`Taskfile.yaml`](Taskfile.yaml)。

GitHub Actions 的发布构建**仅在推送 `v*` tag 时触发**，交叉编译 Windows、Linux、macOS 的 x64 / ARM64 六种组合，生成校验和并发布资源；普通分支推送不触发该工作流。

## 参考与致谢

氪脑参考了 [context-labs/whip](https://github.com/context-labs/whip/) 的架构设计与实现思路，同时借鉴 [OpenAI Codex](https://github.com/openai/codex)、[Grok Build](https://x.ai/cli)、[Pi](https://github.com/badlogic/pi-mono) 和 [Claude Code](https://github.com/anthropics/claude-code) 的终端交互、执行内核及模块组织。README 的组织方式参考 Codex 与 Grok Build。

本项目独立维护，不代表上述项目或其开发者。
## 社区

[LINUX DO](https://linux.do)

## 许可证

[Apache License 2.0](LICENSE)。
