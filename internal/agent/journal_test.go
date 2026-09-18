package agent

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestJournalRecordsEmittedEvents(t *testing.T) {
	r := newTaskRegistry()
	task := &BackgroundTask{ID: "task-1", Status: TaskRunning, Done: make(chan struct{}), cancel: func() {}}
	r.tasks[task.ID] = task

	fire(r.emitter(task.ID))

	events, truncated, ok := r.SubscribeWithJournal(task.ID, Events{})
	if !ok || truncated {
		t.Fatalf("running task with journal: ok=%v truncated=%v", ok, truncated)
	}
	var kinds []int
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}

	want := []int{0, 1, 2, 3}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Fatalf("journal kinds = %v, want %v", kinds, want)
	}
	if events[0].S != "t" {
		t.Fatalf("text event = %q, want %q", events[0].S, "t")
	}
}

func TestJournalCoalescesTextDeltas(t *testing.T) {
	r := newTaskRegistry()
	task := &BackgroundTask{ID: "task-1", Status: TaskRunning, Done: make(chan struct{}), cancel: func() {}}
	r.tasks[task.ID] = task

	em := r.emitter(task.ID)
	em.OnText("hel")
	em.OnText("lo ")
	em.OnText("world")
	em.OnToolStart("tc1", "read", "{}")
	em.OnText("after")

	events, _, _ := r.SubscribeWithJournal(task.ID, Events{})
	if len(events) != 3 {
		t.Fatalf("journal = %d events, want 3 (coalesced text, tool start, text): %+v", len(events), events)
	}
	if events[0].S != "hello world" || events[2].S != "after" {
		t.Fatalf("coalesced text = %q / %q", events[0].S, events[2].S)
	}
}

func TestJournalOverflowDropsOldest(t *testing.T) {
	j := &taskJournal{}
	chunk := strings.Repeat("x", journalBudget/4)
	for range 6 {
		j.append(1, "tool", chunk)
	}
	if !j.Truncated {
		t.Fatal("overflow should mark the journal truncated")
	}
	if j.bytes > journalBudget {
		t.Fatalf("journal bytes %d exceed budget %d after truncation", j.bytes, journalBudget)
	}
	if len(j.events) == 0 || len(j.events) >= 6 {
		t.Fatalf("truncation should keep a bounded tail, kept %d of 6", len(j.events))
	}

	k := &taskJournal{}
	k.append(0, strings.Repeat("y", journalBudget*2), "")
	if k.bytes > journalBudget {
		t.Fatalf("single oversized event: bytes = %d, want <= %d", k.bytes, journalBudget)
	}
	if !k.Truncated {
		t.Fatal("oversized single entry should mark the journal truncated")
	}
}

func TestSubscribeWithJournalIsAtomic(t *testing.T) {
	r := newTaskRegistry()
	task := &BackgroundTask{ID: "task-1", Status: TaskRunning, Done: make(chan struct{}), cancel: func() {}}
	r.tasks[task.ID] = task

	const pre = 50
	em := r.emitter(task.ID)
	for i := range pre {
		em.OnToolStart("", strconv.Itoa(i), "")
	}

	var mu sync.Mutex
	var live []string
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := pre; ; i++ {
			select {
			case <-stop:
				return
			default:
				em.OnToolStart("", strconv.Itoa(i), "")
			}
		}
	})

	events, _, ok := r.SubscribeWithJournal(task.ID, Events{
		OnToolStart: func(_, n, _ string) { mu.Lock(); live = append(live, n); mu.Unlock() },
	})
	close(stop)
	wg.Wait()
	if !ok {
		t.Fatal("running task should report live")
	}

	seen := map[string]int{}
	for _, e := range events {
		seen[e.S]++
	}
	mu.Lock()
	for _, id := range live {
		seen[id]++
	}
	mu.Unlock()
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("event %s seen %d times (journal %d + live %d total = %d)", id, n, len(events), len(live), n)
		}
	}
	if len(seen) < pre {
		t.Fatalf("the %d pre-subscribe events must all be in the journal: saw %d unique", pre, len(seen))
	}
}

func TestJournalSurvivesSettleUntilCleared(t *testing.T) {
	r := newTaskRegistry()
	task := &BackgroundTask{ID: "task-1", Status: TaskRunning, Done: make(chan struct{}), cancel: func() {}}
	r.tasks[task.ID] = task
	fire(r.emitter(task.ID))
	r.settle(task.ID, TaskDone, "report")

	events, _, ok := r.SubscribeWithJournal(task.ID, Events{})
	if ok {
		t.Fatal("a settled task must not report live")
	}
	if len(events) != 4 {
		t.Fatalf("settled task journal = %d events, want 4 (text, tool start, tool end, steer)", len(events))
	}

	r.ClearSettled()
	if events, _, _ := r.SubscribeWithJournal(task.ID, Events{}); events != nil {
		t.Fatal("ClearSettled should drop the journal with the task")
	}
}
