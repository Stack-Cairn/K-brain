package agent

import (
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestWaitConditionMetImmediately(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()

	var woke atomic.Int32
	ag.Waits().OnWake = func(string) { woke.Add(1) }

	w, err := ag.StartWait(WaitTaskSpec{Command: "exit 0", Interval: 50 * time.Millisecond, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("wait never delivered")
	}
	if w.Status() != WaitMet {
		t.Fatalf("status = %q, want %q", w.Status(), WaitMet)
	}
	if !strings.Contains(w.Detail, "condition met") {
		t.Fatalf("detail = %q", w.Detail)
	}
	if got := woke.Load(); got != 1 {
		t.Fatalf("idle wake fired %d times, want exactly 1", got)
	}
}

func TestWaitUntilRegex(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.Waits().OnWake = func(string) {}

	w, err := ag.StartWait(WaitTaskSpec{Command: "echo running", Until: "ready", Interval: 50 * time.Millisecond, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	<-w.Done
	if w.Status() != WaitTimeout {
		t.Fatalf("status = %q, want %q (until never matched)", w.Status(), WaitTimeout)
	}

	if _, err := ag.StartWait(WaitTaskSpec{Command: "true", Until: "[unclosed"}); err == nil {
		t.Fatal("bad until regex should fail StartWait")
	}
}

func TestWaitStrikesOut(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.Waits().OnWake = func(string) {}

	start := time.Now()
	w, err := ag.StartWait(WaitTaskSpec{Command: "exit 1", Interval: 30 * time.Millisecond, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	<-w.Done
	if w.Status() != WaitFailed {
		t.Fatalf("status = %q, want %q", w.Status(), WaitFailed)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("strike-out should beat the timeout: took %s", d)
	}
}

func TestWaitTimeout(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.Waits().OnWake = func(string) {}

	w, err := ag.StartWait(WaitTaskSpec{Command: "echo still-waiting", Until: "never-matches", Interval: 30 * time.Millisecond, Timeout: 150 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	<-w.Done
	if w.Status() != WaitTimeout {
		t.Fatalf("status = %q, want %q", w.Status(), WaitTimeout)
	}
	if !strings.Contains(w.Detail, "timeout") {
		t.Fatalf("detail = %q", w.Detail)
	}
}

func TestWaitBusySteersInsteadOfWaking(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.running.Store(true)

	var woke atomic.Int32
	ag.Waits().OnWake = func(string) { woke.Add(1) }

	w, err := ag.StartWait(WaitTaskSpec{Command: "exit 0", Interval: 30 * time.Millisecond, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	<-w.Done
	if woke.Load() != 0 {
		t.Fatal("busy delivery must not fire OnWake")
	}
	if got := len(ag.drainPending()); got != 1 {
		t.Fatalf("busy delivery should queue exactly one steer, got %d", got)
	}
}

func TestWaitCancel(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.Waits().OnWake = func(string) {}

	w, err := ag.StartWait(WaitTaskSpec{Command: "echo x", Until: "never", Interval: time.Second, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !ag.Waits().CancelWait(w.ID) {
		t.Fatal("cancel of a running wait should succeed")
	}
	if w.Status() != WaitKilled {
		t.Fatalf("status = %q, want %q", w.Status(), WaitKilled)
	}
	if ag.Waits().CancelWait(w.ID) {
		t.Fatal("second cancel should report not-running")
	}
}

func TestWaitToolRegisters(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()

	var wt tools.Tool
	for _, tl := range ag.AllTools() {
		if tl.Def.Function.Name == "wait" {
			wt = tl
		}
	}
	if wt.Def.Function.Name == "" {
		t.Fatal("agent should expose the wait tool")
	}
	out, err := wt.Run(t.Context(), json.RawMessage(`{"command":"exit 0","interval":0.05,"timeout":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "do NOT sleep-poll") {
		t.Fatalf("tool output should state the no-poll contract: %q", out)
	}

	ag.Waits().mu.Lock()
	var ws []*waitTask
	for _, w := range ag.Waits().waits {
		ws = append(ws, w)
	}
	ag.Waits().mu.Unlock()
	if len(ws) > 1 {
		t.Fatalf("at most one wait should be registered, got %d", len(ws))
	}
	if len(ws) == 1 {
		select {
		case <-ws[0].Done:
		case <-time.After(2 * time.Second):
			t.Fatal("registered wait never settled")
		}

		ag.Waits().mu.Lock()
		left := len(ag.Waits().waits)
		ag.Waits().mu.Unlock()
		if left != 0 {
			t.Fatalf("settled wait should be deleted from the registry, %d left", left)
		}
	}
}

func TestWaitTickerPath(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.Waits().OnWake = func(string) {}

	flag := t.TempDir() + "/ready"
	w, err := ag.StartWait(WaitTaskSpec{
		Command:  "cat " + flag + " 2>/dev/null || true",
		Until:    "READY",
		Interval: waitMinInterval,
		Timeout:  30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	time.AfterFunc(500*time.Millisecond, func() {
		os.WriteFile(flag, []byte("READY"), 0o600)
	})
	select {
	case <-w.Done:
	case <-time.After(10 * time.Second):
		t.Fatal("ticker-path wait never settled")
	}
	if w.Status() != WaitMet {
		t.Fatalf("status = %q, want %q", w.Status(), WaitMet)
	}
}

func TestTurnTeardownDrainsOrphanedSteers(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	var woke []string
	ag.OnOrphanedSteer = func(s string) { woke = append(woke, s) }

	ag.running.Store(true)
	ag.Steer("orphaned message")

	ag.running.Store(false)
	ag.drainOrphanedSteers()

	if len(woke) != 1 || woke[0] != "orphaned message" {
		t.Fatalf("orphaned steer should wake, got %v", woke)
	}
	if len(ag.drainPending()) != 0 {
		t.Fatal("pending should be empty after teardown drain")
	}
}

func TestWaitCancelRacesDeliver(t *testing.T) {
	for range 200 {
		ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
		ag.Waits().OnWake = func(string) {}
		w, err := ag.StartWait(WaitTaskSpec{Command: "exit 0", Interval: waitMinInterval, Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}

		go ag.Waits().CancelWait(w.ID)
		<-w.Done
		ag.Waits().Close()
	}
}
