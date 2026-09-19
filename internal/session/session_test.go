package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestTaskRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, err := st.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Minute)

	if err := st.SaveTask(id, Task{ID: "task-1", Description: "probe", Prompt: "look around", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}

	end := time.Now()
	if err := st.SaveTask(id, Task{ID: "task-1", Description: "probe", Prompt: "look around", Status: "done", Report: "the report", StartedAt: start, EndedAt: end}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTask(id, Task{ID: "task-2", Description: "other", Prompt: "p", Status: "error", Report: "boom", StartedAt: start.Add(time.Second), EndedAt: end}); err != nil {
		t.Fatal(err)
	}

	tasks, err := st.LoadTasks(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks (the upsert must not duplicate), got %d", len(tasks))
	}
	if tasks[0].ID != "task-1" || tasks[0].Status != "done" || tasks[0].Report != "the report" {
		t.Fatalf("task-1 should hold the settled state, got %+v", tasks[0])
	}
	if tasks[0].EndedAt.IsZero() {
		t.Fatal("ended_at should round-trip")
	}
	if tasks[1].ID != "task-2" || tasks[1].Status != "error" {
		t.Fatalf("task-2: %+v", tasks[1])
	}

	if other, _ := st.Create("/tmp", "m", "p"); true {
		if got, _ := st.LoadTasks(other); len(got) != 0 {
			t.Fatalf("a fresh session should have no tasks, got %d", len(got))
		}
	}
}

func TestStoreRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, err := st.Create("/tmp", "kimi-k3-fast", "inference")
	if err != nil {
		t.Fatal(err)
	}
	sent := time.Date(2025, 6, 1, 14, 30, 0, 0, time.UTC)
	use := ai.Usage{PromptTokens: 12, CompletionTokens: 4}
	msgs := []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "first question here", Authored: true, SentAt: &sent},
		{
			Role: "assistant", Content: "the answer", Usage: &use, Model: "kimi-k3-fast @ inference",
			ToolCalls: []ai.ToolCall{{ID: "c1", DurationMs: 42, ExitCode: 0}},
		},
		{Role: "tool", Content: "c1 result", ToolCallID: "c1", Name: "bash"},
		{Role: "user", Content: "follow-up"},
		{Role: "assistant", Content: "final\nanswer"},
	}
	if err := st.Save(id, 1, msgs, "kimi-k3-fast", "inference"); err != nil {
		t.Fatal(err)
	}

	meta, got, err := st.Load(id[:4])
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != id || meta.Title != "first question here" {
		t.Fatalf("meta: %+v", meta)
	}
	if len(got) != 5 || got[0].Role != "user" || got[4].Content != "final\nanswer" {
		t.Fatalf("messages: %+v", got)
	}

	if got[0].SentAt == nil || !got[0].SentAt.Equal(sent) {
		t.Fatalf("SentAt did not round-trip: %+v", got[0])
	}

	asst := got[1]
	if asst.Usage == nil || asst.Usage.PromptTokens != 12 || asst.Usage.CompletionTokens != 4 {
		t.Fatalf("usage did not round-trip: %+v", asst.Usage)
	}
	if asst.Model != "kimi-k3-fast @ inference" {
		t.Fatalf("model did not round-trip: %q", asst.Model)
	}
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].DurationMs != 42 {
		t.Fatalf("tool timing did not round-trip: %+v", asst.ToolCalls)
	}

	u, a := st.LastExchange(id)
	if u != "follow-up" || a != "final\nanswer" {
		t.Fatalf("last exchange: %q %q", u, a)
	}

	if len(got) != len(msgs)-1 {
		t.Fatalf("answered history must load verbatim: got %d, saved %d", len(got), len(msgs))
	}

	recent, err := st.Recent(10)
	if err != nil || len(recent) != 1 || recent[0].ID != id {
		t.Fatalf("recent: %v %v", recent, err)
	}

	if _, _, err := st.Load("zzzz"); err == nil {
		t.Fatal("expected not-found error")
	}

	if err := st.Save(id, 1, msgs, "kimi-k3-fast", "inference"); err != nil {
		t.Fatal(err)
	}
	if _, got, _ = st.Load(id); len(got) != 5 {
		t.Fatalf("re-save duplicated rows: %d", len(got))
	}
}

func TestEffortRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, err := st.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(id, 1, []ai.Message{{Role: "system"}, {Role: "user", Content: "q"}}, "m", "p"); err != nil {
		t.Fatal(err)
	}

	meta, _, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Effort != "" {
		t.Fatalf("new session should carry no effort, got %q", meta.Effort)
	}

	if err := st.SetEffort(id, "high"); err != nil {
		t.Fatal(err)
	}
	meta, _, _ = st.Load(id)
	if meta.Effort != "high" {
		t.Fatalf("effort did not round-trip: %q", meta.Effort)
	}

	forkID, err := st.Fork(id, 1, "copy")
	if err != nil {
		t.Fatal(err)
	}
	fmeta, _, err := st.Load(forkID)
	if err != nil {
		t.Fatal(err)
	}
	if fmeta.Effort != "high" {
		t.Fatalf("fork should inherit effort, got %q", fmeta.Effort)
	}
}

func TestUserHistory(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a, _ := st.Create("/proj/a", "m", "p")
	st.Save(a, 0, []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "from folder A", Authored: true},
		{Role: "assistant", Content: "ans"},
	}, "m", "p")
	b, _ := st.Create("/proj/b", "m", "p")
	st.Save(b, 0, []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "from folder B", Authored: true},
		{Role: "assistant", Content: "ans"},
		{Role: "user", Content: "from folder A", Authored: true},
	}, "m", "p")

	hist, err := st.UserHistory(0)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"from folder A", "from folder B"}
	if strings.Join(hist, "|") != strings.Join(want, "|") {
		t.Fatalf("UserHistory = %v, want %v", hist, want)
	}

	lim, _ := st.UserHistory(1)
	if len(lim) != 1 {
		t.Fatalf("limit: got %d", len(lim))
	}
}

func TestUserHistorySkipsInjected(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, _ := st.Create("/proj/x", "m", "p")
	st.Save(id, 0, []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "real question I typed", Authored: true},
		{Role: "assistant", Content: "ans"},
		{Role: "user", Content: "[background task task-1 done] some report\n\n…"},
		{Role: "user", Content: "[goal check] The session goal is:\n…"},
		{Role: "user", Content: "another typed message", Authored: true},
	}, "m", "p")

	hist, err := st.UserHistory(0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"another typed message", "real question I typed"}
	if strings.Join(hist, "|") != strings.Join(want, "|") {
		t.Fatalf("UserHistory = %v, want only typed messages %v", hist, want)
	}
}

func TestStoreEdgeCases(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(blocked, "sessions")); err == nil {
		t.Fatal("expected open error")
	}
	if truncate(strings.Repeat("a", 100), 10) != strings.Repeat("a", 9)+"…" {
		t.Fatal("truncate long")
	}

	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id1, _ := st.Create("/tmp", "m", "p")
	id2, _ := st.Create("/tmp", "m", "p")
	msgs := []ai.Message{{Role: "system"}, {Role: "user", Content: "q"}}
	st.Save(id1, 1, msgs, "m", "p")
	st.Save(id2, 1, msgs, "m", "p")
	if _, _, err := st.Load(""); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous, got %v", err)
	}

	u, a := st.LastExchange(id1)
	if u != "q" || a != "" {
		t.Fatalf("last exchange: %q %q", u, a)
	}

	if err := os.WriteFile(st.TranscriptPath(id1), []byte("{bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Load(id1); err == nil {
		t.Fatal("expected corrupt-row error")
	}
}

func TestGoalPersistence(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, _ := st.Create("/tmp", "m", "p")
	st.Save(id, 1, []ai.Message{{Role: "system"}, {Role: "user", Content: "q"}}, "m", "p")

	if err := st.SetGoal(id, "finish the thing"); err != nil {
		t.Fatal(err)
	}
	meta, _, err := st.Load(id)
	if err != nil || meta.Goal != "finish the thing" {
		t.Fatalf("goal not restored: %+v %v", meta, err)
	}
	st.SetGoal(id, "")
	if meta, _, _ = st.Load(id); meta.Goal != "" {
		t.Fatalf("goal not cleared: %+v", meta)
	}
}

