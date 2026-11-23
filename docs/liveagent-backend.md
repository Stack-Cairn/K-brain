---
title: K-brain 与 LiveAgent 的会话协议
tags: [backend, protocol]
---

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
| 工具调用 | `ai.ToolCall` | `{id,name,arguments}`，历史中另有可选 `exit_code`/`timed_out`/`duration_ms` | `ToolCall` 与工具轨迹 |
| 工具结果 | `tools.Result` | `{id,name,output,failed,cancelled}`，可选 `exit_code`/`timed_out`/`duration_ms` | `ToolResultMessage` |
| 权限 | `tools.GateRequest` | `permission_id`, `tool`, `command`, `options`, `scope`, `tool_call_id`，可选 `preview` | 集中审批栏 |
| 子代理 | `agent.BackgroundTask` | `Subagent` | 状态与报告工具轨迹 |
| 流顺序 | durable event journal | 单会话单调递增 `seq` | 去重与续传游标 |
| 用量 | `ai.Usage` | input/output/cache token 计数 | assistant usage |

规范消息角色：`system`, `developer`, `user`, `assistant`, `tool`。内容块：`text`, `thinking`, `image`。assistant 消息可带 `stop_reason`（如 `stop`、`tool_use`、`length`、`cancelled`、`error`）；`stop_reason:"error"` 的部分回复另带 `error`（失败原因，见下文“模型流在中途失败”）。工具参数必须为 JSON object；工具结果通过 `tool_call_id` 关联。供应商的 response item、chat chunk、Gemini candidate 留在后端适配边界。

## HTTP 接口

`/v1/health` 按 [兼容契约](compatibility.md) 第 2 节返回结构化握手：旧字段 `version: "kbrain.agent.v1"` 与全部历史能力值保持不变，另有 `protocol`（`name`、`major`、`minor`，minor 只用于日志）、`build`（`version`、`revision`、`channel`，由构建参数 `-X main.version` 与 `-X main.revision` 注入；只有 `vX.Y.Z` 形式的发布 tag 报 `stable` 或带 `-` 后缀的 `prerelease`，提交 SHA、`git describe` 的 `-<n>-g<sha>`/`-dirty` 输出和本地标签一律报 `dev`）、`startupContract`、每次进程启动重新生成的 `instanceId`，以及运行时能力状态的 `capabilityRevision`。能力的类型、比较方式、首次发布 tag 与缺失行为登记在 `internal/protocol/capabilities.json`，测试要求 health 与登记表一致。`instance_precondition`、`backend_drain`、`storage` 与 `build.releaseSequence` 尚未实现，不会出现在响应中。

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| GET | `/v1/health` | 健康状态与协议版本 |
| GET | `/v1/debug/stacks` | 把所有 goroutine 的调用栈写到 `k-brain.log` 所在目录的 `stacks-<时间>-<pid>.txt`，返回 `{"path":...}`；只接受本机回环地址，不获取会话锁，backend 卡住时也能用（见用户指南《故障排查》） |
| GET | `/v1/models` | 当前后端模型路由目录 |
| POST | `/v1/sandbox/setup` | 在 Windows 上通过 UAC 安装或修复沙箱账户（`sandbox-user`），返回 setup 状态与新的 `run_sandbox`；`capabilities.sandbox_setup` 表示支持 |
| GET / PUT | `/v1/settings` | 读取脱敏配置 / 原子持久化供应商、模型与默认选择；记忆开关 `memory` 与记忆整理计划 `memoryOrganizer` 也在这里 |
| POST | `/v1/settings/providers/{id}/models` | 用已保存的供应商（可被请求体里的草稿地址、key、请求头覆盖）向上游拉取模型列表，返回 `{version,provider,models}`；失败时见下文“模型发现失败” |
| GET | `/v1/usage/stats` | 所有工作区会话的跨会话用量统计（按天、按模型，含连续天数与费用）；`days`、`tz` 与口径见下文，`capabilities.usage_stats` 表示支持 |
| GET / PUT | `/v1/mcp` | 读取脱敏 MCP 配置 / 原子持久化并 reload stdio/http servers |
| POST | `/v1/mcp/reload` | 从 K-brain-owned MCP 配置重建 live manager |
| GET | `/v1/mcp/tools` | 按 workspace 与 enabled server IDs 返回 discovered schemas；`audience=model|app` 按 `_meta.ui.visibility` 过滤，见下文“MCP 应用界面” |
| POST | `/v1/mcp/search` | ToolSearch 语义的动态工具目录检索 |
| POST | `/v1/mcp/tools/call` | 通过 Go manager 执行 MCP tool call；`ask` 策略先返回 `approval_required`，见下文“MCP 配置与运行选择” |
| POST | `/v1/text/generate` | 无状态辅助文本生成；canonical `messages` 仅允许文本与 system/developer/user/assistant，后端不转发 tools |
| GET / POST | `/v1/sessions` | 列出 / 创建会话；列表的 `archived=exclude|include|only` 见下文，`capabilities.session_archive` 表示支持 |
| POST | `/v1/sessions/exists` | 传 `{"ids":[...]}`（最多 5000 个），返回 `{"existing":[...]}`；只看文件是否存在、不读会话内容，`capabilities.session_exists` 表示支持 |
| POST | `/v1/sessions/snapshots` | 建立 60 秒不可变会话摘要快照，返回 epoch、revision、token、总数和 workdirs；可带 `archived` |
| GET / DELETE | `/v1/sessions/snapshots/{token}` | 按 `offset` / `limit`（最多 500）读取或释放快照；过期或不可续传返回 410 |
| GET | `/v1/sessions/changes` | 按 epoch、since、wait、rescan 长轮询最终列表变化，返回独立的 head_revision 与 next_since；可带 `archived` |
| GET | `/v1/sessions/workdirs` | 从同一摘要缓存汇总默认可见会话的工作目录、数量和最新时间；可带 `archived` |
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
| POST | `/v1/sessions/{id}/runs/{run}/tools/{tool_call_id}/background` | 把该调用正在前台运行的 shell 命令移到后台；见下文，`capabilities.background_tool` 表示支持 |
| GET | `/v1/sessions/{id}/subagents` | 本会话的前台与后台子代理（按开始时间排序）及当前 `last_seq` |
| GET | `/v1/sessions/{id}/subagents/{task}` | 子代理截至此刻的只读转录（运行中或已结束）、`live` 与 `last_seq` |
| GET | `/v1/sessions/{id}/subagents/{task}/events?after_seq=N` | 只含该子代理事件的 SSE，子代理结束后关闭 |
| POST | `/v1/sessions/{id}/subagents/{task}/cancel` | 只停止该子代理，父运行继续；未运行返回 409，未知返回 404 |
| POST | `/v1/sessions/{id}/permissions/{permission}` | 提交审批结果 |
| POST | `/v1/sessions/{id}/questions/{question}` | 回答 `question.requested`；`selected_labels` 见下文，`capabilities.question_selected_labels` 表示支持 |
| POST | `/v1/sessions/{id}/elicitations/{elicitation}` | 回答 MCP 服务器的表单征询 `elicitation.requested`；见下文，`capabilities.mcp_elicitation` 表示支持 |
| POST | `/v1/sessions/{id}/close` | 结束当前会话运行、后台子代理和移到后台的命令并保存 |
| PUT / DELETE | `/v1/host-bridge` | 桌面登记 / 清除宿主桥，供 `ReadTerminal`、`TunnelManager` 回调桌面；见下文，`capabilities.host_bridge` 表示支持 |
| GET / PUT | `/v1/settings/ssh` | 读取（密钥脱敏）/ 保存 `SSHManager` 的 SSH 主机与项目关联；另有 `PUT`/`DELETE /v1/settings/ssh/hosts/{id}` 与 `DELETE /v1/settings/ssh/hosts/{id}/host-keys`；见下文，`capabilities.ssh_manager` 表示支持 |
| GET | `/v1/skills` | 托管 Skills 根目录下的技能（含技能包里的技能）、被遮蔽的副本、无效条目与启用设置；字段见下文"技能包" |
| GET | `/v1/skills/files` / `/v1/skills/file?path=` | 列出各技能的 SKILL.md 相对路径 / 按相对路径读取根目录下的文件（技能包里的嵌套路径同样可读） |
| PUT | `/v1/skills/settings` | 保存启用设置（全局或按 `workdir` 的项目设置）；`packs` 见下文，`capabilities.skill_packs` 表示支持 |
| POST | `/v1/skills/manage` | SkillsManager 操作：`list`、`read`、`validate`、`delete`、`create`、`install`、`install_start` 等 |
| GET / POST / DELETE | `/v1/trust` | 读取 / 授予 / 撤销项目目录信任；见下文，`capabilities.folder_trust` 表示支持 |
| GET / POST / DELETE | `/v1/plugins`、`/v1/plugins/{id}/enable|disable`、`/v1/plugins/install`、`/v1/plugins/reload` | 列出（可长轮询）/ 启用 / 停用 / 安装 / 删除 / 重新扫描插件；见下文，`capabilities.plugins` 表示支持 |
| POST | `/v1/plugins/{id}/tools/call` | 为插件界面执行该插件自己的工具；见下文“插件界面”，`capabilities.plugin_ui` 表示支持 |
| GET / HEAD | `/v1/plugin-assets/{grant}/{plugin}/{path}` | 只读提供插件界面文件，供沙箱 iframe 加载；**不需要令牌**（能力 URL），见下文“插件界面” |
| GET | `/v1/sessions/{id}/mcp-ui?server=&uri=` | 读取 MCP 工具声明的界面资源（`ui://`）HTML，带沙箱响应头；见下文“MCP 应用界面”，`capabilities.mcp_apps` 表示支持 |
| GET / HEAD | `/v1/sessions/{id}/mcp-ui/results/{ref}` | 读取 MCP 应用界面对应工具调用保存的原始 `CallToolResult`（`ui_resource.result_url`）；见下文“MCP 应用界面”，`capabilities.mcp_apps_structured_content` 表示支持 |
| GET | `/v1/sessions/{id}/previews` | 列出会话的 HTML 预览；见下文“HTML 预览”，`capabilities.html_previews` 表示支持 |
| GET / HEAD | `/v1/sessions/{id}/previews/{previewId}` | 返回一个 HTML 预览，带沙箱响应头 |

列表支持 `page`、`page_size`、`cwd`、`cwd_empty`、`shared`、`pinned`、`archived`，返回 `sessions`、`total_count`、`epoch` 和字符串 `revision`。`/v1/health` 的 `capabilities.session_changes: 2` 表示同时支持不可变快照、增量、pinned、workdirs、字符串游标和过期 reset；客户端必须探测完整契约后才能放宽旧轮询。

增量响应的 `head_revision` 只表示服务端当前水位，客户端只能用实际交付批次的 `next_since` 续传；空批不推进。epoch 不匹配、游标早于回收水位或超出 head 时返回 `reset:true`、`next_since:null`。upsert 携带完整 `SessionSummary`；delete 不携带 session。会话列表和变化清单保存在内存中，不修改 transcript 格式。首次列表/快照/changes 才惰性建立摘要缓存；普通 TUI、ACP 与 `kn run` 不启动 HTTP 扫描器。存在 HTTP changes 订阅时，同 Store 共用有界外部文件扫描，退出租约后释放。

HTTP 快照最多保留 4 份，合计 JSON 摘要预算为 64MiB；正在构建的快照也占用这 4 个槽位，并且同一后端同时只允许一个构建者。槽位在读取 Store 和复制摘要之前预约，数量或容量不足返回 `429` 和 `Retry-After`，不驱逐有效快照、不截断内容。取消或构建失败会释放预约；关闭后端会取消构建并停止已保存快照的 TTL 计时器，迟到的构建不能在关闭后重新留下快照。

快照分页的 `sessions` 始终是数组，包括空库和空页；读取完毕返回 `next_offset:null`，不会用 `sessions:null` 表示空集合。

PATCH 会话响应和 DELETE 响应额外返回 `sync_epoch` / `sync_revision`，表示该操作对应的列表变化水位，与历史内容 revision 不同。历史窗口支持 `max_messages`、`before_offset`、`expected_revision`、`include_active`；返回 `session`、`revision`、`oldest_offset`、`has_more_before`、`total_message_count`、与消息数组一一对应的原始 `message_offsets`，以及可选 `active_messages`。原始 offset 可以不连续，客户端应按返回值构造消息引用。

编辑请求为 `{message_ref:{message_id},replacement:<规范 user 消息>,expected_revision}`。分支请求为 `{message_ref:{message_id},expected_revision?,title?}`。过期 revision 返回 409。分支会话的摘要（列表、详情、分支响应）带 `forked_from`（来源会话 ID）和 `fork_seq`（复制到的最后一条消息序号），普通会话省略这两个字段。编辑完成后启动运行时传入 `resume_message_id`，后端验证它指向已持久化的末尾用户消息，避免重复追加。普通发送不使用该字段。

分享请求为 `{enabled,redact_tool_content?}`，默认隐藏工具内容；返回 snake_case 分享状态。公开链接不需要后端令牌，撤销后原 token 失效。分享视图排除系统指令、工作目录、运行日志和内部任务信息。

CLI 可用 `-config FILE -session-dir DIR` 指定配置与存储目录；省略时使用现有用户/项目默认值。

设置 PUT 接受 `{defaultProvider?,defaultModel?,providers?,deleteProviders?}`。供应商包含 `id/name/api/baseUrl/models`，`apiKey` 为仅写入字段：省略保留现值，`clearApiKey:true` 明确清除。GET 仅返回 `apiKeyConfigured` 并脱敏 URL。保存成功后才发布新配置；已运行中的回合继续使用原客户端，下一轮在原 Agent 上刷新模型配置，保留任务和历史。已删除模型的下一轮返回错误。同名模型在不同供应商下使用相同元数据；冲突配置会明确拒绝。直接修改配置文件后需重启后端加载；当前设置接口采用最后成功保存的配置，不提供多编辑者 revision 冲突检测。

记忆总开关：GET 返回 `memory:{enabled}`（配置未写时为 `true`），PUT 接受 `memory:{enabled:true|false}`，开关不写入 config.json：关闭时写在配置文件旁的 `<配置文件名>.memory-switch.json`（默认是 `config.json.memory-switch.json`，内容 `{"enabled":false}`；该文件存在但读不出时按关闭处理），开启是默认值，重新开启会删掉这个文件。不带开关的旧版本解析 config.json 时不接受未知字段，也不读这个文件，所以无论开关处于哪种状态都能直接回退，回退后记忆照常开启；再升级回来时恢复之前的开关状态。早期构建写在 config.json 里的 `memory` 仍然有效，下次保存时迁到这个文件。关闭后从下一轮起（已打开的会话也一样）：不再注入 memory.md 与记忆索引；每个会话收到一次提示，说明用户已关闭记忆、之前注入的记忆不再适用，并要求模型不以任何方式（包括写 memory.md 等文件）保存记忆，遇到"记住"类请求时告诉用户记忆已关闭；隐藏提取器不运行；代理拿不到 `remember`、`forget`、`memory_manager`，运行选项里这三个名字的工具策略被忽略而不是报错。已存记忆不变，`/v1/memory/manage` 与记忆整理接口照常可用。TUI、ACP 与 `kn run` 读取同一配置。`/v1/health` 的 `capabilities.memory_switch: 1` 表示后端支持该开关，客户端据此决定是否显示。

模型发现失败：上游没有给出可用的模型列表时返回 HTTP 502，`error` 仍是 `"model discovery failed"`（旧客户端照常显示），另带：

