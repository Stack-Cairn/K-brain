# 更新日志

## v0.106.0

### 新增

- 新增 `~/.k-brain/system.md` 系统级提示词文件，首次运行自动写入 K-brain 默认系统提示词。
- 新增 TUI `/system` 命令，可直接编辑系统级提示词。
- 新增项目级提示词加载，支持工作区及父目录中的 `AGENTS.md` 和 `.k-brain/brain.md`。
- 新增 `docs/user-guide/` 用户指南，覆盖安装、TUI、配置、模型协议、会话、压缩、rewind、subagent、plugin、sandbox、computer-use、ACP、hooks 和 MCP。

### 调整

- `~/.k-brain/brain.md` 改为空的用户级指令占位文件，不再保存默认系统提示词。
- 系统提示词默认内容从 `internal/config/brain.go` 移至独立的系统提示词实现，并统一使用 K-brain（氪脑）名称。
- TUI、`kn run` 和 ACP 按当前工作目录重新组合系统、用户和项目提示词，编辑后下一轮对话即可生效。
- README 增加提示词层级和用户指南入口，配置文档同步第三方 API、`/v1/models` 和可自定义上下文长度说明。

### 验证

- 通过 `go vet ./...`。
- 通过全仓库编译测试：`go test ./... -run '^$'`。
- 通过系统提示词、brain.md、项目提示词和 TUI 命令覆盖测试。
