# Hooks 与 MCP

Hooks 在指定生命周期运行本地命令。配置示例：

```json
"hooks": {
  "afterTurn": [{ "command": "go test ./...", "timeout": 120 }]
}
```

具体 hook 名称以 `/help` 和配置校验错误为准；命令可指定 `shell` 与超时。

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