- `errorCode`：`invalid_api_key`（上游 401/403）、`not_found`（上游 404，通常是地址或模型列表路径不对）、`rate_limited`（上游 429）、`unreachable`（没有拿到任何 HTTP 响应：DNS 失败、连接被拒或重置、TLS 失败、15 秒超时）、`upstream_error`（其他上游状态码，或上游 200 但返回的不是可解析的模型列表）。
- `upstreamStatus`：上游的 HTTP 状态码（整数），只在上游回了错误状态码时出现。
- `upstreamMessage`：可选，上游 JSON 错误体里的 `error.message`、`error`（字符串）、`message` 或 `detail`，合并空白、最多 200 个字符，去掉供应商 key、请求头值以及形似凭据的词；上游返回 HTML 等非 JSON 内容时不带。

示例：`{"version":"kbrain.agent.v1","error":"model discovery failed","errorCode":"invalid_api_key","upstreamStatus":401,"upstreamMessage":"Incorrect API key provided: [redacted]."}`。请求本身有误时（400 缺少供应商、地址或 key 等；404 供应商不存在）仍是原来只有 `error` 的形状，不带 `errorCode`。旧客户端忽略新增字段。

浏览器设置缓存只保留必要的界面偏好，加载旧缓存时清理其中的供应商和连接凭据。`VITE_KBRAIN_TOKEN` 会进入前端构建产物，应按客户端访问令牌管理；面向多用户的远程部署需要独立的认证与权限设计。

`/v1/text/generate` 请求体为 `{model:{provider,model},messages:[{role,content:[{type:"text",text}]}],output?:"text"|"json"}`，响应为 `{version,text,model,usage?}`。请求使用后端 Bearer 鉴权并遵循 HTTP request context；客户端取消或断开连接会取消供应商请求。后端错误只返回通用生成失败/取消信息，不回显上游 URL、请求头或凭据。

启动请求携带 `client_request_id`。同键同请求返回原 `run_id`，同键不同请求返回冲突。重连继续读取 SSE；客户端不得因为断线而重新提交另一轮 prompt。

每条事件包含 `version`, `conversation_id`, `run_id`, `seq`, `type`, `created_at`, `payload`。主要事件族：

- `run.accepted`, `user.message.appended`
- `assistant.text.delta`, `assistant.thinking.delta`, `assistant.message.created`
- `tool.call`, `tool.result`, `tool.status`
- `permission.requested`, `permission.resolved`, `permission.auto_resolved`
- `question.requested`, `question.resolved`
- `elicitation.requested`, `elicitation.resolved`（只在带 `options.mcp_elicitation` 的运行中出现）
- `preview.created`（只在带 `options.html_previews` 的运行中出现）
- `subagent.started`, `subagent.updated`, `subagent.completed`, `subagent.failed`
- `trajectory.child.text`, `trajectory.child.thinking`, `trajectory.child.steer`, `trajectory.child.tool_start`, `trajectory.child.tool_end`（子代理自己的转录）
- `usage.updated`
- `run.completed`, `run.failed`, `run.cancelled`

`bash`/`Bash` 命令运行期间，后端以 `tool.status{tool_call_id,status:"running",message}` 推送输出：`message` 是截至此刻的输出尾部快照（最多 4 KiB，尽量从整行开始），客户端整体替换而非拼接；同一调用最多每 500ms 发一次，输出停住后补发最后一份，且全部排在该调用的 `tool.result` 之前。快照只进事件日志，会话历史仍只保存最终的 `tool.result`。不认识该字段的旧客户端可以忽略。

`tool.result` 的 `tool_result` 中，`output` 是工具结果文本（只出现这一次）；`content` 只装工具附加的图片等附件（截图、读取的图片），没有附件时省略，不会重复 `output` 的文本。客户端以 `output` 显示文本、以 `content` 显示图片。

`tool.result` 的 `tool_result` 与会话历史里 assistant 消息的 `tool_calls[]` 带可选的运行信息，只用于展示，模型看到的工具结果文本不变：

- `exit_code`：shell 类工具（`bash`、`Bash`、`ManagedProcess` 的 `status`/`read_log`/`wait`/`stop`、`ProcessWait`、`ProcessStop`）运行或观察到的命令退出码，包括 0；其他工具、超时、被取消或被信号杀死、仍在运行的命令不带该字段。
- `timed_out`：命令因超时被杀死时为 `true`。
- `duration_ms`：工具自身的耗时（毫秒，向上取整），不含等待用户审批的时间；工具没有运行时不带。
- `trajectory.child.tool_end` 同样带这三个可选字段。
- `sandbox_denial`：只出现在 `tool.result` 事件里（会话历史不带）。`bash`/`Bash` 命令在 OS 沙箱中失败、且输出能看出是沙箱拦截时给出 `{kind,path,suggested_root}`：`kind` 为 `write`（写工作区可写根之外）、`protected`（工作区内受保护的 git 配置、hooks 或代理设置）、`masked`（被隐藏的凭据目录）或 `network`（本次运行未开网络）；`path` 是被拒绝的路径（绝对路径、已解析符号链接，`network` 不带）；`suggested_root` 只在 `write` 且命令文本本身写出了该路径时给出，是客户端可以提议用户加入可写根的目录，绝不会是包含家目录、凭据目录的目录或系统顶层目录。后端不据此授予任何权限。同一说明以一行 `[sandbox] ...` 追加在模型看到的工具结果文本末尾。

`permission.requested` 对 `write`/`edit`/`Write`/`Edit` 带可选的 `preview:{path,diff,new_file,truncated}`，让客户端在用户决定前显示将要写入的改动。`diff` 与编辑结果里的 diff 块格式相同（每行 `<行号> <标记> <文本>`，标记为空格、`-` 或 `+`），最多 200 行、32 KiB；文件或新内容超过 2 MiB 时只给 `truncated:true` 不给 `diff`；二进制文件、注定失败的编辑（找不到 `old_string`）和其他工具不带 `preview`。`redact_tool_content=true` 的轨迹去掉 `preview`。旧客户端忽略这些字段。

流连接结束与运行终态是两个不同事实。客户端只有消费终态事件后才能确认运行结束。

模型流出的思考在运行结束后保存在会话历史里：有思考的 assistant 消息，`content[]` 以一个 `{type:"thinking",text}` 块开头，内容是该轮通过 `assistant.thinking.delta` 推送的全部思考（Chat Completions 的 `reasoning_content`/`reasoning`/`reasoning_text`、Responses 的推理摘要与 `reasoning_text`、Anthropic 的 thinking、Gemini 的 thought 片段；DeepSeek 搜索的多轮请求合并为一段）。Responses 的多段推理摘要之间以空行分隔，分隔也通过 `assistant.thinking.delta` 推送，实时与历史一致；流里没有推理增量、只有完成的 reasoning 项带摘要时，历史取该摘要。`GET /v1/sessions/{id}` 与 `/history` 都返回这个块，重新打开对话即可显示。思考只用于展示：发给任何供应商的请求都不含它（需要回放的签名思考、加密推理仍走各自的回放通道，请求体不变）。运行结束时的 `assistant.message.created` 不带 `thinking` 块，形状与以前相同：客户端已从增量事件得到整次运行的思考，而该事件只是最后一轮。公开分享仍只含文本与图片。

用户停止运行时，模型已经流出的回复写入会话历史：一条 `stop_reason:"cancelled"` 的 assistant 消息，内容是已推送的文本与思考，不含尚未流完的工具调用。事件流不变，仍以 `run.cancelled` 结束、不另发 `assistant.message.created`（已发布的桌面端会把这类事件并进本地流式副本），客户端从历史读取这条回复。之前几轮已执行的工具调用和结果照常保留，停止时正在执行的工具得到 `stop_reason:"cancelled"` 的结果，历史里不会留下没有结果的调用。还没有任何 assistant 回复就停止时，历史在本轮用户消息后写入一条空的停止标记：`id` 为 `assistant-stopped-<run_id>`、`stop_reason:"cancelled"`、没有 `content`，重新打开对话时据此显示“已停止”；它同样不发 `assistant.message.created`，也不发给模型。之后用 `resume_message_id` 继续这条用户消息时，后端把末尾的停止标记视为不存在，新回复在历史里取代它；客户端找“末尾用户消息”时应跳过这条标记。下一轮请求中，这条回复的文本后面附一行 `[response interrupted by the user]`（只在请求里，不写入历史）；只有思考、没有文本的停止回复不发给模型。`stop_reason` 原本就有 `cancelled`（工具结果使用），旧客户端按已有取值处理；旧版后端读到这条消息时当普通 assistant 文本发送。

后端自己关闭（进程收到正常退出信号、`Close`）时正在进行的运行按上面的停止流程保存，并标出是被关闭打断而不是用户停止：`run.cancelled` 仍是 `{state:"cancelled"}`，另带 `cancel_reason:"shutdown"`；这一轮的部分回复（或没有回复时的 `assistant-stopped-<run_id>` 标记）以及停止时被取消的工具结果仍是 `stop_reason:"cancelled"`，另带 `cancel_reason:"shutdown"`。用户停止、排队消息打断、关闭会话等其他停止不带该字段。客户端可把带 `cancel_reason:"shutdown"` 的回复显示为“已中断”，其余 `cancelled` 显示为“已停止”；未知取值按停止处理。下一轮请求中，被关闭打断的回复文本后附 `[response interrupted because the backend stopped]`（只在请求里）。旧客户端忽略该字段，按停止显示；旧版后端读到时丢弃。

模型流在中途失败（连接断开、流没有完成事件就结束、供应商在流中返回错误）且已经流出内容时，这部分回复同样写入会话历史：一条 `stop_reason:"error"` 的 assistant 消息，内容是已推送的思考与文本，不含尚未流完的工具调用，另带可选字段 `error`（字符串，与本次 `run.failed` 的 `error` 相同）。它就是这次运行的失败消息：事件流仍是 `assistant.message.created` 后接 `run.failed{state:"failed",error}`，`assistant.message.created` 的消息即这条部分回复（带 `stop_reason:"error"` 与 `error`，按上文不含 `thinking` 块），不再另加一条以错误文本为内容的 assistant 消息。还没有任何输出就失败时与以前相同：写入一条内容为错误文本、`stop_reason:"error"`、不带 `error` 字段的 assistant 消息。下一轮请求中，部分回复的文本后面附一行 `[response interrupted by an error]`，`error` 字段不发给模型；只有思考、没有文本的失败回复不发给模型；不带 `error` 字段的错误消息照旧作为 assistant 文本发送。客户端据 `stop_reason` 区分中断原因（`cancelled` 为用户停止、`error` 为失败），失败原因显示 `error`。旧客户端忽略 `error` 字段，历史里看到的是部分回复而不是错误文本；旧版后端读到这条消息时当普通 assistant 文本发送。会话历史除在一轮结束时保存外，每完成一轮工具调用（assistant 的工具调用及全部结果）也保存一次，所以后端进程在一轮进行中崩溃或被强制结束（SIGKILL）时，已完成的步骤留在历史里，只丢失最后一步正在流出的内容与正在执行的工具；重启后历史以本轮用户消息和已完成的工具轮结束。每次保存重写整个会话文件并更新 `.sync` 计数，与该轮模型请求本身发送的历史同一量级。

失败的结构化信息：`run.failed` 与这次运行保存的失败消息（上述部分回复，或还没有输出时的 `assistant-error-<run_id>` 消息；`assistant.message.created` 与历史里都一样）另带可选对象 `error_detail`，原有的 `error`、`stop_reason:"error"` 和错误文本内容不变：

- `kind`：`invalid_api_key`（上游 401/403）、`not_found`（404，地址或模型名不对）、`rate_limited`（429，或流内的限流/配额错误）、`context_limit`（上游判定提示超出上下文窗口）、`bad_request`（其他 4xx）、`server_error`（5xx，或流内的 overloaded/busy/internal 类错误）、`timeout`（408/504、请求超时、流长时间无数据）、`connection_lost`（流没有完成事件就结束、连接被重置）、`unreachable`（连接被拒、DNS、TLS 等，没有拿到响应）、`provider_error`（供应商在流里返回的其他错误）、`unknown`（不是模型请求造成的失败或无法识别）。客户端把未列出的取值当 `unknown`。
- `upstream_status`：上游 HTTP 状态码（整数），只在上游回了错误状态码时出现。
- `provider_message`：可选，供应商自己的错误说明（HTTP 错误体里的 `error.message`、`error`、`message` 或 `detail`，或流内错误的 message），合并空白、最多 200 个字符，去掉该供应商配置的 key、请求头值以及形似凭据的词；HTML 等非 JSON 错误体不带。

示例：`run.failed` 为 `{"state":"failed","error":"401 Unauthorized: {…}","error_detail":{"kind":"invalid_api_key","upstream_status":401,"provider_message":"Incorrect API key provided: [redacted]"}}`；流中途断开时为 `{"state":"failed","error":"Chat Completions stream ended before a completion event: unexpected EOF","error_detail":{"kind":"connection_lost"}}`。`error_detail` 只用于展示，不发给模型。分类与模型发现失败（上文 `errorCode`）共用同一套判断和脱敏，但取值集合不同：运行失败把发现接口的 `upstream_error` 细分为上面的种类。旧客户端忽略该字段；旧版后端读到时丢弃。

模型交给后台的工作（`wait` 轮询、未设 `isolated` 的 `ManagedProcess` 进程、`background=true` 的子代理）在本轮正常结束后继续运行，结果作为下一轮送回；用户停止运行、关闭或删除会话时它们照常一起结束。后台子代理运行期间会话不算忙：用户可以照常发起并完成新的运行，编辑、分支、压缩与目标续跑也不等它；报告在某一轮进行中送达时插进这一轮，会话空闲时则启动唤醒运行，唤醒运行与用户运行冲突时通知留在队列里，等那一轮结束再送。只有会孤立或干扰子代理的操作要等它结束：切换模型或工作目录（会重建持有子代理的 Agent）和检查点回滚（子代理可能仍在写这些文件）返回 409 `session has a running background subagent`，运行时也不会被闲置回收。后端启动会话运行时，把存储里仍标为 running、却已没有进程在跑的子代理记为 error（`interrupted — k-brain exited before this subagent finished`），与 TUI 一致。后端自己送来的通知——`wait` 结果、进程退出、后台子代理与工作流报告，无论是插进正在进行的一轮，还是作为唤醒运行（`client_request_id` 以 `wake-` 开头）的用户消息——在 `user.message.appended` 和会话历史里带 `origin:"background"`，客户端把它显示为系统通知，而不是用户发的消息；用户自己写的消息不带该字段。这类消息不计为用户输入，不进输入历史，也不附加本轮时间前缀。旧客户端忽略该字段，显示与以前相同；旧版后端读到该字段直接忽略。

### 子代理转录（`capabilities.subagent_transcripts: 1`）

`Subagent` 载荷除 `id/description/status/model/started_at/ended_at/report/error` 外增加：`parent_tool_call_id`（发起它的父 `subagent` 工具调用 id，客户端据此把 `subagent.*` 事件并进那张工具卡片）、`background`（后台任务为 `true`）、`usage`（子代理全部请求的 token 合计）、`tool_count`（子代理已发起的工具调用数）与 `duration_ms`（结束后给出）。前台子代理的 `description` 取工具参数里的描述，`model` 为子代理实际使用的模型。用户停止的子代理以 `subagent.failed` 结束，`status:"cancelled"`、`error:"subagent stopped by the user"`；前台子代理被停止后父运行照常继续，父模型看到一次失败的工具结果。

子代理运行期间，后端把它自己的转录以 `trajectory.child.*` 事件写进同一事件日志，`run_id` 为父运行、`payload.task_id` 为子代理 id：

