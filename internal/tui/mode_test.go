package tui

import (
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

func TestShiftTabCyclesPermissionModes(t *testing.T) {
	m := compactCmdModel()
	m.input.SetValue("keep this draft")
	for _, mode := range []string{"plan", "always", "normal"} {
		m = pressKey(m, tea.KeyShiftTab)
		if m.permissionMode != mode || m.agent.PlanMode() != (mode == "plan") || m.input.Value() != "keep this draft" {
			t.Fatalf("mode %s: got %s, plan=%v, draft=%q", mode, m.permissionMode, m.agent.PlanMode(), m.input.Value())
		}
	}
	key, ok := csiUKey("?CSI[57 59 50 117]?")
	if !ok || key.Type != tea.KeyShiftTab {
		t.Fatalf("CSI-u Shift+Tab: %+v %v", key, ok)
	}
}

func TestPermissionModesGateRequests(t *testing.T) {
	for _, mode := range []string{"normal", "plan", "always"} {
		m := compactCmdModel()
		m.setPermissionMode(mode)
		reply := make(chan permAnswer, 1)
		m.receivePermission(permRequest{req: tools.GateRequest{Tool: "write", Command: "file.txt"}, reply: reply})
		if mode == "normal" {
			if m.permDialog == nil {
				t.Fatal("normal must prompt")
			}
			continue
		}
		select {
		case answer := <-reply:
			want := tools.GateAllowOnce
			if mode == "plan" {
				want = tools.GateReject
			}
			if answer.decision != want {
				t.Fatalf("%s: %v", mode, answer.decision)
			}
		default:
			t.Fatalf("%s did not answer", mode)
		}
	}
}
