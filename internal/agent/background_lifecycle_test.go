package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestStartBackgroundWorktreeFailureSettles(t *testing.T) {
	a := New(ai.New("http://unused", "k"), "m", 100, "sys")
	a.WorktreeSubagents = true
	a.WorkingDir = t.TempDir()
	task := a.StartBackground("isolated task", "work", SubModel{})
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("worktree preparation failure did not settle task")
	}
	snap, _ := a.Tasks().Get(task.ID)
	if snap.Status != TaskError || snap.Report == "" {
		t.Fatalf("failed preparation: %s, %s", snap.Status, snap.Report)
	}
}

func TestPrepareBackgroundHonorsCancellation(t *testing.T) {
	a := New(ai.New("http://unused", "k"), "m", 100, "sys")
	a.WorkingDir = t.TempDir()
	task := a.RegisterBackground("cancel before git", "work", SubModel{})
	task.cancel()
	if _, err := a.prepareBackground(task, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("preparation cancellation: %v", err)
	}
	select {
	case <-task.Done:
	default:
		t.Fatal("cancelled preparation not signalled")
	}
	snap, _ := a.Tasks().Get(task.ID)
	if snap.Status != TaskCancelled {
		t.Fatalf("status = %s", snap.Status)
	}
}

func TestTaskSettlementIsIdempotent(t *testing.T) {
	r := newTaskRegistry()
	task := &BackgroundTask{ID: "test", Status: TaskRunning, Done: make(chan struct{})}
	r.tasks[task.ID] = task
	var changed, recorded atomic.Int32
	r.OnChange = func(*BackgroundTask) { changed.Add(1) }
	r.OnRecord = func(string, *BackgroundTask) { recorded.Add(1) }
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() { r.settle(task.ID, TaskDone, "first result") })
	}
	wg.Wait()
	r.settle(task.ID, TaskError, "late result")
	if changed.Load() != 1 || recorded.Load() != 1 {
		t.Fatalf("duplicate notifications: changed=%d recorded=%d", changed.Load(), recorded.Load())
	}
	select {
	case <-task.Done:
	default:
		t.Fatal("completion not signalled")
	}
	if task.Status != TaskDone || task.Report != "first result" {
		t.Fatalf("terminal result overwritten: %+v", task)
	}
}

func TestRegisterBackgroundPreservesRestoredID(t *testing.T) {
	a := New(ai.New("http://unused", "k"), "m", 100, "sys")
	id := taskSlug("restore test", taskIDCounter.Load()+1)
	a.RestoreTask(BackgroundTask{ID: id, Status: TaskDone, Report: "saved report"})
	task := a.RegisterBackground("restore test", "work", SubModel{})
	defer task.cancel()
	if task.ID == id {
		t.Fatal("new task reused a restored ID")
	}
	old, ok := a.Tasks().Get(id)
	if !ok || old.Report != "saved report" || old.Status != TaskDone {
		t.Fatalf("restored task overwritten: %+v", old)
	}
	if len(a.Tasks().List()) != 2 {
		t.Fatal("expected both restored and new tasks")
	}
}

func TestRestoreTaskDoesNotReplaceLiveTask(t *testing.T) {
	a := New(ai.New("http://unused", "k"), "m", 100, "sys")
	task := a.RegisterBackground("live task", "work", SubModel{})
	defer task.cancel()
	a.RestoreTask(BackgroundTask{ID: task.ID, Status: TaskDone, Report: "stale"})
	got, ok := a.Tasks().Get(task.ID)
	if !ok || got.sub != task.sub || got.Status != TaskRunning || got.Done != task.Done {
		t.Fatal("restoration replaced a live task")
	}
}

func TestTasksConcurrentInitialization(t *testing.T) {
	a := &Agent{}
	registries := make(chan *taskRegistry, 32)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() { registries <- a.Tasks() })
	}
	wg.Wait()
	close(registries)
	for reg := range registries {
		if reg != a.Tasks() {
			t.Fatal("concurrent access created multiple registries")
		}
	}
}

func TestTaskSlugPreservesDigits(t *testing.T) {
	if got := taskSlug("Fix API v2 issue 123", 8); got != "fix-api-v2-issue-123-8" {
		t.Fatalf("task slug = %q", got)
	}
}
