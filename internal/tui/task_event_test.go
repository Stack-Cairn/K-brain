package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	tea "github.com/charmbracelet/bubbletea"
)

func TestTaskEventOmittedLineCount(t *testing.T) {
	var buf strings.Builder
	renderTaskEvent(&buf, 2, "read", "第一行\nsecond\nthird\nfourth\nfifth\nsixth\n")
	if !strings.Contains(buf.String(), "… +2 lines") {
		t.Fatalf("incorrect omitted line count: %q", buf.String())
	}
	if strings.Contains(buf.String(), "fifth") {
		t.Fatal("omitted lines were rendered")
	}
}

func TestTaskTranscriptOmittedLineCount(t *testing.T) {
	var buf strings.Builder
	renderTranscript(&buf, []ai.Message{{Role: "tool", Content: "第一行\nsecond\nthird\nfourth\nfifth\nsixth\n"}})
	if !strings.Contains(buf.String(), "… +2 lines") {
		t.Fatalf("incorrect transcript omitted line count: %q", buf.String())
	}
}

func TestTaskViewCloseUnsubscribes(t *testing.T) {
	for _, key := range []tea.KeyType{tea.KeyEsc, tea.KeyCtrlT} {
		m := tasksModel("http://unused")
		calls := 0
		m.taskVP = &taskView{id: "task-1", unsubscribe: func() { calls++ }}
		m.taskViewKey(tea.KeyMsg{Type: key})
		m.closeTaskView()
		if calls != 1 || m.taskVP != nil {
			t.Fatalf("close key=%v: calls=%d, view=%v", key, calls, m.taskVP)
		}
	}
}

func TestTaskViewReopenIgnoresOldEvents(t *testing.T) {
	m := tasksModel("http://unused")
	m.agent.RestoreTask(agent.BackgroundTask{ID: "task-1", Status: agent.TaskDone, Report: "initial"})
	m.openTask("task-1")
	old := m.taskVP
	unsubscribed := false
	old.unsubscribe = func() { unsubscribed = true }
	m.openTask("task-1")
	if !unsubscribed {
		t.Fatal("opening another view did not release the previous subscription")
	}
	current := m.taskVP
	current.busy = true
	before := current.buf.String()
	m.Update(taskEventMsg{view: old, id: "task-1", kind: 0, s: "stale text"})
	m.Update(taskEventMsg{view: old, id: "task-1", kind: 4, s: "stale error"})
	if current.buf.String() != before || !current.busy {
		t.Fatal("old view events modified the reopened view")
	}
	m.Update(taskEventMsg{view: current, id: "task-1", kind: 0, s: "fresh text"})
	if !strings.Contains(current.buf.String(), "fresh text") {
		t.Fatal("current view event was discarded")
	}
}

func TestTaskViewReopensFollowupTranscript(t *testing.T) {
	srv := sseTextServer(t, "reply")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("task", "original prompt", agent.SubModel{})
	waitSettled(t, task)
	if _, err := m.agent.FollowupTask(context.Background(), task.ID, "follow-up question", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	m.openTask(task.ID)
	if !strings.Contains(m.taskVP.buf.String(), "follow-up question") || strings.Count(m.taskVP.buf.String(), "reply") < 2 {
		t.Fatalf("reopened view lost the follow-up: %s", m.taskVP.buf.String())
	}
}

func TestTaskViewReopensAndCancelsActiveFollowup(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"original reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"streaming\"}}]}\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("task", "original", agent.SubModel{})
	waitSettled(t, task)
	m.openTask(task.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := m.agent.FollowupTask(ctx, task.ID, "follow-up question", agent.Events{})
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("follow-up did not start")
	}
	m.closeTaskView()
	m.openTask(task.ID)
	if !m.taskVP.live || !m.taskVP.busy || !strings.Contains(m.taskViewView(), "replying") || !strings.Contains(m.tasksDock(), "replying") {
		t.Fatal("reopened view did not reflect active follow-up state")
	}
	m.Update(taskUpdateMsg{})
	if !m.taskVP.live {
		t.Fatal("task update treated the follow-up as a completed original task")
	}
	m.taskViewKey(tea.KeyMsg{Type: tea.KeyCtrlX})
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("follow-up cancellation = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("reopened view could not cancel the follow-up")
	}
	m.Update(taskUpdateMsg{})
	if m.taskVP.live || m.taskVP.busy {
		t.Fatal("follow-up completion did not refresh the reopened view")
	}
	if !strings.Contains(m.taskVP.buf.String(), "follow-up question") || !strings.Contains(m.taskVP.buf.String(), context.Canceled.Error()) {
		t.Fatalf("follow-up history missing after cancellation: %s", m.taskVP.buf.String())
	}
}
