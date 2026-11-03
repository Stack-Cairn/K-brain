package tools

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLiveAgentManagedProcessWaitCursorAndStop(t *testing.T) {
	root := t.TempDir()
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), root), []WorkspaceRoot{{Path: root, Access: "write"}})
	catalog := LiveAgentCatalog()
	start := findToolForTest(catalog, "ManagedProcess")
	wait := findToolForTest(catalog, "ManagedProcess")
	stop := findToolForTest(catalog, "ProcessStop")

	out, err := start.Run(ctx, json.RawMessage(`{"action":"start","command":"printf first; sleep 0.1; printf second"}`))
	if err != nil {
		t.Fatal(err)
	}
	id := fieldValue(out, "process_id")
	if !strings.HasPrefix(id, "mp-") {
		t.Fatalf("process id=%q output=%q", id, out)
	}
	waitOut, err := wait.Run(ctx, mustJSON(map[string]any{"action": "wait", "process_id": id, "cursor": 0, "yield_time_ms": 5000}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(waitOut, "first") || !strings.Contains(waitOut, "cursor=") {
		t.Fatalf("wait output=%q", waitOut)
	}
	cursor, parseErr := strconv.ParseInt(fieldValue(waitOut, "cursor"), 10, 64)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	waitOut, err = wait.Run(ctx, mustJSON(map[string]any{"action": "wait", "process_id": id, "cursor": cursor, "yield_time_ms": 5000}))
	if err != nil || !strings.Contains(waitOut, "second") {
		t.Fatalf("second wait output=%q err=%v", waitOut, err)
	}
	if _, err := stop.Run(ctx, mustJSON(map[string]any{"session_id": id})); err != nil {
		t.Fatal(err)
	}
}

func TestLiveAgentProcessActionsHonorToolPolicy(t *testing.T) {
	root := t.TempDir()
	catalog := LiveAgentCatalog()
	start := findToolForTest(catalog, "ManagedProcess")
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), root), []WorkspaceRoot{{Path: root, Access: "write"}})
	out, err := start.Run(ctx, json.RawMessage(`{"action":"start","command":"sleep 30"}`))
	if err != nil {
		t.Fatal(err)
	}
	id := fieldValue(out, "process_id")
	if id == "" {
		t.Fatalf("missing process id: %q", out)
	}
	defer func() {
		_, _ = start.Run(ctx, mustJSON(map[string]any{"action": "stop", "process_id": id}))
	}()
	for _, name := range []string{"ProcessWait", "ProcessStop"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			denied := WithGate(ctx, func(req GateRequest) (GateDecision, string) {
				calls++
				if req.Tool != name || req.Command != id {
					t.Fatalf("gate request=%+v", req)
				}
				return GateReject, "test denial"
			})
			result := ExecuteResult(denied, catalog, name, mustJSON(map[string]any{"session_id": id}), false)
			if !result.Failed || calls != 1 {
				t.Fatalf("result=%+v calls=%d", result, calls)
			}
		})
	}
}

func TestLiveAgentProcessCancellationCleansManagedProcess(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(WithWorkspaceRoots(WithWorkingDir(context.Background(), root), []WorkspaceRoot{{Path: root, Access: "write"}}))
	catalog := LiveAgentCatalog()
	start := findToolForTest(catalog, "ManagedProcess")
	out, err := start.Run(ctx, json.RawMessage(`{"action":"start","command":"sleep 30"}`))
	if err != nil {
		t.Fatal(err)
	}
	id := fieldValue(out, "process_id")
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := findToolForTest(catalog, "ManagedProcess").Run(context.Background(), mustJSON(map[string]any{"action": "status", "process_id": id}))
		if statusErr == nil && !strings.Contains(status, "status=running") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The manager is intentionally session-scoped; cancellation must not leave a live child.
	status, statusErr := findToolForTest(catalog, "ManagedProcess").Run(context.Background(), mustJSON(map[string]any{"action": "status", "process_id": id}))
	if statusErr != nil || strings.Contains(status, "status=running") {
		t.Fatalf("managed process survived cancellation: %q %v", status, statusErr)
	}
}