- `trajectory.child.text` / `trajectory.child.thinking`：`{task_id,text}`，文本增量按约 100ms 或 8 KiB 合并后发出，类型切换或工具调用前先发出已积累的文本，因此每个子代理每秒最多约 10 条；
- `trajectory.child.tool_start`：`{task_id,id,name,arguments}`；`trajectory.child.tool_end`：`{task_id,id,name,output,failed,cancelled}`，可选 `exit_code`/`timed_out`/`duration_ms`，MCP 应用界面工具还带 `ui_resource`（见“MCP 应用界面”）；
- `trajectory.child.steer`：`{task_id,text}`，运行中插入给子代理的消息。

这些事件不改变会话历史，旧客户端按未知事件忽略。父运行结束后仍在运行的后台子代理不会出现在主 SSE 中（主流在没有活动运行时关闭），客户端用 `/subagents/{task}/events` 跟随：它先重放 `after_seq` 之后属于该子代理的事件（`subagent.*` 与 `trajectory.child.*`），再持续推送，子代理结束并发出终态事件后关闭。

`/subagents` 与 `/subagents/{task}` 从事件日志和已保存的后台任务折叠得到，重载或后端重启后仍可读取；重启前仍在运行、此后没有终态的子代理报 `cancelled`。已结束的后台子代理返回其保存的完整对话（去掉 system/developer 消息），其他情况由事件重建：首条为子代理收到的提示，随后是 assistant 文本/思考与工具调用、工具结果。轨迹接口带 `redact_tool_content=true` 时，子代理文本与工具内容一样替换为 `[redacted]`。

### Windows 沙箱 setup（`capabilities.sandbox_setup: 1`）

平台沙箱自检在后端启动时于后台进行。`/v1/health` 最多等它 2 秒：自检还没完成时 `run_sandbox.supported` 为 false、`mechanism` 为空、`reason` 说明自检尚未完成，客户端应稍后重新读取（自检卡住不会再拖住 health）。

`capabilities.run_sandbox` 增加两个字段：`setup_available`（本机可以安装沙箱账户，只在 Windows 上为 true）和 `setup_hint`（给用户看的一句英文；setup 未安装时说明它能带来断网和工作区隔离，已安装但自检失败时说明问题并建议重新运行；正常工作时为空）。`mechanism` 为 `sandbox-user` 时命令以专用沙箱账户运行，`network_control` 为 true；仍为 `low-integrity` 时断网请求照旧被拒绝。细节见 [沙箱](user-guide/09-sandbox.md) 的 Windows 部分。

客户端在 `sandbox_setup == 1`、`run_sandbox.setup_available` 为 true 且 `network_control` 为 false 时可以提供"设置断网沙箱"操作，旁边显示 `setup_hint`。操作调用 `POST /v1/sandbox/setup`（无请求体，需要后端令牌，与其他接口相同）：后端在运行它的用户桌面上弹出 UAC 管理员确认，等待用户选择（最多 10 分钟；客户端断开不会取消已批准的 setup），完成后重新自检。响应为 `{version,status,run_sandbox,error?,code?}`，`status` 与 `kn sandbox status --json` 相同（`installed`、`active`、`mechanism`、`network_control`、`accounts`、`groups`、`problem`、`hint` 等）：

- `200`：setup 已完成，`run_sandbox` 是新的能力；
- `403`，`code: "declined"`：用户拒绝或关闭了 UAC 提示，本机没有任何改动；
- `409`，`code: "busy"`：另一个 setup 正在进行；
- `501`，`code: "unsupported"`：不是 Windows；
- `500`，`code: "failed"`：setup 失败，`error` 给出原因（可能已部分安装，重新运行会修复，`kn sandbox uninstall` 会清除）；
- `405`：不是 POST。

除 `501` 外，每次调用都会让 `/v1/health` 的 `capabilityRevision` 加一，客户端据此重新读取能力。卸载只能由用户在终端里运行 `kn sandbox uninstall`，后端不提供卸载接口。

### 提问的结构化多选（`capabilities.question_selected_labels: 1`）

`question` 与 `AskUserQuestion` 两个工具都通过 `question.requested {question_id,tool_call_id,run_id,deadline_at,questions:[{id,header?,prompt,options:[{label,description?,recommended?}],multiple?}]}` 提问。客户端用 `POST /v1/sessions/{id}/questions/{question_id}` 回答，请求体为 `{conversation_id,run_id,question_id,answers:[...]}`，每题一个答案。答案有四种写法：

| 写法 | 含义 |
| --- | --- |
| `{"question_id":"q1","selected_label":"SQLite"}` | 选了一个选项（原有写法） |
| `{"question_id":"q1","selected_label":"随便","custom":true}` | 用户自己输入的答案，最多 2000 字符（原有写法） |
| `{"question_id":"q2","selected_labels":["Docs","Search, filter"]}` | 选了这些选项（新增） |
| `{"question_id":"q2","selected_labels":["Docs"],"selected_label":"再加个徽章","custom":true}` | 选了这些选项，另外输入了自己的答案（新增，只用于多选题） |

`selected_labels` 的规则（不满足时返回 400，错误信息指出题目 id 和出错的标签，问题继续等待回答）：

- 每个标签必须与该题某个选项的 `label` 完全相同（比较时不去掉空白，区分大小写），同一标签不能出现两次；空数组等于没传。
- `multiple: true` 的题可以选一个或多个；单选题只能有一个标签，且不能同时带 `custom`。
- 不带 `custom` 时 `selected_label` 必须省略或为空；带 `custom` 时 `selected_label` 是用户输入的文字，不能为空。
- 后端按选项顺序保存标签，与发送顺序无关。

`question.resolved` 的 `answers` 按题目顺序记录校验后的答案，每项另带 `prompt`。结构化答案带 `selected_labels`；不带 `custom` 时，`selected_label` 由后端填为这些标签用 `, ` 连接的文本，供不认识 `selected_labels` 的读者显示。读者的规则是：有 `selected_labels` 时以它为准；`custom` 为 true 时 `selected_label` 是用户输入的文字，否则是选中的选项。旧写法原样保存，不会补 `selected_labels`。同一答案重复提交返回 `{"ok":true,"duplicate":true}`，比较的是校验后的结果，所以标签顺序不同也算同一答案。

```json
{"question_id":"…","tool_call_id":"call_1","run_id":"…","kind":"ask_user_question","questions":[…],
 "answers":[{"prompt":"Which DB?","question_id":"q1","selected_label":"SQLite"},
            {"prompt":"Add what?","question_id":"q2","selected_label":"再加个徽章","selected_labels":["Search, filter","CI"],"custom":true}],
 "text":"…"}
```

模型看到的结果中，选中的选项直接写标签，与单选相同；用户输入的文字仍标为 Other 输入。例如 `→ Search, filter, CI; plus 再加个徽章 (user-typed answer via Other, not a listed option)`。

历史：两个工具的调用在 `/v1/sessions/{id}/history`（以及 `GET /v1/sessions/{id}`）的 `tool_calls[]` 上带 `question_answers`，内容与 `question.resolved` 的 `answers` 相同，重启后不变；`tool.result` 事件的 `tool_result` 也带这个字段。`AskUserQuestion` 的工具输出本身就是上面的 JSON；`question` 的工具输出是给模型的文本，客户端回放时应读 `question_answers`，不要拆分文本。超时或取消的提问没有 `question_answers`。在 TUI 中回答的 `question` 调用同样记录 `question_answers`（单问题形式的题目 id 为 `q1`）。

旧客户端不发 `selected_labels`，行为不变。旧版后端不认识这个字段，会以 400（未知字段）拒绝，所以客户端必须先确认 `question_selected_labels == 1`；否则仍把多选标签用 `, ` 连接放进 `selected_label` 并设置 `custom: true`。

### MCP 表单征询（`capabilities.mcp_elicitation: 1`）

MCP 服务器可以在自己的工具运行期间请用户填写一张表单（MCP elicitation）。K-brain 只支持 form 模式，不支持 url 模式。

**协议层。** 后端的 MCP manager 在 `initialize` 中声明 `capabilities.elicitation`：协商到 2025-11-25 时为 `{"form":{}}`，更早的协议为 `{}`（含义相同）。服务器在 `tools/call` 进行中发来的 `elicitation/create`，以及 2026-07-28 多轮往返结果里 `inputRequests` 中的 elicitation，走同一条处理路径。以下情况直接回 JSON-RPC 错误 `-32602`，不打扰用户、不发任何事件：`mode` 不是 `form`；`message` 为空或超过 10000 字符；`requestedSchema` 超出下表的子集（错误信息为 `invalid requestedSchema: ...`，指出属性名和关键字；SDK 自己的检查先拒绝一部分，信息以 `elicit schema ...` 开头）。没有工具正在运行时到达的 elicitation 直接回 `decline`；同一次工具调用最多问 8 次，之后的直接回 `decline`。

`requestedSchema` 允许的子集（只能是一层对象，每个属性必须写 `type`；`$schema`、`$id`、`$comment` 接受并忽略，其他任何关键字都会被拒绝）：

| 字段类型 | 写法 | 允许的约束 |
| --- | --- | --- |
| 文本 | `{"type":"string"}` | `title`、`description`、`minLength`、`maxLength`、`pattern`（Go RE2 语法，不锚定）、`format`（`email`/`uri`/`date`/`date-time`）、`default` |
| 单选 | `{"type":"string","enum":[...]}`（可带旧式 `enumNames`）或 `{"type":"string","oneOf":[{"const","title"}]}` | `title`、`description`、`default`；不能再带长度、格式或 pattern |
| 数字 | `{"type":"number"}` / `{"type":"integer"}` | `title`、`description`、`minimum`、`maximum`、`default` |
| 开关 | `{"type":"boolean"}` | `title`、`description`、`default` |
| 多选 | `{"type":"array","items":{"type":"string","enum":[...]}}` 或 `items:{"anyOf":[{"const","title"}]}` | `title`、`description`、`minItems`、`maxItems`、`uniqueItems`、`default` |

根对象可带 `required`（必须是已有属性名、不重复）、`title`、`description` 和布尔 `additionalProperties`。上限：64 个属性，每个选择题 1–100 个互不相同的非空选项，属性名 1–128 字符，`title` 200 字符，`description` 2000 字符。`default` 必须满足字段自己的约束。没有 `requestedSchema` 或没有属性时是一个只需确认的空表单。

**客户端选择加入。** 运行请求带 `options.mcp_elicitation: true`，表示客户端能显示表单。这样的运行在服务器请求时发布 `elicitation.requested` 并等待回答；不带该选项的运行和定时任务等无人值守运行立即回 `decline`，不发布 elicitation 事件，只在该工具调用的 `elicitations` 里留下记录（`reason` 为 `no_client` 或 `unattended`）；`/v1/mcp/tools/call` 直接调用工具时也一律回 `decline`。旧版后端不认识这个选项，会以 400（未知字段）拒绝整次运行，客户端必须先确认 `mcp_elicitation == 1`。

**`elicitation.requested`**：

```json
{"elicitation_id":"elicit-1762490000000000000-42","tool_call_id":"call_1","run_id":"run-…",
 "deadline_at":1762490180000,"server":"tracker","tool":"login","message":"Sign in to the issue tracker",
 "fields":[
  {"name":"username","type":"string","title":"User name","required":true,"min_length":2},
  {"name":"password","type":"string","title":"Password","required":true,"sensitive":true},
  {"name":"plan","type":"string","default":"free","options":[{"value":"free","label":"Free"},{"value":"pro","label":"Pro"}]},
  {"name":"seats","type":"integer","required":true,"minimum":1,"maximum":50},
  {"name":"notify","type":"boolean","default":true},
  {"name":"topics","type":"array","max_items":2,"options":[{"value":"news","label":"news"},{"value":"billing","label":"billing"}]}],
 "requested_schema":{"type":"object","properties":{…},"required":[…]}}
```

- `server` 是用户配置的 MCP 服务器名（不是服务器自报的名字），客户端必须醒目地显示它，让用户知道是谁在要数据；`tool` 是正在运行的工具名（不带 `mcp__` 前缀）。
- `fields` 按服务器书写属性的顺序排列（拿不到原始顺序时，必填字段按 `required` 顺序在前，其余按名字排序）。每项：`name`（答案里的键）、`type`（`string`/`number`/`integer`/`boolean`/`array`）、可选 `title`、`description`、`required`、`default`、`format`、`min_length`、`max_length`、`pattern`、`minimum`、`maximum`、`options`（`[{value,label}]`，`label` 在服务器没给标题时等于 `value`）、`min_items`、`max_items`、`sensitive`。带 `options` 的 `string` 是单选，`array` 一定是多选。
- `sensitive: true` 表示名字或标题看起来像密钥（password、token、API key、secret、PIN、OTP、信用卡号等，只判断自由文本字段）。规范禁止服务器用表单索要密钥；遇到这种字段时客户端应遮住输入并提醒用户核实服务器。后端不会把这类字段的值写进事件日志、历史或日志。
- `requested_schema` 是服务器发来的 schema，供想自己渲染 JSON Schema 的客户端使用。
- `deadline_at`（Unix 毫秒）与提问相同：默认 3 分钟。等待期间该工具的 MCP 超时暂停计时，用户填表的时间也不计入 `duration_ms`。

**回答**：`POST /v1/sessions/{id}/elicitations/{elicitation_id}`，请求体 `{conversation_id,run_id,elicitation_id,action,content?}`：

```json
{"conversation_id":"…","run_id":"run-…","elicitation_id":"elicit-…","action":"accept",
 "content":{"username":"ada","password":"…","plan":"pro","seats":3,"topics":["billing","news"]}}
```

`action` 为 `accept`、`decline`（用户明确拒绝）或 `cancel`（用户关掉表单，未作选择）。`content` 只随 `accept` 发送；省略等于 `{}`。校验规则：

- 每个键必须是某个字段的 `name`；必填字段必须出现且不为 `null`；可选字段的 `null` 等于没填，不发给服务器。
- `string`：必须是字符串；长度按 Unicode 字符计算；`format` 为 `email`（单个裸地址，不带显示名）、`uri`（带 scheme 的绝对 URI）、`date`（`YYYY-MM-DD`）、`date-time`（RFC 3339）；`pattern` 在值中任意位置匹配即可。单选值必须是某个选项的 `value`。
- `number`/`integer`：必须是 JSON 数字，`integer` 必须是整数；`minimum`/`maximum` 为闭区间。
- `boolean`：必须是 `true`/`false`，不接受字符串。
- 多选：字符串数组，每项是某个选项的 `value`，不能重复，个数满足 `min_items`/`max_items`；后端按选项顺序保存。
- 服务器没收到的可选字段由 MCP 层按 schema 的 `default` 补上。

响应：

- `200 {"ok":true}`：已交给服务器；
- `200 {"ok":true,"duplicate":true}`：同一回答重复提交（比较校验后的结果，敏感字段不参与比较）；
- `400`：`action` 无效、`decline`/`cancel` 带了 `content`、请求体有未知字段，或内容校验失败。校验失败时为 `{"version","error":"invalid content: ...","field_errors":{"seats":"must be at least 1",…}}`，`field_errors` 按字段名给出英文说明（未知键只出现在 `error` 里）。表单继续等待，客户端可以修改后重发；
- `404`：这个运行里没有该 elicitation；
- `409`：`conversation_id`/`elicitation_id` 与路径不符，已用其他回答结束，或已不再等待（超时、运行已停止）。

**`elicitation.resolved`**：

