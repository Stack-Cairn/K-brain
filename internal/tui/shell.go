package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/prompts"
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

	dir := cwd()
	if m.agent != nil && m.agent.WorkingDir != "" {
		dir = m.agent.WorkingDir
	}
	if m.prog == nil {

		out := shellExecAt(cmdLine, dir)
		m.applyShellDone(shellDoneMsg{cmd: cmdLine, out: out, localOnly: localOnly})
		return
	}
	p := m.prog
	go func() {
		p.Send(shellDoneMsg{cmd: cmdLine, out: shellExecAt(cmdLine, dir), localOnly: localOnly})
	}()
}

func shellExec(cmdLine string) string {
	return shellExecAt(cmdLine, cwd())
}

func shellExecAt(cmdLine, dir string) string {
	res := bashrun.Run(context.Background(), bashrun.Options{Command: cmdLine, Dir: dir})
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
	if m.busy || m.agent != nil && m.agent.TurnRunning() {
		m.append(errStyle.Render("/cd: wait for the current turn to finish"))
		return
	}
	if m.agent != nil {
		for _, task := range m.agent.Tasks().List() {
			if task.Status == agent.TaskRunning || task.FollowingUp {
				m.append(errStyle.Render("/cd: wait for background subagents to finish or cancel them first"))
				return
			}
		}
	}
	if arg == "~" || strings.HasPrefix(arg, "~/") || strings.HasPrefix(arg, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			m.append(errStyle.Render("/cd: " + err.Error()))
			return
		}
		arg = home + arg[1:]
	}
	oldDir, err := os.Getwd()
	if err != nil {
		m.append(errStyle.Render("/cd: " + err.Error()))
		return
	}
	if err := os.Chdir(arg); err != nil {
		m.append(errStyle.Render("/cd: " + err.Error()))
		return
	}
	dir := cwd()
	clearSnapshots := len(m.snapshots) > 0 && !sameWorkspace(oldDir, dir)
	if clearSnapshots && m.store != nil && m.sessionID != "" {
		if err := m.store.ClearSnapshots(m.sessionID); err != nil {
			if rollbackErr := os.Chdir(oldDir); rollbackErr != nil {
				m.append(errStyle.Render("/cd: cannot return to previous directory: " + rollbackErr.Error()))
			} else {
				m.append(errStyle.Render("/cd: cannot clear previous workspace snapshots: " + err.Error()))
				return
			}
		}
	}
	if clearSnapshots {
		m.snapshots = nil
		m.append(dimStyle.Render("(workspace changed; file rewind snapshots cleared, conversation retained)"))
	}
	policy := m.sandboxPolicy
	if policy == nil && m.agent != nil {
		policy = m.agent.SandboxPolicy
	}
	m.sandboxPolicy = policy.ForRoot(dir)
	m.sysPrompt = prompts.WithWorkingDirectory(m.sysPrompt, dir)
	if m.agent != nil {
		m.agent.WorkingDir = dir
		m.agent.SandboxPolicy = m.sandboxPolicy
		if len(m.agent.Messages) > 0 && m.agent.Messages[0].Role == "system" {
			m.agent.Messages[0].Content = prompts.WithWorkingDirectory(m.agent.Messages[0].Content, dir)
		}
	}
	m.append(dimStyle.Render("→ " + dir))
}

func sameWorkspace(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rootA, err := gitRawAt(ctx, a, "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	rootB, err := gitRawAt(ctx, b, "rev-parse", "--show-toplevel")
	return err == nil && rootA == rootB
}
