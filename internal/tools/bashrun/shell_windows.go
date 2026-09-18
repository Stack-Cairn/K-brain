package bashrun

import (
	"os/exec"
	"syscall"
)

func configureShellCommand(cmd *exec.Cmd, name, command string) {
	if name == "cmd" {

		cmd.SysProcAttr = &syscall.SysProcAttr{
			CmdLine: syscall.EscapeArg(cmd.Path) + ` /D /S /C "` + command + `"`,
		}
	}
}