```json
{"elicitation_id":"elicit-…","tool_call_id":"call_1","run_id":"run-…","server":"tracker","tool":"login",
 "message":"Sign in to the issue tracker","action":"accept","reason":"user",
 "content":{"username":"ada","plan":"pro","seats":3,"topics":["news","billing"]},"redacted":["password"]}
```

`action` 是服务器收到的结果。`reason`：`user`（客户端回答）、`timeout`（到 `deadline_at` 无人回答，回 `cancel`）、`cancelled`（运行被停止或服务器撤回请求，回 `cancel`）。`content` 只在 `accept` 时出现，不含敏感字段的值，`redacted` 列出被去掉的字段名。每个 `elicitation.requested` 恰好对应一个 `elicitation.resolved`，重放时两者都在。

**历史。** `tool.result` 的 `tool_result` 和历史里的 `tool_calls[]` 带 `elicitations`：该调用期间的每次征询按顺序记录 `{elicitation_id?,server,message,fields,action,reason,content?,redacted?}`，包括未询问用户就回绝的（`reason` 为 `no_client`、`unattended` 或 `limit`，这时没有 `elicitation_id`）。服务器拒绝的 schema（`-32602`）不记录。模型看不到这个字段，只看到服务器最终返回的工具结果。

**记录与日志。** 事件日志和历史只保存去掉敏感字段后的回答，完整回答只在内存中交给 MCP 服务器。K-brain 的 MCP 日志只写服务器名、工具名、`action` 与 `reason`，不写任何字段值。服务器自己在工具结果里回显的内容不受此限制。

`kn` 的 TUI 同样声明这一能力，用一张卡片显示表单（标题是服务器名，敏感字段遮住输入，校验规则同上，没有超时）。ACP 不声明，服务器不会经 ACP 发起征询。用户侧说明见 [Hooks 与 MCP](user-guide/11-hooks-mcp.md)。

### 把运行中的命令移到后台（`capabilities.background_tool: 1`）

`POST /v1/sessions/{id}/runs/{run_id}/tools/{tool_call_id}/background`（无请求体）把活动运行里某个工具调用正在前台运行的 shell 命令移到后台。可以移到后台的是 `Bash`（不带 `yield_time_ms` 的前台路径）和 CLI 的 `bash`（非交互）；`tool_call_id` 是 `tool.call` 事件里该调用的 `id`（也是 `tool.status`、`tool.result` 关联用的 id）。

- 命令不会被结束或重启：进程组、沙箱和工作目录不变，到此为止的输出连同之后的输出都写进一个 `ManagedProcess` 日志，进程登记到会话的进程管理器，id 形如 `bash-…`。
- 这次工具调用立即返回成功，结果文本以 `The user moved this command to the background.` 开头，带 `process_id`、`pid`、`cwd`、`log`、`command`、`cursor` 和到 `cursor` 为止的输出末尾（最多 8 KiB），并告诉模型用 `ManagedProcess(action="wait"|"read_log"|"status"|"stop")` 继续读取或停止它。
- 进程自己结束时按现有规则通知会话：没有 `wait` 在看时送一条 `[process bash-… completed|failed exit_code=N]` 通知（`origin:"background"`），会话空闲则启动唤醒运行。
- 移到后台之后，取消这次运行、原调用的超时都不再结束它；`ManagedProcess(action="stop")`、`POST /close`、删除会话和关闭后端会结束它；它运行期间会话不会被闲置回收。
- 响应：`200 {"ok":true,"tool_call_id":"...","process_id":"bash-..."}`。会话不存在返回 404；`run_id` 不是活动运行返回 409 `run is not active`；调用未知、已结束、是交互式命令或已经移过一次返回 409（重复请求是无副作用的 409）。命令恰好在请求到达时结束的，同样返回 409，工具结果是命令的正常输出。
- 同一运行里有多个可移到后台的调用时，每个调用单独请求。子代理里的调用不在主代理的登记表里，返回 409。

旧后端没有这个路由（返回 404），客户端应先检查能力再显示入口。

### 桌面宿主桥（`capabilities.host_bridge: 1`）

有些工具操作的状态在 LiveAgent 桌面的 Rust 进程里：终端标签页和 Gateway 隧道。这类工具由 K-brain 持有（schema、权限、历史、事件都和其他工具一样），副作用交给桌面执行：桌面在 loopback 上开一个 HTTP 服务（宿主桥），K-brain 执行工具时回调它。

**登记。** 桌面用普通的 `/v1` 后端令牌调用 `PUT /v1/host-bridge`：

```json
{"url":"http://127.0.0.1:<port>","token":"<random>","host":"liveagent-desktop","tools":["ReadTerminal","TunnelManager"]}
```

- `url` 只接受 `http`，主机只能是 `127.0.0.1`、`[::1]` 或 `localhost`，可带端口，不能带路径、用户名密码、query 或 fragment；否则返回 400，登记不变。
- `token` 必填，不含空白和控制字符，最长 4096 字节。`host` 可省略（默认 `liveagent-desktop`），最长 128 字节。
- `tools` 里 K-brain 不认识的名字被忽略，请求里的未知字段也被忽略；只有列出的已知工具才会注册。
- 成功返回 `200 {"connected":true,"tools":[...]}`。再次 PUT 整体替换上一次登记。`DELETE /v1/host-bridge` 清除登记，返回 `{"connected":false,"tools":[]}`。其他方法返回 405。
- 登记只保存在后端进程内存里：不写盘，不进日志、事件和历史，也不传给子进程的环境变量。后端重启后登记丢失，桌面应在每次（重新）连接后端时重新 PUT（`/v1/health` 的 `instanceId` 变化即表示后端重启过）。
- `/v1/health` 报告 `host_bridge: {"connected": bool, "tools": [...]}`，不含 url 和 token。

**调用。** K-brain 发 `POST {url}/v1/host/{command}`，带 `Authorization: Bearer <token>` 和 `Content-Type: application/json`，请求体是参数 JSON。桌面必须以 HTTP 200 回答 `{"ok":true,"result":...}` 或 `{"ok":false,"error":{"code":"...","message":"..."}}`；任何非 200 状态都按传输错误处理。`error.message`（`code` 附在括号里）作为工具失败的原因交给模型。每次调用默认 10 秒超时；运行被停止时正在进行的调用随之取消。回答最多 8 MiB。K-brain 的客户端不走代理、不跟随重定向，并拒绝对端不是 loopback 地址的连接。

| command | 参数 | `result` |
| --- | --- | --- |
| `terminal_read_tail` | `{project_path_key, session_id?, max_bytes?}` | `{sessions:[{id,title,cwd,shell,running}], selectedSession, output, truncated}` |
| `gateway_tunnel_state` | `{}` | `{revision, agentOnline, relay, tunnels:[{id,slug,name,targetUrl,publicPath,createdAt,expiresAt,activeConnections,projectPathKey,local}], gatewayUnsupported?}` |
| `gateway_tunnel_create` | `{input:{targetUrl, name?, ttlSeconds?}}` | 不使用 |
| `gateway_tunnel_close` | `{tunnel_id}` | 不使用 |
| `gateway_tunnel_check` | `{tunnel_id?}` | 不使用 |

结果形状与桌面已有的 Tauri 命令 `terminal_read_tail`、`gateway_tunnel_state` 相同。K-brain 总是发送 `max_bytes`（默认 32 KiB，限制在 4–128 KiB）和 `ttlSeconds`（默认 3600）。`project_path_key` 是会话工作目录按桌面 `project_path_key` 的规则归一化后的值：Windows 盘符、UNC 和 `\\?\` 路径改为正斜杠、转小写、去掉根之外的末尾分隔符（`\\?\UNC\Server\Share\Repo\` 得到 `//server/share/repo`）；其他路径保留大小写，只去掉末尾的 `/`。`gateway_tunnel_state` 的结果可以另带不属于契约的 `publicBaseUrl`，有它时工具结果显示完整公网地址，否则显示 `publicPath`。

**工具。** 桥登记了对应工具时，运行里才有它们：

- `ReadTerminal`：沿用 LiveAgent v1 的名字、描述、`terminal_id`/`max_bytes` 参数和结果文本，读取当前项目的桌面终端。它替换 K-brain 自己的同名工具，另加一个 `session_id` 参数：传入本会话 `TerminalSession` 的 id 时仍读本地终端，不经过桌面。
- `TunnelManager`：沿用 v1 的 schema、描述和结果文本。`create` 在调用前后各读一次状态，找出新建的隧道；`check` 触发探测后等待 2.5 秒再读取状态（运行停止时提前结束）。

每次运行开始时按当前登记重新挂载这两个工具，所以会话加载之后才连上的桥在下一次运行生效；桥断开后下一次运行不再提供它们。运行中途桥断开或桌面不可达时，调用返回 `LiveAgent desktop is not connected`。

**权限。**

- `ReadTerminal` 与 `TunnelManager` 的 `list`/`check` 按只读放行（与 Read、本地 `ReadTerminal` 一致）：normal、auto_edit、auto_review 下不询问，发 `permission.auto_resolved`（`source: "read_only"`）。规则匹配的 `command` 分别是终端 id（省略时为空）、`list`、`check` 或 `check <id>`；deny 规则（如 `ReadTerminal`、`TunnelManager(list)`）、按工具的 ask/deny 策略和计划模式照常生效。
- `create`/`close` 默认询问，`command` 为 `create <targetUrl>`、`close <id>`，不生成可记住的规则建议；可以手动添加 `TunnelManager(create:*)`、`TunnelManager(close:*)` 这样的 allow 规则。`full_access`/`always` 直接放行。
- 定时任务等无人值守运行：读取照常按只读放行；`create`/`close` 只有在 allow 规则、按工具的 allow 策略或 `full_access`/`always` 下才执行，否则立即失败（`unattended scheduled runs cannot request interactive approval; add an allow rule for TunnelManager`），不发起审批也不等待。
- 审批在调用桌面之前完成，被拒绝的 `create` 不会到达桌面。

**令牌与沙箱。** 令牌只在 K-brain 进程内存里，不进入任何子进程。沙箱里的命令（联网的运行中）仍能连到本机 loopback 端口，包括桥监听的端口，但拿不到令牌；未开沙箱的命令同样拿不到。桌面必须校验每个请求的 `Authorization`，拒绝缺少或不匹配令牌的请求。

### SSH 主机与 SSHManager（`capabilities.ssh_manager: 1`）

`SSHManager` 是 K-brain 自己的工具：SSH/SFTP 连接、主机密钥校验、登录交互都在后端进程里完成（Go `golang.org/x/crypto/ssh` 与 `github.com/pkg/sftp`），不经过桌面宿主桥。名字、参数、`action` 列表、`session_strategy`、`credential` 取值和结果文本沿用 LiveAgent v1 `sshManagerTools.ts`。

**存储。** 主机保存在 `<配置目录>/ssh/hosts.json`，K-brain 自己的 known_hosts 在同目录的 `known_hosts`（`kn backend` 的配置目录与 TUI 相同；嵌入方不设 `Options.SSHDir` 时为 `<EventDir>/ssh`）。目录权限 0700，文件 0600，原子替换写入。K-brain 从不读取用户的 `~/.ssh/known_hosts`、`~/.ssh/config` 或 ssh-agent。该目录与其他数据目录一样对沙箱里的命令隐藏。

**`GET /v1/settings/ssh`**：

```json
{"revision":3,"knownHostsPath":"/…/ssh/known_hosts",
 "hosts":[{"id":"web","name":"Web","description":"","host":"10.0.0.8","port":22,"username":"deploy",
   "authType":"password","password":"","passwordConfigured":true,
   "privateKey":"","privateKeyPath":"","privateKeyConfigured":false,
   "privateKeyPassphrase":"","privateKeyPassphraseConfigured":false,
   "proxy":{"type":"socks5","url":"","port":0,"username":"","password":"","passwordConfigured":false,"useSystemProxy":false},
   "credential":"saved",
   "hostKeys":[{"algorithm":"ssh-ed25519","fingerprint":"SHA256:…"}]}],
 "projectHostAssociations":{"/Users/me/repo":["web"]}}
```

- 主机字段与 LiveAgent 的 `SshHostConfig` 相同。四个密钥（`password`、`privateKey`、`privateKeyPassphrase`、`proxy.password`）只写不读：响应里永远是空字符串，是否已保存看对应的 `…Configured`。`privateKeyPath` 不是密钥，原样返回。
- `credential`：`saved`（密码主机有密码；私钥主机有 `privateKey` 或 `privateKeyPath`）、`missing`、`interactive`（`authType` 为 `keyboardInteractive`）。
- `hostKeys` 是 known_hosts 里该主机地址（`host:port`，非 22 端口写作 `[host]:port`，IPv6 带方括号）已信任的密钥。
- `projectHostAssociations` 的键按桌面 `project_path_key` 规则归一化（与宿主桥相同）。

**`PUT /v1/settings/ssh`** 请求体 `{baseRevision?,hosts?,projectHostAssociations?}`：

- `hosts` 出现时是完整列表：列表外的主机被删除。每项必填 `id`（不含 `/ \ ? # %`，最长 128 字节）、`name`、`host`；`port` 可为数字或数字字符串，缺省 22；`authType` 为 `password`（缺省）、`privateKey` 或 `keyboardInteractive`；`proxy.type` 为 `socks5`（缺省）或 `http`。最多 256 台主机。
- 密钥字段遵循供应商 `apiKey` 的规则：省略或空字符串保留已存值；非空值替换；`clearPassword`/`clearPrivateKey`/`clearPrivateKeyPassphrase`/`proxy.clearPassword: true` 清除；另外，`…Configured: false` 且值为空也表示清除（设置表单里清空了该项）。所以把 GET 的结果原样 PUT 回来不会丢失任何密钥。改变 `authType` 时不再适用的密钥一并丢弃。`privateKey` 必须是能解析的私钥（加密的也可以）。
- `projectHostAssociations` 出现时整体替换；不存在的主机 id、重复项被丢弃，每个项目最多 64 台。
- `baseRevision` 非零时必须等于当前 `revision`，否则 409；省略则直接覆盖。GET 结果里的 `revision`、`knownHostsPath`、`credential`、`hostKeys` 可以带着发回，会被忽略。
- 成功返回 200 与新的 GET 文档；校验失败 400（英文说明指出主机和字段），写盘失败 500。

单台主机：`PUT /v1/settings/ssh/hosts/{id}`（请求体是一台主机，`id` 可省略，与路径不符为 400）新增或更新；`DELETE /v1/settings/ssh/hosts/{id}` 删除（并删除它的项目关联），不存在为 404。`DELETE /v1/settings/ssh/hosts/{id}/host-keys` 从 known_hosts 删除该主机的密钥，返回 `{"removed":N,"host":{…}}`，用于服务器确实换了密钥之后。主机被删除、连接参数（地址、端口、用户名、认证方式、密钥、代理）被修改或密钥被遗忘时，该主机已打开的会话立即关闭。

**工具挂载。** 每次运行开始时，只有会话工作目录关联了至少一台主机，运行里才有 `SSHManager`（与 v1 一样）；之后建立的关联在下一次运行生效。客户端在 `options.client_tools` 里自己声明了 `SSHManager` 时以客户端的为准。`options.tools` 可以列出 `SSHManager`。

**动作。** `list_hosts`、`create_session`、`list_sessions`、`close_session`、`exec`、`read_session`、`send_input`、`resize`，以及 `sftp_list`、`sftp_stat`、`sftp_read_text`、`sftp_write_text`、`sftp_mkdir`、`sftp_rename`、`sftp_delete`、`sftp_upload`、`sftp_download`、`sftp_transfer_status`、`sftp_cancel_transfer`。

