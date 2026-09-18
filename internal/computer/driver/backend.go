package driver

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

//go:embed windows.ps1
var windowsScript string

//go:embed desktop_common.py
var desktopCommon string

//go:embed desktop_linux.py
var desktopLinux string

//go:embed desktop_macos.py
var desktopMacOS string

type platformBackend struct{}

func NewBackend() Backend { return platformBackend{} }

func (platformBackend) Call(ctx context.Context, method string, p Params, out any) error {
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		return callDesktop(ctx, runtime.GOOS, method, p, out)
	}
	if runtime.GOOS != "windows" {
		return &Fault{6, "unsupported desktop platform: " + runtime.GOOS}
	}
	payload, err := json.Marshal(map[string]any{"method": method, "params": p})
	if err != nil {
		return err
	}
	script, err := os.CreateTemp("", "k-brain-computer-*.ps1")
	if err != nil {
		return err
	}
	defer os.Remove(script.Name())
	if _, err := script.WriteString(windowsScript); err != nil {
		script.Close()
		return err
	}
	if err := script.Close(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script.Name())
	hideWindow(cmd)
	cmd.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("computer backend: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *Fault          `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}), &envelope); err != nil {
		return fmt.Errorf("invalid backend response: %w", err)
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	return json.Unmarshal(envelope.Result, out)
}
