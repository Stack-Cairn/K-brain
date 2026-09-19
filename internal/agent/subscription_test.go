package agent

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestTaskJournalReadsDoNotSubscribe(t *testing.T) {
	r := newTaskRegistry()
	r.tasks["task-1"] = &BackgroundTask{ID: "task-1", Status: TaskRunning}
	r.emitter("task-1").OnText("earlier")
	for range 1000 {
		events, truncated, live := r.SubscribeWithJournal("task-1", Events{})
		if !live || truncated || len(events) != 1 || events[0].S != "earlier" {
			t.Fatalf("journal = %v, truncated=%v, live=%v", events, truncated, live)
		}
	}
	if len(r.subs) != 0 {
		t.Fatalf("read-only snapshots retained subscriptions: %d", len(r.subs))
	}
}

func TestTaskWatchUnsubscribesIndependently(t *testing.T) {
	r := newTaskRegistry()
	r.tasks["task-1"] = &BackgroundTask{ID: "task-1", Status: TaskRunning, Done: make(chan struct{})}
	var first, second int
	_, _, live, unsubscribe := r.WatchTask("task-1", Events{OnText: func(string) { first++ }})
	if !live {
		t.Fatal("running task was not live")
	}
	_, _, _, unsubscribeSecond := r.WatchTask("task-1", Events{OnText: func(string) { second++ }})
	ev := r.emitter("task-1")
	ev.OnText("one")
	unsubscribe()
	unsubscribe()
	ev.OnText("two")
	if first != 1 || second != 2 || len(r.subs["task-1"]) != 1 {
		t.Fatalf("first=%d, second=%d, remaining=%d", first, second, len(r.subs["task-1"]))
	}
	r.settle("task-1", TaskDone, "done")
	if len(r.subs) != 0 {
		t.Fatal("settled task retained subscriptions")
	}
	unsubscribeSecond()
	events, _, live, cancel := r.WatchTask("task-1", Events{OnText: func(string) { t.Error("settled task emitted") }})
	cancel()
	if live || len(events) != 1 || events[0].S != "onetwo" || len(r.subs) != 0 {
		t.Fatalf("settled watch: live=%v, events=%d, subscriptions=%d", live, len(events), len(r.subs))
	}
	_, _, live, cancel = r.WatchTask("missing", Events{})
	cancel()
	if live {
		t.Fatal("unknown task was live")
	}
}

func TestTaskWatchConcurrentUnsubscribe(t *testing.T) {
	r := newTaskRegistry()
	r.tasks["task-1"] = &BackgroundTask{ID: "task-1", Status: TaskRunning}
	var delivered atomic.Int32
	var wg sync.WaitGroup
	wg.Go(func() {
		ev := r.emitter("task-1")
		for range 1000 {
			ev.OnText("chunk")
		}
	})
	for range 20 {
		wg.Go(func() {
			for range 100 {
				_, _, _, cancel := r.WatchTask("task-1", Events{OnText: func(string) { delivered.Add(1) }})
				cancel()
				cancel()
			}
		})
	}
	wg.Wait()
	if len(r.subs) != 0 {
		t.Fatalf("subscriptions leaked after concurrent removal: %d", len(r.subs))
	}
}