- 会话按项目隔离：只能使用当前项目关联的主机，只能看到、使用当前项目打开的会话。`session_strategy` 为 `reuse`（缺省，复用该主机正在运行的会话）、`new`（另开一条连接，不能与 `session_id` 同用）、`require_existing`（没有会话就失败）。会话在后端进程里跨运行保持，空闲 30 分钟、连接断开或后端关闭时结束；不自动重连。
- `exec` 缺省超时 30 秒（最大 300 秒），stdout/stderr 各保留末尾 64 KiB（4–256 KiB 可调）；超时或运行被停止时发 `TERM` 信号并关闭通道，最多再等 3 秒。`cwd` 用 `cd '<cwd>' && ` 前缀实现。
- `read_session`/`send_input`/`resize` 第一次使用时在会话上开启交互式 shell（PTY），缓冲最近 256 KiB 输出。
- `sftp_read_text` 默认仍为 200 KiB 字节分页；`limit`（兼容 `max_bytes`）限定在 4–256 KiB。返回的 `offset` 向前对齐至 UTF-8 字符起点，`bytesRead` 为返回文本的实际字节数，下一页从 `offset + bytesRead` 开始。非法 UTF-8 和 EOF 处不完整字符报错，不再用替换字符掩盖错误。完整读取显式传 `full: true`，不可同时传非 null 的 `offset`、`limit` 或 `max_bytes`；实际读取受 16 MiB 上限约束，超限失败。完整读取额外返回 SHA-256 `contentHash`，分页不返回该字段。
- `sftp_write_text` 的 UTF-8 内容上限为 16 MiB。可同时携带完整读取的 `entry.mtime` → `expected_mtime_ms` 与 `contentHash` → `expected_content_hash`，仅用于允许覆盖时的版本校验；时间只有秒精度，哈希检测同秒修改。版本不匹配或目标消失时拒绝截断。旧调用不传版本字段时保留 `overwrite` 语义，`overwrite: false` 使用独占创建。远端写入仍为原位写入，不提供原子替换或跨进程事务，失败可能留下部分内容。
- 上述 SFTP 能力由原生 `SSHManager` 工具提供，本仓库没有独立的 SFTP 文件资源 HTTP 路由。Gateway 的 SSH relay 需要配套转发这些字段和限制；本地文件 HTTP 请求体和范围预览契约见 [[2026-10-24-filesystem-resources]]。
- `sftp_upload`/`sftp_download` 在后台执行，立即返回 `transfer_id`，用 `sftp_transfer_status` 查询、`sftp_cancel_transfer` 取消；每个会话最多 32 个进行中的传输，目录最多 100000 个文件。`local_path` 按文件工具的规则解析（工作区相对路径、绝对路径、`~/...`、`file://`），必须在运行的工作区根内，上传要求可读、下载要求可写。不跟随本地符号链接；`sftp_delete` 不跟随远端符号链接。
- 代理：`proxy.url` 非空时按 `proxy.type` 走 SOCKS5 或 HTTP CONNECT；`useSystemProxy` 时与供应商请求使用同一个代理（LiveAgent 管理的应用代理，否则 `HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY`）。

**主机密钥。** 连接时用 K-brain 的 known_hosts 校验服务器密钥：

- 未知密钥：发一个普通的提问卡片（`question.requested`），问题文本给出主机、地址、算法和 SHA256 指纹，选项为 `Trust once`（只信任这一次）、`Trust and save`（写入 known_hosts）和推荐的 `Reject`。拒绝、超时或运行被停止都不连接。
- 已知主机出现不同的密钥：直接拒绝，不询问，工具结果以 `REMOTE HOST KEY CHANGED` 开头并给出新旧指纹；用户确认服务器确实换了密钥后，调用 `DELETE …/host-keys` 再重新连接确认。

**登录。** 保存的密码、私钥（`privateKey` 或 `privateKeyPath`；后者由后端进程读取，开头的 `~` 展开为家目录）和口令直接使用。需要用户输入时（没保存的密码、加密私钥没保存口令、keyboard-interactive/MFA 提示）走 MCP 表单征询的同一套事件和回答接口，因此运行必须带 `options.mcp_elicitation: true`，否则调用失败并说明原因：

```json
{"elicitation_id":"elicit-…","tool_call_id":"call_1","run_id":"run-…","deadline_at":…,
 "source":"ssh","server":"ssh:Web","tool":"SSHManager",
 "message":"SSHManager needs the password of Web (deploy@10.0.0.8:22). It is used for this connection only and is not saved.",
 "fields":[{"name":"password","type":"string","title":"Password","required":true,"sensitive":true}]}
```

- `source: "ssh"` 表示这是 SSH 服务器的登录表单，`server` 是 `ssh:<主机名>`，不是 MCP 服务器。加密私钥的口令字段名为 `passphrase`。keyboard-interactive 的字段名为 `response_1`、`response_2`…，标题是服务器的提示文字，服务器不回显的提示标为 `sensitive`。一次登录最多 5 轮提示。
- 用户在表单里输入的值只用于这一次连接，不保存到 `hosts.json`。`elicitation.resolved`、`tool.result` 的 `elicitations` 和历史里 `content` 为 `{}`，`redacted` 列出全部已回答字段——不论字段是否标为敏感都不记录。
- 已保存密码的主机遇到单个不回显的密码提示时自动用保存的密码回答。

**权限。**

- `list_hosts`、`list_sessions`、`read_session`、`sftp_transfer_status` 只读 K-brain 自己的状态，按只读放行（发 `permission.auto_resolved`，`source: "read_only"`）。
- 其余动作默认与 Bash 一样询问；审批在连接之前完成，被拒绝的调用不会连接服务器。规则匹配的 `command` 为 `<action> <host_id 或 session_id> <细节>`，细节是远端命令（`exec`）、输入（`send_input`）、路径、`from -> to`（`sftp_rename`）、`local -> remote`（`sftp_upload`）、`remote -> local`（`sftp_download`）或 `transfer_id`。例如 `SSHManager(exec web:*)` 放行在 `web` 上的所有命令，`SSHManager(sftp_delete:*)` 可以写成 deny 规则。不生成可记住的规则建议。
- 定时任务等无人值守运行不询问也不等待：需要审批的动作只有在 allow 规则、按工具的 allow 策略或 `full_access`/`always` 下才执行，否则立即失败；主机密钥必须已经保存、凭据必须已经保存，否则同样立即失败并说明原因。

**记录与日志。** 密钥不进入事件、历史、日志和工具结果；保存的密钥只在 `hosts.json` 里。远端命令自己输出的内容不受此限制。

### 技能包（`capabilities.skill_packs: 1`）

除了 `<root>/<name>/SKILL.md` 形式的技能，K-brain 还识别**技能包**：一个顶层目录本身没有 SKILL.md，但更深处有 SKILL.md（例如整个克隆下来的技能仓库）。LiveAgent 桌面的 Rust 服务实现同一规则，下面的上限和顺序都属于契约：

- **顶层条目。** 对技能根目录下每个顶层目录 `<dir>`：有 `<dir>/SKILL.md` 就是一个技能（原有行为）；否则，`<dir>` 以下 4 层之内有 SKILL.md 时，`<dir>` 是一个技能包。点开头的目录和下面列出的跳过目录不会成为技能包。
- **遍历。** 广度优先，最多到 `<dir>` 以下 4 层目录，每个包最多访问 5000 个目录、接受 200 个技能；超出时停止并在 `invalid` 中报告。跳过点开头的目录、`node_modules`、`vendor`、`target`、`build`、`dist`、`__pycache__`，以及解析后落在技能根目录之外的符号链接。技能目录本身也会继续向下遍历（路由技能下面可以有子技能）。
- **识别。** 每个含 SKILL.md、且 frontmatter 有 `name` 与 `description` 的目录是一个技能，技能名取 frontmatter 的 `name`。包内技能按宽松规则检查：名字不必等于目录名，允许 `|` 块标量描述；名字不能为空、不能以 `.` 开头、不能含空白、控制字符或路径分隔符，最长 128 字符。严格的命名规则只用于 create / validate 顶层技能。缺少 name 或 description 的 SKILL.md 进入 `invalid`。
- **重名。** 包内最浅的路径保留名字，同深度按 baseDir 字节序取小者；顶层技能优先于任何包内技能；两个包之间，目录名字典序在前的包优先。落败的副本标为 shadowed，不参与提示词、补全和启用。客户端适配目录（例如包内 `plugins/` 下为其他客户端准备的副本）通常就以这种方式被遮蔽。
- **身份。** `baseDir` 是相对技能根目录的路径，始终用正斜杠（Windows 也一样），例如 `reverse-skill/skills/radare2`；`skillFile` 是 `<baseDir>/SKILL.md`，技能可按 `skill://<skillFile>` 引用。技能里的相对路径相对该技能自己的目录解析。

**`GET /v1/skills`。** `skills[]` 每项新增：

| 字段 | 含义 |
| --- | --- |
| `pack` | 所属技能包的目录名；顶层技能为空字符串 |
| `baseDir` | 相对根目录的技能目录，正斜杠 |
| `shadowed` | 在 `skills[]` 中总是 `false` |
| `shadowedBy` | 仅被遮蔽的副本有：胜出技能的 `baseDir` |

`skills[]` 中每个名字只出现一次；被遮蔽的副本放在顶层新增的 `shadowed[]` 数组里（`shadowed: true`），这样按名字索引技能的旧客户端不会看到重复项。`SkillsManager` 的 `list` 返回同样的 `shadowed`。

**启用设置。** 托管根目录的技能仍是选择加入：

- 包内技能和顶层技能一样按名字放进 `selected`（重名已在发现阶段消解，名字在根目录内唯一）。
- `skills-settings.json` 的全局设置与项目 `custom` 设置新增 `packs`，键是技能包目录名：`true` 打开该包的全部技能，`false` 关闭全部技能（优先于 `selected` 里的名字），不在 `packs` 里的包按 `selected` 逐个决定。
- `PUT /v1/skills/settings` 接受 `packs`。省略 `packs` 时保留已存的值（不认识技能包的客户端不会清掉它），传 `{}` 清空。旧后端会以 400 拒绝含 `packs` 的请求（未知字段），客户端应先检查 `capabilities.skill_packs`。
- 兼容目录（项目 `.liveagent/skills`、`.agents/skills` 等）里的技能包与原来一样总是启用。

**提示词。** `<available_skills>` 中包内技能归在 `<skill_pack name="..." location="<包目录>">` 下。每个包最多占 12 KiB：放得下就完整列出；放不下时，路由技能（包内最浅的技能）完整列出，描述截到 1024 字符，其余技能按"路由技能的子孙在前、再按深度和 baseDir"的顺序写成紧凑的 `<skill name="..." dir="<相对包目录>">描述前 80 字符</skill>`，剩下放不下的计入 `<more count="N">`。

**读取授权。** 运行的只读授权仍是整个托管根目录和各兼容扫描目录，包内 `references/`、`scripts/` 等文件因此可读，不会扩大到用户主目录。

**SkillsManager。**

- `read` 接受嵌套路径，如 `reverse-skill/skills/radare2/references/x.md`。
- `validate` 传包目录名时检查整个包（至少一个可用技能，遍历问题作为 `warnings`）；传包内技能的名字或 baseDir 时做宽松检查；结果带 `pack`。
- `delete` 传包内技能（名字或 baseDir）会被拒绝并说明原因：技能包作为一个目录安装（通常是一个 git 仓库），只能整体删除；可以在启用设置里关掉单个技能。传包目录名则删除整个包。
- `install` 的本地目录、zip 或 GitHub 来源在根部没有 SKILL.md、但 4 层内有技能时，整体安装为技能包：包名取 `name` 参数，否则取 GitHub 仓库名（tree URL 取最后一级目录）、目录名或压缩包名。结果带 `pack` 和 `skills`（技能数），`skillFile` 指向路由技能。安装时跳过点开头的文件和目录（如 `.git`），单个技能同样如此。

`kn` 的 TUI 同样识别技能包：`$名字` 补全中包内技能的说明以 `[包名]` 开头，`/extensions skills` 把包内技能按包分组，`kn skills list` 显示所属包、baseDir 和被遮蔽的副本。TUI 没有启用设置，列出所有扫描到的技能。

### 归档会话（`capabilities.session_archive: 1`）

归档是会话元数据里的 `archived` 标记，`PATCH /v1/sessions/{id}` 带 `{"archived":true|false}` 设置，TUI 归档写的是同一个标记。归档会话默认不出现在列表里，客户端用查询参数 `archived` 选择：

| `archived` | 返回 |
| --- | --- |
| 省略或 `exclude` | 只有未归档会话（以前的行为） |
| `include` | 归档与未归档会话 |
| `only` | 只有归档会话 |

- 适用于 `GET /v1/sessions`、`POST /v1/sessions/snapshots`、`GET /v1/sessions/changes`、`GET /v1/sessions/workdirs`；其他取值返回 400。它与 `cwd`、`pinned`、`shared` 等条件同时生效，`total_count` 和快照 `total_count`/`workdirs` 按过滤后的结果计算。子代理会话和没有消息的会话在任何取值下都不列出。
- 每个会话摘要（列表、快照、changes 的 `session`、会话详情、PATCH 响应）都带 `archived: true|false`，`false` 也不省略。
- 归档与取消归档都是一次 `upsert` 变化，revision 与 PATCH 响应的 `sync_revision` 一致；TUI 或其他进程直接改文件的归档由扫描器（或 `rescan=1`）发现，同样报告。`listed` 按本次请求的 `archived` 计算：默认过滤下归档得到 `listed:false`（客户端从列表移除），取消归档得到 `listed:true`；`include` 下两种情况都是 `listed:true`，只是 `session.archived` 变化；`only` 下 `listed` 跟随 `archived`。
- 同一个变化游标可以用不同的 `archived` 读取，epoch/revision 与过滤无关；客户端应让快照与之后的 changes 使用同一个取值。
- 旧后端忽略这个参数，只返回未归档会话；摘要里的 `archived` 在为 `false` 时被省略，客户端按 `false` 处理。

### 其他进程中的回合（`capabilities.session_external_runs: 1`）

`kn` 的 TUI、`kn run` 与后端共用同一个会话目录。同一个会话同一时刻只允许一个进程运行回合：运行期间持有 `sessions/.runs/<id>.lock` 上的操作系统文件锁（进程退出或崩溃时由系统释放），并写 `<id>.json` 说明持有者（`client`、`pid`、`started_at`）。

- 后端的运行（`POST /v1/sessions/{id}/runs`、队列派发、`/compact`）在接受前取得租约；会话正在别处运行时返回 409，错误信息形如 `session is running in kn`。历史编辑、删除等改动消息的请求同样返回 409，存储层也拒绝非持有者的写入。
- 另一个进程正在运行时，会话摘要（列表、快照、changes 的 `session`、会话详情）带 `external_run: {client, pid?, started_at}`；结束后该字段消失，这两次变化都是一次 `upsert`。后端自己的运行不出现在这个字段里，客户端照常用运行事件。`client` 目前是 `kn`（TUI）、`kn run` 或 `backend`。
- TUI 在回合开始时（用户消息已在记录中）立即保存一次，之后在每一轮模型请求开始时保存检查点（至少间隔 2 秒），回合结束再完整保存；`kn run` 只在结束时保存。
- 每次写入会话记录、删除会话、取得或释放租约时，写入进程更新 `sessions/.sync` 计数。有 changes 订阅者时扫描器每 500ms 检查它，变化后立即完整扫描，所以列表约一秒内看到 TUI 的会话和状态变化；不写 `.sync` 的旧版本写入者仍由 30 秒一次的完整扫描兜底。存在外部运行时扫描器每 2 秒探测一次租约，以发现崩溃的进程。
- 后端缓存的会话运行时在接受新运行（以及写入命令记录、循环结果）前检查记录是否被别的进程改写，若是则从磁盘重新加载，回合接着磁盘上的记录继续；持有过期记录的保存被拒绝（`session history changed in another process`，409），不会覆盖另一个进程写入的消息。TUI 同样在回合开始前重新加载被 LiveAgent 改动过的会话，并重绘对话。
- 旧后端不报告 `external_run`，只在 30 秒扫描（或 `rescan=1`）时看到 TUI 写入的会话，也不拒绝并发运行；客户端在能力缺失时不显示外部运行状态。

