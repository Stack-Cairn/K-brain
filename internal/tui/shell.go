package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/tools"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

type shellDoneMsg struct {
	cmd       string
	out       string
	localOnly bool
}

func (m *model) runShell(text string) {
	m.startShell(text, true)
}

func (m *model) runShellQueued(text string) {
	m.startShell(text, false)
}

func (m *model) startShell(text string, echo bool) {
	localOnly := strings.HasPrefix(text, "!!")
	prefix := "!"
	if localOnly {
		prefix = "!!"
	}
	cmdLine := strings.TrimSpace(strings.TrimPrefix(text, prefix))
	if cmdLine == "" {
		m.append(dimStyle.Render("(! <command> — run a shell command, output shared with the model; !! <command> stays local)"))
		return
	}
	if echo {
		m.flushThink()
		m.flushCurrent()
		m.append(youStyle.Render(glyphUser) + text)
	}

	if m.prog == nil {

		out := shellExec(cmdLine)
		m.applyShellDone(shellDoneMsg{cmd: cmdLine, out: out, localOnly: localOnly})
		return
	}
	p := m.prog
	go func() {
		p.Send(shellDoneMsg{cmd: cmdLine, out: shellExec(cmdLine), localOnly: localOnly})
	}()
}

func shellExec(cmdLine string) string {

	res := bashrun.Run(context.Background(), bashrun.Options{Command: cmdLine})
	out := tools.TruncateTail(res.Output)
	if tools.IsBinary([]byte(res.Output)) {

		out = tools.BinaryPlaceholder("", len(res.Output))
	}
	if res.TimedOut {
		out += "\n(command timed out)"
	} else if res.Exit != "" {
		out = fmt.Sprintf("%s\n(%s)", out, res.Exit)
	}
	if strings.TrimSpace(out) == "" {
		out = "(no output)"
	}
	return out
}

func (m *model) applyShellDone(msg shellDoneMsg) {

	m.appendRaw(blockTool, msg.out)
	if msg.localOnly {
		return
	}

	content := "$ " + msg.cmd + "\n" + msg.out
	if m.busy {

		m.agent.Steer(content)
		return
	}

	m.agent.AppendUser(content)
	m.persist()
}

func (m *model) cdCommand(arg string) {
	if arg == "" {
		m.append(dimStyle.Render(cwd()))
		return
	}
	if arg == "~" || strings.HasPrefix(arg, "~/") || strings.HasPrefix(arg, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			m.append(errStyle.Render("/cd: " + err.Error()))
			return
		}
		arg = home + arg[1:]
	}
	if err := os.Chdir(arg); err != nil {
		m.append(errStyle.Render("/cd: " + err.Error()))
		return
	}
	m.append(dimStyle.Render("→ " + cwd()))
}
