package tui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func persistStores(t *testing.T) (kn, other *session.Store, id string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sessions")
	kn, err := session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { kn.Close() })
	other, err = session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	id, err = kn.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	return kn, other, id
}

func storedTask(t *testing.T, st *session.Store, sessionID, id string) session.Task {
	t.Helper()
	tasks, err := st.LoadTasks(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("task %s not stored; have %+v", id, tasks)
	return session.Task{}
}

// The first agent is wired before Run opens the store; background subagents of
// a fresh session must still be saved once it is open.
func TestBackgroundSubagentsSavedWhenStoreOpensAfterWiring(t *testing.T) {
	m := &model{input: newInput(), agent: agent.New(ai.New("http://127.0.0.1:1", "k"), "m", 100, "sys"), queueSel: -1}
	m.wireTasks()
	kn, other, id := persistStores(t)
	m.store = kn
	m.wireTaskRecords()
	m.agent.Tasks().SetSessionID(id)

	running := &agent.BackgroundTask{ID: "survey-1", Description: "Survey", Prompt: "survey it", Status: agent.TaskRunning, StartedAt: time.Now(), ParentToolCallID: "call-bg", SubModel: "m"}
	m.agent.Tasks().OnRecord(id, running)
	got := storedTask(t, other, id, "survey-1")
	if got.Status != "running" || got.Foreground || got.ParentToolCallID != "call-bg" || got.Lease == "" || !other.TaskRunningElsewhere(got) {
		t.Fatalf("running background subagent = %+v", got)
	}

	done := *running
	done.Status, done.Report, done.EndedAt = agent.TaskDone, "all fine", time.Now()
	done.SubMessages = []ai.Message{{Role: "system", Content: "s"}, {Role: "user", Content: "survey it"}, {Role: "assistant", Content: "all fine"}}
	done.SubUsage = ai.Usage{PromptTokens: 9, CompletionTokens: 4}
	m.agent.Tasks().OnRecord(id, &done)
	got = storedTask(t, other, id, "survey-1")
	if got.Status != "done" || got.Report != "all fine" || got.Usage == nil || got.Usage.PromptTokens != 9 || other.TaskRunningElsewhere(got) {
		t.Fatalf("finished background subagent = %+v", got)
	}
	msgs, err := other.SubagentTranscript(id, "survey-1")
	if err != nil || len(msgs) != 3 {
		t.Fatalf("transcript = %d messages, %v", len(msgs), err)
	}
}

func TestSubagentRecorderSavesProgressAndNeverRegresses(t *testing.T) {
	kn, other, id := persistStores(t)
	rec := newSubagentRecorder(kn)
	rec.interval = 10 * time.Millisecond
	msgs := []ai.Message{{Role: "user", Content: "go"}}
	progress := func() ([]ai.Message, ai.Usage) {
		return msgs, ai.Usage{PromptTokens: 3}
	}
	rec.start(id, session.Task{ID: "foreground-7", Status: "running", StartedAt: time.Now(), Foreground: true}, progress)
	rec.touch("foreground-7", true)
	rec.touch("foreground-7", true)
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := storedTask(t, other, id, "foreground-7")
		if got.ToolCount == 2 && got.Usage != nil && got.Usage.PromptTokens == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("progress not saved: %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if saved, _ := other.SubagentTranscript(id, "foreground-7"); len(saved) != 1 {
		t.Fatalf("live transcript = %+v", saved)
	}

	rec.touch("foreground-7", true)
	rec.finish(id, session.Task{ID: "foreground-7", Status: "done", Report: "ok", EndedAt: time.Now()}, msgs, ai.UsageSummary{Total: ai.Usage{PromptTokens: 5}})
	time.Sleep(50 * time.Millisecond)
	got := storedTask(t, other, id, "foreground-7")
	if got.Status != "done" || got.ToolCount != 3 || !got.Foreground || got.Usage.PromptTokens != 5 {
		t.Fatalf("final record = %+v", got)
	}
}

func TestRecordingObserverSavesForegroundSubagents(t *testing.T) {
	kn, other, id := persistStores(t)
	fg := newFgRegistry()
	obs := recordingObserver{fgRegistry: fg, rec: newSubagentRecorder(kn), sessionID: id}
	fg.expect("call-fg", "Check the build")
	child := []ai.Message{{Role: "user", Content: "check"}, {Role: "assistant", Content: "Report: fine."}}
	info := agent.ChildInfo{ID: "foreground-3", Prompt: "check", Model: "m", Provider: "p", ParentToolCallID: "call-fg",
		Progress: func() ([]ai.Message, ai.Usage) { return child, ai.Usage{PromptTokens: 4} }}
	ctx := context.Background()
	obs.ChildStart(ctx, info, func() {})
	got := storedTask(t, other, id, "foreground-3")
	if got.Status != "running" || !got.Foreground || got.Description != "Check the build" || got.ParentToolCallID != "call-fg" || got.Model != "m" {
		t.Fatalf("running foreground subagent = %+v", got)
	}
	obs.ChildEnd(ctx, info, agent.ChildResult{Usage: ai.Usage{PromptTokens: 4}})
	got = storedTask(t, other, id, "foreground-3")
	if got.Status != "done" || got.Report != "Report: fine." || got.EndedAt.IsZero() {
		t.Fatalf("finished foreground subagent = %+v", got)
	}

	// A child of a call this turn did not make (nested in a subagent) is not saved here.
	obs.ChildStart(ctx, agent.ChildInfo{ID: "foreground-4", ParentToolCallID: "elsewhere"}, func() {})
	if tasks, _ := other.LoadTasks(id); len(tasks) != 1 {
		t.Fatalf("unexpected child saved: %+v", tasks)
	}
}
