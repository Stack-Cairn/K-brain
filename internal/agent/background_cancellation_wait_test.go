package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestWaitCancelledTasksDoesNotWaitForNormallyEndedTurn(t *testing.T) {
	parent := New(ai.New("http://unused", "k"), "m", 100, "sys")
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	live := parent.RegisterBackgroundContext(ctx, "outlives turn", "keep working", SubModel{})
	defer live.cancel()
	cancel(tools.ErrTurnEnded)
	waited := make(chan struct{})
	go func() {
		parent.WaitCancelledTasks()
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		parent.Tasks().settle(live.ID, TaskDone, "release failed wait")
		t.Fatal("normal turn completion must not wait for the live background task")
	}
	if err := live.ctx.Err(); err != nil {
		t.Fatalf("normal turn completion cancelled background task: %v", err)
	}
}

func TestWaitCancelledTasksWaitsForRecordingWithoutStoppingLiveTasks(t *testing.T) {
	parent := New(ai.New("http://unused", "k"), "m", 100, "sys")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := parent.RegisterBackgroundContext(ctx, "cancelled child", "work", SubModel{})
	live := parent.RegisterBackground("independent child", "keep working", SubModel{})
	defer live.cancel()

	// Hold recording after the task has changed status. A client must not see a
	// run terminal before the child's final state has been persisted and emitted.
	recording := make(chan struct{})
	releaseRecord := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRecord) }) }
	defer release()
	parent.Tasks().OnRecord = func(_ string, task *BackgroundTask) {
		if task.ID == cancelled.ID && task.Status == TaskCancelled {
			close(recording)
			<-releaseRecord
		}
	}
	cancel()
	go parent.Tasks().settle(cancelled.ID, TaskCancelled, context.Canceled.Error())
	select {
	case <-recording:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled task did not reach recording")
	}

	waited := make(chan struct{})
	go func() {
		parent.WaitCancelledTasks()
		close(waited)
	}()
	select {
	case <-waited:
		t.Fatal("wait returned before the cancelled task finished recording")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not finish while an independent task remained live")
	}
	if err := live.ctx.Err(); err != nil {
		t.Fatalf("independent task was cancelled: %v", err)
	}
	snapshot, _ := parent.Tasks().Get(live.ID)
	if snapshot.Status != TaskRunning {
		t.Fatalf("independent task status = %s", snapshot.Status)
	}
	select {
	case <-live.Done:
		t.Fatal("independent task was settled by the wait")
	default:
	}
}
