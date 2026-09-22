# 安装与首次运行

## 安装

发布包包含 `kn` 可执行文件。Windows 使用 PowerShell 安装脚本，Linux 和 macOS 使用对应的二进制；也可以从源码构建：

```sh
go build -o kn ./cmd/kn
```

启动 TUI：

```sh
kn
```

首次运行时 K-brain 会创建 `~/.k-brain/`（Windows 为 `%USERPROFILE%\\.k-brain`）。没有 API 配置时仍可进入 TUI，之后可编辑配置再发送请求。

## 最小配置

在 `~/.k-brain/config.json` 写入一个 Pi 风格 provider：

```json
{
  "name": "my-provider",
  "api": "openai-completions",
  "baseUrl": "https://api.example.com/v1",
  "apiKey": "YOUR_KEY",
  "models": []
}
```

也可以使用完整配置（见[配置文件](05-configuration.md)）。API key 支持直接填写值，也支持环境变量引用。
