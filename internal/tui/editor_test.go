package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPromptEditorApply(t *testing.T) {
	for _, tc := range []struct {
		name, data                  string
		failure, conflict, retained bool
	}{
		{"unicode", "你好\r\nworld", false, false, false},
		{"empty", "", false, false, false},
		{"failed", "new", true, false, true},
		{"conflict", "new", false, true, true},
		{"invalid", "\x00bad", false, false, true},
		{"too-large", strings.Repeat("a", 1<<20+1), false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := compactCmdModel()
			m.input.SetValue("original")
			m.pasteBuf = "stored paste"
			path := filepath.Join(t.TempDir(), "draft.md")
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			msg := promptEditedMsg{path: path, original: "original"}
			if tc.failure {
				msg.err = errors.New("editor exited")
			}
			want := "original"
			if tc.conflict {
				m.input.SetValue("new input")
				want = "new input"
			}
			m.applyPromptEdit(msg)
			if !tc.retained {
				want = strings.ReplaceAll(tc.data, "\r\n", "\n")
			}
			if m.input.Value() != want {
				t.Fatalf("input=%q want=%q", m.input.Value(), want)
			}
			_, err := os.Stat(path)
			if tc.retained && err != nil {
				t.Fatal("recovery file removed")
			}
			if !tc.retained && !os.IsNotExist(err) {
				t.Fatal("temporary file not removed")
			}
			if !tc.retained && m.pasteBuf != "" {
				t.Fatal("stale collapsed paste")
			}
		})
	}
}

func TestPromptEditorBusyAndFailure(t *testing.T) {
	m := compactCmdModel()
	m.input.SetValue("keep")
	m.busy = true
	_, cmd := m.key(tea.KeyMsg{Type: tea.KeyCtrlG})
	if cmd != nil || m.input.Value() != "keep" {
		t.Fatal("busy editor must not launch")
	}
	m.busy = false
	t.Setenv("VISUAL", `"unclosed`)
	if m.openPromptEditor() != nil || m.input.Value() != "keep" {
		t.Fatal("failed launch lost draft")
	}
	if !strings.Contains(helpText(), "Ctrl+G") || registryFind("/editor").Name != "/editor" {
		t.Fatal("missing discoverability")
	}
}
