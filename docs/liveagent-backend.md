# K-brain 与 LiveAgent 的会话协议

## 边界

K-brain 提供 HTTP JSON 与 Server-Sent Events（SSE）接口。协议版本为 `kbrain.agent.v1`。供应商原始请求与流格式由 `internal/ai` 适配；LiveAgent 消费 `internal/protocol` 定义的消息与事件。

LiveAgent 使用 K-brain 作为唯一模型与 Agent 执行后端。桌面安装包携带版本配套的 K-brain 二进制，由 Tauri 宿主启动、等待健康检查并将动态 loopback 地址和进程令牌交给前端；启动失败展示错误及重试入口。聊天模型目录、会话列表与历史读取来自后端；本地 conversation ID 与后端 session ID 双向映射，并使用稳定存储作用域跨动态端口恢复。重开会话会恢复消息和子代理报告。

历史的重命名、删除、置顶、分支、编辑重发、分享和分页通过后端接口持久化。编辑和分支使用后端消息 ID 与历史 revision；成功运行后前端重新读取权威历史。辅助文本生成和自动标题通过后端 `/v1/text/generate` 使用同一版本化规范消息模型，后端强制无工具并持有供应商凭据。模型设置页面通过 `/v1/settings` 管理供应商、模型和密钥；模型目录由后端配置维护，前端不执行供应商 discovery。

供应商里的生图模型直接出现在图像工作台（`/v1/image/targets`），不需要验证。每个模型的 `imageAdapter` 是用户选择：省略或空串表示自动识别（模型名带 `image`、声明输出图片或在已知名单里），`"enabled"` 表示是，`"disabled"` 表示否，也可以写具体协议名。读取设置时每个模型带只读的 `imageModel`，给出生效结果；写入时可以原样带回，后端忽略它。`/v1/health` 的 `capabilities.image_model_toggle` 表示支持这个开关。后端启动时会删掉没有目标的 `provider_ref` 图像连接。

工具执行、技能扫描、记忆和提示词由后端负责。LiveAgent 保留输入、流式展示、审批及窗口/文件选择等原生宿主交互。手动压缩、检查点 preview/rewind 和轨迹已有后端接口及客户端适配；大文件、目录恢复和跨宿主等语义仍按兼容矩阵逐项验收。Gateway 通过 `/ws/v2` 转发聊天、队列及已接管的资源请求；历史恢复、终端、SSH 和文件浏览需要分别核对后端身份、原生宿主边界和实际 UI 链路。功能对齐目标保持原入口和既有行为，当前完成范围见 [兼容矩阵](liveagent-compatibility-matrix.md)，开发设计见 [aidocs](aidocs/README.md)。

## 启动

桌面应用自动管理后端，无需设置 `VITE_KBRAIN_BACKEND`。浏览器开发环境可以独立启动后端，在 K-brain 中配置供应商与模型后运行：

```sh
go run ./cmd/kn backend -listen 127.0.0.1:47321
```

后端令牌可用 `K_BRAIN_BACKEND_TOKEN` 设置。LiveAgent 的连接参数为：

```sh
VITE_KBRAIN_URL=http://127.0.0.1:47321
VITE_KBRAIN_TOKEN=<backend-token>
```

`VITE_KBRAIN_TOKEN` 是前端可见的后端访问令牌。供应商 API key 属于后端配置。`model.provider` 与 `model.model` 应使用后端 `/v1/models` 返回的路由标识。

## 字段盘点与映射

| 概念 | K-brain 内部 | 规范协议 | LiveAgent 展示 |
| --- | --- | --- | --- |
| 会话 | `session.Meta.ID` | `conversation_id` / session `id` | 独立的前端 conversation ID 映射 |
| 一轮运行 | backend run record | `run_id`, `client_request_id` | 前端消息 ID 作为幂等键 |
| 模型 | Agent model/provider | `{provider, model}` | assistant provider/model |
| 用户/助手 | `ai.Message` | `Message` | LiveAgent 自有 `Message` 展示类型 |
| 工具调用 | `ai.ToolCall` | `{id,name,arguments}` | `ToolCall` 与工具轨迹 |
| 工具结果 | `tools.Result` | `{id,name,output,failed,cancelled}` | `ToolResultMessage` |
| 权限 | `tools.GateRequest` | `permission_id`, `tool`, `command`, `options`, `scope`, `tool_call_id` | 集中审批栏 |
| 子代理 | `agent.BackgroundTask` | `Subagent` | 状态与报告工具轨迹 |
| 流顺序 | durable event journal | 单会话单调递增 `seq` | 去重与续传游标 |
| 用量 | `ai.Usage` | input/output/cache token 计数 | assistant usage |

