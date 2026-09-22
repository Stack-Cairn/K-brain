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
| `/subagent <prompt>` | 启动后台 subagent |
| `/subagents` | 查看 subagent 面板 |
| `/privacy on\|off\|toggle` | 开关本地隐私网关 |
| `/ancient on\|off\|toggle` | 开关古人竖排输出模式 |
| `/export [path]` | 导出 Markdown、JSONL 或 HTML |
| `/diff [--staged\|--stat]` | 查看 Git 变更 |

命令参数可以通过 `/` 补全。`!command` 执行 shell 并把结果发送给模型，`!!command` 只执行命令而不发送结果。
