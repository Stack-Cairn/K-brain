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
