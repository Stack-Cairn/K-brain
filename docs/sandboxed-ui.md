# 沙箱界面：插件面板、MCP 应用界面与宿主桥接协议

本文写给图形客户端（桌面端、WebUI）。三类界面共用同一安全模型：插件面板（`capabilities.plugin_ui`）、MCP 工具声明的界面（`capabilities.mcp_apps`），以及模型生成的 HTML 预览（`capabilities.html_previews`）。K-brain 后端只负责列出界面、以只读方式提供文件并带上沙箱响应头；渲染、与用户确认、以及下面的 postMessage 协议都由客户端（下称“宿主”）实现。HTTP 接口细节见 [liveagent-backend.md](liveagent-backend.md) 的“插件”“插件界面”两节。

## 安全模型

- **只在沙箱 iframe 里渲染**：`<iframe sandbox="allow-scripts" src="...">`。**绝不**加 `allow-same-origin`（否则页面与后端同源，可以读取后端的其他响应）；也不加 `allow-top-navigation`、`allow-popups`、`allow-forms`、`allow-modals`。后端的响应头 `Content-Security-Policy: sandbox allow-scripts; ...` 也强制同样的沙箱，即使宿主忘了 `sandbox` 属性，页面仍是不透明源（`origin` 为 `"null"`）。
- **不联网**：响应的 CSP 是 `default-src 'none'`、`connect-src 'none'`、`frame-src 'none'`、`form-action 'none'`。页面只能加载本插件 UI 目录下的脚本、样式、图片、字体、媒体，以及内联脚本与样式、`data:`/`blob:` 图片与媒体；`fetch`、XHR、WebSocket、子框架、表单提交都被浏览器拦下。页面需要宿主或后端的数据，只能通过下面的桥接协议向宿主请求。
- **不带令牌、没有 Cookie**：iframe 发不出 `Authorization` 头，所以插件文件走能力 URL（`/v1/plugin-assets/{grant}/...`），`grant` 只由需要令牌的 `GET /v1/plugins` 发放，后端重启、插件停用或移动、项目撤销信任后失效。后端从不设置 Cookie；响应带 `Cache-Control: no-store`、`Referrer-Policy: no-referrer`、`X-Content-Type-Options: nosniff`。
- **只读、不越界**：文件只从插件目录下的 UI 根目录（`ui.root`，默认 `ui`）读取，路径段不能为空、`.`、`..` 或以 `.` 开头，不能含 `\`、`:` 与控制字符；编码的 `/` 被拒绝；用 `os.Root` 打开，符号链接不能指向 UI 根目录之外；只提供普通文件，单个最大 10 MiB。
- **宿主是唯一的权限边界**：页面的每个请求都可能是恶意的。宿主只接受来自该 iframe 的消息，按下文校验参数，涉及工具调用、打开文件等动作时由用户确认。

## 传输

- 页面 → 宿主：`window.parent.postMessage(message, "*")`。
- 宿主 → 页面：`iframe.contentWindow.postMessage(message, "*")`。页面是不透明源，目标源只能写 `"*"`，因此宿主发给页面的内容不能含令牌、路径以外的本机信息等秘密。
- 宿主收到 `message` 事件时**必须**检查 `event.source === iframe.contentWindow`，不能依赖 `event.origin`（总是 `"null"`）。同时存在多个面板时，按 `event.source` 区分是哪个插件的哪个面板。
- 消息是 JSON-RPC 2.0 对象（结构化克隆的普通对象，不是字符串）：请求 `{jsonrpc:"2.0", id, method, params?}`，响应 `{jsonrpc:"2.0", id, result}` 或 `{jsonrpc:"2.0", id, error:{code, message, data?}}`，通知 `{jsonrpc:"2.0", method, params?}`（没有 `id`）。不符合的消息直接忽略，不回复。
- 宿主应限制单条消息大小（建议 1 MiB）与速率（建议每个面板每秒 20 个请求），超出时回复错误 `-32003`。

## 握手

页面加载后先发 `host.initialize`；宿主在收到它之前不发任何通知。

```json
{"jsonrpc":"2.0","id":1,"method":"host.initialize","params":{"protocolVersion":"1"}}
```

```json
{"jsonrpc":"2.0","id":1,"result":{
  "protocolVersion":"1",
  "plugin":{"id":"board","version":"1.2.0"},
  "panel":{"id":"main","placement":"sidePane"},
  "theme":{"mode":"dark","colors":{"background":"#1e1e1e","foreground":"#d4d4d4","muted":"#808080","accent":"#4c8bf5","border":"#333333"},"fontFamily":"system-ui","fontSize":13},
  "locale":"zh-CN",
  "methods":["host.initialize","host.getTheme","host.notify","host.openFile","host.callTool"]
}}
```

`protocolVersion` 是宿主实现的版本；页面不认识时应只使用 `methods` 里列出的方法。`plugin`、`panel` 由宿主根据加载的面板填写，不来自页面。

## 页面可调用的方法

| 方法 | params | result | 说明 |
|---|---|---|---|
| `host.initialize` | `{protocolVersion}` | 见上 | 只回复一次；再次调用返回同样的结果 |
| `host.getTheme` | 无 | `theme`（同上） | 当前主题 |
| `host.notify` | `{message, level?: "info"\|"success"\|"warning"\|"error"}` | `null` | 宿主以纯文本显示一条提示（不解析 HTML/Markdown），可截断到 200 字符并合并频繁的提示 |
| `host.openFile` | `{path, line?}` | `null` | 在宿主中打开文件。`path` 相对于当前会话工作目录或为其中的绝对路径；规范化后不在工作目录内的路径返回 `-32002`。不读取文件内容给页面 |
| `host.callTool` | `{name, arguments?}` | `{output, failed}` | 调用工具，见下 |

`host.callTool`：

- 插件面板只能调用**本插件自己的工具**：`name` 是 `plugin.json` 里 `tools[].name`（不是模型看到的 `plugin_<插件>_<工具>`）。宿主用 `POST /v1/plugins/{id}/tools/call {name, arguments, workdir}` 执行，把回复的 `output`、`failed` 转给页面。
- 每次调用前宿主向用户显示插件名、工具名与参数并请求确认；用户可以选择“本会话内对此插件的此工具不再询问”。用户拒绝返回 `-32001`。这条 HTTP 调用不在运行中，后端不会再弹审批，**宿主的确认就是唯一的审批**。
- 插件停用、不存在的工具返回后端的 404，宿主转成 `-32002`。

## 宿主发给页面的通知

| 方法 | params | 时机 |
|---|---|---|
| `panel.themeChanged` | `theme` | 用户切换主题 |
| `panel.visibilityChanged` | `{visible}` | 面板被隐藏或重新显示（宿主可以在隐藏时保留 iframe） |

## 错误码

| code | 含义 |
|---|---|
| `-32600` / `-32601` / `-32602` | JSON-RPC 标准：无效请求 / 方法不存在 / 参数无效 |
| `-32001` | 用户拒绝 |
| `-32002` | 不允许（超出范围、目标不存在、插件已停用） |
| `-32003` | 超出速率或大小限制 |
| `-32000` | 执行失败，`message` 为原因 |

## MCP 应用界面

`tool.result` 的 `ui_resource.url` 需要令牌，iframe 不能直接加载：宿主用带 `Authorization` 的 `fetch` 取回 HTML，放进 `<iframe sandbox="allow-scripts" srcdoc="...">`（文档开头已插入同样的 CSP `<meta>`）。不要把 HTML 转成同源的 blob URL 打开。

MCP 应用界面按公开的 MCP Apps 约定通过 postMessage 收发 JSON-RPC 2.0（方法名带 `ui/` 前缀，与上文 `host.*` 不同）；传输规则（只认该 iframe 的 `event.source`、目标源 `"*"`、限制大小与速率）与上文相同。宿主至少应：

- 回复 `ui/initialize`（主题、语言、宿主信息），之后用 `ui/notifications/tool-input`、`ui/notifications/tool-result` 把本次调用的参数与结果（`output`）推给界面；
- 处理 `tools/call`：只允许调用**同一 server** 的工具，经 `POST /v1/mcp/tools/call`（`cwd` 为会话工作目录）执行，server 的 `policy` 照常生效；`deny` 由后端拒绝，`ask` 由宿主先征得用户同意（这条 HTTP 调用不会再弹审批）；
- 其他方法（打开链接、向对话发消息等）要么由用户确认后执行，要么回复 `-32601`；
- 忽略界面请求的尺寸以外的显示提示，按自己的布局限制高度。

## HTML 预览

`preview.created` 的 `url` 同样需要令牌：取回后用 `srcdoc` 放进 `sandbox="allow-scripts"` 的 iframe。预览是模型生成的内容，宿主**不**为它实现任何 `host.*` 方法，忽略它发来的所有消息；它不能联网（`connect-src 'none'`），也不能加载外部资源。放大查看时也在新的沙箱 iframe 里渲染同一 HTML，不要给它同源权限。

## 宿主实现要点

1. 从 `GET /v1/plugins?workdir=<会话工作目录>` 的 `plugins[].contributes.ui[]` 取面板，只显示带 `url` 的面板（插件已启用）；`placement:"sidePane"` 放侧边栏，`"settings"` 放设置页中该插件的条目下。
2. `src` 为后端地址加 `url`（`url` 是后端上的路径）。列表的 `revision` 变化后重新取列表，URL 可能变化。
3. 面板加载失败（404）说明授权已失效：重新取列表后再加载一次。
4. 卸载面板时移除 iframe 并丢弃挂起的请求。
