//go:build !windows

package process

import (
	"os"
	"os/exec"
	"syscall"
)

func Configure(cmd *exec.Cmd, session bool) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: !session, Setsid: session}
	cmd.Cancel = func() error { return Kill(cmd) }
}

func Kill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

func Alive(cmd *exec.Cmd) bool {
	return cmd.Process != nil && cmd.Process.Signal(syscall.Signal(0)) == nil
}