### 记忆整理计划（`capabilities.memory_organizer_settings: 1`）

后端按计划运行记忆整理器（与 `POST /v1/memory/organizer` 的手动运行相同，trigger 为 `scheduled`）。计划放在 `/v1/settings`，与记忆开关 `memory` 并列：

```json
{"memoryOrganizer": {"enabled": true, "intervalHours": 24}}
```

- GET（以及 PUT 的响应）总是带 `memoryOrganizer`，给出生效值。优先级：保存过的设置 > 启动参数 `kn backend -memory-organizer-interval`（默认 24h，`0` 或负数也按 24h）> 默认值（开启、24 小时）。没有保存过设置时 `enabled` 为 `true`，`intervalHours` 来自启动参数（可以是小数，可能不在 1–168 之内）。
- PUT 接受 `memoryOrganizer:{enabled?,intervalHours?}`，至少给一个字段；省略的字段沿用当前生效值，保存结果总是同时含两个字段，此后启动参数不再起作用。`intervalHours` 是数字（可以带小数），范围 1–168（含两端）；超出范围、类型不对、空对象或未知字段返回 400，什么都不改。没有配置文件路径的嵌入后端返回 503。
- 保存位置是配置文件旁的 `<配置文件名>.memory-organizer.json`（默认 `config.json.memory-organizer.json`，内容 `{"enabled":bool,"intervalHours":number}`），不写进 config.json，所以不认识它的旧版本照常启动，回退后按启动参数运行。只改 `memoryOrganizer` 的 PUT 不写 config.json、不增加设置 revision；与其他设置一起 PUT 时先写计划文件再写 config.json，config.json 写失败会还原计划文件并返回 500。文件读不出或内容无效时后端在 stderr 报告并按启动参数运行，下一次成功保存会覆盖它。
- 保存后立即对正在运行的整理器生效，不需要重启：关闭后不再按计划运行（正在进行的一次整理照常完成），开启或修改间隔时倒计时从保存时刻重新开始，下一次计划运行在 `intervalHours` 之后；两次计划运行之间也按上一次结束后重新计时，不会重叠。手动整理不受 `enabled` 影响。
- TUI 的 `/memory` 列表与记忆面板显示同一份保存值（`organizer: every 6h (LiveAgent memory settings)`、`organizer: off ...`，没有保存值时为 `organizer: every 24h (backend default)`）；TUI 自己不运行整理器，也看不到后端的启动参数。
- 旧后端的 PUT 遇到 `memoryOrganizer` 这个未知字段返回 400，客户端在能力缺失时不要发送它。

### 联网搜索回退（`capabilities.web_search_fallback: 1`）

运行的 `options.search: "enabled"` 原本只对能原生搜索的 API 有效：Responses、Anthropic Messages、官方 Gemini 端点。Chat Completions 开启搜索会让运行失败，Gemini 中转端点在有函数工具时静默丢掉搜索。有这项能力的后端在这些 provider 上改为提供 `web_search` 函数工具，调用 `/v1/settings` 里 `webSearch` 配置的第三方搜索服务（Tavily、Brave Search、SearXNG、博查、Jina）。宿主的运行请求不需要改动，仍然只发 `search`。

- 生效条件（每次模型请求都重新判断）：`search` 为 `enabled`、当前模型的 API 不能原生搜索、`webSearch.available` 为 `true`。此时请求不再带原生搜索标记，工具列表里多出 `web_search`；`mode: "chat"` 的运行也会得到这一个工具。能原生搜索的 API 不受影响，不会同时收到 `web_search`。没有配置 `webSearch` 时行为与旧版本相同（Chat Completions 仍然报错，错误信息提示配置 `webSearch`）。
- `options.tools.policies` 可以写 `web_search`（即使当前没有提供这个工具也不会报 unavailable）：`deny` 不提供工具，`allow` 不再询问，`ask` 或省略按权限模式询问。计划模式下 `web_search` 询问而不是拒绝。`permission.requested` 的 `tool` 为 `web_search`，`command` 为搜索词，建议的可记住规则是整个工具 `web_search`。工具事件里的参数是 `{query, max_results?, recency?, domains?}`，结果是文本：`provider=`、`query=`、`results=N` 等头部行后跟编号列表（标题、URL、`published:`、摘要），最多 12000 个字符。
- 能力判断：`providers[].nativeWebSearchSupported`（只读，PUT 时可以原样带回，被忽略）说明该 provider 的 API 能否原生搜索，不是逐模型标记。Chat Completions 的 `deepseek-flash` 是模型级例外：即使 provider 标记为 `false`，也支持 DeepSeek 自有搜索，配置第三方服务不会取代它；同 provider 的其他模型仍使用回退。宿主在 `nativeWebSearchSupported || (api == "openai-completions" && model == "deepseek-flash") || webSearch.available` 时允许打开联网，否则提示用户配置搜索服务（模型名不区分大小写）。运行时与路由包装器按所选模型判断；联网关闭时两条搜索路径都不启用。Flash 开启思考时使用服务端网页工具；关闭或未指定思考且与本地工具并用时，使用 DeepSeek Search/Fetch API，不强制开启思考。

GET `/v1/settings`（以及 PUT 的响应）总是带 `webSearch`：

```json
{"webSearch": {"provider": "tavily", "baseUrl": "", "maxResults": 5, "apiKeyConfigured": true, "apiKeySource": "config", "available": true, "reason": "", "providers": ["tavily", "brave", "searxng", "bocha", "jina"]}}
```

- `provider`、`baseUrl`、`maxResults` 是 config.json 里保存的值（`maxResults` 为 `0` 表示默认 5；`baseUrl` 与 provider 的 `baseUrl` 一样去掉了凭据）。其余字段反映环境变量覆盖之后的生效状态：`apiKeyConfigured` 与 `apiKeySource`（`config` 或 `env`）说明是否有密钥及其来源，`available` 为 `false` 时 `reason` 说明原因（没有选择服务、缺少密钥、SearXNG 缺少 `baseUrl` 等）。密钥本身永远不返回。环境变量 `K_BRAIN_WEB_SEARCH_PROVIDER`、`K_BRAIN_WEB_SEARCH_BASE_URL`、`K_BRAIN_WEB_SEARCH_API_KEY` 与 `TAVILY_API_KEY`、`BRAVE_SEARCH_API_KEY`、`BOCHA_API_KEY`、`JINA_API_KEY` 优先于保存值。
- PUT 接受 `webSearch:{provider?, apiKey?, clearApiKey?, baseUrl?, maxResults?}`，省略的字段不变。`apiKey` 为空字符串表示保留已保存的密钥，`clearApiKey: true` 删除它，两者同时给出新密钥与 `clearApiKey` 返回 400；密钥可以是明文或 `$VAR`/`${VAR}` 引用，`!命令` 返回 400。`provider` 不在列表中、`maxResults` 不在 0–10、`baseUrl` 不是 http(s) 地址时返回 400，什么都不改。GET 返回的只读字段（`apiKeyConfigured`、`apiKeySource`、`available`、`reason`、`providers`）可以原样带回，会被忽略。
- 保存后对已有会话的下一次模型请求生效，不需要重启或重建会话。
- 旧后端的 PUT 遇到 `webSearch` 这个未知字段返回 400，旧版本读取含 `webSearch` 的 config.json 也会报未知字段；客户端在能力缺失时不要发送它。

### 跨会话用量统计（`capabilities.usage_stats: 1`）

`GET /v1/usage/stats?days=N&tz=IANA` 汇总会话目录里所有工作区、所有会话（含归档会话与子代理转录）的 token 用量。`days` 默认 371（52 周加余量），取整到 1–1100，不是整数时返回 400；`tz` 是按天分组用的 IANA 时区名，默认为后端进程的本地时区（取不到名称时 `timezone` 为 `Local`），无效名称返回 400 `{version, error}`。所有数字只统计 `tz` 下最近 `days` 天（含今天）。

```json
{
  "generatedAt": "2026-11-15T18:00:00+08:00",
  "timezone": "Asia/Shanghai",
  "days": 371,
  "totals": {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0, "turns": 0, "sessions": 0, "costUSD": null},
  "summary": {"activeDays": 0, "currentStreak": 0, "longestStreak": 0, "topModel": "", "longestSession": null},
  "daily": [{"date": "2026-11-15", "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0, "turns": 0, "models": {"<model>": 0}}],
  "models": [{"model": "", "provider": "", "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0, "turns": 0, "costUSD": null}]
}
```

- `input` 与 `usage.updated` 的 `input_tokens` 同义：每次请求的完整输入，已经包含缓存读取与写入；`cacheRead`、`cacheWrite` 是其中的部分。`total = input + output`。
- `turns` 是带用量的模型回复数；只有元数据合计的旧会话计入 token，不计 `turns`。`sessions` 是范围内有用量的会话数，子代理的用量归到父会话。
- `daily` 只列有用量的日子，按日期升序；`daily[].models` 以不含 provider 的模型名为键，值为当天 `total`。`models` 按 `(model, provider)` 分组、按 `total` 降序；`summary.topModel` 是各 provider 合计后 `total` 最高的模型名。
- `currentStreak` 是截至今天的连续有用量天数，今天还没有用量时从昨天算起；`longestStreak` 只看范围内。`longestSession` 是范围内 `total` 最高的会话 `{id, title, total}`，没有用量时为 `null`。
- `costUSD` 用模型目录价格计算（与 kn 的 `/usage` 卡片相同），按订阅计费的 provider 不计价；单个模型无价格时为 `null`，总计只加能计价的模型，一个都不能计价时为 `null`。
- 时间与去重：回复按 `sent_at` 分日，旧回复没有时间时取它之前最近一条消息的时间，再没有就取会话创建时间；消息里没有用量的旧会话把元数据合计记在最后活动那天。子代理的转录会话与父会话的子代理合计是同一份用量，只计一次（父会话没有带用量的子代理转录时才用合计）；分支与复制出的会话里同 id 的回复只计一次。
- 第一次请求要读一遍所有会话文件；之后按文件大小与修改时间缓存每个会话的用量，只重读变化的会话。统计不获取会话锁，不会阻塞正在进行的运行。

### 项目目录信任（`capabilities.folder_trust: 1`）

项目级 Markdown 提示词模板（`<项目>/.liveagent/prompts/`）只在项目目录受信任时才出现在 `/v1/prompts` 的 `files`、`/v1/prompts/templates/{name}/expand` 和 `/v1/commands` 里。信任记录与 TUI 共用 `trusted_folders.toml`（格式见 README），桌面端用下面的接口读取和修改，接口需要后端令牌，与其他接口相同：

| 方法 | 请求 | 作用 |
|---|---|---|
| GET | `/v1/trust?workdir=<绝对路径>` | 读取信任状态 |
| POST | `/v1/trust`，请求体 `{"workdir":"<绝对路径>","trusted"?:true}` | 信任该目录；`trusted:false` 等同撤销 |
| DELETE | `/v1/trust?workdir=<绝对路径>` | 撤销该目录的信任 |

三种请求都返回 `{"version","workdir","trusted","source"?}`：`workdir` 是解析符号链接后的规范路径（与提示词、斜杠命令查找使用同一规范化），信任记录按它保存；`trusted` 是该目录当前是否受信任；`source` 是做出这个判断的记录（目录自身或最近的祖先目录），没有记录覆盖时省略。

- `workdir` 为空、不是绝对路径、不存在或不是目录时返回 400 `{version, error}`，不写任何记录；请求体有未知字段同样返回 400；其他方法返回 405。
- 信任目录同时覆盖它的子目录。撤销删除该目录自己的记录；若某个祖先目录仍会让它受信任，则改为写入该目录的 `trusted = false` 记录，撤销对它和它的子目录生效，祖先目录的信任不变。撤销没有记录的目录是空操作。
- 每次请求都重新读取 `trusted_folders.toml`，修改后下一次 `/v1/prompts`、`/v1/commands` 或提示词命令运行立即反映，不需要重启后端；TUI 在下次启动时看到同一结果。
- 同一后端进程内的修改串行写入（临时文件加重命名）；与同时运行的 TUI 之间没有跨进程锁。
- 旧后端没有这个接口（返回 404），客户端在能力缺失时应隐藏信任开关，提示用户在 `kn` 终端界面中打开一次该目录来信任它。

### 插件（`capabilities.plugins: 1`）

`kn backend` 与 `kn run`、TUI、ACP 一样加载插件（`plugin.json`，格式见 `kn plugin` 与 `internal/plugins`）：每个会话都加载用户插件（数据目录 `plugins/<名称>/`），只有会话工作目录受信任（见上一节）时才加载项目自己的插件（`<项目>/.liveagent/plugins/<名称>/`）；同名时项目插件优先。信任与启用状态在每一轮开始时重新读取：信任、撤销信任、启用、停用都从下一轮生效，不需要重启后端或重建会话。

插件给会话带来三样东西：

- 工具：模型看到的名字是 `plugin_<插件>_<工具>`。每次调用先走本次运行的权限判定（与 Bash 等内置工具相同：计划模式与 `approval_policy:"deny"` 拒绝，`normal` 等模式弹出 `permission.requested`，`tool` 为该工具名），再运行插件命令；运行时再次确认插件仍然启用、目录仍然受信任。
- 系统提示词：已启用插件的 `prompt` 追加在系统提示词末尾，每轮重新生成。
- 钩子：`hooks` 里的生命周期事件（`SessionStart`、`PreToolUse`、`PostToolUse`、`Stop` 等）运行对应命令。

接口需要后端令牌。`workdir` 是会话工作目录的绝对路径，GET/DELETE 放在查询参数里，POST 可放在请求体或查询参数里；省略时只看用户插件。

| 方法 | 请求 | 作用 |
|---|---|---|
| GET | `/v1/plugins?workdir=&since=&wait=` | 列出插件；带 `since`（上次的 `revision`）与 `wait`（秒，最多 60）时，在 `revision` 超过 `since` 或超时前不返回 |
| POST | `/v1/plugins/{id}/enable`、`/v1/plugins/{id}/disable`，请求体 `{"workdir"?}` | 启用 / 停用；状态保存在数据目录 `plugins.json`，与 `kn plugin enable|disable` 共用 |
| POST | `/v1/plugins/install`，请求体 `{"path":"<插件目录绝对路径>","workdir"?}` | 把本机目录复制到用户插件目录（与 `kn plugin install` 相同），默认停用，除非清单写了 `"enabled":true`；已存在同名插件返回 409 |
| DELETE | `/v1/plugins/{id}?workdir=` | 删除用户插件目录；项目插件属于项目，返回 409 |
| POST | `/v1/plugins/reload`，请求体 `{"workdir"?}` | 重新扫描插件目录（命令行或手工改动后） |

除错误外都返回列表：

