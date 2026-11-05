# Skills、Plugins 与 Subagent

## Skills

Skills 是可复用的 Markdown 指令和资源。把 skill 放入项目约定的 skills 目录后，K-brain 会在匹配任务时加载；用 `/prompts` 查看已发现的提示模板。

## Plugins

Plugins 为本地扩展提供工具或命令。`/plugins list` 查看插件，`/plugins enable NAME`、`/plugins disable NAME` 切换，`/plugins reload` 重新加载。插件配置和脚本只在本机执行。

## Subagent

`/subagent [-m model] <prompt>` 启动后台子代理，`/subagents`（或 `Ctrl+T`）显示运行状态、日志和结果。前台子代理（模型调用的 `subagent` 工具，未设 `background`）同样出现在面板中，可以打开只读的实时记录；面板中的 `Ctrl+X` 可停止后台子代理。按键详见 [TUI 与快捷键](03-tui.md)。子代理可以使用独立模型；`worktreeSubagents` 可让实现使用 Git worktree 隔离。

主模型还可以直接调用以下工具（在 K-brain 后端执行，CLI 与 LiveAgent 共用）：

| 工具 | 用途 |
| --- | --- |
| `subagent` | 前台委派，或用 `background: true` 后台执行 |
| `subagent_steer` | 给运行中的子代理补充指导，在后续循环边界处理 |
| `subagent_status` | 不传 `id` 获取任务摘要，传 `id` 查看原始任务报告和是否可续聊 |
| `subagent_cancel` | 请求取消任务或当前续聊；返回不代表进程清理已经完成 |
| `subagent_followup` | 用 `id`、`message` 继续已完成的子代理，复用原对话、模型和工作目录，等待新报告 |

这些控制工具只能操作当前父代理持有的任务，不会被子代理继承。后台完成报告仍自动回传，无需反复查询。

指导和续聊消息最多 64 KiB；每个子代理最多排队 32 条尚未处理的指导。查询列表最多返回 100 条、按 ID 排序，单条描述最多约 256 字节；`total` 和 `truncated` 表示列表是否完整，已知 ID 可单独查询。自动回传及工具返回的报告使用现有约 50 KB 截断规则，完整子代理对话仍按原机制保存。

续聊保留原任务 ID 和原始完成报告；新回复通过工具返回，并写入子代理对话。只有仍在内存中持有执行上下文的子代理可以续聊。重启、闲置回收或清理任务后，不保证上下文仍可续用；只有历史摘要时会明确报错，不会创建一个冒充原任务的新对话。