func TestLoadSynthesizesDanglingToolResults(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	call := func(id, name string) ai.ToolCall {
		var tc ai.ToolCall
		tc.ID, tc.Function.Name = id, name
		return tc
	}
	id, _ := st.Create("/tmp", "m", "p")
	msgs := []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "go"},

		{Role: "assistant", ToolCalls: []ai.ToolCall{call("c1", "read"), call("c2", "bash")}},
		{Role: "tool", Content: "file body", ToolCallID: "c1", Name: "read"},
		{Role: "user", Content: "next"},

		{Role: "assistant", ToolCalls: []ai.ToolCall{call("c3", "edit"), call("c4", "write")}},
	}
	if err := st.Save(id, 1, msgs, "m", "p"); err != nil {
		t.Fatal(err)
	}

	_, got, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}

	wantRoles := []string{"user", "assistant", "tool", "tool", "user", "assistant", "tool", "tool"}
	if len(got) != len(wantRoles) {
		t.Fatalf("loaded %d messages, want %d: %+v", len(got), len(wantRoles), got)
	}
	for i, role := range wantRoles {
		if got[i].Role != role {
			t.Fatalf("message %d: role %q, want %q (%+v)", i, got[i].Role, role, got)
		}
	}
	for i, id := range map[int]string{2: "c2", 6: "c3", 7: "c4"} {
		m := got[i]
		if m.ToolCallID != id || m.Name == "" || !strings.Contains(m.Content, "interrupted") {
			t.Fatalf("synthetic result %d malformed: %+v", i, m)
		}
	}

	if got[3].Content != "file body" {
		t.Fatalf("answered result changed: %+v", got[3])
	}
}

func TestCompactionEvent(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, _ := st.Create("/tmp", "m", "p")
	msgs := []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}
	if err := st.Save(id, 0, msgs, "m", "p"); err != nil {
		t.Fatal(err)
	}
	rawBefore := len(st.RawMessages(id))

	if err := st.RecordCompaction(id, 4, "q1/q2 were about testing", "", ai.Usage{}); err != nil {
		t.Fatal(err)
	}

	if got := len(st.RawMessages(id)); got != rawBefore {
		t.Fatalf("raw log must survive compaction: %d → %d", rawBefore, got)
	}

	_, got, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	wantRoles := []string{"system", "system", "assistant", "user", "assistant"}
	if len(got) != len(wantRoles) {
		t.Fatalf("derived view: %d messages, want %d: %+v", len(got), len(wantRoles), got)
	}
	for i, role := range wantRoles {
		if got[i].Role != role {
			t.Fatalf("view message %d: role %q, want %q", i, got[i].Role, role)
		}
	}
	if !strings.Contains(got[1].Content, "q1/q2 were about testing") {
		t.Fatalf("summary message: %q", got[1].Content)
	}

	if err := st.Save(id, 7, []ai.Message{
		{},
		{},
		{},
		{},
		{},
		{},
		{},
		{Role: "user", Content: "q4"},
		{Role: "assistant", Content: "a4"},
	}, "m", "p"); err != nil {
		t.Fatal(err)
	}
	if raw := st.RawMessages(id); len(raw) != 9 {
		t.Fatalf("post-compaction save should append, not rewrite: %d raw rows", len(raw))
	}

	_, got, _ = st.Load(id)
	if len(got) != 7 || got[2].Content != "a2" || got[6].Content != "a4" {
		t.Fatalf("view after save: %+v", got)
	}

	events := st.Compactions(id)
	if len(events) != 1 || events[0].Cutoff != 4 || events[0].Summary != "q1/q2 were about testing" {
		t.Fatalf("compaction events: %+v", events)
	}

	if err := st.DeleteCompaction(id, 1); err != nil {
		t.Fatal(err)
	}
	_, got, _ = st.Load(id)
	if len(got) != 9 || got[1].Content != "q1" || got[8].Content != "a4" {
		t.Fatalf("after deleting the event, raw history should load: %+v", got)
	}
}

