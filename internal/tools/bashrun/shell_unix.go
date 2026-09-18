//go:build !windows

package bashrun

import "os/exec"

func configureShellCommand(_ *exec.Cmd, _, _ string) {}
