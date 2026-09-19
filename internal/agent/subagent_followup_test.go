package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestFollowupTaskConcurrentCancellationAndRetry(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Messages[len(req.Messages)-1].Content == "block" {
			entered <- struct{}{}
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	defer close(release)
	a := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	task := a.StartBackground("test", "original", SubModel{})
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("initial task never completed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.FollowupTask(ctx, task.ID, "block", Events{})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("follow-up never started")
	}
	if _, err := a.FollowupTask(context.Background(), task.ID, "duplicate", Events{}); err == nil || !strings.Contains(err.Error(), "follow-up running") {
		t.Fatalf("concurrent follow-up = %v", err)
	}
	if n := a.Tasks().ClearSettled(); n != 0 {
		t.Fatalf("active follow-up was cleared: %d", n)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled follow-up = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow-up did not cancel")
	}
	snap, _ := a.Tasks().Get(task.ID)
	if snap.FollowingUp || snap.Status != TaskDone || snap.Report != "reply" {
		t.Fatalf("follow-up changed original task state: %+v", snap)
	}
	for _, msg := range snap.SubMessages {
		if msg.Content == "duplicate" {
			t.Fatal("rejected follow-up was added to the transcript")
		}
	}
	if out, err := a.FollowupTask(context.Background(), task.ID, "retry", Events{}); err != nil || out != "reply" {
		t.Fatalf("retry = %q, %v", out, err)
	}
	if n := a.Tasks().ClearSettled(); n != 1 {
		t.Fatalf("completed follow-up could not be cleared: %d", n)
	}
}

func TestFollowupTaskRejectsEmptyAndCancelledInput(t *testing.T) {
	a := New(nil, "m", 100, "sys")
	if _, err := a.FollowupTask(context.Background(), "missing", " \n\t", Events{}); err == nil || !strings.Contains(err.Error(), "message is required") {
		t.Fatalf("empty follow-up = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.FollowupTask(ctx, "missing", "hello", Events{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled follow-up = %v", err)
	}
}

func TestFollowupTaskCanBeWatchedAndCancelledFromRegistry(t *testing.T) {
	started := make(chan struct{})
	emit := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if req.Messages[len(req.Messages)-1].Content != "follow-up" {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"original report\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		close(started)
		select {
		case <-emit:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"follow-up stream\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	a := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	task := a.StartBackground("task", "original", SubModel{})
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("initial task did not finish")
	}
	var changes atomic.Int32
	a.Tasks().OnChange = func(*BackgroundTask) { changes.Add(1) }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.FollowupTask(ctx, task.ID, "follow-up", Events{})
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("follow-up did not start")
	}
	snap, _ := a.Tasks().Get(task.ID)
	if !snap.FollowingUp || snap.Status != TaskDone || snap.Report != "original report" {
		t.Fatalf("follow-up state = %+v", snap)
	}
	chunks := make(chan string, 1)
	events, _, live, stop := a.Tasks().WatchTask(task.ID, Events{OnText: func(s string) { chunks <- s }})
	defer stop()
	if !live || len(events) < 2 || events[len(events)-1].S != "follow-up" {
		t.Fatalf("follow-up replay = %v, live=%v", events, live)
	}
	close(emit)
	select {
	case text := <-chunks:
		if text != "follow-up stream" {
			t.Fatalf("stream = %q", text)
		}
	case <-ctx.Done():
		t.Fatal("watcher did not receive the follow-up stream")
	}
	if !a.Tasks().Cancel(task.ID) {
		t.Fatal("registry did not accept follow-up cancellation")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("follow-up cancellation = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("registry cancellation did not stop the follow-up")
	}
	snap, _ = a.Tasks().Get(task.ID)
	if snap.FollowingUp || snap.Status != TaskDone || snap.Report != "original report" {
		t.Fatalf("original result changed after follow-up cancellation: %+v", snap)
	}
	if a.Tasks().Cancel(task.ID) {
		t.Fatal("completed follow-up remained cancellable")
	}
	if changes.Load() != 2 {
		t.Fatalf("start/end notifications = %d, want 2", changes.Load())
	}
	events, _, live = a.Tasks().SubscribeWithJournal(task.ID, Events{})
	if live || events[len(events)-1].Kind != 4 || !strings.Contains(events[len(events)-1].S, context.Canceled.Error()) {
		t.Fatalf("cancelled follow-up journal = %v, live=%v", events, live)
	}
}
