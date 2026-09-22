# Sandbox 与 Computer-use

## Sandbox

通过 `config.json` 的 `sandbox` 设置执行边界：

```json
"sandbox": {
  "mode": "workspace",
  "backend": "auto",
  "network": false,
  "writable": ["tmp"],
  "readOnly": ["docs"]
}
```

`mode` 控制是否限制在工作区，`network` 控制网络访问，`writable` 和 `readOnly` 增加路径规则。不同平台会选择可用的本地 backend。

## Computer-use

启用 `computer.enabled` 后，模型可以通过工具驱动桌面；`allow`、`deny` 和 `defaultDeny` 控制应用范围。TUI 中输入 `/computer-use <task>` 执行任务，先用 `allow|deny <app>` 调整当前会话授权。