func TestTodosAndUsagePersistence(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, _ := st.Create("/tmp", "m", "p")
	st.Save(id, 1, []ai.Message{{Role: "system"}, {Role: "user", Content: "q"}}, "m", "p")

	if got := st.Todos(id); got != "" {
		t.Fatalf("fresh session should have no todos, got %q", got)
	}
	plan := `[{"content":"write tests","status":"in_progress"}]`
	if err := st.SetTodos(id, plan); err != nil {
		t.Fatal(err)
	}
	if got := st.Todos(id); got != plan {
		t.Fatalf("todos did not round-trip: %q", got)
	}

	if err := st.SetTodos(id, ""); err != nil {
		t.Fatal(err)
	}
	if got := st.Todos(id); got != "" {
		t.Fatalf("todos not cleared: %q", got)
	}

	if got := st.Todos("nope"); got != "" {
		t.Fatalf("unknown session todos: %q", got)
	}

	subs := map[string]ai.Usage{"sub-m @ p": {PromptTokens: 9, CompletionTokens: 3}}
	if err := st.SetUsage(id, 100, 40, 7, subs); err != nil {
		t.Fatal(err)
	}
	meta, _, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if meta.UsageIn != 100 || meta.UsageCached != 40 || meta.UsageOut != 7 {
		t.Fatalf("usage did not round-trip: %+v", meta)
	}
	if u := meta.SubUsage["sub-m @ p"]; u.PromptTokens != 9 || u.CompletionTokens != 3 {
		t.Fatalf("sub usage did not round-trip: %+v", meta.SubUsage)
	}

	if err := st.SetUsage(id, 100, 40, 7, nil); err != nil {
		t.Fatal(err)
	}
	if meta, _, _ = st.Load(id); meta.SubUsage != nil {
		t.Fatalf("nil ledger should clear sub_usage, got %+v", meta.SubUsage)
	}
}

func TestRecordCompactionCarriesModelAndUsage(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := st.Create("/tmp", "m", "p")
	if err := st.RecordCompaction(id, 2, "sum", "cheap @ p", ai.Usage{PromptTokens: 500, CompletionTokens: 50}); err != nil {
		t.Fatal(err)
	}
	evs := st.Compactions(id)
	if len(evs) != 1 || evs[0].Model != "cheap @ p" || evs[0].Usage.PromptTokens != 500 || evs[0].Usage.CompletionTokens != 50 {
		t.Fatalf("compaction should carry model+usage: %+v", evs)
	}
}

func TestClearMessages(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, _ := st.Create("/tmp", "m", "p")
	st.Save(id, 0, []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "a"},
	}, "m", "p")

	if err := st.ClearMessages(id); err != nil {
		t.Fatal(err)
	}
	if raw := st.RawMessages(id); len(raw) != 0 {
		t.Fatalf("messages should be gone, got %d", len(raw))
	}

	meta, msgs, err := st.Load(id)
	if err != nil || meta.ID != id || len(msgs) != 0 {
		t.Fatalf("session row must survive ClearMessages: %+v %d %v", meta, len(msgs), err)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, _ := st.Create("/tmp", "m", "p")

	if got := st.Snapshots(id); len(got) != 0 {
		t.Fatalf("fresh session should have no snapshots, got %v", got)
	}
	if err := st.SetSnapshot(id, 1, "stash@{0}"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSnapshot(id, 3, "stash@{1}"); err != nil {
		t.Fatal(err)
	}

	if err := st.SetSnapshot(id, 1, "stash@{9}"); err != nil {
		t.Fatal(err)
	}
	got := st.Snapshots(id)
	if len(got) != 2 || got[1] != "stash@{9}" || got[3] != "stash@{1}" {
		t.Fatalf("snapshots: %v", got)
	}

	if err := st.SetSnapshot(id, 3, ""); err != nil {
		t.Fatal(err)
	}
	if got = st.Snapshots(id); len(got) != 1 || got[1] != "stash@{9}" {
		t.Fatalf("after delete: %v", got)
	}

	st.SetSnapshot(id, 5, "stash@{2}")
	if err := st.DeleteFrom(id, 5); err != nil {
		t.Fatal(err)
	}
	if got = st.Snapshots(id); len(got) != 1 || got[1] != "stash@{9}" {
		t.Fatalf("DeleteFrom should trim snapshots too: %v", got)
	}

	if err := st.ClearSnapshots(id); err != nil {
		t.Fatal(err)
	}
	if got = st.Snapshots(id); len(got) != 0 {
		t.Fatalf("ClearSnapshots left rows: %v", got)
	}
}

func TestScheduleRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, _ := st.Create("/tmp", "m", "p")

	if got := st.Schedules(id); len(got) != 0 {
		t.Fatalf("fresh session should have no schedules, got %v", got)
	}
	anchor := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	id1, err := st.AddSchedule(id, "@every 10m", "check the build", anchor)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := st.AddSchedule(id, "@at 2026-01-03T00:00:00Z", "one shot", anchor)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != 1 || id2 != 2 {
		t.Fatalf("ids should increment per session: %d %d", id1, id2)
	}

	other, _ := st.Create("/tmp", "m", "p")
	if oid, _ := st.AddSchedule(other, "@every 1h", "p", anchor); oid != 1 {
		t.Fatalf("other session's first schedule id: %d", oid)
	}

	scs := st.Schedules(id)
	if len(scs) != 2 {
		t.Fatalf("schedules: %+v", scs)
	}
	if scs[0].ID != 1 || scs[0].Schedule != "@every 10m" || scs[0].Prompt != "check the build" {
		t.Fatalf("schedule 1: %+v", scs[0])
	}
	if !scs[0].Anchor.Equal(anchor) {
		t.Fatalf("anchor did not round-trip: %v", scs[0].Anchor)
	}
	if !scs[0].LastFire.IsZero() {
		t.Fatalf("never-fired task should have zero LastFire: %v", scs[0].LastFire)
	}

	fired := anchor.Add(10 * time.Minute)
	if err := st.MarkFired(id, id1, fired); err != nil {
		t.Fatal(err)
	}
	scs = st.Schedules(id)
	if !scs[0].LastFire.Equal(fired) {
		t.Fatalf("MarkFired did not stamp: %v", scs[0].LastFire)
	}
	if !scs[1].LastFire.IsZero() {
		t.Fatalf("MarkFired touched the wrong row: %+v", scs[1])
	}

	if err := st.DeleteSchedule(id, id1); err != nil {
		t.Fatal(err)
	}
	scs = st.Schedules(id)
	if len(scs) != 1 || scs[0].ID != id2 {
		t.Fatalf("after delete: %+v", scs)
	}
}

func TestSubagentTranscriptRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	parent, err := st.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []ai.Message{
		{Role: "system", Content: "sub sys"},
		{Role: "user", Content: "explore the codebase"},
		{Role: "assistant", Content: "found 3 things"},
	}
	id, err := st.SaveSubagentTranscript(parent, "probe-1", msgs, "sub-m", "sub-p")
	if err != nil {
		t.Fatal(err)
	}
	if id != "task-"+parent+"-probe-1" {
		t.Fatalf("id = %q", id)
	}
	meta, got, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if meta.TaskID != "probe-1" || meta.ForkedFrom != parent {
		t.Fatalf("attribution: taskID=%q forkedFrom=%q", meta.TaskID, meta.ForkedFrom)
	}
	if len(got) != 3 || got[1].Content != "explore the codebase" || got[2].Content != "found 3 things" {
		t.Fatalf("transcript round-trip: %+v", got)
	}

	msgs = append(msgs, ai.Message{Role: "user", Content: "follow up"}, ai.Message{Role: "assistant", Content: "answered"})
	if _, err := st.SaveSubagentTranscript(parent, "probe-1", msgs, "sub-m", "sub-p"); err != nil {
		t.Fatal(err)
	}
	_, got, err = st.Load(id)
	if err != nil || len(got) != 5 {
		t.Fatalf("after follow-up: %d msgs, err %v", len(got), err)
	}

	if id, err := st.SaveSubagentTranscript("", "t", msgs, "m", "p"); id != "" || err != nil {
		t.Fatalf("empty parent should no-op: id=%q err=%v", id, err)
	}
}

func TestSubagentTranscriptExactIDNoPrefixCollision(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	parent, err := st.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}

	mk := []ai.Message{{Role: "user", Content: "one"}}
	if _, err := st.SaveSubagentTranscript(parent, "foo-1", mk, "m", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSubagentTranscript(parent, "foo-12", mk, "m", "p"); err != nil {
		t.Fatal(err)
	}

	got, err := st.SubagentTranscript(parent, "foo-1")
	if err != nil {
		t.Fatalf("exact-id transcript load: %v", err)
	}
	if len(got) != 1 || got[0].Content != "one" {
		t.Fatalf("wrong transcript: %+v", got)
	}

	if got, err := st.SubagentTranscript(parent, "nope-9"); err != nil || len(got) != 0 {
		t.Fatalf("missing transcript should be empty, got %d msgs err %v", len(got), err)
	}
}

