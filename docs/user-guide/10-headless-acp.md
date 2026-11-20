# Headless 与 ACP

不进入 TUI 时可使用 `kn run`：

```sh
kn run "总结当前仓库的改动"
kn run -model model1 "修复测试并运行 go test ./..."
kn run -system-file ./system.md "按该系统提示词执行任务"
```

标准输入也可以提供提示词，输出写入标准输出，适合脚本和 CI。`-system` 可直接传递一次性系统提示词；`-system-file` 从文件读取。

### 常用参数

```sh
kn run -cwd ~/src/app -c "继续上次的任务"
kn run -allowed-tools read,grep,glob "只读地梳理模块结构"
kn run -permission-mode normal -allow 'Bash(go test:*)' -deny 'Bash(rm:*)' "跑测试并修复失败"
kn run -image ./screenshot.png -effort high "这个界面哪里有问题？"
```

| 参数 | 说明 |
| --- | --- |
| `-cwd DIR` | 在指定目录中运行，等同于先 `cd` 进去：系统提示词、插件、沙箱、权限规则中的相对路径和会话归属都以该目录为准。目录不存在时报错。 |
| `-c` / `-continue` | 续接该工作目录下最近一次会话（与 TUI 的 `kn -c` 相同）。没有可续接的会话时报错，不会悄悄新建；不能与 `-resume` 或 `-no-session` 同用。 |
| `-allowed-tools a,b` | 本次运行只提供这些工具，其余工具既不发给模型也无法执行。 |
| `-disallowed-tools a,b` | 本次运行移除这些工具；可与 `-allowed-tools` 同用，先取允许列表再去掉禁用项。工具名写错时报错，并列出当前可用的工具名。 |
| `-allow RULE` | 本次运行允许匹配该规则的操作，可重复。规则写法与权限规则文件相同，例如 `Bash(go test:*)`、`Edit(src/**)`、`web_fetch(domain:example.com)`。只在本次运行生效，不写入任何规则文件。 |
| `-deny RULE` | 本次运行拒绝匹配该规则的操作，可重复，同样不保存。拒绝规则优先于 `-allow` 和 `-permission-mode`（包括 `full_access` / `always`）。 |
| `-image PATH` | 把图片作为视觉输入随提示词发送，可重复；支持 png、jpg、jpeg、gif、webp、bmp，处理方式与 TUI 中附加图片相同。当前模型不支持图片输入时报错。 |
| `-effort LEVEL` | 本次运行的推理强度：`off`，或模型支持的档位（如 `low`、`medium`、`high`，模型目录列出更多档位时也可用）。填写不支持的档位会报错并列出可选值。 |

`-allow` / `-deny` 只决定需要审批的操作：命中 `-deny` 的直接拒绝；命中 `-allow` 的直接放行；两者都未命中时按 `-permission-mode` 处理，未指定模式时与以往一样放行。无法逐段核对的复合命令遇到 `-deny` 规则时一律拒绝。`kn run -h` 列出全部参数。

ACP（Agent Client Protocol）入口用于编辑器或其他客户端连接 K-brain。使用 `kn acp` 启动 ACP 服务，provider、模型和系统提示词仍从同一份 `~/.liveagent` 配置读取。
