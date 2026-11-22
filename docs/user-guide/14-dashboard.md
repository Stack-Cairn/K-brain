# 会话总览（Dashboard）

会话总览是一个全屏列表，集中显示各个工作区的 `kn` 会话：当前工作区的会话排在最前，随后是其他工作区最近活动的会话，最上方还有本会话的 subagent。

## 打开方式

- 输入 `/dashboard` 回车（回合进行中也可以打开）；
- 或按 `Ctrl+P` 打开命令面板，选择 **Dashboard**。

`Esc`（或 `q`）关闭总览，回到原来的会话。

## 每一行显示什么

```
    session                               status   workspace            model           tokens · cost   active
  This session's subagents
  ● scan the store for stale run leases   running  sub-1                                               just now
  ✗ summarize test failures               error    sub-2                                                 5m ago

  This workspace
❯ ● (current) Refactor the session store  running  ~                    kimi-k3-fast           191.4k   2m ago
  ○ Fix flaky terminal capture test       idle     ~                    glm-5.2-fast            42.0k   3h ago

  Other workspaces
  ● Gateway rate limiter                  running  src/api-gateway      glm-5.2-fast            55.1k   1m ago
```

- **标题**：会话标题；未命名的会话显示 `(untitled)`，当前会话前面标 `(current)`。
- **状态**（`status`）：`running` 运行中、`waiting` 等待你确认、`idle` 空闲、`error` 失败（只有 subagent 会出现）、`done` 已完成（subagent）。行首的符号与状态对应：`●` 运行中、`◆` 等待、`○` 空闲、`✗` 失败、`✓` 完成；ASCII 模式下分别是 `*`、`+`、`o`、`x`、`v`。
- **工作区**：会话所在目录的最后两级（家目录显示为 `~`）。
- **模型**、**令牌 · 费用**：会话保存的模型和累计令牌数；所用模型都有价格信息时附上费用。
- **活动**：最后一次保存的时间。

列会随终端宽度增减：很窄时只保留状态符号、标题和活动时间，宽度够时依次加上状态、工作区、模型、令牌与费用。

选中一行后，下方的预览区显示：会话 ID、模型与目录、状态的依据、令牌数与消息数、标签、会话目标（`◎ goal`）、保存的待办进度（`☰ todos 1/3 done · next: …`，只读取选中的那一行），以及最后一轮的提问与回复。

## 状态从哪里来

- **当前会话**：直接取自本进程。正在执行回合为 `running`；有权限确认、提问卡片、计划审批等待你处理时为 `waiting`；否则为 `idle`。
- **其他会话**：依据会话存储的运行租约。另一个 `kn` 进程或后端在某个会话里执行回合时会持有这个租约，总览据此显示 `running`，预览里写明是哪个客户端和进程号（例如 `running a turn in kn (pid 4242)`）。
- 在别处打开、但当前没有执行回合的会话无法与无人打开的会话区分，二者都显示 `idle`，并在预览里注明这是“最后活动时间（已保存的状态）”。总览底部也有一行说明。
- `error` 只用于 subagent：会话本身不保存失败状态。

总览打开期间每 2 秒自动刷新一次，也可以按 `r` 立即刷新。

## 按键

| 按键 | 作用 |
|---|---|
| `↑` / `↓`（或 `k` / `j`） | 选择 |
| `PgUp` / `PgDn`、`Home` / `End`（或 `g` / `G`） | 翻页、跳到首尾 |
| `Enter` | 打开选中的会话（等同 `/resume <id>`）；选中 subagent 时打开它的实时视图；选中当前会话时直接关闭总览 |
| `n` | 关闭总览并新建会话（等同 `/new`） |
| `d` / `Ctrl+D` | 删除选中的会话，需再按 `y` 确认，其他任意键取消；与 `/resume` 列表的删除是同一操作 |
| `/` | 开始输入筛选词，按标题、ID、工作区、模型、目标或标签匹配；`Enter` 结束输入并保留筛选，`Esc` 清除 |
| `Tab` | 在“当前工作区 / 其他工作区”和“按工作区分组”之间切换 |
| `r` | 立即刷新 |
| `Esc` / `q` | 有筛选词时先清除筛选，否则关闭总览 |

以下操作会被拒绝，并在底部说明原因：

- 删除当前会话，或删除正在别处执行回合的会话；
- 在本会话执行回合时打开另一个会话（等本回合结束，或先按 `Esc` 中断）；
- 删除 subagent（请在 subagent 面板用 `Ctrl+X` 停止）。

## 范围与限制

- 每次刷新读取当前工作区最近 100 个会话和所有工作区最近 60 个会话；已归档的会话和 subagent 自身的记录不显示。更早的会话请用 `/resume` 或 `/search` 查找。
- subagent 区只列出本会话的 subagent（最多 8 个，运行中的在前）。
- 费用按会话保存的用量和当前已知的模型价格计算；订阅计费的服务商不显示费用。
