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

## LiveAgent 桌面工具（ReadTerminal、TunnelManager）

在 LiveAgent 桌面里使用 K-brain 时，桌面连上后端后会登记一个只在本机 loopback 上监听的宿主桥，模型因此多出两个工具，名字、参数和返回文本与 LiveAgent v1 相同：

| 工具 | 用途 | 默认权限 |
| --- | --- | --- |
| `ReadTerminal` | 读取当前项目在桌面里打开的终端的最近输出（只读，不能输入）；传 `session_id` 时读取本会话 `TerminalSession` 的输出 | 按只读放行，不询问 |
| `TunnelManager` | 通过 Gateway 列出（`list`）、新建（`create`）、关闭（`close`）、探测（`check`）临时的远程 HTTP/WebSocket/SSE 隧道 | `list`/`check` 放行；`create`/`close` 询问 |

- 桌面没有连接时运行里没有这两个工具（`ReadTerminal` 回到只读本会话终端的版本）；桌面在会话打开之后才连上，下一次发送消息时生效。运行中途桌面断开，调用返回 `LiveAgent desktop is not connected`。
- 每次调用最多等桌面 10 秒，停止运行会立即取消。
- 权限规则照常适用：例如 deny 规则 `ReadTerminal` 禁止读取终端，allow 规则 `TunnelManager(create:*)` 让新建隧道不再询问。定时任务等无人值守的运行无法弹出审批，`create`/`close` 必须事先有这样的 allow 规则（或以 `full_access` 运行），否则立即失败。
- 宿主桥的地址和令牌只保存在后端内存里，不写盘、不进日志和会话历史，也不会传给命令的环境变量。沙箱里的命令仍可以连到本机端口，但没有令牌，无法调用宿主桥。

协议细节见 [LiveAgent 后端协议](../liveagent-backend.md) 的"桌面宿主桥"一节。

## SSH 主机（SSHManager）

在 LiveAgent 的 Settings > SSH 里添加主机，并把主机关联到项目后，这个项目里的对话会多出 `SSHManager` 工具：模型可以在关联的主机上执行命令、使用交互式 shell，以及通过 SFTP 列目录、读写文件、上传下载。连接由 K-brain 后端自己建立，不经过桌面。

- 只能使用当前项目关联的主机；没有关联主机的项目里没有这个工具。关联改动在下一次发送消息时生效。
- 主机保存在 K-brain 配置目录下的 `ssh/hosts.json`，密码、私钥和口令只写不读：设置页只能看到"已保存"，读不回原值。K-brain 不读取 `~/.ssh/config`、`~/.ssh/known_hosts` 或 ssh-agent；私钥可以直接粘贴，也可以填 `privateKeyPath`。
- 第一次连接某台主机时，会弹出提问卡片显示主机密钥的 SHA256 指纹：`Trust once` 只信任这一次，`Trust and save` 记入 K-brain 自己的 known_hosts（同一目录下的 `ssh/known_hosts`），`Reject` 不连接。已保存的主机密钥变了时一律拒绝连接；确认服务器确实更换了密钥后，在设置里删除旧密钥再重新连接。
- 没有保存密码、加密私钥没有保存口令，或服务器要求一次性验证码等 keyboard-interactive 登录时，会弹出登录表单。表单里输入的内容只用于这一次连接，不保存，也不写进会话历史和日志。不要把密码直接发在对话里。
- 查看主机列表、会话列表、shell 输出和传输进度不询问；执行命令、输入、SFTP 读写和传输等默认像 Bash 一样询问。可以添加 allow 规则，例如 `SSHManager(exec web:*)` 让在 `web` 上执行命令不再询问；也可以用 deny 规则禁止某类操作，例如 `SSHManager(sftp_delete:*)`。
- 定时任务等无人值守的运行不会弹窗：需要审批的操作必须事先有 allow 规则（或以 `full_access` 运行），主机密钥和密码必须已经保存，否则立即失败。
- 会话在后端里保持，同一项目的后续对话可以继续使用；空闲 30 分钟、断线、修改或删除主机、后端退出时关闭，不会自动重连。上传下载的本地路径必须在工作区内。

协议细节见 [LiveAgent 后端协议](../liveagent-backend.md) 的"SSH 主机与 SSHManager"一节。
