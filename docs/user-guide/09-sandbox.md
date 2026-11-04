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

`mode` 控制是否限制在工作区，`network` 控制网络访问，`writable` 和 `readOnly` 增加路径规则。不同平台会选择可用的本地 backend：macOS 用 seatbelt（`/usr/bin/sandbox-exec`），Linux 用 bubblewrap（只从 `/usr/bin`、`/usr/local/bin` 或工作区外的 PATH 目录解析 `bwrap`）。

受限的是命令类工具：`Bash`/`bash`、`ManagedProcess`、`TerminalSession` 以及插件 hook。命令可以读整台机器，只能写工作区根、额外 `writable` 路径和系统临时目录（`TMPDIR`、`/tmp`、`/var/tmp`）；`/dev/null`、`/dev/tty` 等设备节点可写。

## 按次运行的沙箱（`options.sandbox`）

客户端可以在单次运行请求里打开沙箱，无需改全局配置：

```json
"options": {
  "permission_mode": "auto_edit",
  "sandbox": {"mode": "workspace", "network": false}
}
```

- `mode` 目前只有 `workspace`（省略即 `workspace`）；`network: false` 断网。
- 可写范围是运行的 `write` 工作区根（`workspace_roots` 中 access 为 `write` 的目录）加临时目录。
- 以下目录对命令不可读写：`~/.ssh`、`~/.aws`、`~/.gnupg`、`~/.config/gh`，以及 K-brain 自己的数据目录（配置/API key、会话、记忆、权限规则）。工作区本身位于这些目录内时，工作区仍可用，但其外的部分保持隐藏；工作区**包含**这些目录（例如把家目录当工作区）时运行会被拒绝。
- 即使在可写根里，`.liveagent/`、`.k-brain/` 和 `.git/hooks/` 也只读：这些文件下次会话会被自动信任。
- 全局配置已开启沙箱时，按次请求只能收紧：网络只要任一方关闭就关闭，配置的可写/只读路径不变，并额外加上上面的屏蔽与只读规则。
- 平台无法隔离时（Windows 原生、没有可用的 bubblewrap、容器内无法创建命名空间），请求 `400` 拒绝并给出原因，**不会**退回到不隔离运行。只要求断网而平台不能隔离网络时同样拒绝。
- `/v1/health` 的 `capabilities.run_sandbox` 报告 `supported`、`mechanism`、`platform`、`network_control`、`read_masking`、`modes` 和不可用时的 `reason`。客户端应仅在 `supported` 为 true 时提供沙箱选项，仅在 `network_control` 也为 true 时提供断网选项。

### 与权限审批的关系

沙箱运行里，权限模式为 `normal`（或省略）或 `auto_edit` 时，命令类工具不再逐条询问：操作系统边界代替了执行前确认。以下规则仍优先生效：

- 计划模式、`approval_policy: deny`、按工具的 `deny`/`ask` 策略；
- 用户/项目权限规则中的 Deny 和 Ask；
- `auto_review` 模式和已配置的 AI 审批：审批者仍会看到每条命令；
- `full_access`/`always` 照旧全部放行。

文件编辑、MCP、Computer-use 等在 K-brain 进程内执行的工具不受沙箱约束，仍按权限模式与工作区授权处理。

被沙箱拦截的命令会得到 `Operation not permitted`（或只读文件系统）错误；不要用 `sudo` 绕过，需要更大权限时请用户切换模式。

## Computer-use

启用 `computer.enabled` 后，模型可以通过工具驱动桌面；`allow`、`deny` 和 `defaultDeny` 控制应用范围。TUI 中输入 `/computer-use <task>` 执行任务，先用 `allow|deny <app>` 调整当前会话授权。