规范消息角色：`system`, `developer`, `user`, `assistant`, `tool`。内容块：`text`, `thinking`, `image`。工具参数必须为 JSON object；工具结果通过 `tool_call_id` 关联。供应商的 response item、chat chunk、Gemini candidate 留在后端适配边界。

## HTTP 接口

`/v1/health` 按 [兼容契约](compatibility.md) 第 2 节返回结构化握手：旧字段 `version: "kbrain.agent.v1"` 与全部历史能力值保持不变，另有 `protocol`（`name`、`major`、`minor`，minor 只用于日志）、`build`（`version`、`revision`、`channel`，由构建参数 `-X main.version` 与 `-X main.revision` 注入；只有 `vX.Y.Z` 形式的发布 tag 报 `stable` 或带 `-` 后缀的 `prerelease`，提交 SHA、`git describe` 的 `-<n>-g<sha>`/`-dirty` 输出和本地标签一律报 `dev`）、`startupContract`、每次进程启动重新生成的 `instanceId`，以及运行时能力状态的 `capabilityRevision`。能力的类型、比较方式、首次发布 tag 与缺失行为登记在 `internal/protocol/capabilities.json`，测试要求 health 与登记表一致。`instance_precondition`、`backend_drain`、`storage` 与 `build.releaseSequence` 尚未实现，不会出现在响应中。

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| GET | `/v1/health` | 健康状态与协议版本 |
| GET | `/v1/models` | 当前后端模型路由目录 |
| GET / PUT | `/v1/settings` | 读取脱敏配置 / 原子持久化供应商、模型与默认选择 |
| GET / PUT | `/v1/mcp` | 读取脱敏 MCP 配置 / 原子持久化并 reload stdio/http servers |
| POST | `/v1/mcp/reload` | 从 K-brain-owned MCP 配置重建 live manager |
| GET | `/v1/mcp/tools` | 按 workspace 与 enabled server IDs 返回 discovered schemas |
| POST | `/v1/mcp/search` | ToolSearch 语义的动态工具目录检索 |
| POST | `/v1/mcp/tools/call` | 通过 Go manager 执行 MCP tool call |
| POST | `/v1/text/generate` | 无状态辅助文本生成；canonical `messages` 仅允许文本与 system/developer/user/assistant，后端不转发 tools |
| GET / POST | `/v1/sessions` | 列出 / 创建会话 |
| POST | `/v1/sessions/exists` | 传 `{"ids":[...]}`（最多 5000 个），返回 `{"existing":[...]}`；只看文件是否存在、不读会话内容，`capabilities.session_exists` 表示支持 |
| POST | `/v1/sessions/snapshots` | 建立 60 秒不可变会话摘要快照，返回 epoch、revision、token、总数和 workdirs |
| GET / DELETE | `/v1/sessions/snapshots/{token}` | 按 `offset` / `limit`（最多 500）读取或释放快照；过期或不可续传返回 410 |
| GET | `/v1/sessions/changes` | 按 epoch、since、wait、rescan 长轮询最终列表变化，返回独立的 head_revision 与 next_since |
| GET | `/v1/sessions/workdirs` | 从同一摘要缓存汇总默认可见会话的工作目录、数量和最新时间 |
| GET | `/v1/sessions/{id}` | 消息、子代理任务与最近事件序号 |
| PATCH | `/v1/sessions/{id}` | 持久化 title、pinned、archived 或 model |
| DELETE | `/v1/sessions/{id}` | 删除会话与运行日志并取消仍在运行的后台子代理；正在进行的一轮返回 409 |
| GET | `/v1/sessions/{id}/history` | 按 raw offset 分页读取历史，可校验 revision |
| POST | `/v1/sessions/{id}/branch` | 从指定用户消息分支，保留其回复 |
| POST | `/v1/sessions/{id}/edit` | 替换用户消息并删除后续历史，校验 revision |
| GET / POST | `/v1/sessions/{id}/share` | 获取 / 设置可撤销分享 |
| GET | `/v1/shares/{token}` / `/share/{token}` | 公开分享 JSON / HTML 页面 |
| POST | `/v1/sessions/{id}/runs` | 提交提示及可选模型切换 |
| GET | `/v1/sessions/{id}/events?after_seq=N` | 从指定序号之后重放并订阅事件 |
| POST | `/v1/sessions/{id}/runs/{run}/cancel` | 取消运行 |
| GET | `/v1/sessions/{id}/subagents` | 本会话的前台与后台子代理（按开始时间排序）及当前 `last_seq` |
| GET | `/v1/sessions/{id}/subagents/{task}` | 子代理截至此刻的只读转录（运行中或已结束）、`live` 与 `last_seq` |
| GET | `/v1/sessions/{id}/subagents/{task}/events?after_seq=N` | 只含该子代理事件的 SSE，子代理结束后关闭 |
| POST | `/v1/sessions/{id}/subagents/{task}/cancel` | 只停止该子代理，父运行继续；未运行返回 409，未知返回 404 |
| POST | `/v1/sessions/{id}/permissions/{permission}` | 提交审批结果 |
| POST | `/v1/sessions/{id}/close` | 结束当前会话运行与后台子代理并保存 |

