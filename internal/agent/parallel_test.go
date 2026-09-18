package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func slowTool(name string, conc, maxConc *atomic.Int32) tools.Tool {
	return tools.Tool{
		Def: ai.NewTool(name, "slow", `{"type":"object","properties":{"s":{"type":"string"}}}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			n := conc.Add(1)
			for {
				m := maxConc.Load()
				if n <= m || maxConc.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			conc.Add(-1)
			return name + "-done", nil
		},
	}
}

func parallelServer(t *testing.T) *httptest.Server {
	t.Helper()
	call := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		w.Header().Set("Content-Type", "text/event-stream")
		if call == 1 {
			for i, id := range []string{"a", "b", "c"} {

				args := fmt.Sprintf(`{"s":%q}`, id)
				fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":%d,"id":%q,"type":"function","function":{"name":"slow","arguments":%q}}]}}]}`+"\n\n", i, id, args)
			}
			fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		} else {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func TestToolCallsRunInParallel(t *testing.T) {
	srv := parallelServer(t)
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	var conc, maxConc atomic.Int32

	ag.Tools = []tools.Tool{slowTool("slow", &conc, &maxConc)}

	if _, err := ag.Turn(context.Background(), "go", Events{}); err != nil {
		t.Fatal(err)
	}
	if maxConc.Load() < 2 {
		t.Fatalf("tool calls did not overlap: max concurrency %d", maxConc.Load())
	}
}

func TestSamePathEditsSerialize(t *testing.T) {

	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")

	var conc, maxConc atomic.Int32
	write := tools.Tool{
		Def: ai.NewTool("write", "w", `{"type":"object","properties":{"path":{"type":"string"}}}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			n := conc.Add(1)
			for {
				m := maxConc.Load()
				if n <= m || maxConc.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(25 * time.Millisecond)
			conc.Add(-1)
			return "ok", nil
		},
	}
	ag.Tools = []tools.Tool{write}

	calls := []ai.ToolCall{
		{ID: "1", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "write", Arguments: `{"path":"/tmp/same.go"}`}},
		{ID: "2", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "write", Arguments: `{"path":"/tmp/same.go"}`}},
	}
	ag.runTools(context.Background(), calls, Events{})
	if maxConc.Load() != 1 {
		t.Fatalf("same-path writes must serialize (max concurrency 1), got %d", maxConc.Load())
	}
}

func TestBackgroundTaskDeliversReport(t *testing.T) {
	srv := textServer(t, func(n int, req ai.Request) string { return "report-body" })
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	task := ag.StartBackground("probe", "do the thing", SubModel{})

	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("task never settled")
	}
	snap, ok := ag.Tasks().Get(task.ID)
	if !ok {
		t.Fatal("task not in registry")
	}
	if snap.Status != TaskDone || snap.Report != "report-body" {
		t.Fatalf("settled task: %+v", snap)
	}

	var pending []pendingSteer
	for range 100 {
		if pending = ag.drainPending(); len(pending) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(pending) != 1 || !strings.Contains(pending[0].text, "report-body") {
		t.Fatalf("expected steered report, got %v", pending)
	}
}

func TestBackgroundTaskBroadcastsToManyWaiters(t *testing.T) {
	srv := textServer(t, func(n int, req ai.Request) string {
		time.Sleep(50 * time.Millisecond)
		return "ok"
	})
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	task := ag.StartBackground("d", "p", SubModel{})

	const waiters = 8
	var woken atomic.Int32
	var wg sync.WaitGroup
	for range waiters {
		wg.Go(func() {
			select {
			case <-task.Done:
				woken.Add(1)
			case <-time.After(5 * time.Second):
			}
		})
	}
	wg.Wait()
	if woken.Load() != waiters {
		t.Fatalf("only %d/%d waiters woke on close", woken.Load(), waiters)
	}
}

func TestBackgroundTaskCancel(t *testing.T) {

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		<-r.Context().Done()
	}))
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	task := ag.StartBackground("d", "p", SubModel{})
	if !ag.Tasks().Cancel(task.ID) {
		t.Fatal("cancel should succeed on a running task")
	}
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled task never settled")
	}
	snap, _ := ag.Tasks().Get(task.ID)
	if snap.Status != TaskCancelled {
		t.Fatalf("status: %s", snap.Status)
	}
	if ag.Tasks().Cancel(task.ID) {
		t.Fatal("cancel on a settled task should report false")
	}
}

func TestTaskListStableOrder(t *testing.T) {
	r := newTaskRegistry()
	now := time.Now()

	for _, id := range []string{"task-3", "task-1", "task-4", "task-2"} {
		t := BackgroundTask{ID: id, Status: TaskRunning, StartedAt: now}
		r.tasks[id] = &t
	}
	first := r.List()
	var ids []string
	for _, tk := range first {
		ids = append(ids, tk.ID)
	}
	want := []string{"task-1", "task-2", "task-3", "task-4"}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("stable order: got %v want %v", ids, want)
		}
	}

	for i := range 20 {
		got := r.List()
		for j := range want {
			if got[j].ID != want[j] {
				t.Fatalf("call %d reshuffled: %v", i, got)
			}
		}
	}
}

func TestClearSettledKeepsRunning(t *testing.T) {
	r := newTaskRegistry()
	now := time.Now()
	add := func(id string, st TaskStatus) {
		tk := BackgroundTask{ID: id, Status: st, StartedAt: now}
		r.tasks[id] = &tk
	}
	add("task-1", TaskDone)
	add("task-2", TaskError)
	add("task-3", TaskRunning)
	add("task-4", TaskCancelled)

	if n := r.ClearSettled(); n != 3 {
		t.Fatalf("cleared %d, want 3", n)
	}
	got := r.List()
	if len(got) != 1 || got[0].ID != "task-3" {
		t.Fatalf("only the running task should remain: %+v", got)
	}
	if _, ok := r.Get("task-1"); ok {
		t.Fatal("settled task should be gone")
	}
}

func TestAcquireGlobalSerializes(t *testing.T) {
	f := newFileLocks()
	release := f.acquireGlobal()

	acquired := make(chan struct{})
	go func() {
		r2 := f.acquireGlobal()
		close(acquired)
		r2()
	}()
	select {
	case <-acquired:
		t.Fatal("second global acquire should block while the first holds it")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("release never unblocked the waiter")
	}
}

func TestRegistrySessionID(t *testing.T) {
	r := newTaskRegistry()
	if got := r.recordSession(); got != "" {
		t.Fatalf("fresh registry session: %q", got)
	}
	r.SetSessionID("s1")
	if got := r.recordSession(); got != "s1" {
		t.Fatalf("after set: %q", got)
	}
	r.SetSessionID("")
	if got := r.recordSession(); got != "" {
		t.Fatalf("after clear: %q", got)
	}

	var recorded string
	r.OnRecord = func(id string, _ *BackgroundTask) { recorded = id }
	r.SetSessionID("s2")
	r.tasks["task-1"] = &BackgroundTask{ID: "task-1", Status: TaskRunning, Done: make(chan struct{})}
	r.settle("task-1", TaskDone, "report")
	if recorded != "s2" {
		t.Fatalf("settle should record against the published session, got %q", recorded)
	}
}

func TestCanonicalPathKey(t *testing.T) {
	a := canonicalPathKey("foo/../bar/baz.go")
	b := canonicalPathKey("bar/baz.go")
	if a != b {
		t.Fatalf("canonical keys differ: %q vs %q", a, b)
	}
}

func TestToolMutationPath(t *testing.T) {
	if p, ok := toolMutationPath("write", `{"path":"/a/b.go"}`); !ok || p != "/a/b.go" {
		t.Fatalf("write: %q %v", p, ok)
	}
	if p, ok := toolMutationPath("edit", `{"path":"rel.go"}`); !ok || p != "rel.go" {
		t.Fatalf("edit: %q %v", p, ok)
	}
	if _, ok := toolMutationPath("bash", `{"command":"ls"}`); ok {
		t.Fatal("bash must be global (not path-scoped)")
	}
	if _, ok := toolMutationPath("read", `{"path":"/a"}`); ok {
		t.Fatal("read is not a mutation")
	}
	if _, ok := toolMutationPath("write", `{bad`); ok {
		t.Fatal("malformed write args fall back to global")
	}
}

func TestBackgroundTaskSubscribersSeeLiveStream(t *testing.T) {
	srv := textServer(t, func(n int, req ai.Request) string {
		time.Sleep(50 * time.Millisecond)
		return "stream-body"
	})
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	task := ag.StartBackground("d", "p", SubModel{})

	var got atomic.Int32
	_, _, ok := ag.Tasks().SubscribeWithJournal(task.ID, Events{OnText: func(s string) { got.Add(int32(len(s))) }})
	if !ok {
		t.Fatal("SubscribeWithJournal on a running task should report live")
	}
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("task never settled")
	}
	if got.Load() == 0 {
		t.Fatal("subscriber saw no text events")
	}
	if _, _, ok := ag.Tasks().SubscribeWithJournal(task.ID, Events{}); ok {
		t.Fatal("SubscribeWithJournal on a settled task should not report live")
	}
}

func TestFanIn(t *testing.T) {
	var a, b, usage atomic.Int32
	ev := FanIn(
		Events{OnText: func(string) { a.Add(1) }, OnUsage: func(ai.Usage) { usage.Add(1) }},
		Events{OnText: func(string) { b.Add(1) }},
	)
	ev.OnText("x")
	ev.OnThink("y")
	ev.OnUsage(ai.Usage{})
	if a.Load() != 1 || b.Load() != 1 || usage.Load() != 1 {
		t.Fatalf("fan-in miscounted: a=%d b=%d usage=%d", a.Load(), b.Load(), usage.Load())
	}
}

func toolLoopServer(t *testing.T) *httptest.Server {
	t.Helper()
	call := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		call++
		switch call {
		case 1:
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"read","arguments":"{\"path\":\"/tmp/x\"}"}}]}}]}`+"\n\n")
			fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		default:
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"final report"},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func TestBackgroundTaskSubscriberSeesToolEvents(t *testing.T) {
	srv := toolLoopServer(t)
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	task := ag.StartBackground("d", "p", SubModel{})

	var mu sync.Mutex
	var seq []string
	replay, _, ok := ag.Tasks().SubscribeWithJournal(task.ID, Events{
		OnText:      func(s string) { mu.Lock(); seq = append(seq, "text:"+s); mu.Unlock() },
		OnToolStart: func(_, n, _ string) { mu.Lock(); seq = append(seq, "start:"+n); mu.Unlock() },
		OnToolEnd:   func(_, n, r string) { mu.Lock(); seq = append(seq, "end:"+n+":"+r); mu.Unlock() },
	})
	if !ok {
		t.Fatal("SubscribeWithJournal on a running task should report live")
	}

	mu.Lock()
	for _, e := range replay {
		switch e.Kind {
		case 0:
			seq = append(seq, "text:"+e.S)
		case 1:
			seq = append(seq, "start:"+e.S)
		case 2:
			seq = append(seq, "end:"+e.S+":"+e.S2)
		}
	}
	mu.Unlock()
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("task never settled")
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(seq, "|")
	for _, want := range []string{"start:read", "end:read:", "text:final report"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("subscriber stream %q missing %q", joined, want)
		}
	}
}

func TestBackgroundTaskManySubscribers(t *testing.T) {
	srv := textServer(t, func(n int, req ai.Request) string {
		time.Sleep(30 * time.Millisecond)
		return "broadcast-body"
	})
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	task := ag.StartBackground("d", "p", SubModel{})

	const subs = 4
	var counts [subs]atomic.Int32
	for i := range subs {
		if _, _, ok := ag.Tasks().SubscribeWithJournal(task.ID, Events{OnText: func(s string) { counts[i].Add(int32(len(s))) }}); !ok {
			t.Fatalf("subscriber %d rejected", i)
		}
	}
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("task never settled")
	}
	for i := range counts {
		if counts[i].Load() == 0 {
			t.Fatalf("subscriber %d saw no events", i)
		}
	}
}

func TestSubscribeUnknownTask(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	if _, _, ok := ag.Tasks().SubscribeWithJournal("task-999", Events{}); ok {
		t.Fatal("SubscribeWithJournal on an unknown id should report not-ok")
	}
}

func TestBackgroundTaskUsageNotDoubleCounted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":5}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	task := ag.StartBackground("d", "p", SubModel{})
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("task never settled")
	}
	if u := ag.Usage(); u.PromptTokens != 0 || u.CompletionTokens != 0 {
		t.Fatalf("background subagent usage must not roll into the parent's own Usage: %+v", u)
	}
	if u := ag.SubUsage()["m @ "]; u.PromptTokens != 50 || u.CompletionTokens != 5 {
		t.Fatalf("background subagent usage should be ledgered under its model: %+v", ag.SubUsage())
	}
	if u := ag.TotalUsage(); u.PromptTokens != 50 || u.CompletionTokens != 5 {
		t.Fatalf("TotalUsage should be own + subs: %+v", u)
	}
}

