# 模型与 API 协议

每个 provider 只声明一次 endpoint，模型通过 `models.<id>.providers` 绑定 provider。支持第三方 API key 和自定义 `baseUrl`，不要求 K-brain 账户。

`api` 可使用：

- `openai-completions`：OpenAI Chat Completions 兼容接口。
- `openai-responses`：OpenAI Responses 接口。
- `anthropic-messages`：Anthropic Messages 接口。

例如：

```json
{
  "providers": {
    "api": {
      "name": "api",
      "api": "openai-responses",
      "baseUrl": "https://api.example.com/v1",
      "apiKey": "API_KEY"
    }
  },
  "models": {
    "model1": { "providers": ["api"], "context": 200000 }
  },
  "defaultModel": "model1"
}
```

在 TUI 输入 `/model refresh` 获取 `/v1/models`，输入 `/model <id>` 选择模型。上下文长度由 `context` 控制；`compactModel` 可指定压缩时使用的模型。
