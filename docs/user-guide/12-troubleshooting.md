# 故障排查

## 没有回复

确认 provider 的 `baseUrl` 以 `/v1` 结尾、`api` 与服务协议匹配，并且 `apiKey` 有效。用 `/doctor` 查看运行诊断；用 `/model refresh` 检查 `/v1/models` 是否可访问。

## 模型或上下文错误

确认 `models.<id>.providers` 中的 provider 名称存在，并为模型设置正确的 `context` 和 `maxOut`。可以用 `/compact` 释放上下文，或切换 `compactModel`。

## 配置重置

JSONC 解析错误会在启动时显示文件位置。修正 `~/.k-brain/config.json` 后重启；系统提示词和用户指令分别检查 `system.md`、`brain.md` 及项目 `AGENTS.md`。

## Windows

路径可使用反斜杠或正斜杠；PowerShell、Windows Terminal 和 WSL 均可运行 `kn`。执行权限、sandbox backend 和 shell 命令取决于当前平台配置。
