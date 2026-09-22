# Headless 与 ACP

不进入 TUI 时可使用 `kn run`：

```sh
kn run "总结当前仓库的改动"
kn run -model model1 "修复测试并运行 go test ./..."
kn run -system-file ./system.md "按该系统提示词执行任务"
```

标准输入也可以提供提示词，输出写入标准输出，适合脚本和 CI。`-system` 可直接传递一次性系统提示词；`-system-file` 从文件读取。

ACP（Agent Client Protocol）入口用于编辑器或其他客户端连接 K-brain。使用 `kn acp` 启动 ACP 服务，provider、模型和系统提示词仍从同一份 `~/.k-brain` 配置读取。
