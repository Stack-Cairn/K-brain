---
title: 后端文件资源接口
tags: [backend, filesystem, webui]
---

# 后端文件资源接口

认证后的 `POST /v1/fs` 为 LiveAgent 资源交互提供独立于模型运行的文件访问。Gateway 只转发固定类型，文件访问由 KB 执行。原生 SSH/SFTP 工具契约及本协议的反向引用见 [[liveagent-backend]]。

## 配置

`kn backend -filesystem-root /path/to/projects` 可重复指定。未指定时仅暴露后端当前工作目录。请求中的 `workdir` 不会自动授予访问权；启动者需要增加需要浏览的根目录。

## 请求

- `{"action":"roots"}`：列出配置的根目录。
- `{"action":"list_dirs","path":"/path/to/projects","max_results":200}`：目录选择器。
- `{"action":"list","workdir":"/path/to/projects/demo","path":"src","depth":1,"offset":0,"max_results":200,"show_hidden":false}`：文件树。
- `{"action":"read_text","workdir":"/path/to/projects/demo","path":"README.md"}`：完整 UTF-8 文本及内容摘要、大小、修改时间、行数，上限 16 MiB（16,777,216 字节）。读取通过有界 reader 执行，文件在 stat 后增长也不会绕过上限。
- `{"action":"read_text","workdir":"/path/to/projects/demo","path":"README.md","preview_offset":0}`：从指定字节偏移读取最多 256 KiB（262,144 字节）的 UTF-8 片段，可用于超过完整读取上限的文件。偏移落在字符内部时向前对齐到下一个字符起点，尾部保留完整字符；非法 UTF-8 和 NUL 被拒绝。
- `read_preview`：返回 Base64 数据、MIME 类型、摘要、时间和大小，供文本及二进制预览使用，上限 2 MiB。
- `write_text`：`content`、`mode: "rewrite"`；覆盖现有文件必须同时提交此前读取的 `expected_mtime_ms` 和 `expected_content_hash`。
- `mkdir`、`delete`：创建目录、删除文件或空目录；不隐式递归删除非空目录。
- `rename`：`path` 与 `to_path` 均为工作空间相对路径，不覆盖目标。目前独占重命名支持 macOS/Linux，其他平台明确拒绝。

范围预览仅在请求携带 `preview_offset` 时返回 `preview_offset`（实际对齐后的起点）和 `preview_next_offset`（后续字节位置，EOF 为 `null`）。`size_bytes` 始终为完整文件大小，`mtime_ms` 为文件修改时间。片段不返回 `content_hash` 或 `total_lines`，不能用作覆盖保存的版本依据。实现直接 seek，不扫描前缀、不索引全文；I/O 最多为 256 KiB + 4 字节窗口及 3 字节字符边界回查，内存不随整文件大小增加。完整读取仍返回 SHA-256 和总行数。

`/v1/fs` 的 JSON 请求体上限为 `6 × 16 MiB + 64 KiB = 100,728,832` 字节。六倍空间覆盖 `\u0001` 这类 JSON 转义，64 KiB 留给路径、版本和请求字段；解码后的 `content` 仍单独校验 16 MiB 上限。其他普通 API 继续使用 4 MiB 请求体上限。

路径在 `os.Root` 范围内解析。文件读取额外限定在所选 workdir，拒绝符号链接逃逸。完整文本读写上限 16 MiB，二进制 `read_preview` 上限 2 MiB，列表深度上限 20，单页上限 10000。列表不跟随符号链接目录；暂未实现项目 ignore 规则。写操作串行，现有文件经版本校验后临时文件原子替换；新文件独占创建。外部进程可在最后校验与替换间修改文件，因此不宣称跨进程强事务。

## 验证与待办

16 MiB 完整读写、超限拒绝、大文件版本冲突、UTF-8 分页重组、末字符内偏移、直接 seek 与 I/O 上限、最坏 JSON 转义和请求体限制已增加回归用例。本次开发阶段尚未执行测试；按仓库使用者的测试时机要求，待验收并提交、推送 PR 后与 CI 同时执行。

路径隔离、认证、分页、隐藏文件、文本和取消测试通过。KB 全量测试、vet、backend/planning race 通过。Web 两档视口经 Gateway 实测目录选择、文件列表与文本读取。

文件编辑版本校验、原子写入、创建/重命名/删除和预览已补齐。Web 两档实际编辑 `brain.md` 并保存回读；`AGENTS.md`/`CLAUDE.md` 实际 WS 生命周期和陈旧写入拒绝通过。下一步仍需完整图片/文件引用、ignore、Windows 独占重命名与 SSH/SFTP/隧道迁移。不能声称完整文件管理或 SSH/隧道已由 KB 接管。

外部服务续测：Google 公共 ICS 已真实同步，隔离数据保存 30 个事件；私人 Google/iCloud/Exchange 账号和双向同步未验收。macOS debug 裸二进制通知测试明确要求 `.app` bundle；真实横幅未验收。图像后端 identity 与页面读取通过，但无真实生成模型。语音仍在 Gateway，未使用私人服务密钥。