func TestSubagentTranscriptResaveDeletesOrphanRows(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	parent, err := st.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	long := []ai.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "u3"},
		{Role: "assistant", Content: "a3"},
	}
	if _, err := st.SaveSubagentTranscript(parent, "t-1", long, "m", "p"); err != nil {
		t.Fatal(err)
	}

	short := []ai.Message{
		{Role: "system", Content: "Summary of the conversation so far: ..."},
		{Role: "assistant", Content: "a3"},
	}
	if _, err := st.SaveSubagentTranscript(parent, "t-1", short, "m", "p"); err != nil {
		t.Fatal(err)
	}
	got, err := st.SubagentTranscript(parent, "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(short) {
		t.Fatalf("orphan rows survived: %d msgs, want %d (%+v)", len(got), len(short), got)
	}
}

func TestSubagentTranscriptAttributesSubModel(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	parent, err := st.Create("/tmp", "parent-model", "parent-prov")
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.SaveSubagentTranscript(parent, "t-1",
		[]ai.Message{{Role: "user", Content: "x"}}, "sub-model-id", "")
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Model != "sub-model-id" {
		t.Fatalf("transcript should attribute the sub's model, got %q", meta.Model)
	}
}

func TestLatestInDir(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	stamp := func(id string, at time.Time) {
		if err := st.update(id, func(d *sessionData) error { d.Meta.UpdatedAt = at.UTC(); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := st.Create("/a", "m", "p")
	st.Save(a, 0, []ai.Message{{Role: "user", Content: "a"}}, "m", "p")
	stamp(a, time.Now().Add(-2*time.Hour))
	b, _ := st.Create("/a", "m", "p")
	st.Save(b, 0, []ai.Message{{Role: "user", Content: "b"}}, "m", "p")
	stamp(b, time.Now().Add(-1*time.Hour))
	c, _ := st.Create("/b", "m", "p")
	st.Save(c, 0, []ai.Message{{Role: "user", Content: "c"}}, "m", "p")
	stamp(c, time.Now().Add(-1*time.Hour))

	if meta, err := st.LatestInDir("/a"); err != nil {
		t.Fatalf("/a: %v", err)
	} else if meta.ID != b {
		t.Fatalf("LatestInDir(/a) = %s, want newest %s", meta.ID, b)
	}
	if meta, err := st.LatestInDir("/b"); err != nil {
		t.Fatalf("/b: %v", err)
	} else if meta.ID != c {
		t.Fatalf("LatestInDir(/b) = %s, want %s", meta.ID, c)
	}
	if _, err := st.LatestInDir("/none"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LatestInDir(/none) = %v, want ErrNotFound", err)
	}
}

func TestLatestInDirExcludesSubagentTranscripts(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ordinary, _ := st.Create("/proj", "m", "p")
	st.Save(ordinary, 0, []ai.Message{{Role: "user", Content: "main"}}, "m", "p")
	subID, err := st.SaveSubagentTranscript(ordinary, "probe-1",
		[]ai.Message{{Role: "user", Content: "sub"}}, "sub-m", "sub-p")
	if err != nil || subID == "" {
		t.Fatalf("subagent transcript save: id=%q err=%v", subID, err)
	}
	if err := st.update(subID, func(d *sessionData) error { d.Meta.CWD = "/proj"; d.Meta.UpdatedAt = time.Now().UTC(); return nil }); err != nil {
		t.Fatal(err)
	}

	meta, err := st.LatestInDir("/proj")
	if err != nil {
		t.Fatalf("/proj: %v", err)
	}
	if meta.ID != ordinary {
		t.Fatalf("LatestInDir(/proj) = %s (subagent transcript), want ordinary %s", meta.ID, ordinary)
	}
	if meta.TaskID != "" {
		t.Fatalf("returned a subagent transcript row: task_id=%q", meta.TaskID)
	}
}

func TestLatestInDirSkipsEmptySessions(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	empty, _ := st.Create("/x", "m", "p")
	if err := st.update(empty, func(d *sessionData) error { d.Meta.UpdatedAt = time.Now().Add(-time.Hour).UTC(); return nil }); err != nil {
		t.Fatal(err)
	}
	full, _ := st.Create("/x", "m", "p")
	st.Save(full, 0, []ai.Message{{Role: "user", Content: "hi"}}, "m", "p")

	meta, err := st.LatestInDir("/x")
	if err != nil {
		t.Fatalf("/x: %v", err)
	}
	if meta.ID != full {
		t.Fatalf("LatestInDir(/x) = %s, want the only session with messages %s", meta.ID, full)
	}
}
