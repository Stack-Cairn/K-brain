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

安装脚本从 [GitHub Releases](https://github.com/Stack-Cairn/K-brain/releases) 下载对应平台的二进制，并校验 SHA-256。Windows 默认安装到 `%LOCALAPPDATA%\Programs\k-brain`，安装后打开新终端使 PATH 生效。若尚无适合的平台发布包，可[从源码构建](#源码构建)。

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

### 会话文件

会话保存在 `~/.k-brain/sessions/<project-id>/<session-id>/session.jsonl`；设置 `K_BRAIN_HOME` 时位于该目录下的 `sessions`。项目 ID 是工作区路径的稳定哈希，会话按项目分组，同时保留完整 session ID。

```text
~/.k-brain/
  config.json
  brain.md
  sessions/<project-id>/<session-id>/session.jsonl
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

### 无界面运行与后端集成

```sh
kn run "解释这个项目的结构"
git diff | kn run "审查这些变更"
kn run --format json --quiet --max-turns 8 --timeout 5m "定位并修复构建失败"
```

`--format json` 输出逐行 JSON 事件流（NDJSON），不是单个 JSON 文档。

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
