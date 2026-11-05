# 配置文件

主配置为 `~/.liveagent/config.json`，接受 JSONC（注释和尾逗号）。配置采用 Pi 风格的 provider 字段：`name`、`api`、`baseUrl`、`apiKey`、`models`。

完整配置示例：

```json
{
  "defaultModel": "model1",
  "compactModel": "model1",
  "providers": {
    "demo": {
      "name": "demo",
      "api": "openai-completions",
      "baseUrl": "https://api.example.com/v1",
      "apiKey": "${API_KEY}"
    }
  },
  "models": {
    "model1": { "providers": ["demo"], "context": 128000, "maxOut": 8192 }
  },
  "sandbox": { "mode": "workspace", "network": false },
  "computer": { "enabled": false },
  "theme": "auto",
  "language": "zh_cn"
}
```

`baseUrl` 必须包含 `/v1`，模型目录固定从 `${baseUrl}/models`（即 `/v1/models`）读取。`context`、`maxOut`、`vision` 和采样参数都可按模型自定义，不会把特定模型写死在代码中。`apiKey` 可写明文、环境变量名或 `${VAR}` 引用。

主要可选项：`defaultEffort`、`goalMaxRounds`、`mouse`、`thinking`、`collapsePaste`、`maxRetries`、`sandbox`、`browser`、`computer`、`hooks`、`mcp`、`lsp` 和 `rulesCompat`。

`rulesCompat` 控制是否读取其他代理工具的规则文件，两项默认开启：`{"rulesCompat": {"claude": false, "cursor": false}}` 可分别关闭 Claude Code（`CLAUDE.md`、`CLAUDE.local.md`、`.claude/CLAUDE*.md`、`~/.claude/CLAUDE.md`、项目 `.claude/skills`）和 Cursor（`.cursor/rules`、`.cursorrules`）兼容。详见[提示词层级](02-prompts.md)。

终端界面的通知和状态行：

```json
{
  "notifications": { "desktop": "auto", "title": true },
  "statusLine": { "command": "~/.k-brain/statusline.sh", "intervalMs": 2000, "timeoutMs": 1000 }
}
```

`notifications.desktop` 为 `auto`（默认）时，只在终端报告过焦点状态、且当前失去焦点的情况下，于回合结束或等待批准/回答时发出桌面通知；`on` 在焦点未知时也通知，`off` 关闭，`osc9`、`osc777`、`bel` 可固定通知方式。`title` 控制窗口标题（会话名，回合进行中带旋转指示）。`statusLine.command` 默认不设置；设置后，命令从标准输入读取会话 JSON（模型、上下文、用量、费用、git 等），其输出的第一行显示在底栏左侧。命令在后台运行，两次运行至少间隔 `intervalMs`，超过 `timeoutMs` 即被终止。
