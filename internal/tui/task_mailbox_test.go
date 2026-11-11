package tui

import (
	"io"
	"runtime"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/agent"
)

// A program that is not running takes no message, as a UI stuck in a long
// frame or an external editor does not. Background-task changes and the open
// subagent view's batches must not each park a goroutine on it: they queue in
// the program's mailbox, in order, and repeated refreshes fold into one.
func TestTaskChangesQueueInTheMailbox(t *testing.T) {
	m := tasksModel("http://unused")
	p := tea.NewProgram(nil, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	m.prog = p
	defer forgetUIPoster(p)
	defer p.Kill()
	m.wireProgram()

	settle := func() int {
		runtime.Gosched()
		time.Sleep(20 * time.Millisecond)
		return runtime.NumGoroutine()
	}
	before := settle()
	const burst = 1000
	task := &agent.BackgroundTask{ID: "task-1"}
	for range burst {
		m.agent.Tasks().OnChange(task)
	}
	tv := &taskView{id: "task-1"}
	for range burst {
		sendTaskMsg(p, taskEventMsg{view: tv, id: "task-1", kind: taskEventText, s: "x"})
	}
	after := settle()
	if grown := after - before; grown > 2 {
		t.Fatalf("%d task events parked %d goroutines on a busy UI", 2*burst, grown)
	}
	if n := uiMailboxes.byProgram[p].pending(); n > 2 {
		t.Fatalf("the mailbox should hold one refresh and one batch notice, holds %d", n)
	}
	if events, _ := tv.stream.drain(); len(events) != 1 || len(events[0].s) != burst {
		t.Fatalf("the streamed text should gather in one event, got %d", len(events))
	}
}
