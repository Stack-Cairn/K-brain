package backend

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/session"
)

func testCronTask(dir string) *CronTask {
	return &CronTask{ID: "task-1", Name: "test", Cron: "* * * * * *", Enabled: true, Type: "bash", Script: "printf ok", Workdir: dir, TimeoutSeconds: 5}
}

func TestCronRunRecordWireIncludesExplicitCountedFalse(t *testing.T) {
	encoded, err := json.Marshal(CronRunRecord{ID: "manual", TaskID: "task", State: "done"})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	value, ok := wire["counted"]
	if !ok || value != false {
		t.Fatalf("counted wire value = %#v, wire = %s", value, encoded)
	}
}

func TestCronStorePersistsRevisionTasksAndRuns(t *testing.T) {
	dir := t.TempDir()
	sessions, err := session.Open(filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	store, err := newCronStore(sessions)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.apply(CronApplyInput{BaseRevision: 0, Ops: []CronOperation{{Op: "create", Item: map[string]any{"id": "task-1", "name": "test", "cron": "* * * * * *", "enabled": true, "type": "bash", "script": "printf ok", "workdir": dir}}}})
	if err != nil || created.Status != "ok" || created.Cron.Revision != 1 {
		t.Fatalf("create = %+v, %v", created, err)
	}
	if err := store.record(CronRunRecord{ID: "run-1", TaskID: "task-1", State: "done", StartedAt: 1, Output: "ok"}, false); err != nil {
		t.Fatal(err)
	}
	store2, err := newCronStore(sessions)
	if err != nil {
		t.Fatal(err)
	}
	if got := store2.snapshot(); got.Revision != 1 || len(got.Tasks) != 1 {
		t.Fatalf("reloaded snapshot = %+v", got)
	}
	runs, err := store2.runs("task-1", 10)
	if err != nil || len(runs) != 1 || runs[0].Output != "ok" {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	if conflict, err := store2.apply(CronApplyInput{BaseRevision: 0}); err != nil || conflict.Status != "conflict" {
		t.Fatalf("conflict = %+v, %v", conflict, err)
	}
}

func TestCronSessionBindingOwnershipAndWorkspaceChange(t *testing.T) {
	root := t.TempDir()
	sessions, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	store, err := newCronStore(sessions)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.apply(CronApplyInput{Ops: []CronOperation{{Op: "create", Item: map[string]any{"id": "task", "name": "task", "cron": "0 0 0 1 1 0", "type": "prompt", "prompt": "hello", "workdir": root, "selectedModel": CronModelRef{CustomProviderID: "p", Model: "m"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	original := store.snapshot().Tasks[0]
	if err := store.bindSession(original, "session-one"); err != nil {
		t.Fatal(err)
	}
	bound := store.snapshot()
	if bound.Tasks[0].SessionID != "session-one" {
		t.Fatal("binding missing")
	}
	if _, err := store.apply(CronApplyInput{BaseRevision: bound.Revision, Ops: []CronOperation{{Op: "update", ID: "task", Patch: map[string]any{"sessionId": "foreign-session"}}}}); err == nil {
		t.Fatal("caller changed backend session binding")
	}
	if store.snapshot().Revision != bound.Revision {
		t.Fatal("failed mutation changed revision")
	}
	changed, err := store.apply(CronApplyInput{BaseRevision: bound.Revision, Ops: []CronOperation{{Op: "update", ID: "task", Patch: map[string]any{"workdir": t.TempDir()}}}})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Cron.Tasks[0].SessionID != "" {
		t.Fatal("workspace change retained old session")
	}
	if err := store.bindSession(original, "stale-session"); err == nil {
		t.Fatal("stale worker rebound changed task")
	}
	reloaded, err := newCronStore(sessions)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.snapshot().Tasks[0].SessionID != "" {
		t.Fatal("stale binding persisted")
	}
}

func TestCronExpressionValidation(t *testing.T) {
	for _, expression := range []string{"* * * * * *", "*/5 0-10/2 * 1,2 * 0-6"} {
		if err := validateCronExpression(expression); err != nil {
			t.Errorf("valid %q: %v", expression, err)
		}
	}
	for _, expression := range []string{"* * *", "60 * * * * *", "* * * * * * *", "*/0 * * * * *"} {
		if err := validateCronExpression(expression); err == nil {
			t.Errorf("invalid %q accepted", expression)
		}
	}
}

func TestCronStoreRestartRecoveryAndRollback(t *testing.T) {
	root := t.TempDir()
	sessions, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	store, err := newCronStore(sessions)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.apply(CronApplyInput{Ops: []CronOperation{{Op: "create", Item: map[string]any{"id": "recover", "name": "recover", "cron": "* * * * * *", "enabled": true, "type": "bash", "script": "true", "remainingExecutions": 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.reserve("recover", true)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := newCronStore(sessions)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := recovered.snapshot()
	if snapshot.Tasks[0].Enabled || *snapshot.Tasks[0].RemainingExecutions != 0 {
		t.Fatalf("recovery count: %+v", snapshot)
	}
	runs, err := recovered.runs("recover", 100)
	if err != nil || len(runs) != 1 || runs[0].State != "expired" {
		t.Fatalf("recovered runs: %+v %v", runs, err)
	}
	recovered.path = root // Renaming a file over a directory fails.
	if _, _, err := recovered.reserve("recover", false); err == nil {
		t.Fatal("expected persistence failure")
	}
	if len(recovered.disk.Runs) != 1 {
		t.Fatal("failed reservation remained in memory")
	}
	response, err := recovered.apply(CronApplyInput{BaseRevision: snapshot.Revision, Ops: []CronOperation{{Op: "delete", ID: "recover"}}})
	if err == nil {
		t.Fatalf("expected failed deletion: %+v", response)
	}
	if len(recovered.snapshot().Tasks) != 1 || len(recovered.disk.Runs) != 1 {
		t.Fatal("failed delete was not rolled back")
	}
}

func TestCronCalendarMatching(t *testing.T) {
	for _, expression := range []string{"0 0 9 * JAN MON-FRI", "0 0 9 ? * 7", "0/5 * * * * *"} {
		if _, err := parseCron(expression); err != nil {
			t.Fatalf("%s: %v", expression, err)
		}
	}
	schedule, err := parseCron("0 0 9 1 * MON")
	if err != nil {
		t.Fatal(err)
	}
	if !schedule.matches(time.Date(2026, 9, 28, 9, 0, 0, 0, time.Local)) {
		t.Fatal("restricted weekday did not match")
	}
	if !schedule.matches(time.Date(2026, 9, 1, 9, 0, 0, 0, time.Local)) {
		t.Fatal("restricted month day did not match")
	}
	if schedule.matches(time.Date(2026, 9, 29, 9, 0, 0, 0, time.Local)) {
		t.Fatal("unrelated date matched")
	}
}

func TestCronStoreCRUDHistoryAndMaskedHeaders(t *testing.T) {
	root := t.TempDir()
	sessions, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	store, err := newCronStore(sessions)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(ops ...CronOperation) CronSnapshot {
		t.Helper()
		response, err := store.apply(CronApplyInput{BaseRevision: store.snapshot().Revision, Ops: ops})
		if err != nil || response.Status != "ok" {
			t.Fatalf("apply: %+v %v", response, err)
		}
		return response.Cron
	}
	apply(CronOperation{Op: "create", Item: map[string]any{"id": "http", "name": "http", "cron": "* * * * * *", "type": "http", "requests": []any{map[string]any{"id": "request", "url": "https://example.com", "headers": map[string]any{"Authorization": "secret"}}}}}, CronOperation{Op: "create", Item: map[string]any{"id": "bash", "name": "bash", "cron": "* * * * * *", "type": "bash", "script": "true"}})
	if store.snapshot().Tasks[0].Requests[0].Headers["Authorization"] != cronMaskedHeaderValue {
		t.Fatal("snapshot leaked header")
	}
	apply(CronOperation{Op: "update", ID: "http", Patch: map[string]any{"requests": []any{map[string]any{"id": "request", "url": "https://example.com/new", "headers": map[string]any{"Authorization": cronMaskedHeaderValue}}}}})
	if store.task("http").Requests[0].Headers["Authorization"] != "secret" {
		t.Fatal("masked update lost header")
	}
	snapshot := apply(CronOperation{Op: "reorder", IDs: []string{"bash", "http"}})
	if snapshot.Tasks[0].ID != "bash" {
		t.Fatal("reorder failed")
	}
	for i := 0; i < cronRunLimit+1; i++ {
		now := time.Now().UnixMilli() + int64(i)
		if err := store.record(CronRunRecord{ID: fmt.Sprint(i), TaskID: "bash", State: "done", StartedAt: now, FinishedAt: &now}, false); err != nil {
			t.Fatal(err)
		}
	}
	runs, _ := store.runs("bash", 500)
	if len(runs) != cronRunLimit {
		t.Fatalf("retention count: %d", len(runs))
	}
	if n, err := store.clearRuns("bash"); err != nil || n != cronRunLimit {
		t.Fatalf("clear: %d %v", n, err)
	}
	apply(CronOperation{Op: "delete", ID: "bash"}, CronOperation{Op: "delete", ID: "http"})
	restarted, err := newCronStore(sessions)
	if err != nil || len(restarted.snapshot().Tasks) != 0 {
		t.Fatalf("delete persistence: %v", err)
	}
}

func TestCronStoreIgnoresLateCompletionAfterTerminalOrClear(t *testing.T) {
	root := t.TempDir()
	sessions, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	store, err := newCronStore(sessions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.apply(CronApplyInput{Ops: []CronOperation{{Op: "create", Item: map[string]any{"id": "late", "name": "late", "cron": "* * * * * *", "enabled": true, "type": "bash", "script": "true"}}}}); err != nil {
		t.Fatal(err)
	}
	_, leased, err := store.reserve("late", false)
	if err != nil {
		t.Fatal(err)
	}
	finished := leased
	finished.State, finished.Success, finished.Output = "done", true, "first completion"
	if err := store.recordCompletion(finished, false); err != nil {
		t.Fatal(err)
	}
	late := finished
	late.Success, late.Output = false, "late completion"
	if err := store.recordCompletion(late, false); err != nil {
		t.Fatal(err)
	}
	runs, err := store.runs("late", 10)
	if err != nil || len(runs) != 1 || !runs[0].Success || runs[0].Output != "first completion" {
		t.Fatalf("terminal overwrite: %+v %v", runs, err)
	}
	if count, err := store.clearRuns("late"); err != nil || count != 1 {
		t.Fatalf("clear: %d %v", count, err)
	}
	if err := store.recordCompletion(late, false); err != nil {
		t.Fatal(err)
	}
	runs, err = store.runs("late", 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("late completion resurrected run: %+v %v", runs, err)
	}
}

func TestCronStoreRunsSameMillisecondKeepStartOrder(t *testing.T) {
	dir := t.TempDir()
	sessions, err := session.Open(filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	store, err := newCronStore(sessions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.apply(CronApplyInput{Ops: []CronOperation{{Op: "create", Item: map[string]any{"id": "task-1", "name": "test", "cron": "0 0 0 1 1 0", "type": "bash", "script": "printf ok", "workdir": dir}}}}); err != nil {
		t.Fatal(err)
	}
	// Runs that start within one millisecond share StartedAt; every record
	// prunes and re-sorts history, which must not reorder them.
	started := time.Now().UnixMilli()
	for _, id := range []string{"run-a", "run-b", "run-c"} {
		if err := store.record(CronRunRecord{ID: id, TaskID: "task-1", State: "done", StartedAt: started, FinishedAt: &started}, false); err != nil {
			t.Fatal(err)
		}
		runs, err := store.runs("task-1", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == 0 || runs[0].ID != id {
			t.Fatalf("after recording %s, latest run = %s (runs %+v)", id, runs[0].ID, runs)
		}
	}
	runs, err := store.runs("task-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range runs {
		got = append(got, r.ID)
	}
	if fmt.Sprint(got) != "[run-c run-b run-a]" {
		t.Fatalf("runs newest first = %v", got)
	}
}
