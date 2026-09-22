# Skills、Plugins 与 Subagent

## Skills

Skills 是可复用的 Markdown 指令和资源。把 skill 放入项目约定的 skills 目录后，K-brain 会在匹配任务时加载；用 `/prompts` 查看已发现的提示模板。

## Plugins

Plugins 为本地扩展提供工具或命令。`/plugins list` 查看插件，`/plugins enable NAME`、`/plugins disable NAME` 切换，`/plugins reload` 重新加载。插件配置和脚本只在本机执行。

## Subagent

`/subagent [-m model] <prompt>` 启动后台子代理，`/subagents`（或 `Ctrl+T`）显示运行状态、日志和结果。子代理可以使用独立模型；`worktreeSubagents` 可让实现使用 Git worktree 隔离。
