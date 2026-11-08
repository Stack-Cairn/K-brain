package tui

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestUpdateAnswersTheWatchdog(t *testing.T) {
	m, _ := fragModel(t)
	done := make(chan struct{})
	m.Update(watchdogPing{done: done})
	select {
	case <-done:
	default:
		t.Fatal("Update must answer a watchdog ping")
	}
}

// fakeLoop stands in for the event loop: it answers pings until blocked.
func fakeLoop(ctx context.Context, blocked <-chan struct{}) func(tea.Msg) {
	msgs := make(chan tea.Msg)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-msgs:
				select {
				case <-blocked:
					<-ctx.Done() // wedged, like a View waiting on a lock
					return
				default:
				}
				if ping, ok := msg.(watchdogPing); ok {
					close(ping.done)
				}
			}
		}
	}()
	return func(msg tea.Msg) {
		select {
		case msgs <- msg:
		case <-ctx.Done():
		}
	}
}

func TestUIWatchdogDumpsWhenTheEventLoopStalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blocked := make(chan struct{})
	wd := uiWatchdog(fakeLoop(ctx, blocked), 200*time.Millisecond)
	wd.Interval = 20 * time.Millisecond
	reasons := make(chan string, 4)
	wd.Write = func(reason string) (string, error) { reasons <- reason; return "dump", nil }
	wd.Start(ctx)

	select {
	case r := <-reasons:
		t.Fatalf("a responsive loop must not dump, got %q", r)
	case <-time.After(600 * time.Millisecond):
	}
	close(blocked)
	select {
	case <-reasons:
	case <-time.After(5 * time.Second):
		t.Fatal("no dump for a stalled event loop")
	}
}

func TestUIWatchdogIgnoresAnEditorInTheForeground(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blocked := make(chan struct{})
	close(blocked)
	externalProgramRunning.Store(true)
	t.Cleanup(func() { externalProgramRunning.Store(false) })
	wd := uiWatchdog(fakeLoop(ctx, blocked), 100*time.Millisecond)
	wd.Interval = 20 * time.Millisecond
	reasons := make(chan string, 4)
	wd.Write = func(reason string) (string, error) { reasons <- reason; return "dump", nil }
	wd.Start(ctx)
	select {
	case r := <-reasons:
		t.Fatalf("an editor in the foreground parks the loop on purpose, got %q", r)
	case <-time.After(600 * time.Millisecond):
	}
}
