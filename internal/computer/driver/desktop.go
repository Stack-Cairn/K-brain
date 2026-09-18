package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func desktopScript(platform string) (string, error) {
	switch platform {
	case "linux":
		return desktopCommon + "\n" + desktopLinux + "\nserve(LinuxDesktop)\n", nil
	case "darwin":
		return desktopCommon + "\n" + desktopMacOS + "\nserve(MacDesktop)\n", nil
	default:
		return "", &Fault{6, "unsupported desktop platform: " + platform}
	}
}

func callDesktop(ctx context.Context, platform, method string, p Params, out any) error {
	script, err := desktopScript(platform)
	if err != nil {
		return err
	}
	python := os.Getenv("K_BRAIN_COMPUTER_PYTHON")
	if python == "" {
		python = "python3"
	}
	path, err := exec.LookPath(python)
	if err != nil {
		hint := "Python 3 is required; set K_BRAIN_COMPUTER_PYTHON to the desktop adapter interpreter"
		if method == "permissions.status" || method == "permissions.request" {
			data, _ := json.Marshal(map[string]any{"accessibility": false, "screenRecording": false, "hint": hint})
			return json.Unmarshal(data, out)
		}
		return &Fault{6, hint}
	}
	payload, err := json.Marshal(map[string]any{"method": method, "params": p})
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, path, "-I", "-c", script)
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("computer desktop adapter: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *Fault          `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("invalid desktop response: %w", err)
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	if len(envelope.Result) == 0 {
		return fmt.Errorf("desktop adapter returned no result")
	}
	return json.Unmarshal(envelope.Result, out)
}