列表支持 `page`、`page_size`、`cwd`、`cwd_empty`、`shared`、`pinned`，返回 `sessions`、`total_count`、`epoch` 和字符串 `revision`。`/v1/health` 的 `capabilities.session_changes: 2` 表示同时支持不可变快照、增量、pinned、workdirs、字符串游标和过期 reset；客户端必须探测完整契约后才能放宽旧轮询。

增量响应的 `head_revision` 只表示服务端当前水位，客户端只能用实际交付批次的 `next_since` 续传；空批不推进。epoch 不匹配、游标早于回收水位或超出 head 时返回 `reset:true`、`next_since:null`。upsert 携带完整 `SessionSummary`；delete 不携带 session。会话列表和变化清单保存在内存中，不修改 transcript 格式。首次列表/快照/changes 才惰性建立摘要缓存；普通 TUI、ACP 与 `kn run` 不启动 HTTP 扫描器。存在 HTTP changes 订阅时，同 Store 共用有界外部文件扫描，退出租约后释放。

HTTP 快照最多保留 4 份，合计 JSON 摘要预算为 64MiB；正在构建的快照也占用这 4 个槽位，并且同一后端同时只允许一个构建者。槽位在读取 Store 和复制摘要之前预约，数量或容量不足返回 `429` 和 `Retry-After`，不驱逐有效快照、不截断内容。取消或构建失败会释放预约；关闭后端会取消构建并停止已保存快照的 TTL 计时器，迟到的构建不能在关闭后重新留下快照。

快照分页的 `sessions` 始终是数组，包括空库和空页；读取完毕返回 `next_offset:null`，不会用 `sessions:null` 表示空集合。

PATCH 会话响应和 DELETE 响应额外返回 `sync_epoch` / `sync_revision`，表示该操作对应的列表变化水位，与历史内容 revision 不同。历史窗口支持 `max_messages`、`before_offset`、`expected_revision`、`include_active`；返回 `session`、`revision`、`oldest_offset`、`has_more_before`、`total_message_count`、与消息数组一一对应的原始 `message_offsets`，以及可选 `active_messages`。原始 offset 可以不连续，客户端应按返回值构造消息引用。

编辑请求为 `{message_ref:{message_id},replacement:<规范 user 消息>,expected_revision}`。分支请求为 `{message_ref:{message_id},expected_revision?,title?}`。过期 revision 返回 409。分支会话的摘要（列表、详情、分支响应）带 `forked_from`（来源会话 ID）和 `fork_seq`（复制到的最后一条消息序号），普通会话省略这两个字段。编辑完成后启动运行时传入 `resume_message_id`，后端验证它指向已持久化的末尾用户消息，避免重复追加。普通发送不使用该字段。

