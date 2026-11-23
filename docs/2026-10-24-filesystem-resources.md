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
- `{"action":"mention_list","workdir":"/path/to/projects/demo","path":"","query":"serv","max_results":50}`：@ 提及候选。见下节。

### mention_list

返回 `path`（目录，工作空间相对，省略或 `""` 为整个 workdir）以下可被提及的文件和目录：

```json
{"path":"","query":"serv","entries":[{"path":"server","kind":"dir"},{"path":"internal/server.go","kind":"file"}],"truncated":false}
```

- `entries[].path` 是相对 workdir 的路径（`/` 分隔，目录不带结尾 `/`），`kind` 为 `file` 或 `dir`。符号链接和其他特殊文件不列出，也不进入符号链接目录。
- 忽略规则：任何层级名为 `.git`、`.hg`、`.svn`、`.jj`、`node_modules`、`__pycache__`、`.venv`、`.DS_Store` 的项一律跳过（目录不再进入）；从 workdir 根到每一层目录的 `.gitignore` 与 `.ignore` 都生效，不要求是 Git 仓库。规则按 gitignore 写法：`#` 注释、`!` 取反、结尾 `/` 只匹配目录、开头 `/` 或含中间 `/` 的模式相对该文件所在目录，否则在任意深度匹配，后出现的规则优先。`path` 落在被忽略目录内时返回空列表。以 `.` 开头的其他文件照常列出（不受 `show_hidden` 影响）。
- 排序：`query` 不区分大小写。名称以 `query` 开头的排最前，其次名称包含、路径包含、名称按序含有 `query` 的各字符、路径按序含有各字符；不匹配的不返回。同一档内路径短的在前，再按路径字典序。`query` 为空时全部匹配，按深度（浅的在前）再按路径字典序。
- 上限：`max_results` 默认 50，最大 500（超过返回 422）。一次请求最多查看 50000 个目录项，深度最多 32 层。匹配项多于 `max_results`，或因查看上限没有走完整棵树时 `truncated` 为 `true`；客户端应提示继续输入以缩小范围。
- 健康检查的 `capabilities.filesystem_mention_list` 为 1 时可用；旧后端对该 action 返回 422，对 `query` 字段返回 400。

范围预览仅在请求携带 `preview_offset` 时返回 `preview_offset`（实际对齐后的起点）和 `preview_next_offset`（后续字节位置，EOF 为 `null`）。`size_bytes` 始终为完整文件大小，`mtime_ms` 为文件修改时间。片段不返回 `content_hash` 或 `total_lines`，不能用作覆盖保存的版本依据。实现直接 seek，不扫描前缀、不索引全文；I/O 最多为 256 KiB + 4 字节窗口及 3 字节字符边界回查，内存不随整文件大小增加。完整读取仍返回 SHA-256 和总行数。

`/v1/fs` 的 JSON 请求体上限为 `6 × 16 MiB + 64 KiB = 100,728,832` 字节。六倍空间覆盖 `\u0001` 这类 JSON 转义，64 KiB 留给路径、版本和请求字段；解码后的 `content` 仍单独校验 16 MiB 上限。其他普通 API 继续使用 4 MiB 请求体上限。

路径在 `os.Root` 范围内解析。文件读取额外限定在所选 workdir，拒绝符号链接逃逸。完整文本读写上限 16 MiB，二进制 `read_preview` 上限 2 MiB，列表深度上限 20，单页上限 10000。列表不跟随符号链接目录；`list` 不应用项目 ignore 规则，`mention_list` 应用（见上）。写操作串行，现有文件经版本校验后临时文件原子替换；新文件独占创建。外部进程可在最后校验与替换间修改文件，因此不宣称跨进程强事务。

## 验证与待办

16 MiB 完整读写、超限拒绝、大文件版本冲突、UTF-8 分页重组、末字符内偏移、直接 seek 与 I/O 上限、最坏 JSON 转义和请求体限制已增加回归用例。本次开发阶段尚未执行测试；按仓库使用者的测试时机要求，待验收并提交、推送 PR 后与 CI 同时执行。

路径隔离、认证、分页、隐藏文件、文本和取消测试通过。KB 全量测试、vet、backend/planning race 通过。Web 两档视口经 Gateway 实测目录选择、文件列表与文本读取。

文件编辑版本校验、原子写入、创建/重命名/删除和预览已补齐。Web 两档实际编辑 `brain.md` 并保存回读；`AGENTS.md`/`CLAUDE.md` 实际 WS 生命周期和陈旧写入拒绝通过。下一步仍需完整图片/文件引用、ignore、Windows 独占重命名与 SSH/SFTP/隧道迁移。不能声称完整文件管理或 SSH/隧道已由 KB 接管。

外部服务续测：Google 公共 ICS 已真实同步，隔离数据保存 30 个事件；私人 Google/iCloud/Exchange 账号和双向同步未验收。macOS debug 裸二进制通知测试明确要求 `.app` bundle；真实横幅未验收。图像后端 identity 与页面读取通过，但无真实生成模型。语音仍在 Gateway，未使用私人服务密钥。
