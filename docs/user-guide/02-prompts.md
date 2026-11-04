# 提示词层级

K-brain 按以下顺序组合提示词，后面的项目规则可以补充前面的内容：

1. `~/.liveagent/system.md`：系统级提示词。首次运行自动写入 K-brain 默认英文提示词；编辑它即可完全替换默认系统提示词。
2. `~/.liveagent/brain.md`：用户级常驻指令。该文件首次创建为空，适合放个人偏好。
3. `~/.claude/CLAUDE.md`：Claude Code 的用户级规则（兼容项，作为用户规则注入）。
4. 从项目根目录（含 `.git` 或 `.liveagent` 的目录）到当前工作目录，每一层依次加载：
   1. `AGENTS.md`
   2. `CLAUDE.md`、`.claude/CLAUDE.md`、`CLAUDE.local.md`、`.claude/CLAUDE.local.md`（Claude Code 兼容）
   3. `.cursorrules` 与 `.cursor/rules/**/*.md|*.mdc`（Cursor 兼容）
   4. `.liveagent/brain.md`：项目级指令。

越深、越本地的文件越靠后，指令冲突时以后者为准。

去重规则：同一文件通过软链接、硬链接或大小写不敏感文件系统以多个名字出现时只加载一次；内容与已加载文件完全相同的文件跳过；只包含 `@AGENTS.md` 这类导入且目标已加载的 `CLAUDE.md` 也跳过。同一目录下内容不同的 `AGENTS.md` 和 `CLAUDE.md` 会同时加载。

Cursor 规则：frontmatter 中 `alwaysApply: true` 的规则，以及没有 frontmatter 的 `.md` 文件，会直接注入；带 `globs` 或 `description` 的规则和手动规则只以路径、描述和 globs 列成索引，模型在相关时再用 read 工具读取。每个 rules 目录最多读取 64 个文件。

兼容文件的大小上限：单个文件 64 KiB（超出部分截断并提示完整路径），所有兼容文件合计 256 KiB（超出预算的文件只列出路径）。`AGENTS.md` 与 `brain.md` 不受此限制。可用 `config.json` 的 `rulesCompat` 关闭兼容项，见[配置文件](05-configuration.md)。

项目内的 `.claude/skills/` 也会作为 skills 目录扫描（排在 `.liveagent/skills`、`.agents/skills` 之后，同名时前者优先）。

文件使用 UTF-8 Markdown。TUI 中 `/system` 编辑系统提示词，`/brain` 编辑用户级 brain.md。修改会在下一轮对话生效；`kn run -system-file path` 可为一次 headless 运行指定系统提示词。

`system.md` 中可以写完整代理规则，也可以只写团队规范。K-brain 仍会自动附加当前工作目录和运行环境信息。
