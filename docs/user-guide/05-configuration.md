# 配置文件

主配置为 `~/.k-brain/config.json`，接受 JSONC（注释和尾逗号）。配置采用 Pi 风格的 provider 字段：`name`、`api`、`baseUrl`、`apiKey`、`models`。

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

主要可选项：`defaultEffort`、`goalMaxRounds`、`mouse`、`thinking`、`collapsePaste`、`maxRetries`、`sandbox`、`browser`、`computer`、`hooks`、`mcp` 和 `lsp`。
