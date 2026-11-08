# 故障排查

## 没有回复

确认 provider 的 `baseUrl` 以 `/v1` 结尾、`api` 与服务协议匹配，并且 `apiKey` 有效。用 `/doctor` 查看运行诊断；用 `/model refresh` 检查 `/v1/models` 是否可访问。

## 模型或上下文错误

确认 `models.<id>.providers` 中的 provider 名称存在，并为模型设置正确的 `context` 和 `maxOut`。可以用 `/compact` 释放上下文，或切换 `compactModel`。

## 配置重置

JSONC 解析错误会在启动时显示文件位置。修正 `~/.liveagent/config.json` 后重启；系统提示词和用户指令分别检查 `system.md`、`brain.md` 及项目 `AGENTS.md`。

## Windows

路径可使用反斜杠或正斜杠；PowerShell、Windows Terminal 和 WSL 均可运行 `kn`。执行权限、sandbox backend 和 shell 命令取决于当前平台配置。

## 卡死（界面或 backend 无响应）

`kn` 和 `kn backend` 都可以在不退出的情况下把所有 goroutine 的调用栈写到文件里，用于定位卡死原因。文件写在 `k-brain.log` 所在目录（默认 `~/.liveagent/`，设置了 `LIVEAGENT_HOME` 时为该目录），名为 `stacks-<UTC 时间>-<pid>.txt`，只保留最近 20 份；每次写入都会在 `k-brain.log` 里记一行 `stacks`。

手动触发：

- macOS / Linux：在另一个终端执行 `kn debug stacks <pid>`（向该进程发送 `SIGUSR1`，等待并打印文件路径）。也可以直接 `kill -USR1 <pid>`。
- backend（所有平台，包括 Windows）：`kn debug stacks --backend`，默认连接 `http://127.0.0.1:47321`，token 取自 `K_BRAIN_BACKEND_TOKEN` 或 `--token`；其他地址用 `--url`。对应接口是 `GET /v1/debug/stacks`，需要与其他接口相同的 Bearer token，且只接受来自本机回环地址的请求，返回 `{"path": "..."}`。该接口不获取任何会话锁，backend 其余部分卡住时依然可用。

自动触发（看门狗）：

- `kn` 的界面事件循环超过 20 秒没有处理任何消息（按键因此也无法处理）时，自动写一份调用栈，并在 `k-brain.log` 里记一条 `watchdog` 警告。外部编辑器（`Ctrl+G`、`/brain`、`/system`）在前台运行期间不计时。
- backend 的全局锁、任一会话锁或 health 响应超过 20 秒没有进展时，同样自动写一份并记录警告。
- 每次卡顿只写一份，每个进程最多自动写 3 份。

报告问题时请附上对应的 `stacks-*.txt` 和 `k-brain.log` 中相邻的几行。
