# 外观与状态行

本页说明终端界面（TUI）的配色主题以及与外观相关的设置。所有设置都写在主配置 `~/.liveagent/config.json` 中，也可以在 TUI 里通过 `/settings`（F2）或对应的斜杠命令修改。

## 主题

`theme` 选择配色。主题只决定 `kn` 自己使用的颜色（强调色、状态色、代码高亮、diff 底色、面板底色等）；正文始终使用终端自己的前景色，`kn` 也不会给整个屏幕铺背景色。

| 名称 | 变体 | 说明 |
|---|---|---|
| `auto`（默认） | 深色 + 浅色 | 默认配色，按终端背景自动选择深色或浅色变体 |
| `light` / `dark` | 浅色 / 深色 | 默认配色，固定使用某个变体 |
| `dusk` | 仅深色 | 紫灰底调，琥珀色强调 |
| `harbor` | 深色 + 浅色 | 石板蓝底调，天蓝色强调；按终端背景切换变体 |
| `paper` | 仅浅色 | 暖纸色底调，墨色文字与铁锈色强调 |
| `contrast` | 深色 + 浅色 | 高对比度：正文类颜色对比度不低于 7:1，其余信号色不低于 4.5:1 |
| `mono` | — | 不使用颜色，只用粗体、暗淡、斜体和反显区分结构（`NO_COLOR` 同样生效） |

同时有深浅两个变体的主题（`auto`、`harbor`、`contrast` 和自定义主题）会跟随终端背景；在名称后加 `:light` 或 `:dark` 可固定变体，例如 `"theme": "harbor:light"`。只有一个变体的主题始终使用该变体，因此 `dusk` 适合深色终端，`paper` 适合浅色终端。

切换方式：

- `/theme`：打开主题选择器。上下移动即在整个界面上实时预览，`Enter`（或 `←`/`→`）保存，`Esc` 恢复原来的主题。选择器下方的示例行展示当前预览主题的信号色、代码高亮和 diff 底色。
- `/theme <名称>`：直接切换并保存，例如 `/theme dusk`、`/theme harbor:dark`。
- `/settings` → Display → Theme：`←`/`→` 依次切换所有主题，`Enter` 打开选择器。

每个内置主题都为真彩色、256 色和 16 色终端分别挑选了颜色，而不是简单降采样：真彩色与 256 色下，文字类颜色在典型背景上满足 WCAG AA（4.5:1），其他信号色满足 3:1；16 色下不会使用通常就是终端背景的那个色槽。没有颜色的终端（`NO_COLOR`、`TERM=dumb`）和 `K_BRAIN_ASCII=1` 的 ASCII 模式在任何主题下都可用。

## 跟随系统深浅色

主题跟随终端背景时（`auto`、未固定变体的 `harbor`、`contrast` 或自定义主题），`kn` 在运行中也会跟着切换，无需重启：

- 终端窗口重新获得焦点时，以及每 10 秒（窗口在前台时），`kn` 用 OSC 11 再次询问终端背景色，同时发送颜色方案查询（`CSI ? 996 n`）。
- 启动时打开终端的颜色方案通知（模式 2031）；支持它的终端在系统切换深浅色时会主动通知 `kn`。
- 检测到变化后立即切换深色/浅色变体，重新渲染整个对话记录，并在对话记录中提示一行 `◐ theme: auto → light (terminal appearance changed)`。

终端的应答会从键盘输入中剔除，不会出现在输入框里；只在查询后的短时间内识别应答，看起来像应答开头但不是应答的按键会按原顺序照常输入。固定变体的主题（`light`、`dark`、`harbor:dark`、`dusk`、`paper` 等）和 `mono` 不会发送查询。设置了 `K_BRAIN_THEME=light|dark` 时以环境变量为准。在 tmux 中还会读取 tmux 的 `client_theme`。

## 自定义主题

在 `~/.liveagent/themes/` 下放置 `*.json` 文件即可添加自己的主题。每个文件以一个内置主题为基础，只覆盖想改的颜色：

```json
{
  "name": "ocean",
  "description": "deep blue",
  "base": "harbor",
  "dark": {
    "accent": "#4fc3f7",
    "state.ok": { "hex": "#7fd18b", "x256": 114, "ansi": 10 },
    "surface.band": "#10202c"
  },
  "light": {
    "accent": "#0277bd"
  }
}
```

| 字段 | 说明 |
|---|---|
| `name` | 可选。主题名，1–32 个小写字母、数字或 `-`；省略时取文件名。不能与内置主题或 `auto`、`light`、`dark`、`mono`、`default` 重名 |
| `description` | 可选。选择器里显示的一行说明 |
| `base` | 可选。基础主题：`default`（默认）、`dusk`、`harbor`、`paper` 或 `contrast`。自定义主题拥有与基础主题相同的变体，例如以 `dusk` 为基础就只有深色变体 |
| `dark` / `light` | 可选。对应变体要覆盖的颜色，键为颜色名，未写出的颜色沿用基础主题 |

颜色值可以是 `"#rrggbb"`，也可以是对象 `{"hex": "#rrggbb", "x256": 0–255, "ansi": 0–15}`，用来为 256 色和 16 色终端指定颜色。省略时 `kn` 自动选取最接近的 256 色，以及最接近且不是终端背景色槽的 16 色。背景类颜色（`surface.*`、`diff.*.bg`）在 16 色终端上不着色，因此不接受 `ansi`。

可覆盖的颜色名：

| 类别 | 颜色名 |
|---|---|
| 文字 | `text.secondary`、`text.muted`、`text.hint` |
| 底色与分隔线 | `surface.band`（用户消息、代码块底色）、`surface.raised`（选中行、面板）、`line.rule` |
| 强调与角色 | `accent`（光标、选中项、焦点）、`role.assistant` |
| 状态 | `state.running`、`state.ok`、`state.warn`、`state.error` |
| diff | `diff.add.bg`、`diff.del.bg` |
| 代码高亮 | `syn.keyword`、`syn.string`、`syn.number`、`syn.func`、`syn.type`、`syn.comment` |
| 其他 | `chip.ink`（模式标签上的文字）、`logo.stem`、`logo.upper`、`logo.lower` |

正文颜色（`text.primary`）始终是终端自己的前景色，不能覆盖。

校验与加载：

- 文件必须是合法 JSON，不认识的字段、颜色名，格式错误的颜色、超出范围的 `x256` / `ansi` 都会让整个文件被跳过。错误会写明文件和字段，例如 `themes/ocean.json: dark.accent: "teal" is not a #rrggbb colour`，显示在启动时的对话记录和 `/theme` 选择器顶部。
- 对比度低于内置主题的标准（正文类 4.5:1、其他信号色 3:1，以 `contrast` 为基础时为 7:1 和 4.5:1）只给出警告，主题仍可使用；警告显示在选择器中该主题下方。
- 打开 `/theme`、执行 `/theme <名称>` 时会重新读取主题目录；`kn` 运行期间每秒检查一次目录，保存文件后正在使用的自定义主题立即刷新。文件被删除或改坏时退回默认主题，但设置保留，文件修好后自动恢复。
