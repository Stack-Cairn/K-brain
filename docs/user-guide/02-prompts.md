# 提示词层级

K-brain 按以下顺序组合提示词，后面的项目规则可以补充前面的内容：

1. `~/.k-brain/system.md`：系统级提示词。首次运行自动写入 K-brain 默认英文提示词；编辑它即可完全替换默认系统提示词。
2. `~/.k-brain/brain.md`：用户级常驻指令。该文件首次创建为空，适合放个人偏好。
3. 项目目录向下的 `AGENTS.md`：从项目根目录到当前工作目录依次加载。
4. 当前项目的 `.k-brain/brain.md`：项目级指令。

文件使用 UTF-8 Markdown。TUI 中 `/system` 编辑系统提示词，`/brain` 编辑用户级 brain.md。修改会在下一轮对话生效；`kn run -system-file path` 可为一次 headless 运行指定系统提示词。

`system.md` 中可以写完整代理规则，也可以只写团队规范。K-brain 仍会自动附加当前工作目录和运行环境信息。
