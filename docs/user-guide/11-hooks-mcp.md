# Hooks 与 MCP

Hooks 在指定的生命周期节点运行本地命令。配置示例：

```json
"hooks": {
  "PostToolUseFailure": [{ "command": "notify-send 'tool failed'", "timeout": 5 }],
  "PreToolUse": [{ "command": "./scripts/guard.sh", "shell": "sh", "timeout": 10, "matcher": "bash" }],
  "PostToolUse": [{ "command": "./scripts/fmt-check.sh", "matcher": "edit|write" }],
  "Stop": [{ "command": "./scripts/require-tests.sh", "timeout": 120 }]
}
```

每个事件下可以配置多条命令，按顺序执行。`shell` 可选，`timeout` 单位为秒，默认 10，最多 120。命令在会话工作目录中运行，事件内容以 JSON 形式同时写入命令的 **标准输入** 和环境变量 `K_BRAIN_HOOK_EVENT`(两者内容相同;只读环境变量的旧 hook 不受影响)。事件内容较大时(例如很长的 `toolResult`)请从标准输入读取,环境变量受系统对单个变量长度的限制。所有事件都带有 `event`、`sessionId`、`cwd` 字段，其余字段见下表。

### 按工具筛选:`matcher`

`PreToolUse`、`PostToolUse`、`PostToolUseFailure` 的每条 hook 可以加 `matcher`,只对名字匹配的工具运行:

- 省略、空字符串或 `*`:匹配所有工具(与不写 `matcher` 的旧配置相同);
- `bash`:只匹配名为 `bash` 的工具,不会匹配 `bashrc` 之类的名字;
- `edit|write`:匹配其中任意一个;
- 其他写法按正则表达式处理,并且必须匹配 **完整** 的工具名,例如 `mcp__github__.*` 匹配 `github` 这个 MCP server 的所有工具。

正则写错,或在其他事件上写了 `matcher`(包括 `*`),配置加载时直接报错,不会悄悄忽略。

| 事件 | 触发时机 | 额外字段 | 命令失败(非 0 退出 / 超时)时 |
| --- | --- | --- | --- |
| `SessionStart` | 会话开始时(第一轮对话之前) | — | 会话不会开始 |
| `UserPromptSubmit` | 用户消息提交后、请求模型之前 | `prompt` | 本轮不再执行 |
| `PreToolUse` | 每次工具调用执行之前;支持 `matcher` | `toolId`、`toolName`、`toolArgs` | 拒绝这次调用,错误信息作为工具结果返回给模型 |
| `PostToolUse` | 每次工具调用执行之后(成功和失败都会触发);支持 `matcher` | 同上,外加 `toolResult` | 错误信息追加到工具结果末尾 |
| `PostToolUseFailure` | 工具返回错误时,在 `PostToolUse` 之后额外触发;被中断的调用不触发;支持 `matcher` | 同上,`toolResult` 为错误文本 | 错误信息追加到工具结果末尾 |
| `PreCompact` | 上下文压缩开始前(自动压缩或 `/compact`) | `trigger`(`auto`/`manual`)、`messagesBefore`、`tokensBefore` | 忽略,不会阻止压缩 |
| `PostCompact` | 压缩成功之后;压缩失败时不触发 | 同上,外加 `messagesAfter`、`tokensAfter`、`summary` | 忽略 |
| `Stop` | 每一轮对话结束时;正常完成时可以要求继续(见下文) | `stopHookActive`(因前一次 `Stop` 阻止而继续时为 `true`) | 忽略,本轮照常结束 |
| `SessionEnd` | `kn run` 结束、ACP 会话关闭,或同一个 agent 切换到另一个会话时 | `reason`(`exit`/`close`/`switch`) | 忽略 |

`tokensBefore`/`tokensAfter` 是 K-brain 自己估算的 token 数,不是 provider 报告的用量。

### 结构化输出

hook **退出码为 0** 时,可以输出一个 JSON 对象(可以是全部输出,也可以是最后一行)来影响后续行为。输出不是 JSON,或字段不适用于当前事件时会被忽略,所以只打印普通文本的旧 hook 行为不变。退出码非 0 或超时时不读取输出,按上表的失败规则处理。

| 事件 | 输出 | 效果 |
| --- | --- | --- |
| `PreToolUse` | `{"decision": "deny", "reason": "..."}` | 拒绝这次调用,`reason` 作为拒绝原因返回给模型 |
| `Stop` | `{"decision": "block", "reason": "..."}` | 不结束本轮:`reason` 作为一条系统提醒加入对话,模型再回答一轮 |
| `UserPromptSubmit` | `{"additionalContext": "..."}` | 内容随这条用户消息一起发给模型(只给模型看,不改动界面上显示的消息) |
| `PostToolUse`、`PostToolUseFailure` | `{"additionalContext": "..."}` | 内容附加在这次工具结果末尾,模型在下一次请求中看到 |

