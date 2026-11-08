package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/i18n"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestCtrlBIdleMovesCursorLeft(t *testing.T) {
	m := compactCmdModel()
	m.input.SetValue("hello")
	m.input.CursorEnd()
	tm, cmd := m.key(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = tm.(*model)
	if got := m.input.LineInfo().CharOffset; got != len("hello")-1 {
		t.Fatalf("ctrl+b with nothing to background should move the cursor left, char offset = %d", got)
	}
	if cmd != nil && cmd() != nil {
		t.Fatal("ctrl+b with nothing to background produced a message")
	}

	// Busy without a backgroundable call (a model reply streaming) is the same.
	m.busy = true
	m.blocks = append(m.blocks, block{kind: blockToolRun, toolID: "read-1", toolRunning: true, toolName: "read", toolArgs: `{"path":"a"}`})
	m.key(tea.KeyMsg{Type: tea.KeyCtrlB})
	if got := m.input.LineInfo().CharOffset; got != len("hello")-2 {
		t.Fatalf("busy ctrl+b without a shell command should move the cursor left, char offset = %d", got)
	}
}

func TestToolBackgroundHintRendersOnlyWhileBackgroundable(t *testing.T) {
	for language, want := range map[string]string{i18n.English: "ctrl+b to background", i18n.Chinese: "ctrl+b 后台运行", i18n.TraditionalChinese: "ctrl+b 背景執行"} {
		m := compactCmdModel()
		m.cfg.Language = language
		m.onToolStart(toolStartMsg{id: "call-1", name: "bash", args: `{"command":"sleep 30"}`})
		row := func() string { return renderToolHeader(&m.blocks[len(m.blocks)-1], 100) }
		if strings.Contains(row(), want) {
			t.Fatalf("%s: hint shown before the call is backgroundable: %q", language, row())
		}
		m.onToolBackgroundable(toolBackgroundableMsg{id: "call-1", ok: true})
		if !strings.Contains(row(), want) {
			t.Fatalf("%s: hint missing on a backgroundable call: %q", language, row())
		}
		m.onToolBackgroundable(toolBackgroundableMsg{id: "call-1", ok: false})
		if strings.Contains(row(), want) {
			t.Fatalf("%s: hint outlived the backgroundable call: %q", language, row())
		}
	}

	// A call reported backgroundable before its row appears gets the cue too.
	m := compactCmdModel()
	m.onToolBackgroundable(toolBackgroundableMsg{id: "call-2", ok: true})
	m.onToolStart(toolStartMsg{id: "call-2", name: "bash", args: `{"command":"sleep 30"}`})
	if row := renderToolHeader(&m.blocks[len(m.blocks)-1], 100); !strings.Contains(row, "ctrl+b to background") {
		t.Fatalf("hint missing on a row started after the report: %q", row)
	}
	m.onToolEnd(toolEndMsg{id: "call-2", name: "bash", result: "done"})
	if len(m.backgroundable) != 0 {
		t.Fatalf("ended call still backgroundable: %v", m.backgroundable)
	}
}

func TestCtrlBBackgroundsRunningShellCommand(t *testing.T) {
	m := compactCmdModel()
	m.busy = true
	m.input.SetValue("hello")
	m.input.CursorEnd()
	msgs := make(chan tea.Msg, 8)
	m.wireBackgrounding(func(msg tea.Msg) { msgs <- msg })
	processes := tools.NewManagedProcessManager()
	defer processes.CloseAll()

	gate := filepath.Join(t.TempDir(), "gate")
	command := `echo first; while [ ! -f '` + gate + `' ]; do sleep 0.05; done; echo second`
	if runtime.GOOS == "windows" {
		command = `Write-Output first; while (-not (Test-Path '` + gate + `')) { Start-Sleep -Milliseconds 50 }; Write-Output second`
	}
	defer os.WriteFile(gate, nil, 0o600)
	args, _ := json.Marshal(map[string]any{"command": command})
	m.onToolStart(toolStartMsg{id: "call-1", name: "bash", args: string(args)})
	// Allow the command whatever permission gate another test left installed.
	allow := func(tools.GateRequest) (tools.GateDecision, string) { return tools.GateAllowOnce, "" }
	ctx := tools.WithGate(tools.WithToolCallID(context.Background(), "call-1"), allow)
	ctx = tools.WithBackgrounding(ctx, m.agent.Foreground(), func() *tools.ManagedProcessManager { return processes })
	done := make(chan string, 1)
	go func() {
		res := tools.ExecuteResult(ctx, tools.All(), "bash", args, false)
		done <- res.Text
	}()

	select {
	case msg := <-msgs:
		m.Update(msg)
	case out := <-done:
		t.Fatalf("command ended before it was backgroundable: %q", out)
	case <-time.After(20 * time.Second):
		t.Fatal("command never became backgroundable")
	}
	if row := renderToolHeader(&m.blocks[len(m.blocks)-1], 100); !strings.Contains(row, "ctrl+b to background") {
		t.Fatalf("running row has no hint: %q", row)
	}

	tm, cmd := m.key(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = tm.(*model)
	if cmd == nil {
		t.Fatal("ctrl+b did not background the running command")
	}
	if got := m.input.LineInfo().CharOffset; got != len("hello") {
		t.Fatalf("ctrl+b that backgrounds moved the composer cursor to %d", got)
	}
	if row := renderToolHeader(&m.blocks[len(m.blocks)-1], 100); strings.Contains(row, "ctrl+b to background") {
		t.Fatalf("hint still shown after ctrl+b: %q", row)
	}
	cmd()
	select {
	case out := <-done:
		if !strings.Contains(out, "moved this command to the background") || !strings.Contains(out, "first") {
			t.Fatalf("tool result after ctrl+b: %q", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the tool call did not return after ctrl+b")
	}
	if !processes.HasRunningProcesses() {
		t.Fatal("the backgrounded command is not running")
	}

	// Pressed again with nothing left to background, Ctrl+B is cursor left.
	for len(msgs) > 0 {
		m.Update(<-msgs)
	}
	m.key(tea.KeyMsg{Type: tea.KeyCtrlB})
	if got := m.input.LineInfo().CharOffset; got != len("hello")-1 {
		t.Fatalf("second ctrl+b should move the cursor left, char offset = %d", got)
	}
}