分享请求为 `{enabled,redact_tool_content?}`，默认隐藏工具内容；返回 snake_case 分享状态。公开链接不需要后端令牌，撤销后原 token 失效。分享视图排除系统指令、工作目录、运行日志和内部任务信息。

CLI 可用 `-config FILE -session-dir DIR` 指定配置与存储目录；省略时使用现有用户/项目默认值。

设置 PUT 接受 `{defaultProvider?,defaultModel?,providers?,deleteProviders?}`。供应商包含 `id/name/api/baseUrl/models`，`apiKey` 为仅写入字段：省略保留现值，`clearApiKey:true` 明确清除。GET 仅返回 `apiKeyConfigured` 并脱敏 URL。保存成功后才发布新配置；已运行中的回合继续使用原客户端，下一轮在原 Agent 上刷新模型配置，保留任务和历史。已删除模型的下一轮返回错误。同名模型在不同供应商下使用相同元数据；冲突配置会明确拒绝。直接修改配置文件后需重启后端加载；当前设置接口采用最后成功保存的配置，不提供多编辑者 revision 冲突检测。

记忆总开关：GET 返回 `memory:{enabled}`（配置未写时为 `true`），PUT 接受 `memory:{enabled:true|false}`，开关不写入 config.json：关闭时写在配置文件旁的 `<配置文件名>.memory-switch.json`（默认是 `config.json.memory-switch.json`，内容 `{"enabled":false}`；该文件存在但读不出时按关闭处理），开启是默认值，重新开启会删掉这个文件。不带开关的旧版本解析 config.json 时不接受未知字段，也不读这个文件，所以无论开关处于哪种状态都能直接回退，回退后记忆照常开启；再升级回来时恢复之前的开关状态。早期构建写在 config.json 里的 `memory` 仍然有效，下次保存时迁到这个文件。关闭后从下一轮起（已打开的会话也一样）：不再注入 memory.md 与记忆索引；每个会话收到一次提示，说明用户已关闭记忆、之前注入的记忆不再适用，并要求模型不以任何方式（包括写 memory.md 等文件）保存记忆，遇到"记住"类请求时告诉用户记忆已关闭；隐藏提取器不运行；代理拿不到 `remember`、`forget`、`memory_manager`，运行选项里这三个名字的工具策略被忽略而不是报错。已存记忆不变，`/v1/memory/manage` 与记忆整理接口照常可用。TUI、ACP 与 `kn run` 读取同一配置。`/v1/health` 的 `capabilities.memory_switch: 1` 表示后端支持该开关，客户端据此决定是否显示。

浏览器设置缓存只保留必要的界面偏好，加载旧缓存时清理其中的供应商和连接凭据。`VITE_KBRAIN_TOKEN` 会进入前端构建产物，应按客户端访问令牌管理；面向多用户的远程部署需要独立的认证与权限设计。

`/v1/text/generate` 请求体为 `{model:{provider,model},messages:[{role,content:[{type:"text",text}]}],output?:"text"|"json"}`，响应为 `{version,text,model,usage?}`。请求使用后端 Bearer 鉴权并遵循 HTTP request context；客户端取消或断开连接会取消供应商请求。后端错误只返回通用生成失败/取消信息，不回显上游 URL、请求头或凭据。

启动请求携带 `client_request_id`。同键同请求返回原 `run_id`，同键不同请求返回冲突。重连继续读取 SSE；客户端不得因为断线而重新提交另一轮 prompt。

每条事件包含 `version`, `conversation_id`, `run_id`, `seq`, `type`, `created_at`, `payload`。主要事件族：

- `run.accepted`, `user.message.appended`
- `assistant.text.delta`, `assistant.thinking.delta`, `assistant.message.created`
- `tool.call`, `tool.result`, `tool.status`
- `permission.requested`, `permission.resolved`, `permission.auto_resolved`
- `subagent.started`, `subagent.updated`, `subagent.completed`, `subagent.failed`
- `trajectory.child.text`, `trajectory.child.thinking`, `trajectory.child.steer`, `trajectory.child.tool_start`, `trajectory.child.tool_end`（子代理自己的转录）
- `usage.updated`
- `run.completed`, `run.failed`, `run.cancelled`