func fieldValue(text, key string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	return ""
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func TestManagedProcessCanonicalOwnershipAndReadPolicy(t *testing.T) {
	manager := NewManagedProcessManager()
	defer manager.CloseAll()
	catalog := LiveAgentCatalogWithManagers(nil, NewTerminalManager(), manager)
	tool := findToolForTest(catalog, "ManagedProcess")
	owner := WithRunIdentity(WithWorkingDir(context.Background(), t.TempDir()), RunIdentity{ConversationID: "conversation", RunID: "run"})
	out, err := tool.Run(owner, mustJSON(map[string]any{"action": "start", "command": "printf owned; sleep 30"}))
	if err != nil {
		t.Fatal(err)
	}
	id := fieldValue(out, "process_id")
	// A later run of the same conversation reads and stops what an earlier run started;
	// other conversations and calls without a run identity never see it.
	later := WithRunIdentity(owner, RunIdentity{ConversationID: "conversation", RunID: "later"})
	for _, action := range []string{"status", "read_log", "wait"} {
		if _, err := tool.Run(later, mustJSON(map[string]any{"action": action, "process_id": id, "yield_time_ms": 1})); err != nil {
			t.Fatalf("later run %s: %v", action, err)
		}
	}
	if out, err := tool.Run(later, mustJSON(map[string]any{"action": "status"})); err != nil || !strings.Contains(out, id) {
		t.Fatalf("later run list=%q %v", out, err)
	}
	for _, ctx := range []context.Context{context.Background(), WithRunIdentity(owner, RunIdentity{ConversationID: "other", RunID: "run"})} {
		for _, action := range []string{"status", "read_log", "wait", "stop"} {
			if _, err := tool.Run(ctx, mustJSON(map[string]any{"action": action, "process_id": id, "yield_time_ms": 1})); err == nil {
				t.Fatalf("foreign %s allowed", action)
			}
		}
		out, err := tool.Run(ctx, mustJSON(map[string]any{"action": "status"}))
		if err != nil || strings.Contains(out, id) {
			t.Fatalf("foreign list=%q %v", out, err)
		}
	}
	deny := WithGate(owner, func(GateRequest) (GateDecision, string) { return GateReject, "denied" })
	for _, action := range []string{"status", "read_log"} {
		if _, err := tool.Run(deny, mustJSON(map[string]any{"action": action, "process_id": id})); err == nil {
			t.Fatalf("policy bypass: %s", action)
		}
	}
	if _, err := tool.Run(later, mustJSON(map[string]any{"action": "stop", "process_id": id})); err != nil {
		t.Fatal(err)
	}
}

// The reported failure: Bash yielded a session in one run, the run ended, and the
// next user turn's ProcessWait on that session_id was refused.
func TestBashSessionOutputReadableInLaterRun(t *testing.T) {
	root := t.TempDir()
	base := WithWorkspaceRoots(WithWorkingDir(context.Background(), root), []WorkspaceRoot{{Path: root, Access: "write"}})
	manager := NewManagedProcessManager()
	defer manager.CloseAll()
	catalog := LiveAgentCatalogWithManagers(nil, NewTerminalManager(), manager)
	first, endFirst := context.WithCancel(WithRunIdentity(base, RunIdentity{ConversationID: "conversation", RunID: "run-1"}))
	out, err := findToolForTest(catalog, "Bash").Run(first, mustJSON(map[string]any{"command": "echo cross-run-probe", "yield_time_ms": 1}))
	if err != nil {
		t.Fatal(err)
	}
	id := fieldValue(out, "process_id")
	if id == "" {
		t.Fatalf("Bash did not yield a session: %q", out)
	}
	p, err := manager.get(id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("probe did not finish")
	}
	endFirst()
	next := WithRunIdentity(base, RunIdentity{ConversationID: "conversation", RunID: "run-2"})
	waited, err := findToolForTest(catalog, "ProcessWait").Run(next, mustJSON(map[string]any{"session_id": id, "cursor": 0, "yield_time_ms": 1000}))
	if err != nil {
		t.Fatalf("ProcessWait in the next run: %v", err)
	}
	if !strings.Contains(waited, "cross-run-probe") || !strings.Contains(waited, "status=completed") {
		t.Fatalf("ProcessWait output=%q", waited)
	}
	other := WithRunIdentity(base, RunIdentity{ConversationID: "other", RunID: "run-2"})
	if _, err := findToolForTest(catalog, "ProcessWait").Run(other, mustJSON(map[string]any{"session_id": id, "yield_time_ms": 1})); err == nil {
		t.Fatal("another conversation read the session")
	}
}

func TestManagedProcessRunningRetention(t *testing.T) {
	manager := newLiveAgentProcessManager()
	defer manager.CloseAll()
	if manager.HasRunningProcesses() {
		t.Fatal("empty manager reports running")
	}
	done := make(chan struct{})
	manager.processes["isolated"] = &liveagentProcess{done: done, isolated: true}
	if !manager.HasRunningProcesses() {
		t.Fatal("isolated running process ignored")
	}
	close(done)
	if manager.HasRunningProcesses() {
		t.Fatal("completed process reports running")
	}
}
