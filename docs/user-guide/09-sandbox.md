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

`mode` 控制是否限制在工作区，`network` 控制网络访问，`writable` 和 `readOnly` 增加路径规则。不同平台会选择可用的本地 backend：macOS 用 seatbelt（`/usr/bin/sandbox-exec`），Linux 优先用 bubblewrap（只从 `/usr/bin`、`/usr/local/bin` 或工作区外的 PATH 目录解析 `bwrap`），bubblewrap 未安装或无法创建命名空间时（`backend: "auto"`）退回 Landlock，见下文。`backend` 可显式设为 `bwrap` 或 `landlock`；显式指定的 backend 不可用时直接报错，不会换用另一种。

### Linux 退回方案：Landlock + seccomp

没有可用的 bubblewrap 时（常见于禁止非特权用户命名空间的发行版和容器），K-brain 以自身为启动器重新执行：子进程在 `exec` 目标命令之前给自己加上 Landlock 规则和 seccomp 过滤器，K-brain 主进程不受影响。需要 Linux 5.13 及以上且启用了 Landlock（`/sys/kernel/security/lsm` 含 `landlock`），目前支持 amd64 与 arm64。

- 写入同样只限于工作区根、`writable` 路径、临时目录和设备节点；
- 断网时只能创建 `AF_UNIX` 套接字（内核支持时还会禁止 TCP bind/connect 和抽象 unix 套接字）；任何时候都禁止 `AF_VSOCK`、io_uring 以及向终端注入输入的 `TIOCSTI`/`TIOCLINUX`；
- 内核支持时（ABI 6），命令无法向沙箱外的进程发信号。

Landlock 只能"允许"不能"屏蔽"，因此有几处弱于 bubblewrap，并在能力中如实报告：

- 不屏蔽读取：`~/.ssh` 等凭据目录仍可读（但不可写），`read_masking` 为 false；
- 临时目录不隔离：bubblewrap 给每条命令一个私有的 `/tmp`，Landlock 下命令直接读写主机的 `/tmp`；
- 不保护可写根内的路径：`.git/hooks/` 等保护路径仍可写，`path_protection` 为 false；配置的 `readOnly` 路径若位于可写范围内，命令会被拒绝执行而不是静默放宽。

启动器自身无法完成隔离时，命令不会运行，退出码为 125，stderr 以 `k-brain sandbox:` 开头。

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
- 平台无法隔离时（Windows 原生、Linux 上 bubblewrap 与 Landlock 都不可用），请求 `400` 拒绝并给出原因，**不会**退回到不隔离运行。只要求断网而平台不能隔离网络时同样拒绝。
- `/v1/health` 的 `capabilities.run_sandbox` 报告 `supported`、`mechanism`、`platform`、`network_control`、`read_masking`、`path_protection`、`modes` 和 `reason`（不可用的原因，或可用但有局限时的说明）。`mechanism` 为 `bubblewrap`、`landlock` 或 `seatbelt`。客户端应仅在 `supported` 为 true 时提供沙箱选项，仅在 `network_control` 也为 true 时提供断网选项；`read_masking` 或 `path_protection` 为 false 时应提示用户凭据目录可读、保护路径可写。

### 与权限审批的关系

沙箱运行里，权限模式为 `normal`（或省略）或 `auto_edit` 时，命令类工具不再逐条询问：操作系统边界代替了执行前确认。以下规则仍优先生效：

- 计划模式、`approval_policy: deny`、按工具的 `deny`/`ask` 策略；
- 用户/项目权限规则中的 Deny 和 Ask；
- `auto_review` 模式和已配置的 AI 审批：审批者仍会看到每条命令；
- `full_access`/`always` 照旧全部放行。

文件编辑、MCP、Computer-use 等在 K-brain 进程内执行的工具不受沙箱约束，仍按权限模式与工作区授权处理。

被沙箱拦截的命令会得到 `Operation not permitted`（或只读文件系统）错误；不要用 `sudo` 绕过，需要更大权限时请用户切换模式。

`bash`/`Bash` 命令失败后，K-brain 会根据退出码和输出末尾的错误（`Operation not permitted`、`Permission denied`、`Read-only file system`，以及离线时的域名解析失败）判断是否为沙箱拦截，并在工具结果末尾追加一行 `[sandbox] ...` 说明：写到了可写根之外、碰到了受保护路径或被隐藏的凭据目录，或本次运行未开网络。只有被拒绝的路径在宿主机上本来就可写、且不在可写根内时才算沙箱拦截；工作区内的普通权限错误、`/etc` 这类本来就不可写的路径不会被误报。命令本身写出了目标路径时，说明会附上建议加入的可写根（最近的已存在目录；家目录、凭据目录的上级和系统顶层目录除外），客户端在 `tool.result` 的 `sandbox_denial` 字段里拿到同样的信息。这只是提示，不会自动放宽沙箱，也不会记住任何授权。

## Computer-use

启用 `computer.enabled` 后，模型可以通过工具驱动桌面；`allow`、`deny` 和 `defaultDeny` 控制应用范围。TUI 中输入 `/computer-use <task>` 执行任务，先用 `allow|deny <app>` 调整当前会话授权。
