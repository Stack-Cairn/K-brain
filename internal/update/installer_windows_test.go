package update

import (
	"strings"
	"testing"
)

func TestWindowsInstallerCommand(t *testing.T) {
	cmd := InstallerCommand()
	if !strings.HasSuffix(strings.ToLower(cmd.Path), "powershell.exe") {
		t.Fatalf("%v", cmd)
	}
	script := cmd.Args[len(cmd.Args)-1]
	if !strings.Contains(script, "https://raw.githubusercontent.com/Stack-Cairn/K-brain/main/install.ps1") || !strings.Contains(script, "-InstallDir '") {
		t.Fatal(script)
	}
}
