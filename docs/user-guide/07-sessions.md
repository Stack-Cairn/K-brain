# 会话、压缩与回退

会话保存在 `~/.liveagent/sessions/` 下，并按项目工作目录分类；每个会话包含稳定的 session id、标题、消息和元数据。`/resume`、`/search`、`/archive`、`/export` 用于管理历史记录。

## 上下文压缩

达到模型上下文预算时，K-brain 使用摘要压缩旧消息并保留最近对话。`/compact` 可立即执行，`/compact fresh` 创建新的上下文窗口但保留原始历史，`/compact off` 关闭自动压缩，`/compact log` 查看压缩记录。

每次摘要压缩还会把被移出上下文的消息保存为可读的 Markdown，放在会话目录下的 `compaction/` 里：`segment_001.md`、`segment_002.md`…… 各对应一次压缩，`INDEX.md` 列出所有分段。发给模型的摘要末尾会附上这个目录的路径，模型需要摘要里没写的细节时可以用 read 或 grep 去查。LiveAgent 后端会把这个目录作为只读根授权给每次运行。图片和其他 base64 内容在分段里用占位符代替。单条消息最多保留 64 KiB，单个分段最多 4 MiB，一个会话的分段合计超过 64 MiB 时从最旧的开始删除。会话删除或移动时，这个目录跟着删除或移动。

## Rewind

空闲时按两次 `Esc` 或输入 `/rewind`，用方向键选择轮次并按 `Enter` 回退；按 `f` 可从该轮 fork 新会话。`/undo` 是直接打开回退界面的别名。
