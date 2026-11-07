# Hooks 与 MCP

Hooks 在指定的生命周期节点运行本地命令。配置示例：

```json
"hooks": {
  "PostToolUseFailure": [{ "command": "notify-send 'tool failed'", "timeout": 5 }],
  "PreToolUse": [{ "command": "./scripts/guard.sh", "shell": "sh", "timeout": 10 }]
}
```

每个事件下可以配置多条命令，按顺序执行。`shell` 可选，`timeout` 单位为秒，默认 10，最多 120。命令在会话工作目录中运行，事件内容以 JSON 形式放在环境变量 `K_BRAIN_HOOK_EVENT` 中。所有事件都带有 `event`、`sessionId`、`cwd` 字段，其余字段见下表。

| 事件 | 触发时机 | 额外字段 | 命令失败(非 0 退出 / 超时)时 |
| --- | --- | --- | --- |
| `SessionStart` | 会话开始时(第一轮对话之前) | — | 会话不会开始 |
| `UserPromptSubmit` | 用户消息提交后、请求模型之前 | `prompt` | 本轮不再执行 |
| `PreToolUse` | 每次工具调用执行之前 | `toolId`、`toolName`、`toolArgs` | 拒绝这次调用,错误信息作为工具结果返回给模型 |
| `PostToolUse` | 每次工具调用执行之后(成功和失败都会触发) | 同上,外加 `toolResult` | 错误信息追加到工具结果末尾 |
| `PostToolUseFailure` | 工具返回错误时,在 `PostToolUse` 之后额外触发;被中断的调用不触发 | 同上,`toolResult` 为错误文本 | 错误信息追加到工具结果末尾 |
| `PreCompact` | 上下文压缩开始前(自动压缩或 `/compact`) | `trigger`(`auto`/`manual`)、`messagesBefore`、`tokensBefore` | 忽略,不会阻止压缩 |
| `PostCompact` | 压缩成功之后;压缩失败时不触发 | 同上,外加 `messagesAfter`、`tokensAfter`、`summary` | 忽略 |
| `Stop` | 每一轮对话结束时 | — | 忽略 |
| `SessionEnd` | `kn run` 结束、ACP 会话关闭,或同一个 agent 切换到另一个会话时 | `reason`(`exit`/`close`/`switch`) | 忽略 |

`tokensBefore`/`tokensAfter` 是 K-brain 自己估算的 token 数,不是 provider 报告的用量。

`PreToolUse` 除了用退出码表达拒绝,也可以在 **退出码为 0** 时输出一个 JSON 对象(可以是全部输出,也可以是最后一行):

```json
{"decision": "deny", "reason": "不允许在这个仓库里联网"}
```

这时调用会被拒绝,`reason` 作为拒绝原因返回给模型。`decision` 不是 `deny`,或输出不是 JSON 时,调用照常执行;所以只打印普通文本的旧 hook 行为不变。只有 `PreToolUse` 会读取这个 JSON。

插件 hook 会收到同样的事件名和 payload。

MCP server 在 `mcp` 中声明 command 或 URL：

```json
"mcp": {
  "local": {
    "command": ["node", "server.js"],
    "cwd": ".",
    "enabled": true
  }
}
```

`/mcp` 或 `/mcps` 查看状态，参数 `enable`、`disable`、`reconnect` 管理服务器。环境变量使用 `env` 字段传入。

## MCP 服务器请求填写表单

有些 MCP server 会在工具运行过程中请你补充信息，例如选择一个项目、确认一个数量（MCP 中称为 elicitation）。K-brain 只在能显示表单的界面里接受这类请求，目前是通过 `/v1` 接入的客户端（客户端需要支持这个功能）；`kn run`、定时任务、ACP 等无人值守或不能显示表单的场合会直接替你拒绝，工具照常结束，不会卡住等待。

表单上方显示发出请求的 **MCP server 名字**，也就是你在 `mcp` 配置里写的键名（上例中的 `local`），而不是服务器自己报告的名字。填写前请确认这是你预期的服务器，再决定是否提供信息。你可以：

- **提交**：填好的内容只交给这个 server；
- **拒绝**：明确告诉 server 不提供；
- **取消**：关掉表单，不作选择。

约 3 分钟无人回答时按取消处理；停止当前回复也会取消表单。表单打开期间，这个工具的 MCP 超时暂停计时。

MCP 规范不允许 server 用表单索要密码、令牌等密钥。字段名或标题看起来像密钥（password、token、API key、PIN、验证码、信用卡号等）时，表单会特别提醒；如果你不信任这个 server，请拒绝。这类字段的值只交给 server，不写入 K-brain 的会话历史、事件记录或日志；其余字段的回答会保存在该工具调用的记录里，以便回看。

K-brain 只接受简单的一层表单：文本（可限制长度、格式 email/uri/date/date-time 或正则）、数字、开关、单选和多选。超出这个范围的表单会被直接退回给 server，不会显示给你；同一次工具调用最多弹出 8 个表单。
