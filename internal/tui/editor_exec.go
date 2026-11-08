package tui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// externalProgramRunning is set while an external editor owns the terminal.
// The event loop is legitimately parked for as long as the editor runs, so
// the UI watchdog must not report it as a stall.
var externalProgramRunning atomic.Bool

// editorExec hands the terminal to an external editor. bubbletea only undoes
// the modes it set itself; the keyboard protocol kn pushed would otherwise
// stay on, and an editor that does not speak it (nano, emacs -nw, older vi)
// receives Esc as "\x1b[27u" and cannot be left — the terminal looks frozen.
type editorExec struct {
	cmd    *exec.Cmd
	ran    bool
	runErr error
}

func (e *editorExec) SetStdin(r io.Reader) {
	if e.cmd.Stdin == nil {
		e.cmd.Stdin = r
	}
}

func (e *editorExec) SetStdout(w io.Writer) {
	if e.cmd.Stdout == nil {
		e.cmd.Stdout = w
	}
}

func (e *editorExec) SetStderr(w io.Writer) {
	if e.cmd.Stderr == nil {
		e.cmd.Stderr = w
	}
}

func (e *editorExec) Run() error {
	if tuiRunning {
		disableKeyboardEnhancement(termModeOut())
	}
	externalProgramRunning.Store(true)
	defer externalProgramRunning.Store(false)
	e.ran = true
	e.runErr = e.cmd.Run()
	return e.runErr
}

// terminalLostMsg reports that the editor exited cleanly but bubbletea could
// not take the terminal back. Its input reader is then gone for good: every
// key would be ignored while the screen still looks alive.
type terminalLostMsg struct{ err error }

// execEditor runs cmd as an external editor. done builds the result message
// exactly as tea.ExecProcess would; a failure to reclaim the terminal is
// reported as terminalLostMsg instead of as an editor error.
func execEditor(cmd *exec.Cmd, done func(error) tea.Msg) tea.Cmd {
	e := &editorExec{cmd: cmd}
	return tea.Exec(e, func(err error) tea.Msg { return e.result(err, done) })
}

func (e *editorExec) result(err error, done func(error) tea.Msg) tea.Msg {
	if err != nil && e.ran && e.runErr == nil {
		return terminalLostMsg{err: err}
	}
	return done(err)
}

func (m *model) onTerminalLost(msg terminalLostMsg) (tea.Model, tea.Cmd) {
	config.LogEvent("tui", "terminal not restored after external editor: "+msg.err.Error())
	fmt.Fprintln(os.Stderr, "kn: could not take the terminal back after the editor exited ("+msg.err.Error()+"); exiting so the session is saved instead of ignoring every key.")
	return m, tea.Quit
}
