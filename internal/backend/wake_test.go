package backend

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// wakeRuns returns the accepted wake runs of rt and whether all are terminal.
func wakeRuns(rt *runtimeSession) (runs []runRecord, settled bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	settled = rt.cancel == nil || rt.runDone
	for id, rec := range rt.runs {
		if strings.HasPrefix(id, "wake-") {
			runs = append(runs, rec)
			settled = settled && rec.Terminal
		}
	}
	return runs, settled
}

func waitForWakeRuns(t *testing.T, rt *runtimeSession, want int) []runRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, settled := wakeRuns(rt)
		if len(runs) == want && settled {
			return runs
		}
		time.Sleep(10 * time.Millisecond)
	}
	runs, _ := wakeRuns(rt)
	t.Fatalf("wake runs = %+v, want %d settled", runs, want)
	return nil
}

func newWakeTestServer(t *testing.T) (*Server, string, *runtimeSession, *scriptedClient) {
	t.Helper()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	client := &scriptedClient{response: "noted"}
	s, hs := newTestServer(t, store, t.TempDir(), client)
	sess := createTestSession(t, hs.URL)
	rt, err := s.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.releaseRuntime(rt) })
	return s, hs.URL, rt, client
}

func userMessageTexts(t *testing.T, events []protocol.Event, runID string) string {
	t.Helper()
	var b strings.Builder
	for _, e := range events {
		if e.Type != protocol.EventUserMessage || e.RunID != runID {
			continue
		}
		var msg protocol.Message
		if err := json.Unmarshal(e.Payload, &msg); err != nil {
			t.Fatal(err)
		}
		for _, block := range msg.Content {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

func TestOrphanedSteerStartsWakeRun(t *testing.T) {
	_, base, rt, client := newWakeTestServer(t)
	rt.agent.Steer("[subagent bg-1 done] audit\n\nall clear")

	runs := waitForWakeRuns(t, rt, 1)
	events := readEventsAfter(t, base, rt.id, runs[0].AcceptedSeq-1)
	if text := userMessageTexts(t, events, runs[0].RunID); !strings.Contains(text, "all clear") {
		t.Fatalf("wake user message = %q", text)
	}
	completed := false
	for _, e := range events {
		completed = completed || (e.Type == protocol.EventRunCompleted && e.RunID == runs[0].RunID)
	}
	if !completed {
		t.Fatal("wake run did not complete")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.requests) != 1 || !strings.Contains(client.requests[0][len(client.requests[0])-1].TextContent(), "all clear") {
		t.Fatalf("model requests = %+v", client.requests)
	}
}

func TestWaitCompletionWakesIdleSession(t *testing.T) {
	_, base, rt, _ := newWakeTestServer(t)
	if _, err := rt.agent.StartWait(agent.WaitTaskSpec{Command: "echo ready", Until: "ready", Interval: time.Second, Timeout: 10 * time.Second}); err != nil {
		t.Fatal(err)
	}
	runs := waitForWakeRuns(t, rt, 1)
	events := readEventsAfter(t, base, rt.id, runs[0].AcceptedSeq-1)
	if text := userMessageTexts(t, events, runs[0].RunID); !strings.Contains(text, "condition met") {
		t.Fatalf("wake user message = %q", text)
	}
}

func TestManagedProcessExitNoticeWakesSession(t *testing.T) {
	s, base, rt, _ := newWakeTestServer(t)
	s.steerRuntime(rt, "[process mp-1 completed exit_code=0] build")
	runs := waitForWakeRuns(t, rt, 1)
	if text := userMessageTexts(t, readEventsAfter(t, base, rt.id, runs[0].AcceptedSeq-1), runs[0].RunID); !strings.Contains(text, "mp-1 completed") {
		t.Fatalf("wake user message = %q", text)
	}
}

func TestWakeWaitsForUserRunAndBatchesNotices(t *testing.T) {
	_, base, rt, client := newWakeTestServer(t)
	client.mu.Lock()
	client.wait = true
	client.mu.Unlock()
	accepted := runRequest(t, base, rt.id, "user-1", "long task")
	rt.agent.Steer("first notice")
	rt.agent.Steer("second notice")
	time.Sleep(50 * time.Millisecond)
	if runs, _ := wakeRuns(rt); len(runs) != 0 {
		t.Fatalf("wake run started during a user run: %+v", runs)
	}
	client.mu.Lock()
	client.wait = false
	client.mu.Unlock()
	cancel := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+rt.id+"/runs/"+accepted.RunID+"/cancel", map[string]any{}, nil)
	_ = cancel.Body.Close()
	runs := waitForWakeRuns(t, rt, 1)
	events := readEventsAfter(t, base, rt.id, runs[0].AcceptedSeq-1)
	text := userMessageTexts(t, events, runs[0].RunID)
	if !strings.Contains(text, "first notice") || !strings.Contains(text, "second notice") {
		for _, e := range events {
			t.Logf("%d %s %s %s", e.Seq, e.RunID, e.Type, e.Payload)
		}
		t.Fatalf("wake user message = %q (run %s)", text, runs[0].RunID)
	}
}

func TestWakeStreakLimitHoldsUntilNextRun(t *testing.T) {
	_, base, rt, _ := newWakeTestServer(t)
	rt.wake.mu.Lock()
	rt.wake.streak = maxWakeStreak
	rt.wake.mu.Unlock()
	rt.agent.Steer("looping notice")
	time.Sleep(100 * time.Millisecond)
	if runs, _ := wakeRuns(rt); len(runs) != 0 {
		t.Fatalf("wake run started past the streak limit: %+v", runs)
	}
	rt.mu.Lock()
	held := rt.wake.held && len(rt.wake.texts) == 1
	rt.mu.Unlock()
	if !held {
		t.Fatal("notice was not held")
	}
	// A run started by the user resets the streak; the held notice follows it.
	runRequest(t, base, rt.id, "user-1", "hello")
	waitForWakeRuns(t, rt, 1)
}

func TestDeletedSessionDoesNotWake(t *testing.T) {
	s, base, rt, _ := newWakeTestServer(t)
	ag := rt.agent
	if _, err := ag.StartWait(agent.WaitTaskSpec{Command: "false", Interval: time.Hour, Timeout: time.Hour}); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodDelete, base+"/v1/sessions/"+rt.id, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status %d", resp.StatusCode)
	}
	if n := ag.Waits().Active(); n != 0 {
		t.Fatalf("waits still polling after delete: %d", n)
	}
	s.queueWake(rt, "late notice")
	time.Sleep(50 * time.Millisecond)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.wake.texts) != 0 || rt.wake.dispatching {
		t.Fatalf("deleted session kept wake state: %+v", rt.wake.texts)
	}
}
