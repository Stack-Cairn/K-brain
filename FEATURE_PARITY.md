# 氪脑功能对齐记录

核对日期：2026-09-18。本记录是对本地源码的首轮核对与实现范围，不是四个上游项目全部功能的兼容性声明，也不是对所有协议、插件和 UI 的逐项认证。

## 参考快照与入口

| 项目 | 本地快照 | 本轮核对入口 |
| --- | --- | --- |
| Codex | `7abf2a3b5` | `codex-rs/tui/src/slash_command.rs`、`codex-rs/app-server/`、`codex-rs/windows-sandbox-service/` |
| Grok Build | `b69f802e` | `crates/codegen/xai-grok-pager/src/slash/registry.rs`、`slash/commands/copy.rs`、`slash/commands/` |
| Pi | `eed5263cd` | `packages/coding-agent/README.md`、`docs/prompt-templates.md`、`src/core/session-manager.ts`、`src/core/extensions/` |
| Claude Code | `d239e3f` | `src/commands/`、`src/commands/copy/index.ts`、`src/schemas/hooks.ts`、`src/keybindings/`、`src/vim/` |

仅参考能力和交互，新增实现为 Go。项目仍为氪脑 / K-brain，入口为 `kn`，配置目录仍为 `~/.k-brain`。

## 本轮已补齐

| 能力 | 实现 | 范围与差异 |
| --- | --- | --- |
| Pi 风格 Markdown 提示词模板 | `internal/prompts/templates`、`internal/tui/prompt_templates.go` | 全局/可信项目加载、项目覆盖、参数替换、补全、刷新；展开到编辑框后确认发送。不包含 npm/git 包安装、完整 YAML、CLI 显式模板路径或资源热加载体系 |
| 最近 / 第 N 条助手消息复制 | `internal/tui/copy.go` | Windows/macOS/Linux 剪贴板适配、显式另存新文件、错误反馈；不含回复分块选择器。文本选择复制复用同一系统剪贴板写入函数 |
| `!!` 本地 shell | `internal/tui/shell.go` | 复用现有跨平台执行器，结果只显示，不追加消息或 steer；`!` 原语义不变 |
| `/diff` 本地差异 | `internal/tui/diff.go` | 工作区 / 暂存区 / 统计，禁用外部 diff/textconv，限时和输出上限；不含未跟踪文件，也不等同于模型 `/review` |

## 已有能力：保留，不重复实现

| 能力 | 本地依据 | 说明 |
| --- | --- | --- |
| 第三方 API、模型目录与上下文配置 | `internal/ai`、`internal/config`、`internal/routing` | 已有 Chat Completions / Responses / Anthropic Messages 适配，不代表上游全部传输功能相同 |
| 压缩、缓存用量与上下文诊断 | `internal/ai`、`internal/agent`、`internal/tui/context_doctor.go` | 已有基础实现；不同服务端缓存行为仍由提供商决定 |
| 会话持久化、恢复、fork、rewind | `internal/session`、`internal/tui/fork.go`、`internal/tui/rewind.go` | 包含工作区快照恢复路径，但不是 Pi 的 parentId 会话树协议 |
| 子任务、steer、队列、隔离 worktree | `internal/agent/subagent.go`、`internal/agent/worktree.go`、`internal/tui/tui.go` | 子任务隔离不等于主会话 `/worktree` 管理 UI |
| MCP、Skills、LSP、浏览器、ACP | 对应 `internal/` 模块 | 保留现有能力，不重新引入已要求删除的导入与登录功能 |
| 计划、持续目标、定时任务、工作流 | `internal/agent/todo.go`、`internal/scheduler`、`internal/workflow` | 模型管理的计划不等同于独立只读计划模式；JS 工作流不等同于任意插件体系 |

## 待补齐 / 部分实现

下列项目需要独立设计、实现和测试；本轮未将它们标记为完成。

| 优先级 | 能力 | 当前差距 |
| --- | --- | --- |
| P1 | 生命周期 Hooks | 没有与上游相当的可配置 SessionStart / UserPromptSubmit / PreToolUse / PostToolUse / Stop 事件执行框架；现有 Agent 回调不等同于用户 Hooks |
| P1 | 面向外部前端的稳定后端服务 | 已有 ACP/MCP/JSON CLI，但没有完整对齐 Pi RPC / Codex app-server 的会话 CRUD、订阅、取消和服务版本合同；LiveAgent 接入需要明确适配层 |
| P1 | 会话树与结构化导入导出 | 已有线性会话、fork、rewind 和 Markdown 导出；尚未对齐 Pi parentId 树导航、JSONL 导入/导出和 HTML 导出 |
| P1 | 独立计划与审查模式 | 已有 plan/todo，但未对齐只读计划模式、结构化代码审查及独立 `/review` 流程 |
| P1 | 持久化权限策略与系统隔离 | 已有交互权限门和规则保存，但不等同于 Codex 的 OS sandbox / execpolicy / 网络隔离 |
| P2 | 插件/扩展生命周期 | 无 Pi/Claude 等价的扩展注册、安装、卸载、事件订阅与自定义工具 UI；现有工作流保持独立 |
| P2 | 编辑器能力 | 未对齐可配置键位、Vim 模式、外部编辑器编辑当前提示词；`/me` 编辑常驻指令不是提示词编辑器 |
| P2 | 独立旁路提问 | 未对齐 `/btw` 临时分支问答及结果处理；不能简单冒充现有 subagent |
| P2 | 会话与导航 UI | 尚未对齐归档/删除/标签、主会话 worktree 管理、时间线、全文搜索 |

## 明确不恢复的范围

- 官方登录、OAuth、订阅认证、特定提供商硬编码、OpenRouter 专用接入。
- desktop 前端、旧 uiMode、自带旧版配置兼容体系。
- 自动导入其他工具 MCP 配置的入口。
- 上游账号用量、云共享、托管远程服务等依赖专有基础设施的功能。

本记录不声称已清理历史遗留的所有符号或配置字段。既有能力描述基于代码检查，而非本轮全部重新端到端验证。

## 本轮验证

- 新模板模块单元测试通过，语句覆盖率 92.2%。
- TUI 定向测试通过：模板发现/信任/覆盖/刷新/忙碌输入、复制选取与新文件写入、`!!` 上下文隔离、Git 差异与输出限制，以及相关补全/帮助/队列/输入回归。
- `go vet ./internal/prompts/templates ./internal/tui` 通过。
- `go test ./... -run '^$' -count=1` 通过，仅代表全项目测试代码编译成功。
- `CGO_ENABLED=0` 下 `cmd/kn` 的 Windows / Linux / macOS × amd64 / arm64 六平台构建通过。
- 未运行完整回归套件，未宣称六平台 TUI 和系统剪贴板均已交互验证；macOS/Linux 构建未在对应系统运行。