`bash`/`Bash` 命令运行期间，后端以 `tool.status{tool_call_id,status:"running",message}` 推送输出：`message` 是截至此刻的输出尾部快照（最多 4 KiB，尽量从整行开始），客户端整体替换而非拼接；同一调用最多每 500ms 发一次，输出停住后补发最后一份，且全部排在该调用的 `tool.result` 之前。快照只进事件日志，会话历史仍只保存最终的 `tool.result`。不认识该字段的旧客户端可以忽略。

流连接结束与运行终态是两个不同事实。客户端只有消费终态事件后才能确认运行结束。

用户停止运行时，模型已经流出的回复写入会话历史：一条 `stop_reason:"cancelled"` 的 assistant 消息，内容是已推送的文本与思考，不含尚未流完的工具调用。事件流不变，仍以 `run.cancelled` 结束、不另发 `assistant.message.created`（已发布的桌面端会把这类事件并进本地流式副本），客户端从历史读取这条回复。之前几轮已执行的工具调用和结果照常保留，停止时正在执行的工具得到 `stop_reason:"cancelled"` 的结果，历史里不会留下没有结果的调用。还没有任何输出就停止时不写 assistant 消息，历史只多出本轮用户消息。下一轮请求中，这条回复的文本后面附一行 `[response interrupted by the user]`（只在请求里，不写入历史）；只有思考、没有文本的停止回复不发给模型。`stop_reason` 原本就有 `cancelled`（工具结果使用），旧客户端按已有取值处理；旧版后端读到这条消息时当普通 assistant 文本发送。

模型交给后台的工作（`wait` 轮询、未设 `isolated` 的 `ManagedProcess` 进程、`background=true` 的子代理）在本轮正常结束后继续运行，结果作为下一轮送回；用户停止运行、关闭或删除会话时它们照常一起结束。后台子代理运行期间会话不算忙：用户可以照常发起并完成新的运行，编辑、分支、压缩与目标续跑也不等它；报告在某一轮进行中送达时插进这一轮，会话空闲时则启动唤醒运行，唤醒运行与用户运行冲突时通知留在队列里，等那一轮结束再送。只有会孤立或干扰子代理的操作要等它结束：切换模型或工作目录（会重建持有子代理的 Agent）和检查点回滚（子代理可能仍在写这些文件）返回 409 `session has a running background subagent`，运行时也不会被闲置回收。后端启动会话运行时，把存储里仍标为 running、却已没有进程在跑的子代理记为 error（`interrupted — k-brain exited before this subagent finished`），与 TUI 一致。后端自己送来的通知——`wait` 结果、进程退出、后台子代理与工作流报告，无论是插进正在进行的一轮，还是作为唤醒运行（`client_request_id` 以 `wake-` 开头）的用户消息——在 `user.message.appended` 和会话历史里带 `origin:"background"`，客户端把它显示为系统通知，而不是用户发的消息；用户自己写的消息不带该字段。这类消息不计为用户输入，不进输入历史，也不附加本轮时间前缀。旧客户端忽略该字段，显示与以前相同；旧版后端读到该字段直接忽略。

### 子代理转录（`capabilities.subagent_transcripts: 1`）

`Subagent` 载荷除 `id/description/status/model/started_at/ended_at/result/error` 外增加：`parent_tool_call_id`（发起它的父 `subagent` 工具调用 id，客户端据此把 `subagent.*` 事件并进那张工具卡片）、`background`（后台任务为 `true`）、`usage`（子代理全部请求的 token 合计）、`tool_count`（子代理已发起的工具调用数）与 `duration_ms`（结束后给出）。前台子代理的 `description` 取工具参数里的描述，`model` 为子代理实际使用的模型。用户停止的子代理以 `subagent.failed` 结束，`status:"cancelled"`、`error:"subagent stopped by the user"`；前台子代理被停止后父运行照常继续，父模型看到一次失败的工具结果。

