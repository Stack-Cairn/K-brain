package update

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const InstallURL = "https://raw.githubusercontent.com/Stack-Cairn/K-brain/main/install.sh"

func InstallerCommand() *exec.Cmd {
	if runtime.GOOS != "windows" {
		return exec.CommandContext(context.Background(), "sh", "-c", "curl -fsSL "+InstallURL+" | sh")
	}
	script := "[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12; $ErrorActionPreference = 'Stop'; & ([scriptblock]::Create((Invoke-WebRequest -UseBasicParsing '" + strings.TrimSuffix(InstallURL, "sh") + "ps1').Content))"
	if exe, err := os.Executable(); err == nil {
		script += " -InstallDir '" + strings.ReplaceAll(filepath.Dir(exe), "'", "''") + "'"
	}
	return exec.CommandContext(context.Background(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
}
