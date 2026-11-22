# 斜杠命令

在输入框输入 `/help` 查看当前版本完整列表。常用命令如下：

| 命令 | 作用 |
|---|---|
| `/system` | 编辑 `system.md` |
| `/brain` | 编辑用户级 `brain.md` |
| `/model <name>` | 切换模型；`/model refresh` 从 `<baseUrl>/v1/models` 刷新 |
| `/permissions normal\|plan\|always` | 设置执行权限模式 |
| `/compact [model\|fresh\|off\|retry\|log]` | 压缩上下文 |
| `/rewind` / `/undo` | 浏览并回退到之前的轮次 |
| `/new` / `/clear` | 新建或清空会话 |
| `/resume [id]` | 恢复会话 |
| `/rename [title]` | 修改 session title |
| `/goal <text>` | 设定会话目标，模型会持续多轮工作直到完成；`/goal clear\|resume\|rounds [n]\|verify [on\|off]` |
| `/subagent <prompt>` | 启动后台 subagent |
| `/subagents` | 查看 subagent 面板 |
| `/privacy on\|off\|toggle` | 开关本地隐私网关 |
| `/ancient on\|off\|toggle` | 开关古人竖排输出模式 |
| `/vim [on\|off]` | 开关输入框的 vim 模式（默认关闭，见“快捷键”一章） |
| `/export [path]` | 导出 Markdown、JSONL 或 HTML |
| `/diff [--staged\|--stat]` | 查看 Git 变更 |

命令参数可以通过 `/` 补全。`!command` 执行 shell 并把结果发送给模型，`!!command` 只执行命令而不发送结果。

## /goal 独立校验

`/goal <text>` 设定目标后，模型每轮结束若未完成就自动进入下一轮，直到回复以 `GOAL_MET` 开头、需要用户输入（`GOAL_NEEDS_INPUT`）或达到轮数上限（`/goal rounds`，默认 100）。

模型声明 `GOAL_MET` 时，kn 默认会用当前会话模型发起一次隐藏的校验请求（不会写入可见对话）：请求只包含目标文本、最近工作的截断摘录（最近 40 条消息、约 24KB，含用户消息、助手回复和工具结果）以及这次声明本身，不提供任何工具，并要求模型只回答 `{"met": true|false, "reason": "..."}`。

- 判定已完成：目标结束，并显示校验理由。
- 判定未完成：目标继续，校验理由以 `[goal verifier]` 标注写入下一轮提示，这一轮计入轮数上限。
- 校验失败（请求出错、超时 2 分钟或回复无法解析）：按原有行为接受声明，并显示一条简短提示，不会卡住。
- 同一目标被驳回 3 次后（`goalVerifyMaxRejections` 可调），下一次声明直接接受并给出警告，避免校验器始终不同意导致目标无法结束。

`/goal verify off` 关闭校验（写入全局配置 `"goalVerify": false`），`/goal verify on` 恢复默认，`/goal verify` 查看当前状态。桌面端后台运行的目标同样遵循这些配置，校验结果出现在目标状态的 `verification` 字段中。