子代理运行期间，后端把它自己的转录以 `trajectory.child.*` 事件写进同一事件日志，`run_id` 为父运行、`payload.task_id` 为子代理 id：

- `trajectory.child.text` / `trajectory.child.thinking`：`{task_id,text}`，文本增量按约 100ms 或 8 KiB 合并后发出，类型切换或工具调用前先发出已积累的文本，因此每个子代理每秒最多约 10 条；
- `trajectory.child.tool_start`：`{task_id,id,name,arguments}`；`trajectory.child.tool_end`：`{task_id,id,name,output,failed,cancelled}`；
- `trajectory.child.steer`：`{task_id,text}`，运行中插入给子代理的消息。

这些事件不改变会话历史，旧客户端按未知事件忽略。父运行结束后仍在运行的后台子代理不会出现在主 SSE 中（主流在没有活动运行时关闭），客户端用 `/subagents/{task}/events` 跟随：它先重放 `after_seq` 之后属于该子代理的事件（`subagent.*` 与 `trajectory.child.*`），再持续推送，子代理结束并发出终态事件后关闭。

`/subagents` 与 `/subagents/{task}` 从事件日志和已保存的后台任务折叠得到，重载或后端重启后仍可读取；重启前仍在运行、此后没有终态的子代理报 `cancelled`。已结束的后台子代理返回其保存的完整对话（去掉 system/developer 消息），其他情况由事件重建：首条为子代理收到的提示，随后是 assistant 文本/思考与工具调用、工具结果。轨迹接口带 `redact_tool_content=true` 时，子代理文本与工具内容一样替换为 `[redacted]`。

### 记住的权限规则（`capabilities.permission_rules: 1`、`readonly_auto_allow: 1`）

规则写法参照 Claude Code：`Bash(npm test:*)` 前缀、`Bash(git status)` 精确、`Edit(src/**)` 项目内路径（Write/Edit 共用）、`mcp__srv__tool` 整个工具；规则引擎在 `internal/permissions`，TUI、ACP 与 HTTP 后端共用。复合命令每一段都要被规则或只读判定覆盖才放行；命令替换、子 shell、写文件重定向、包装命令等看不清的命令不受 allow 规则影响。

规则分三个作用域：会话（`<EventDir>/<会话 id>.permission-rules.json`，随会话删除）、项目（数据目录 `permissions/projects/<项目根哈希>.json`，项目根取运行第一个可写的 `workspace_roots`，否则会话工作目录）、用户（数据目录 `permission-rules.json`，与 TUI 共用）。`kn backend` 的数据目录与 TUI 相同；嵌入方不设 `Options.PermissionRulesDir` 时只有会话规则。

判定顺序：计划模式 → `approval_policy: deny` → 工具策略 deny → deny 规则（任何模式都拒绝）→ 看不清又有 deny 规则的命令必须询问（任何模式）→ 工具策略 allow/ask → `always`/`full_access` → allow 规则或只读放行 → 权限模式。AI 审查判定需人工确认时照常询问；只有审查只判 medium、其余维度都是 low 时，命中的 allow 规则可以代替询问。规则文件读不出时按"必须询问"处理。

