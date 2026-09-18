# Computer-use

氪脑通过 `k-brain-computer` Go RPC helper 提供桌面操作，不包含独立 Desktop 前端。

## 平台范围

| 平台 | 实现与依赖 | 验证状态 |
| --- | --- | --- |
| Windows x64 / ARM64 | Windows PowerShell 5.1、UI Automation、Win32 | 使用现有适配 |
| macOS x64 / ARM64 | Python 3、PyObjC、Accessibility、CoreGraphics | 已实现，尚待真机验证 |
| Linux X11 x64 / ARM64 | Python 3、AT-SPI2、xdotool、Pillow | amd64 GTK/Xvfb 集成验证通过 |
| Linux Wayland | AT-SPI2；部分合成器支持 grim 截图 | 部分语义操作可用，未实现通用 RemoteDesktop/ScreenCast portal |

六平台交叉编译不等于六平台桌面端到端验证。macOS/Linux 使用嵌入式 Python 适配，不是纯 Go 系统调用。

## 安装

```sh
go install ./cmd/kn ./cmd/k-brain-computer
```

发布包中的 helper 放在 `kn` 旁，或用 `K_BRAIN_COMPUTER_BIN` 指定路径。macOS/Linux 可用 `K_BRAIN_COMPUTER_PYTHON` 指定解释器，默认查找 `python3`。

### Windows

在当前用户已登录且未锁定的桌面运行，使用系统自带 Windows PowerShell。受保护或更高权限的窗口可能拒绝操作。

### macOS

```sh
python3 -m venv ~/.k-brain/computer-venv
~/.k-brain/computer-venv/bin/python -m pip install pyobjc-framework-Cocoa pyobjc-framework-Quartz pyobjc-framework-ApplicationServices
export K_BRAIN_COMPUTER_PYTHON="$HOME/.k-brain/computer-venv/bin/python"
```

在系统设置中为实际终端和解释器授予辅助功能与屏幕录制权限。坐标采用桌面点坐标，不是 Retina 截图像素。

### Linux X11（Debian / Ubuntu）

```sh
sudo apt-get install python3-pyatspi python3-pil python3-pil.imagetk xdotool
export K_BRAIN_COMPUTER_PYTHON=/usr/bin/python3
```

在图形会话内运行，保留 DISPLAY、D-Bus 和 accessibility bus。Wayland 不会静默回退到 xdotool，通用授权截图和全局输入仍待实现。

## 接口

支持 `apps`、`permissions.status`、`permissions.request`、`state`、`ax`、`screenshot`、`click`、`type`、`press`、`set`、`select`、`menu`、`scroll`。

先获取 `state(app)`，再将返回的 `generation` 作为操作的 `gen`。UI 变化后需重新读取，不能复用旧控件索引。操作能力取决于应用的 accessibility 接口。

## 验证

```sh
go test ./internal/computer/... -count=1
python3 -I internal/computer/driver/desktop_test.py
```

Linux GTK/Xvfb 及 Go RPC 集成测试见 [发布工作流](.github/workflows/build.yml)。Windows 桌面测试通过 `K_BRAIN_TEST_DESKTOP=1` 显式启用。macOS 真机、ARM 桌面和通用 Wayland portal 未完成验证。
