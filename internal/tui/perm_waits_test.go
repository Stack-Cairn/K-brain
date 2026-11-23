package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// Two subagents ask at once: the card names the one it asks for and says one
// more waits; both rows say they wait for approval, with no clock running.
func TestSubagentApprovalCardAndRows(t *testing.T) {
	m := tasksModel("http://unused")
	m.cfg = &config.Config{Language: "en"}
	now := time.Unix(2000, 0)
	m.now = func() time.Time { return now }
	m.Update(mkWinSize(120, 30))
	fg := m.fgReg()
	for id, desc := range map[string]string{"foreground-1": "Audit the changelog", "foreground-2": "Survey the tests"} {
		fg.children[id] = &fgChild{toolID: "call-" + id, task: agent.BackgroundTask{ID: id, Description: desc, Status: agent.TaskRunning, StartedAt: now.Add(-12 * time.Second)}}
	}
	ask := func(id string) tools.GateRequest {
		return tools.GateRequest{Context: agent.WithRequestTask(context.Background(), id), Tool: "bash", Command: "mkdir -p build"}
	}

	running, _ := m.subTask("foreground-2")
	if row := stripAll(m.subagentRowText(running, false)); !strings.Contains(row, "running") || !strings.Contains(row, "12s") {
		t.Fatalf("with no card the row runs as before: %q", row)
	}

	waits := m.permWaits()
	waits.add("foreground-1")
	waits.add("foreground-2")
	m.openPermDialog(permRequest{req: ask("foreground-1"), reply: make(chan permAnswer, 1)}, "")

	if got, want := m.permTitle(m.permDialog.req), "Subagent · Audit the changelog wants to run this command"; got != want {
		t.Errorf("title %q, want %q", got, want)
	}
	if view := stripAll(m.permView()); !strings.Contains(view, "+1 pending") {
		t.Errorf("the card does not count the waiting request:\n%s", view)
	}
	for _, id := range []string{"foreground-1", "foreground-2"} {
		task, _ := m.subTask(id)
		first := stripAll(m.subagentRowText(task, false))
		now = now.Add(5 * time.Second)
		second := stripAll(m.subagentRowText(task, false))
		if !strings.Contains(first, "waiting for approval") || strings.Contains(first, "running") {
			t.Errorf("%s row: %q", id, first)
		}
		if first != second || strings.Contains(first, "12s") {
			t.Errorf("%s row ticks while it waits: %q then %q", id, first, second)
		}
		if dock := stripAll(m.taskDockRow(task, false)); !strings.Contains(dock, "waiting for approval") || strings.Contains(dock, "s ·") || strings.HasSuffix(strings.TrimSpace(dock), "s") {
			t.Errorf("%s dock row: %q", id, dock)
		}
	}

	// Answered, the other request takes the card: nothing more pending.
	waits.done("foreground-1")
	m.openPermDialog(permRequest{req: ask("foreground-2"), reply: make(chan permAnswer, 1)}, "")
	if view := stripAll(m.permView()); strings.Contains(view, "pending") || !strings.Contains(view, "Survey the tests wants to") {
		t.Errorf("second card:\n%s", view)
	}
	first, _ := m.subTask("foreground-1")
	if row := stripAll(m.subagentRowText(first, false)); strings.Contains(row, "waiting") {
		t.Errorf("the answered subagent still waits: %q", row)
	}

	// The main agent's own request keeps its plain title.
	if got := m.permTitle(tools.GateRequest{Context: context.Background(), Tool: "bash"}); got != "Run this command?" {
		t.Errorf("main agent title %q", got)
	}
}

// The gate counts a request while it waits and forgets it once answered.
func TestPermGateCountsWaitingRequests(t *testing.T) {
	saved := tools.Gate
	defer func() { tools.Gate = saved }()
	m := tasksModel("http://unused")
	m.installPermGate()
	ctx := agent.WithRequestTask(context.Background(), "foreground-3")
	if d, _ := tools.Gate(tools.GateRequest{Context: ctx, Tool: "bash"}); d != tools.GateReject {
		t.Fatalf("with no UI the gate should reject, got %v", d)
	}
	if n := m.permWaits().count(); n != 0 || m.permWaits().waiting("foreground-3") {
		t.Fatalf("an answered request is still counted: %d", n)
	}
}