拒绝示例:

```json
{"decision": "deny", "reason": "不允许在这个仓库里联网"}
```

同一事件的多条 hook 按顺序执行:第一个 `deny` 或 `block` 生效后,后面的 hook 不再执行;多条 hook 的 `additionalContext` 按顺序拼接,总长超过 10 KB 时截断。

### 让 `Stop` 阻止结束

模型准备结束本轮(没有工具调用、也没有排队的消息)时运行 `Stop` hook。hook 输出 `{"decision": "block", "reason": "还没运行测试"}` 时,本轮不会结束,`reason` 作为提醒交给模型,模型继续工作;之后它再次准备结束时,`Stop` hook 会再次运行,payload 中 `stopHookActive` 为 `true`。

为避免无限循环,同一轮最多被 `Stop` 阻止 **5 次**;达到上限后 `Stop` hook 仍会运行,但它的 `block` 会被忽略,本轮结束。下一条用户消息重新计数。建议脚本检查 `stopHookActive`,对短时间内无法满足的条件不要反复阻止。例如:

```sh
#!/bin/sh
# 测试没过就要求继续;已经因为本 hook 继续过一次就放行
event=$(cat)
case "$event" in *'"stopHookActive":true'*) exit 0 ;; esac
go test ./... >/dev/null 2>&1 || printf '{"decision":"block","reason":"go test 未通过,请修复后再结束"}'
```

只有正常完成的一轮会被阻止。被中断、出错、达到 `kn run --max-turns` 上限或在计划模式中退出的一轮,`Stop` 仍会运行,但输出被忽略;因 `Stop` 阻止而多出的回合也计入 `--max-turns`。子 agent 共用同一组 hook,它们的每一轮也会运行 `Stop`。

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

有些 MCP server 会在工具运行过程中请你补充信息，例如选择一个项目、确认一个数量（MCP 中称为 elicitation）。K-brain 只在能显示表单的界面里接受这类请求：`kn` 的交互界面，以及通过 `/v1` 接入且支持这个功能的客户端。`kn run`、定时任务、ACP 等无人值守或不能显示表单的场合会直接替你拒绝，工具照常结束，不会卡住等待。

表单上方显示发出请求的 **MCP server 名字**，也就是你在 `mcp` 配置里写的键名（上例中的 `local`），而不是服务器自己报告的名字。填写前请确认这是你预期的服务器，再决定是否提供信息。你可以：

- **提交**：填好的内容只交给这个 server；
- **拒绝**：明确告诉 server 不提供；
- **取消**：关掉表单，不作选择。

在 `kn` 中，表单以卡片形式出现在输入框上方，标题是发出请求的 server 名：

- `↑`/`↓`（或 `Tab`/`Shift+Tab`）在字段之间移动，`Enter` 跳到下一项，最后来到「提交 · 拒绝 · 取消」一行，用 `←`/`→` 选择、`Enter` 确认；
- 文本和数字字段直接输入，`Backspace` 删除，`Ctrl+U` 清空；
- 单选字段用 `←`/`→` 切换选项，非必填的单选可以切回「未选择」；开关字段用空格切换；多选字段用 `←`/`→` 移动、空格勾选；
- `Esc` 取消表单，`Ctrl+C` 取消表单并停止当前回复；
- 提交时会检查每个字段（必填、长度、格式、范围、选项个数），有问题的字段下方显示原因，光标跳到第一个有问题的字段，表单保持打开。

`kn` 中的表单没有时间限制，等你回答或停止回复为止。`/v1` 客户端的表单约 3 分钟无人回答时按取消处理，停止当前回复也会取消表单。表单打开期间，这个工具的 MCP 超时暂停计时。

MCP 规范不允许 server 用表单索要密码、令牌等密钥。字段名或标题看起来像密钥（password、token、API key、PIN、验证码、信用卡号等）时，表单会特别提醒，`kn` 中的输入以圆点遮住；如果你不信任这个 server，请拒绝。这类字段的值只交给 server，不写入 K-brain 的会话历史、事件记录或日志；其余字段的回答会保存在该工具调用的记录里，以便回看。

K-brain 只接受简单的一层表单：文本（可限制长度、格式 email/uri/date/date-time 或正则）、数字、开关、单选和多选。超出这个范围的表单会被直接退回给 server，不会显示给你；同一次工具调用最多弹出 8 个表单。
