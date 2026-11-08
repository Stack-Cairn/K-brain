package tui

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEditorRunsWithoutTheKeyboardProtocol(t *testing.T) {
	var out bytes.Buffer
	prev := termModeOut
	termModeOut = func() io.Writer { return &out }
	tuiRunning = true
	t.Cleanup(func() { termModeOut = prev; tuiRunning = false })

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	e := &editorExec{cmd: cmd}
	e.SetStdout(io.Discard)
	e.SetStderr(io.Discard)
	if err := e.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\x1b[<u") {
		t.Fatalf("the keyboard protocol must be popped before the editor runs, wrote %q", out.String())
	}
	if externalProgramRunning.Load() {
		t.Fatal("the external-program flag must clear when the editor exits")
	}
}

func TestEditorReturnKeepsTheKeyboardStackFlat(t *testing.T) {
	var out bytes.Buffer
	prev := termModeOut
	termModeOut = func() io.Writer { return &out }
	tuiRunning = true
	t.Cleanup(func() { termModeOut = prev; tuiRunning = false })
	m, _ := fragModel(t)
	for range 3 {
		m.Update(brainEditedMsg{path: "/nonexistent"})
	}
	if push, pop := strings.Count(out.String(), "\x1b[>1u"), strings.Count(out.String(), "\x1b[<u"); push != pop {
		t.Fatalf("each return must pop what it pushes: %d pushes, %d pops", push, pop)
	}
}

func TestLostTerminalAfterEditorQuitsInsteadOfIgnoringKeys(t *testing.T) {
	done := func(err error) tea.Msg { return promptEditedMsg{err: err} }
	restoreErr := errors.New("restore failed")

	clean := &editorExec{ran: true}
	if _, ok := clean.result(restoreErr, done).(terminalLostMsg); !ok {
		t.Fatal("an editor that exited cleanly followed by a restore error must report a lost terminal")
	}
	failed := &editorExec{ran: true, runErr: errors.New("exit status 1")}
	if _, ok := failed.result(failed.runErr, done).(promptEditedMsg); !ok {
		t.Fatal("an editor failure stays an editor failure")
	}
	notStarted := &editorExec{}
	if _, ok := notStarted.result(restoreErr, done).(promptEditedMsg); !ok {
		t.Fatal("a failure to hand over the terminal stays an editor failure")
	}

	m, _ := fragModel(t)
	_, cmd := m.Update(terminalLostMsg{err: restoreErr})
	if cmd == nil {
		t.Fatal("a lost terminal must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("a lost terminal must quit")
	}
}
