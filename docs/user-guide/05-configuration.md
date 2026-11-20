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

主要可选项：`defaultEffort`、`goalMaxRounds`、`goalVerify`、`goalVerifyMaxRejections`、`mouse`、`thinking`、`collapsePaste`、`maxRetries`、`sandbox`、`browser`、`computer`、`hooks`、`mcp`、`lsp` 和 `rulesCompat`。

`goalVerify`（默认开启）控制 `/goal` 在模型声明完成时是否先做一次独立校验，`goalVerifyMaxRejections`（默认 3）是同一目标最多驳回的次数，详见[斜杠命令](04-commands.md#goal-独立校验)。

`rulesCompat` 控制是否读取其他代理工具的规则文件，两项默认开启：`{"rulesCompat": {"claude": false, "cursor": false}}` 可分别关闭 Claude Code（`CLAUDE.md`、`CLAUDE.local.md`、`.claude/CLAUDE*.md`、`~/.claude/CLAUDE.md`、项目 `.claude/skills`）和 Cursor（`.cursor/rules`、`.cursorrules`）兼容。详见[提示词层级](02-prompts.md)。

终端界面的通知和状态行：

```json
{
  "notifications": { "desktop": "auto", "title": true },
  "statusLine": { "command": "~/.k-brain/statusline.sh", "intervalMs": 2000, "timeoutMs": 1000 }
}
```

`notifications.desktop` 为 `auto`（默认）时，只在终端报告过焦点状态、且当前失去焦点的情况下，于回合结束或等待批准/回答时发出桌面通知；`on` 在焦点未知时也通知，`off` 关闭，`osc9`、`osc777`、`bel` 可固定通知方式。`title` 控制窗口标题（会话名，回合进行中带旋转指示）。`statusLine.command` 默认不设置；设置后，命令从标准输入读取会话 JSON（模型、上下文、用量、费用、git 等），其输出的第一行显示在底栏左侧。命令在后台运行，两次运行至少间隔 `intervalMs`，超过 `timeoutMs` 即被终止。

## 联网搜索回退（`webSearch`）

provider 开启联网搜索（`nativeWebSearchEnabled: true`，或 LiveAgent 打开"联网"开关）时，Responses、Anthropic Messages 与官方 Gemini 端点使用模型服务自带的搜索。OpenAI Chat Completions 没有原生搜索的请求格式，Gemini 中转端点在有函数工具时也会丢掉 `google_search`；这类 provider 在配置了 `webSearch` 后改为提供一个 `web_search` 函数工具，由模型调用第三方搜索服务。没有配置 `webSearch` 时行为不变：Chat Completions 开启搜索仍然报错，错误信息会提示配置 `webSearch`。

```json
{
  "webSearch": {
    "provider": "tavily",
    "apiKey": "${TAVILY_API_KEY}",
    "maxResults": 5
  }
}
```

| 字段 | 说明 |
| --- | --- |
| `provider` | `tavily`、`brave`、`searxng`、`bocha` 或 `jina` |
| `apiKey` | 服务的密钥，与 provider 的 `apiKey` 一样可写明文、`$VAR`、`${VAR}` 或 `!命令`（运行搜索时才解析）；`searxng` 可不填，填了按 `Authorization: Bearer` 发送 |
| `baseUrl` | 服务根地址；`searxng` 必填（例如 `http://127.0.0.1:8888`），其他服务可用它指向兼容的代理 |
| `maxResults` | 每次搜索的结果数，默认 5，最多 10；模型调用时可用 `max_results` 另行指定，同样不超过 10 |

环境变量优先于配置文件：`K_BRAIN_WEB_SEARCH_PROVIDER`、`K_BRAIN_WEB_SEARCH_BASE_URL` 覆盖对应字段；密钥依次取 `K_BRAIN_WEB_SEARCH_API_KEY`、服务自己的变量（`TAVILY_API_KEY`、`BRAVE_SEARCH_API_KEY`、`BOCHA_API_KEY`、`JINA_API_KEY`）、`webSearch.apiKey`。

各服务的请求方式：

- Tavily：`POST {baseUrl}/search`，默认 `https://api.tavily.com`，`Authorization: Bearer`。
- Brave Search：`GET {baseUrl}/res/v1/web/search`，默认 `https://api.search.brave.com`，`X-Subscription-Token`。
- SearXNG：`GET {baseUrl}/search?format=json`。实例需要在 `settings.yml` 的 `search.formats` 中开启 `json`，否则返回 403。
- 博查（Bocha）：`POST {baseUrl}/v1/web-search`，默认 `https://api.bochaai.com`，`Authorization: Bearer`。
- Jina：`GET {baseUrl}/?q=`，默认 `https://s.jina.ai`，`Authorization: Bearer`；不支持按时间筛选，`recency` 参数会被忽略并在结果中注明。

`web_search` 的参数是 `query`（必填，最长 400 个字符）、`max_results`、`recency`（`day`、`week`、`month`、`year`）和 `domains`（只搜这些站点，最多 10 个；Tavily 与博查使用服务自带的站点过滤，其他服务改写成 `site:` 查询）。结果是编号列表，每条含标题、URL、发布时间（服务提供时）和摘要，总长度不超过 12000 个字符；单次搜索 15 秒超时。请求走与 `web_fetch` 相同的代理设置，本机地址（例如本地 SearXNG）不走代理。

`web_search` 与 `web_fetch` 一样需要批准：普通模式下每次询问，可以记住整个工具（规则 `web_search`）；计划模式中也会询问而不是拒绝。密钥不会出现在工具结果、错误信息、日志或权限请求里；服务在响应中回显的密钥会被替换为 `[redacted]`。子代理不继承联网搜索。