```json
{
  "version": "kbrain.agent.v1",
  "workdir": "/Users/me/project",
  "trusted": true,
  "revision": 7,
  "plugins": [
    {
      "id": "build",
      "name": "build",
      "version": "1.0.0",
      "description": "Project build helpers",
      "source": "workspace",
      "enabled": true,
      "path": "/Users/me/project/.liveagent/plugins/build",
      "contributes": {
        "tools": ["plugin_build_run"],
        "toolDetails": [{"name": "run", "modelName": "plugin_build_run", "description": "Run the build"}],
        "hooks": ["SessionStart"], "prompt": true
      }
    }
  ],
  "errors": ["/Users/me/project/.liveagent/plugins/broken: invalid plugin name \"bad name\""]
}
```

- `id` 等于 `name`，用于路由。`source` 是 `user` 或 `workspace`。`trusted` 是 `workdir` 当前是否受信任（未受信任时列表里没有项目插件）；`workdir` 是规范化后的路径。`errors` 是加载失败的插件目录（清单无效等），这些插件不出现在 `plugins` 里。
- `revision` 是进程内的变化序号：本后端的启用、停用、安装、删除、重新扫描都会推进它并唤醒等待中的 GET；其他进程或手工做的改动在下一次 GET 发现列表变化时推进。客户端可循环 `GET /v1/plugins?since=<revision>&wait=30` 保持列表最新。后端重启后序号从 0 开始，客户端看到更小的 `revision` 时应直接采用新列表。
- `contributes.tools` 是模型看到的工具名；`contributes.toolDetails`（`capabilities.plugin_tool_names`）按同一顺序列出 `{name,modelName,description}`，`name` 是清单里的工具名，`modelName` 是模型看到的名字。
- 模型看到的工具名是 `plugin_<key>_<工具>`。插件名不含 `_` 时 `key` 就是插件名（与以前相同）；含 `_` 时每个 `_` 换成 `-` 并追加 `-<8 位十六进制 FNV-1a 哈希>`，例如插件 `my_tools` 的 `run` 是 `plugin_my-tools-xxxxxxxx_run`，这样名字总能拆回唯一的插件和工具。客户端不要自己拼名字，取 `toolDetails[].modelName`；权限规则、`options.tools` 等需要工具名的地方都用它。旧后端上名字含 `_` 的插件工具无法运行。
- 清单目前只有工具、钩子、提示词与界面四种贡献，没有命令、技能或 MCP server。清单里本后端不认识的字段被忽略（`capabilities.plugin_manifest_unknown_fields`），较新的插件可以在旧一些的后端上加载；旧后端会把这样的插件报在 `errors` 里。
- `id` 不存在返回 404；`workdir` 或 `path` 不是绝对路径、请求体有未知字段返回 400；其他方法与路径返回 404；嵌入方没有配置插件（`Options.Plugins` 为空）时所有插件路由返回 501。旧后端没有这些路由（404），客户端应隐藏插件管理并提示用户在终端用 `kn plugin`。

### 插件界面（`capabilities.plugin_ui: 1`）

插件可以在 `plugin.json` 里声明静态网页面板，客户端在沙箱 iframe 里显示。只有界面的插件不需要 `command`（`command` 只在有 `tools` 时必填）。

```json
{
  "name": "board",
  "version": "1.2.0",
  "enabled": true,
  "ui": {
    "root": "ui",
    "panels": [
      {"id": "main", "title": "Board", "entry": "index.html", "placement": "sidePane"},
      {"id": "prefs", "title": "Board settings", "entry": "settings/index.html", "placement": "settings"}
    ]
  }
}
```

- `root`：插件目录下存放界面文件的子目录，默认 `ui`；只提供这个目录里的文件。
- `panels`：最多 8 个。`id` 与插件名规则相同且不重复；`title` 1–80 个字符；`entry` 是 `root` 下的 `.html`/`.htm` 相对路径；`placement` 是 `sidePane`（侧边栏）或 `settings`（设置页）。路径段不能为空、`.`、`..` 或以 `.` 开头，不能含 `\`、`:`。
- 不符合规则的清单整体加载失败，出现在列表的 `errors` 里。更旧的 kn 拒绝不认识的字段，所以不认识 `ui` 字段时同样加载失败。

`GET /v1/plugins` 的每个插件多一项 `contributes.ui`：

```json
"contributes": {
  "tools": [], "hooks": [], "prompt": false,
  "ui": [
    {"id": "main", "title": "Board", "placement": "sidePane", "entry": "index.html",
     "url": "/v1/plugin-assets/3f9c0a6d2b7e4c1a8d5f0e2b6a9c7d41/board/index.html"}
  ]
}
```

`url` 只在插件启用时出现，是后端上的路径，客户端加上后端地址作为 iframe 的 `src`。页面里的相对路径（`app.js`、`assets/style.css`）解析到同一前缀下。

插件启用、但面板入口文件打不开时（`capabilities.plugin_ui_errors`），该面板多出 `errorCode` 与 `error`（给人看的说明），`url` 照常给出：

| `errorCode` | 含义 |
|---|---|
| `ui_root_missing` | 插件目录下没有 `root` 目录（或它不是目录） |
| `entry_missing` | `root` 下没有 `entry` 文件 |
| `entry_not_file` | `entry` 不是普通文件（目录等） |
| `entry_too_large` | `entry` 超过 10 MiB |

客户端应把带 `errorCode` 的面板显示为错误状态（显示 `error`），而不是加载一个空白 iframe。这一检查只在列出插件时做一次；文件之后才被删除时由下面的资源错误发现。

`GET|HEAD /v1/plugin-assets/{grant}/{plugin}/{path}`：

- 不检查 `Authorization`（iframe 发不出请求头）；`grant` 是后端进程内随机密钥对项目、插件名与插件目录的 HMAC，只由 `GET /v1/plugins` 发放。每次请求都重新确认：插件仍启用、目录没变、项目插件所在目录仍受信任；任何一项不满足、路径不合规、文件不存在或不是普通文件都返回 404。后端重启后旧 URL 全部失效，重新取列表即可。
- 失败的请求（`capabilities.plugin_ui_errors`）带响应头 `X-K-Brain-Asset-Error`：`asset_not_found`（插件有效，但 UI 根目录下没有这个文件，或它不是普通文件）、`plugin_unavailable`（插件停用、目录变了、项目不再受信任或链接已失效，应重新取列表）、`asset_too_large`（413）。状态码不变。请求的是 `.html`/`.htm` 时，响应体是一个沙箱小页面（`Content-Security-Policy: sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'`），显示说明并向父窗口发送通知 `{"jsonrpc":"2.0","method":"host.panelLoadError","params":{"status":404,"code":"asset_not_found","message":"..."}}`，宿主据此把面板显示为错误状态（见 [sandboxed-ui.md](sandboxed-ui.md)）；其他文件的响应体是纯文本。路径格式本身不合规（编码的 `/`、grant 长度不对等）的 404 不带这个头。
- 文件从 UI 根目录用 `os.Root` 打开，符号链接不能离开 UI 根目录；编码的 `/`（`%2F`）被拒绝；单个文件超过 10 MiB 返回 413；其他方法返回 405。
- `Content-Type` 按扩展名取自白名单（html、js/mjs、css、json、map、txt、svg、png、jpg、gif、webp、avif、ico、woff/woff2、ttf/otf、mp3、wav、mp4、webm、wasm），其他一律 `application/octet-stream`。
- 每个响应都带：

```
Content-Security-Policy: sandbox allow-scripts; default-src 'none'; script-src <base> 'unsafe-inline'; style-src <base> 'unsafe-inline'; img-src <base> data: blob:; font-src <base> data:; media-src <base> data: blob:; connect-src 'none'; frame-src 'none'; worker-src 'none'; form-action 'none'; base-uri 'none'; object-src 'none'
X-Content-Type-Options: nosniff
Cache-Control: no-store
Referrer-Policy: no-referrer
Cross-Origin-Resource-Policy: cross-origin
```

  `<base>` 是 `http://<请求的 Host>/v1/plugin-assets/{grant}/{plugin}/`，页面只能加载本插件的文件。从不设置 Cookie。

`POST /v1/plugins/{id}/tools/call`，请求体 `{"name":"<工具名>","arguments":{...},"workdir"?:"<绝对路径>"}`，需要令牌：执行插件 `{id}` 自己的工具，返回 `{"version","name":"<模型看到的工具名>","toolName":"<清单里的工具名>","output","failed","cancelled"}`。`name` 先按清单里的工具名查找，找不到再按模型看到的名字（`toolDetails[].modelName`，必须属于插件 `{id}`）查找；旧后端只接受清单里的名字，也不返回 `toolName`。插件停用、项目未受信任或工具不存在返回 404，缺少 `name` 返回 400。这条调用不在运行中，不会弹出审批：客户端（面板的 `host.callTool`）必须先征得用户同意。

面板与宿主之间的 postMessage 协议（JSON-RPC 2.0：`host.initialize`、`host.getTheme`、`host.notify`、`host.openFile`、`host.callTool` 与 `panel.*` 通知）和 iframe 的沙箱要求见 [sandboxed-ui.md](sandboxed-ui.md)。

### MCP 应用界面（`capabilities.mcp_apps: 1`）

MCP 工具可以在 `tools/list` 的 `_meta` 里声明一个界面资源：`_meta.ui.resourceUri`（早期草案的扁平键 `_meta["ui/resourceUri"]` 也认），值必须是 `ui://` URI，其他值忽略。客户端在工具结果旁的沙箱 iframe 里显示它。

- `tool.result`：MCP 工具**成功**返回、所在 server 已启用且 `workspaceRoots` 允许会话工作目录、`policy` 不是 `deny` 时，`tool_result` 多一项 `ui_resource`：

```json
{"tool_result": {
  "id": "call_1", "name": "mcp__apps__board", "output": "board ready",
  "ui_resource": {"server_id": "apps", "uri": "ui://board/view.html", "tool_name": "board",
                  "url": "/v1/sessions/s_123/mcp-ui?server=apps&uri=ui%3A%2F%2Fboard%2Fview.html",
                  "result": {"content": [{"type": "text", "text": "board ready"}],
                             "structuredContent": {"series": [1, 2, 3]}, "_meta": {"...": "..."}},
                  "result_url": "/v1/sessions/s_123/mcp-ui/results/4f0c9a1e7b2d3c5a6e8f9a0b1c2d3e4f"}
}}
```

  失败、取消的调用与没有声明界面的工具不带这一项。
- 原始结果（`capabilities.mcp_apps_structured_content`）：`server_id` 是配置里的 server id，`tool_name` 是 server 自己的工具名（不带 `mcp__` 前缀）。`result` 是 server 返回的 `CallToolResult` 原样 JSON（`content`、`structuredContent`、`_meta`、`isError`），客户端把它作为 MCP Apps 的 `ui/notifications/tool-result` 交给界面，而不是只给 `output` 文本。原始结果不超过 1 MiB 时保存在会话数据目录下，`result_url` 指向它；不超过 256 KiB 时还内联在 `result` 里。超过 1 MiB 的结果既不内联也不保存（没有 `result` 和 `result_url`），客户端只能退回 `output`。原始结果只给界面，模型看到的仍是 `output`。
- 历史：`/v1/sessions/{id}/history`、`GET /v1/sessions/{id}`（及其快照）里 assistant 消息的 `tool_calls[]` 对这类调用带同样的 `ui_resource`（`server_id`、`uri`、`tool_name`、`url`，结果保存过时有 `result_url`），但不带内联的 `result`：客户端重新显示界面时从 `result_url` 取。公开分享的会话去掉 `ui_resource`。
- 子代理：子代理调用这类工具时，`trajectory.child.tool_end` 带同样的 `ui_resource`（含内联 `result`），`/subagents/{task}` 的转录在对应的工具调用上带 `ui_resource`（不带内联 `result`）。
- `GET|HEAD /v1/sessions/{id}/mcp-ui/results/{ref}`（需要令牌）：`ref` 只取自 `result_url`（32 位小写十六进制，按工具调用 id 生成）。返回 `application/json`、`Cache-Control: no-store`：`{"version":"kbrain.agent.v1","tool_call_id","server_id","tool_name","uri","result":<CallToolResult>}`。会话不存在、`ref` 格式不对或没有保存过：404；其他方法：405。删除会话时一起删除。
- `GET /v1/mcp/tools` 与 `POST /v1/mcp/search` 的条目在工具声明界面时多一项 `uiResourceUri`。
- 可见性（`capabilities.mcp_tool_visibility`）：工具的 `_meta.ui.visibility` 是 `["model"]`、`["app"]` 或两者。没有这一项、空数组或只有不认识的值时视为两者都可见。只含 `app` 的工具不出现在模型的工具列表里（也不能被模型或 ToolSearch 调用），只能由界面经 `POST /v1/mcp/tools/call` 带 `"caller":"app"` 调用；只含 `model` 的工具不能由界面调用。`GET /v1/mcp/tools?audience=model|app` 按调用方过滤（默认 `model`，其他值 400），每项带 `visibility`（例如 `["model","app"]`）；`POST /v1/mcp/search` 只搜模型可见的工具。
- `GET /v1/sessions/{id}/mcp-ui?server=<server id>&uri=<ui:// URI>`（需要令牌）：按会话工作目录，用 MCP `resources/read` 读取该资源并返回 HTML。只读取**该 server 某个未被 deny 的工具声明过的** `ui://` URI，不能用来读任意资源。取 `mimeType` 以 `text/html` 开头（含 `text/html;profile=mcp-app`）的内容，`text` 或 `blob` 均可，最大 4 MiB，读取超时 30 秒。
  - 缺少 `server` 或 `uri` 不是 `ui://`：400；会话不存在、server 未启用或不允许、URI 未被声明：404；server 读取失败、没有 HTML 内容或超过大小：502；没有 MCP：501。
  - 响应头 `Content-Security-Policy: sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline' <资源域>; style-src 'unsafe-inline' <资源域>; img-src data: blob: <资源域>; font-src data: <资源域>; media-src data: blob: <资源域>; connect-src <连接域或 'none'>; frame-src 'none'; worker-src 'none'; form-action 'none'; base-uri 'none'; object-src 'none'`，以及 `nosniff`、`no-store`、`no-referrer`。资源域与连接域取自资源内容的 `_meta.ui.csp.resourceDomains` / `connectDomains`，只保留纯 `https://host[:port]` 源（最多 16 个），其余丢弃。
  - 同一策略（不含 `sandbox`）还以 `<meta http-equiv="Content-Security-Policy">` 插到文档最前面（`<head>` 之后，否则 doctype 之后，否则开头），客户端用 `fetch` 带令牌取回后放进 `<iframe sandbox="allow-scripts" srcdoc=...>` 时限制同样生效。
- 已知缺口：不处理 `_meta.ui.domain`、`prefersBorder` 等显示提示；超过 1 MiB 的原始结果不转发。界面与宿主之间的消息协议见 [sandboxed-ui.md](sandboxed-ui.md)。

### HTML 预览（`capabilities.html_previews: 1`）

客户端在 `POST /v1/sessions/{id}/runs` 的 `options` 里传 `"html_previews": true`，表示自己能显示 HTML 预览卡片。旧版后端不认识这个选项，会以 400（未知字段）拒绝整次运行，客户端必须先确认 `html_previews == 1`。只有带该选项的运行才会：

