# 会话、压缩与回退

会话保存在 `~/.k-brain/sessions/` 下，并按项目工作目录分类；每个会话包含稳定的 session id、标题、消息和元数据。`/resume`、`/search`、`/archive`、`/export` 用于管理历史记录。

## 上下文压缩

达到模型上下文预算时，K-brain 使用摘要压缩旧消息并保留最近对话。`/compact` 可立即执行，`/compact fresh` 创建新的上下文窗口但保留原始历史，`/compact off` 关闭自动压缩，`/compact log` 查看压缩记录。

## Rewind

空闲时按两次 `Esc` 或输入 `/rewind`，用方向键选择轮次并按 `Enter` 回退；按 `f` 可从该轮 fork 新会话。`/undo` 是直接打开回退界面的别名。
