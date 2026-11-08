package agent

import (
	"context"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// A front end may block in OnSteer until its UI loop takes the event, and that
// loop reads the transcript while drawing (the TUI footer calls ContextTokens).
// OnSteer used to run under msgsMu, so the turn waited on the UI and the UI on
// the lock: the TUI froze for good when a steered message landed mid-render.
func TestOnSteerRunsWithoutTranscriptLock(t *testing.T) {
	srv := textServer(t, func(n int, req ai.Request) string { return "ok" })
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.Steer("also do this")
	var sawSteer bool
	done := make(chan error, 1)
	go func() {
		_, err := ag.Turn(context.Background(), "go", Events{
			OnSteer: func(s string) {
				// The UI loop: draw a frame (ContextTokens, MessagesSnapshot)
				// before the event is taken.
				drawn := make(chan []ai.Message, 1)
				go func() {
					_ = ag.ContextTokens()
					drawn <- ag.MessagesSnapshot()
				}()
				select {
				case msgs := <-drawn:
					last := msgs[len(msgs)-1]
					sawSteer = last.Role == "user" && last.Content == s
				case <-time.After(5 * time.Second):
					t.Error("OnSteer runs while the transcript lock is held: a UI drawing the context meter deadlocks")
				}
			},
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("turn did not finish")
	}
	if !sawSteer {
		t.Fatal("the steered message was not in the transcript when OnSteer ran")
	}
}

func TestContextTokensNowNeverWaits(t *testing.T) {
	ag := New(nil, "m", 100, "sys")
	ag.RestoreMessages([]ai.Message{{Role: "user", Content: "hello there, a few tokens"}})
	want := ag.ContextTokens()
	if got := ag.ContextTokensNow(); got != want {
		t.Fatalf("ContextTokensNow() = %d, want %d", got, want)
	}
	ag.msgsMu.Lock()
	got := make(chan int, 1)
	go func() { got <- ag.ContextTokensNow() }()
	select {
	case n := <-got:
		if n != want {
			t.Errorf("while the lock is held ContextTokensNow() = %d, want the last count %d", n, want)
		}
	case <-time.After(5 * time.Second):
		t.Error("ContextTokensNow waited for the transcript lock")
	}
	ag.msgsMu.Unlock()
}
