//go:build !windows

package driver

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