func TestSubUsageForwardsThroughNestedSubs(t *testing.T) {
	root := New(ai.New("http://x", "k"), "root", 100, "sys")
	sub := root.newSub(SubModel{})
	sub.Model = "sub-m"
	subsub := sub.newSub(SubModel{})
	subsub.Model = "leaf-m"

	sub.AddUsage(ai.Usage{PromptTokens: 10, CompletionTokens: 1})
	subsub.AddUsage(ai.Usage{PromptTokens: 20, CompletionTokens: 2})

	if u := root.Usage(); u.PromptTokens != 0 {
		t.Fatalf("root's own usage must stay 0, got %+v", u)
	}
	got := root.SubUsage()
	if got["sub-m @ "].PromptTokens != 10 || got["leaf-m @ "].PromptTokens != 20 {
		t.Fatalf("root ledger should hold both levels by model: %+v", got)
	}
	if u := root.TotalUsage(); u.PromptTokens != 30 || u.CompletionTokens != 3 {
		t.Fatalf("root total should be 30/3, got %+v", u)
	}

	if u := sub.SubUsage()["leaf-m @ "]; u.PromptTokens != 20 {
		t.Fatalf("sub ledger should hold the leaf's spend: %+v", sub.SubUsage())
	}
	root.ResetUsage()
	if root.SubUsage() != nil {
		t.Fatal("ResetUsage should clear the sub ledger too")
	}
}

