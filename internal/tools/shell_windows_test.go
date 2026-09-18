package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWindowsToolShellSelection(t *testing.T) {
	t.Setenv("K_BRAIN_SHELL", "missing-shell")
	args := json.RawMessage(`{"shell":"powershell", "command":"Write-Output 'selected-powershell'"}`)
	out, err := bashTool().Run(t.Context(), args)
	if err != nil || !strings.Contains(out, "selected-powershell") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestWindowsToolInteractiveError(t *testing.T) {
	_, err := bashTool().Run(t.Context(), json.RawMessage(`{"shell":"powershell", "command":"Read-Host", "interactive":true}`))
	if err == nil || !strings.Contains(err.Error(), "PTY") {
		t.Fatalf("err=%v", err)
	}
}