1. 在系统提示末尾追加一段说明，告诉模型可以把完整的 HTML 文档放进信息串为 `html preview` 的围栏代码块（例如 ` ```html preview `），并说明预览在无网络的沙箱里运行。不传该选项的运行（其他客户端、CLI）系统提示不变。
2. 识别预览并发布 `preview.created` 事件：
   - 助手文本中的围栏块：开头围栏为至少 3 个 `` ` `` 或 `~`（最多缩进 3 个空格），信息串按空白归一后恰为 `html preview`；结束围栏为同一字符、长度不小于开头且没有信息串。未闭合、为空或超过 2 MiB 的块忽略。普通的 ` ```html ` 块不算预览。
   - `Write`（以及 CLI 工具集的 `write`）成功写入扩展名为 `.html` / `.htm` 的文件：预览内容取自工具参数 `content`，不会回读磁盘。
   - 每次运行最多 16 个预览。

```json
{"type": "preview.created", "run_id": "run_1", "payload": {
  "preview_id": "3f2a9c0d1b7e4a56", "source": "message", "title": "销售图表",
  "bytes": 2048, "url": "/v1/sessions/s_123/previews/3f2a9c0d1b7e4a56",
  "created_at": "2026-11-22T08:00:00Z"
}}
```

   `source` 为 `message` 或 `file`；`file` 时另有 `path`（工具收到的原样路径）与 `tool_call_id`。`title` 取文档的 `<title>`，没有时取文件名。`preview_id` 是内容 SHA-256 的前 16 个十六进制字符，同一内容得到同一 id。事件写入会话日志，重放历史时同样出现。
- `GET /v1/sessions/{id}/previews`（需要令牌）：`{"version": "...", "previews": [<同上 payload>...]}`，按创建时间排序。
- `GET /v1/sessions/{id}/previews/{previewId}`（需要令牌）：返回 HTML，`Content-Security-Policy: sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:; connect-src 'none'; frame-src 'none'; worker-src 'none'; form-action 'none'; base-uri 'none'; object-src 'none'`，另有 `nosniff`、`no-store`、`no-referrer`；同一策略（不含 `sandbox`）以 `<meta>` 插在文档开头。id 不是 16 位小写十六进制或不存在：404。
- 预览保存在事件目录的 `previews/<会话 id>/` 下，删除会话时一并删除。
- 显示方式见 [sandboxed-ui.md](sandboxed-ui.md)：用带令牌的 `fetch` 取回，放进 `<iframe sandbox="allow-scripts" srcdoc=...>`；预览不提供任何宿主方法。

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

### MCP 配置与运行选择

`PUT /v1/mcp` 的请求体与 GET 相同：`{servers:[{id,name,enabled,transport:"stdio"|"http",command,args,env,cwd,url,headers,auth,timeoutMs,workspaceRoots,policy}],selected}`。`name` 是可选的显示名，只用于 `GET /v1/mcp/tools` 的 `serverLabel`，不影响工具名与策略。

- 整体替换：请求里没有的 server 被删除，省略的 `selected`、`policy`、`workspaceRoots` 被清空。唯一的例外是凭据：已存在的 id 省略 `env`/`headers`（或给 `null`）时沿用旧值，给 `{}` 才清空；新旧都是 oauth 且新的 `credentialRef` 为空时沿用旧的。GET 对这些值脱敏，所以 GET 改完再 PUT 不会丢凭据。id 去掉首尾空白，空 id 与重复 id 直接丢弃；未知字段与校验失败返回 400。每次成功的 PUT 都会重连全部 server；重建失败时恢复之前的配置。
- `policy` 按 server 配置，是 `{工具名: "allow"|"ask"|"deny"}`，值必须是这三个小写字符串。键是模型看到的完整工具名 `mcp__<serverKey>__<工具名>`（取 `GET /v1/mcp/tools` 返回的 `tools[].name`，不要自己拼：id 含 `[A-Za-z0-9_-]` 以外的字符或 `__` 时 serverKey 会加哈希后缀，工具名里的其他字符换成 `_`），原始 MCP 工具名不会匹配。查找先按完整名精确匹配（区分大小写），没有再用 `"*"`；没有其他通配。
  - `allow` 与不设相同。`deny`：工具仍出现在模型的工具列表里，调用时直接返回错误 `MCP tool "<name>" is denied by policy`，所有模式（含 `full_access`、无人值守运行）都生效。`ask`：调用前走本次运行的权限判定，规则与内置工具相同——计划模式与 `approval_policy:"deny"` 拒绝，deny/ask 规则任何模式都生效，`full_access`/`always` 直接放行，记住的 allow 规则放行，`normal`/`auto_edit`/`auto_review` 弹出 `permission.requested`（可"总是允许"，记成整个工具名的规则）；定时任务、loop 等无人值守运行里 `ask` 直接拒绝（`unattended scheduled runs cannot request interactive approval`）。
  - 没有 `policy`（或 `allow`）的 MCP 工具不经过权限判定：`normal`、`auto_edit`、`full_access` 和无人值守运行里都直接执行，`approval_policy:"deny"` 也拦不住；只有计划模式（MCP 工具被移出工具列表）、`mode:"chat"`（没有工具）、运行选项 `options.tools.policies` 里的 `deny`（移除）/`ask`/`allow`（走权限判定），以及 AI 审查（`auto_review` 或全局 AI 审批，审查不通过时询问）会拦它。客户端要求"每次确认"时应设 `ask`。
  - `POST /v1/mcp/tools/call` 同样应用 `selected`、`workspaceRoots` 与 `policy`，但这条 HTTP 调用没有运行上下文，不会弹出 `permission.requested`。`deny` 生效；`ask` 见下文的审批流程（`capabilities.mcp_tool_call_approval`，旧后端直接执行）。
- `selected` 是默认的 server id 白名单：为空表示全部已启用的 server；不校验 id 是否存在。

`GET /v1/mcp/tools` 的每项（`capabilities.mcp_tool_server_ids`）：

- `name`：模型看到的完整工具名 `mcp__<serverKey>__<工具名>`，策略键与 `POST /v1/mcp/tools/call` 的 `name` 都用它。
- `serverId`：配置里的 server id（`PUT /v1/mcp` 的 `servers[].id`）。旧后端在这里给的是 serverKey，id 只含 `[A-Za-z0-9_-]` 且不含 `__` 时两者相同。
- `serverKey`：工具名里的 server 部分。
- `serverLabel`：server 的显示名 `name`，没有时等于 `serverId`。
- `serverToolName`：server 自己的工具名（未经替换）；`toolName` 是工具名里替换过字符的部分。
- `visibility`、`uiResourceUri`：见“MCP 应用界面”。

`POST /v1/mcp/tools/call`，请求体 `{"name","arguments"?,"cwd"?,"caller"?:"model"|"app","approval_id"?}`：

- `caller` 默认 `model`，界面发起的调用传 `app`；每种调用方只能调用自己可见的工具（见可见性），其他值 400。旧后端以 400（未知字段）拒绝 `caller` 与 `approval_id`：客户端只在 `capabilities.mcp_tool_visibility`、`capabilities.mcp_tool_call_approval` 为 1 时分别发送。
- 响应：`{"name","output","failed","cancelled","status","result"?,"approval"?}`，`status` 是 `completed`、`failed`、`cancelled` 或 `approval_required`。工具不存在、被 deny 或对调用方不可见时仍是 200，`status:"failed"`。工具返回了原始 `CallToolResult`（不超过 1 MiB）时 `result` 是它的 JSON，界面的 `tools/call` 应直接返回它。
- 策略为 `ask` 的工具不立即执行，而是返回 200：

```json
{"name": "mcp__apps__board", "output": "MCP tool mcp__apps__board needs the user's approval: repeat the call with approval_id",
 "failed": true, "cancelled": false, "status": "approval_required",
 "approval": {"id": "mcpa_9b2f0c3e4d5a6b7c8d9e0f1a2b3c4d5e", "name": "mcp__apps__board", "server_id": "apps",
              "tool_name": "board", "policy": "ask", "arguments": {"x": 1}, "expires_at": "2026-11-22T10:05:00Z"}}
```

  客户端向用户显示 server、工具与参数，用户同意后用**相同的** `name`、`arguments`、`cwd`、`caller` 加上 `approval_id` 再请求一次，这次才执行。批准 5 分钟内有效、只能用一次，与调用绑定（参数的空白不计）；未知、过期、已用过或参数不一致的 `approval_id` 返回 409（不一致时该 id 也作废）。后端最多同时保留 256 个待批准的调用，多出时丢弃最早的。用户拒绝时客户端不再请求即可。
- `workspaceRoots` 按路径层级判断会话工作目录是否在某个根之内（不是字符串前缀，不解析符号链接）；为空表示任何目录都可用。判断用的是会话工作目录，不是运行选项里的 `workspace_roots`。

运行选项 `mcp_server_ids`（`RunOptions.MCPServerIDs`）：

- 省略、`null` 与 `[]` 含义相同：使用配置里的 `selected`，`selected` 也为空时使用全部已启用且 `workspaceRoots` 允许的 server。
- 非空时只用列出的 server（再与已启用、`workspaceRoots` 取交集），替换而不是叠加 `selected`；未知 id 被忽略。因此不能用这个字段关闭全部 MCP，要关闭只能传一个不存在的 id（例如 `["__none__"]`），或用 `mode:"chat"`、`options.tools` 的 `deny`。
- 每次运行开始时重新计算，只作用于本次运行，不会粘在会话上；下一次运行不传就回到 `selected`。例外是后台子代理完成后的唤醒运行，它沿用上一次用户运行的全部选项（包括 `mcp_server_ids`）。子代理在启动时复制父运行当前的 MCP 工具集。
- `options.tools` 里的工具名在应用本次 `mcp_server_ids` 之前校验，按的是会话当前（上一次运行留下的）工具集；针对本次才新加入的 MCP 工具的策略可能被拒为未知工具。

### 技能设置的工作目录覆盖

`GET /v1/skills?workdir=<路径>` 返回 `{version,revision,rootDir,skills,shadowed,invalid,enabled,selected,settings,warnings}`，`settings` 是该目录合并后的 `{enabled,selected,mode?,skillNames?,packs?}`。`PUT /v1/skills/settings` 的请求体是 `{workdir?,enabled,selected,mode?,skillNames?,packs?}`（未知字段 400，其他校验失败 422；`packs` 见上文技能包），响应 `{version,revision,settings}`，`settings` 是合并后的结果。`revision` 是全局计数，每次保存加一。

- 不带 `workdir` 时整体替换全局设置；省略的 `enabled` 按 `false` 处理，会关闭全部技能。省略 `packs` 时保留已存的值。
- 带 `workdir` 时写该目录的覆盖。目录必须存在；按解析符号链接后的绝对路径识别（相对路径按后端进程的工作目录解析）。`mode` 为 `inherit`、`custom`、`off` 或空；空等于 `custom` 且 `skillNames = selected`。读取时从全局设置出发：`custom` 用 `skillNames` 作为 `selected`、用自己的 `packs`，`off` 关闭技能，`inherit` 完全跟随全局；覆盖自己的 `enabled`/`selected` 不参与合并，所以全局 `enabled:false` 时 `custom` 也打不开技能。`skills-creator` 与 `skills-installer` 总在 `selected` 里。
- 没有删除覆盖的接口；清除覆盖就 PUT `{workdir, mode:"inherit", enabled:true, selected:[], packs:{}}`（不支持技能包的后端去掉 `packs`），之后该目录完全跟随全局（条目仍保存在文件里）。
- 运行按会话的工作目录取用：系统提示词每一轮重新生成，技能部分按会话 cwd 读取合并后的设置，所以保存后的下一轮就生效，不需要重载会话。会话 cwd 已不存在时退回未托管的技能扫描。子代理不带技能提示。

### `usage.updated`

载荷是 `{input_tokens,output_tokens,cached_tokens,cache_write_tokens}`，每个字段为 0 时省略（可能是 `{}`）。它是单次模型请求的用量，不是累计值；`input_tokens` 是该次请求的完整输入（含缓存读写的部分，大致等于当时的上下文大小），`cached_tokens`/`cache_write_tokens` 是其中缓存读取、写入的部分。没有上下文窗口字段。

- 运行中主代理每次模型请求结束后发一条（包括工具轮数用尽后的收尾请求，以及出错的请求），`run_id` 为当前运行；用户、定时、loop 与唤醒运行都一样。客户端自己累加得到本次运行的合计；`run.completed` 不带用量。
- 手动压缩（`POST /v1/sessions/{id}/compact`）在 `compaction.completed` 之前发一条，是压缩请求的用量；运行中的自动压缩不单独发。
- 子代理不发 `usage.updated`；它的用量在 `subagent.*` 载荷的 `usage`（子代理全部请求的合计）里。

### 子代理工具

模型可见的五个工具只在主代理上，子代理不能再派生或控制其他子代理。`status`/`steer`/`cancel`/`followup` 只能针对后台子代理（前台子代理不进任务表，返回 unknown subagent）；后台子代理 id 由描述的前几个词加序号组成（例如 `fix-login-tests-1`，没有描述时为 `subagent-task-N`），前台为 `foreground-N`。任务表属于当前 Agent，后端重启、切换模型或工作目录后旧 id 不能再被控制。

| 工具 | 参数 | 结果 |
| --- | --- | --- |
| `subagent` | `prompt`（必填）、`description`、`background`（默认 false）、`model`、`provider`、`effort`、`worktree` | 前台：子代理最终报告（最多 50,000 字节，worktree 模式追加 `Worktree: <路径>`），失败时 `Error: …`；后台：`Started background subagent <id>: <description>. …`，报告稍后以 `[subagent <id> <status>] <description>` 通知送回并触发唤醒运行 |
| `subagent_status` | `id`（可选） | 带 id：JSON `{id,description,status,following_up,can_followup,report?}`；不带：`{tasks:[…不含 report],total,truncated}`，最多 100 条。`status` 为 `running`、`done`、`error`、`cancelled` |
| `subagent_steer` | `id`、`message`（都必填，消息最多 64 KiB，最多 32 条待处理） | `Steered <id>; …`；运行中或 follow-up 进行中可用，消息在子代理下一个循环边界送达。错误以非失败的 `Error: …` 文本返回 |
| `subagent_cancel` | `id`（必填） | `Cancellation requested; …` 或 `Subagent is already inactive; …`；有 follow-up 时先取消 follow-up |
| `subagent_followup` | `id`、`message`（都必填） | 同步返回这次追问的最终报告。子代理仍在运行（应改用 steer）、已有 follow-up、从旧会话恢复（不再活跃）或仍在收尾时返回失败 |

事件与转录的关联：

- `subagent.*` 的 `parent_tool_call_id` 是发起它的那次 `subagent` 工具调用 id。`subagent_status`/`steer`/`cancel`/`followup` 的工具调用通过参数 `arguments.id` 指向子代理 id。客户端应按子代理 id（即 `trajectory.child.*` 的 `task_id`）归组，而不是按 `run_id`。
- follow-up 沿用同一个子代理 id 与同一段对话，不产生新 id，也不发新的 `subagent.started`。追问消息以 `trajectory.child.steer {task_id,text}` 记录（转录里显示为一条用户消息），之后的 `trajectory.child.*` 事件继续用同一 `task_id`，并记在该子代理第一次 `subagent.started` 所在的运行下（即使追问发生在之后的运行里）。追问开始和结束时各重发一次 `subagent.completed`/`subagent.failed`，`status` 与 `report` 保持原来的值，`usage` 更新；追问的报告只作为 `subagent_followup` 的工具结果返回，不写入 `report`。追问结束后保存的完整转录包含原对话与追问。
- 追问进行中子代理不算 live：`GET /v1/sessions/{id}/subagents/{task}` 返回上次保存的转录且 `live:false`，`/events` 立即结束；实时内容只在主会话 SSE 的 `trajectory.child.*` 里。

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
