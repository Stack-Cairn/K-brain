package agent

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestJournalBoundsEveryField(t *testing.T) {
	large := strings.Repeat("氪脑🧠", journalBudget/2)
	for _, event := range []JournaledEvent{
		{Kind: 0, S: large},
		{Kind: 1, S: "bash", S2: large},
		{Kind: 2, S: large, S2: large},
		{Kind: 3, S: large, S2: "guidance"},
	} {
		t.Run(strconv.Itoa(event.Kind), func(t *testing.T) {
			j := &taskJournal{}
			j.append(event.Kind, event.S, event.S2)
			if !j.Truncated || len(j.events) != 1 {
				t.Fatalf("oversized event not truncated: %d events, truncated=%v", len(j.events), j.Truncated)
			}
			got := j.events[0]
			actual := len(got.S) + len(got.S2)
			if j.bytes != actual || actual > journalBudget {
				t.Fatalf("cache accounting: bytes=%d actual=%d budget=%d", j.bytes, actual, journalBudget)
			}
			if !utf8.ValidString(got.S) || !utf8.ValidString(got.S2) {
				t.Fatal("journal truncation broke UTF-8")
			}
			if !strings.HasSuffix(event.S, got.S) || !strings.HasSuffix(event.S2, got.S2) {
				t.Fatal("journal did not retain newest content")
			}
			if event.Kind == 1 && got.S != "bash" {
				t.Fatal("tool name lost while trimming arguments")
			}
		})
	}
}

func TestJournalBoundsEmptyEvents(t *testing.T) {
	j := &taskJournal{}
	for range journalMaxEvents * 4 {
		j.append(2, "", "")
	}
	if len(j.events) != journalMaxEvents || j.bytes != 0 || !j.Truncated {
		t.Fatalf("empty events escaped limit: count=%d bytes=%d truncated=%v", len(j.events), j.bytes, j.Truncated)
	}
}

func TestJournalMixedOverflowAccounting(t *testing.T) {
	j := &taskJournal{}
	for i := range 3000 {
		s, s2 := "氪脑", strings.Repeat("🧠", i%200)
		if i%37 == 0 {
			s2 = strings.Repeat("恢复", journalBudget)
		}
		j.append(i%4, s, s2)
		actual := 0
		for _, event := range j.events {
			actual += len(event.S) + len(event.S2)
			if !utf8.ValidString(event.S) || !utf8.ValidString(event.S2) {
				t.Fatal("mixed stream produced invalid UTF-8")
			}
		}
		if actual != j.bytes || actual > journalBudget || len(j.events) > journalMaxEvents {
			t.Fatalf("append %d: bytes=%d actual=%d events=%d", i, j.bytes, actual, len(j.events))
		}
	}
}

func TestJournalLimitDoesNotTruncateLiveOutput(t *testing.T) {
	r := newTaskRegistry()
	task := &BackgroundTask{ID: "task-live", Status: TaskRunning, Done: make(chan struct{})}
	r.tasks[task.ID] = task
	output := strings.Repeat("完整工具输出", journalBudget)
	var live string
	r.SubscribeWithJournal(task.ID, Events{OnToolEnd: func(_, _, result string) { live = result }})
	r.emitter(task.ID).OnToolEnd("call", "bash", output)
	if live != output {
		t.Fatal("journal limit changed live tool output")
	}
	events, truncated, ok := r.SubscribeWithJournal(task.ID, Events{})
	if !ok || !truncated || len(events) != 1 || len(events[0].S)+len(events[0].S2) > journalBudget {
		t.Fatal("replay did not enforce journal limit")
	}
}

func TestJournalIgnoresLateEvents(t *testing.T) {
	r := newTaskRegistry()
	task := &BackgroundTask{ID: "task-ended", Status: TaskRunning, Done: make(chan struct{})}
	r.tasks[task.ID] = task
	em := r.emitter(task.ID)
	em.OnText("original")
	r.settle(task.ID, TaskDone, "done")
	em.OnText("late")
	events, _, _ := r.SubscribeWithJournal(task.ID, Events{})
	if len(events) != 1 || events[0].S != "original" {
		t.Fatal("late event changed settled journal")
	}
	r.ClearSettled()
	em.OnText("after removal")
	r.emitter("unknown").OnText("orphan")
	if len(r.journals) != 0 {
		t.Fatal("late events recreated orphan journals")
	}
}

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