- `permission.requested` 增加 `shell` 与 `suggestion:{rules,default_scope}`；有建议时 `options` 带 `allow_always`。删除类命令、Delete 工具与 computer use 不给建议。
- `POST /v1/sessions/{id}/permissions/{pid}` 的 `decision.scope` 取 `session|project|user`，`allow_always` 把建议里的全部规则存进该作用域；**省略 scope 等于 session**（旧客户端把这个选项显示为"本会话允许"）。作用域无效或写入失败返回 400，审批继续等待。记住后，同会话中被新规则覆盖、且没有被审查送去人工的其他等待审批一并放行。
- 因规则或只读判定直接放行时发 `permission.auto_resolved {tool_call_id,tool,command,source:"rule"|"read_only",rule,scope}`。
- `GET /v1/permissions/rules?workspace=<项目根>&session_id=<id>` 返回这些作用域的规则；`POST /v1/permissions/rules {rule,effect:"allow"|"deny",scope,workspace?,session_id?}` 新增；`DELETE /v1/permissions/rules/{id}?scope=&workspace=&session_id=` 删除。
- normal、auto_edit、auto_review 模式下，Read/Image/List/Glob/Grep 与只读命令（ls、cat、rg、git status/diff/log 等白名单）直接执行；cmd 会先在工作目录查找程序，cmd（以及 Windows 上未知的 shell）不做只读放行。显式的工具策略 ask 仍然询问。
- 查看本会话自己启动的进程或终端的输出也按只读放行：`ProcessWait`、`ManagedProcess` 的 `status`/`read_log`/`wait`、`ReadTerminal`。这类请求的 `command` 是进程或终端 id，规则按文本匹配它；deny 规则照常生效，不生成可记住的规则。启动、停止进程与终端输入仍然询问。
- 进程与终端归属会话：同一会话后续的运行可以查看、等待、停止之前运行启动的进程和终端（例如上一轮 Bash `yield_time_ms` 留下的 `session_id`）；其他会话、没有运行身份的调用一律拒绝。普通进程仍随启动它的运行结束而取消，之后读到的是 `cancelled` 等终态。
- Windows 路径：`\\?\D:\...` 与 `\\?\UNC\server\share\...` 先还原成 `D:\...`、`\\server\share\...` 再比较授权根、匹配权限规则和读写；还原后含义会变的写法（`.`、`..`、`/`、末尾的点或空格、`:` 流、保留设备名）以及 `\\.\`、`\??\`、卷 GUID、GLOBALROOT 直接拒绝。
- 只读命令白名单另含：`hostname`（不带参数）、`whoami`、`date`（不带参数或只带 `+格式`）、`Get-Date`、`Get-Process`/`ps`、`Get-FileHash`、任意程序只带 `--version`、`git stash list|show`、`git tag`（不带参数或 `-l/--list`）、`git config --get|--get-all|--get-regexp|--list|-l`、`cd`/`Set-Location`/`pushd`/`popd`，以及 PowerShell 管道里的 `Select-Object`、`Sort-Object`、`Measure-Object`、`Format-Table`、`Format-List`、`Format-Wide`、`Out-String`。
- 不做只读放行：含 `$` 的变量展开（`echo $OPENAI_API_KEY`、`$env:X`）、PowerShell 非文件驱动器（`Env:`、`Variable:`、`Function:`、`Alias:`、`Cert:`、`HKLM:`、`HKCU:`、`WSMan:`、`Registry::`）、`git -C <目录>`，以及 `cd` 之后再运行的 git——git 读取所在仓库的配置，`core.fsmonitor`、`diff.external` 等配置会运行程序。

## Fixtures 与验证入口

- `internal/protocol/testdata/canonical_messages.json`
- `internal/protocol/testdata/canonical_events.json`
- `internal/protocol/protocol_test.go` 与 `fixtures_test.go`：规范消息、图片顺序、工具参数校验、模型归属与用量持久化。
- `internal/protocol/providers_integration_test.go`：同一段历史依次经过 Chat Completions → Responses → Gemini → Chat Completions；使用真实客户端工厂和本地 HTTP 上游，检查工具关联、文本、用量、终止原因、上游错误与提前 EOF。
- `internal/ai/gemini_test.go` 与已有 Chat/Responses stream fixtures
- `internal/backend/server_test.go`、`history_contract_test.go`、`history_extended_test.go`：运行、历史变更、并发校验、稀疏分页、分享脱敏与重启。
- `internal/backend/tool_history_test.go`、`subagent_workflow_test.go`：真实工具审批、失败标志、子代理事件与持久化恢复。
- `cmd/kn/backend_cli_test.go`：构建真实可执行文件，两次启动与会话恢复。
- LiveAgent `test/providers/kbrain-client.test.mjs`
- LiveAgent `test/chat/kbrain-conversation-turn.test.mjs`：调用真实 text/agent turn 入口、本地 HTTP fixture、真实审批状态服务与展示归并函数。

```sh
go test ./internal/protocol ./internal/ai ./internal/backend
# 在 LiveAgent/crates/agent-gui 中：
node --test test/providers/kbrain-client.test.mjs test/chat/kbrain-conversation-turn.test.mjs
```
