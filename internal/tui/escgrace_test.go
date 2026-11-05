package tui

import (
	"context"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// Esc presses right after an interrupt are absorbed, so pressing Esc two or
// three times to stop a reply never arms or opens rewind.
func TestEscRightAfterInterruptDoesNotArmRewind(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	prev := escGraceNow
	escGraceNow = func() time.Time { return now }
	defer func() { escGraceNow = prev }()

	m := rewindModel(t,
		ai.Message{Role: "user", Content: "q1", Authored: true},
		ai.Message{Role: "assistant", Content: "a1"},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.busy, m.cancel = true, cancel

	press(t, m, esc(m))
	if ctx.Err() != context.Canceled {
		t.Fatal("the first Esc should still interrupt the turn")
	}
	m.busy = false // the turn winds down

	for _, after := range []time.Duration{300 * time.Millisecond, 900 * time.Millisecond, 1400 * time.Millisecond} {
		now = now.Add(after)
		press(t, m, esc(m))
		if m.esc1 || m.rew != nil {
			t.Fatalf("Esc %v after the last press should be absorbed (armed=%v rewind=%v)", after, m.esc1, m.rew != nil)
		}
	}

	// Once the window has passed, Esc Esc is rewind again.
	now = now.Add(escGraceWindow + time.Millisecond)
	press(t, m, esc(m))
	if !m.esc1 {
		t.Fatal("an Esc after the window should arm rewind as usual")
	}
	press(t, m, esc(m))
	if m.rew == nil {
		t.Fatal("Esc Esc after the window should open rewind")
	}
}

func TestEscWithoutInterruptArmsRewindImmediately(t *testing.T) {
	m := rewindModel(t, ai.Message{Role: "user", Content: "q1", Authored: true})
	press(t, m, esc(m))
	if !m.esc1 {
		t.Fatal("with no interrupt to absorb, the first Esc arms rewind")
	}
}

func TestEscGraceSlidesAndExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	prev := escGraceNow
	escGraceNow = func() time.Time { return now }
	defer func() { escGraceNow = prev }()

	var g escGrace
	if g.swallow() {
		t.Fatal("nothing to swallow before an interrupt")
	}
	g.mark()
	now = now.Add(escGraceWindow)
	if !g.swallow() {
		t.Fatal("a press at the edge of the window is absorbed")
	}
	now = now.Add(escGraceWindow) // measured from the last press, not the interrupt
	if !g.swallow() {
		t.Fatal("the window should slide with each absorbed press")
	}
	now = now.Add(escGraceWindow + time.Nanosecond)
	if g.swallow() {
		t.Fatal("a press after the window passes through")
	}
	if g.swallow() {
		t.Fatal("an expired window stays closed")
	}
}