func TestRestoreTaskSettledAndVisible(t *testing.T) {
	a := New(nil, "m", 100, "sys")
	a.RestoreTask(BackgroundTask{ID: "task-9", Description: "old", Status: TaskDone, Report: "done report"})

	got, ok := a.Tasks().Get("task-9")
	if !ok || got.Status != TaskDone || got.Report != "done report" {
		t.Fatalf("restored task should be visible, got %+v ok=%v", got, ok)
	}
	select {
	case <-got.Done:
	default:
		t.Fatal("a restored task's Done must be closed")
	}
	if a.Tasks().Cancel("task-9") {
		t.Fatal("a settled restored task must not be cancellable")
	}
	if n := len(a.Tasks().List()); n != 1 {
		t.Fatalf("List should include the restored task, got %d", n)
	}
}

func TestBroadcastBlockingSubscriberCannotDeadlock(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"hi"}}]}`+"\n\n")
		w.(http.Flusher).Flush()

		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release) })
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	task := ag.StartBackground("probe", "p", SubModel{})

	inCallback := make(chan struct{})
	notify := sync.OnceFunc(func() { close(inCallback) })
	if _, _, ok := ag.Tasks().SubscribeWithJournal(task.ID, Events{
		OnText: func(string) {
			notify()
			<-release
		},
	}); !ok {
		t.Fatal("task should accept a subscriber while running")
	}

	select {
	case <-inCallback:
	case <-time.After(5 * time.Second):
		t.Fatal("subscriber never received the stream's OnText")
	}

	done := make(chan struct{})
	go func() {
		ag.Tasks().List()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("List blocked behind a parked subscriber — registry mutex held across a blocking callback")
	}

	if !ag.Tasks().Cancel(task.ID) {
		t.Fatal("Cancel should accept a running task even with a parked subscriber")
	}
}
