# TUI 与快捷键

运行 `kn` 进入终端界面。输入框固定在底部，顶部显示模型、工作目录和当前模式，右下角显示 session title。

常用快捷键：

| 快捷键 | 作用 |
|---|---|
| `Enter` | 发送消息 |
| `Ctrl+J` / `Shift+Enter` | 在输入框换行 |
| `Tab` | 补全斜杠命令 |
| `Shift+Tab` | 切换 Normal、Plan、Always allow 权限模式 |
| `Esc` | 中断当前运行 |
| `Esc Esc`（空闲时） | 打开 rewind |
| `Ctrl+B`（shell 命令运行中） | 把正在运行的 shell 命令移到后台，见下文 |
| `Ctrl+T` | 聚焦 subagent 面板（再按一次回到输入框） |
| `Ctrl+G` | 用外部编辑器编辑草稿 |
| `Ctrl+V` | 粘贴图片 |
| `Ctrl+K` | 清空当前会话 |
| `Ctrl+C` 两次 | 退出 |

`/theme light|dark|auto` 切换配色，`/language zh_cn|zh_Hant|en` 切换界面语言。

### 把命令移到后台（Ctrl+B）

`bash` 调用运行时，工具行右侧显示 `ctrl+b 后台运行`（英文界面为 `ctrl+b to background`）。此时按 `Ctrl+B`：

- 命令不会被结束或重启：进程、沙箱和输出都保持原样，输出继续写进日志。
- 这次工具调用立即返回，结果告诉模型：用户把命令移到了后台、它的 `process_id` 和日志路径、到此为止的输出末尾，以及之后用 `ManagedProcess`（`wait`、`read_log`、`status`、`stop`）读取或停止它。这个工具在第一次移到后台后才出现在模型的工具列表里，只能查看和停止，不能启动进程。
- 命令结束时，会话会收到一条通知（带退出码和最后的输出）；空闲时这条通知会启动新的一轮。
- 移到后台之后按 `Esc` 中断本轮不会结束这个命令。`/clear`、`/resume` 切换会话和退出 `kn` 会停止它。
- 同时有几个可移到后台的调用时，`Ctrl+B` 一次移走全部（实际上 `bash` 调用彼此串行，同一时刻最多一个）。子代理里的命令不受影响。
- 没有可移到后台的命令时（包括命令已经结束、或刚按过一次），`Ctrl+B` 仍是输入框里的光标左移。交互式（`interactive: true`）命令不能移到后台。

`Ctrl+B` 是固定键位，目前没有自定义快捷键的配置。它也是 tmux 的默认前缀键：在 tmux 里要连按两次 `Ctrl+B`（tmux 把第二次按键原样发给 `kn`），或者改用别的 tmux 前缀。

## Subagent 面板与实时视图

前台和后台 subagent 都会出现在输入框下方的面板里：运行中的排在前面，已结束的随后，各组内新的在前。底部状态栏的 `⚙ N sub` 统计正在运行的 subagent 数量（含前台）。

对话记录中，每个 subagent 调用下方有一行实时状态：

```
● Subagent(Survey the loader)
  ⎿ ◌ running · 2 tools · Grep(LoadConfig) · 12s · 3.4k tok · Ctrl+T view
```

后台任务的状态行会显示任务 ID，结束后变成 `⎿ ✓ <id> done · 5 tools · 43s · 12.3k tok`。后台完成通知以状态行（`◆ subagent <id> done · …`，报告默认折叠，`Ctrl+E` 展开）显示，不会显示成用户输入；它以后台消息身份发给模型。

| 按键（面板聚焦时） | 作用 |
|---|---|
| `↑` / `↓` | 选择 subagent；在首行按 `↑` 回到输入框 |
| `Space` | 在面板内预览最近输出 |
| `Enter` | 打开该 subagent 的实时记录视图 |
| `Ctrl+X` | 停止选中的后台 subagent（前台 subagent 用 `Esc` 中断整个回合） |
| `Ctrl+T` | 回到输入框 |

| 按键（subagent 视图中） | 作用 |
|---|---|
| `Enter` | 运行中：补充指令（steer）；已结束：续聊（follow-up）。前台 subagent 只读 |
| `Ctrl+X` | 停止该 subagent 或当前续聊 |
| `PgUp` / `PgDn`、`↑` / `↓` | 滚动；`Ctrl+Home` / `Ctrl+End` 跳到开头 / 最新 |
| `Esc` / `Ctrl+T` | 返回主界面，焦点留在面板 |
