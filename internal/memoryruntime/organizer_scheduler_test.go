package memoryruntime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func waitForScheduler(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// stable reports whether the counter stops moving once a pending tick drained.
func stable(t *testing.T, runs *atomic.Int32) int32 {
	t.Helper()
	time.Sleep(20 * time.Millisecond)
	settled := runs.Load()
	time.Sleep(40 * time.Millisecond)
	if got := runs.Load(); got != settled {
		t.Fatalf("organizer kept running: %d -> %d", settled, got)
	}
	return settled
}

func TestOrganizerSchedulerHotUpdate(t *testing.T) {
	var runs atomic.Int32
	scheduler := newOrganizerScheduler(context.Background(), false, time.Millisecond, func(context.Context) { runs.Add(1) })
	defer scheduler.Close()

	if got := stable(t, &runs); got != 0 {
		t.Fatalf("disabled scheduler ran %d times", got)
	}
	if enabled, interval := scheduler.State(); enabled || interval != time.Millisecond {
		t.Fatalf("state = %v %v", enabled, interval)
	}

	scheduler.Update(true, time.Millisecond)
	waitForScheduler(t, "runs after enabling", func() bool { return runs.Load() >= 3 })

	scheduler.Update(false, time.Millisecond)
	paused := stable(t, &runs)

	// A long interval restarts the countdown: nothing runs soon after it.
	scheduler.Update(true, time.Hour)
	if got := stable(t, &runs); got != paused {
		t.Fatalf("1h schedule ran early: %d -> %d", paused, got)
	}
	if enabled, interval := scheduler.State(); !enabled || interval != time.Hour {
		t.Fatalf("state = %v %v", enabled, interval)
	}

	scheduler.Update(true, time.Millisecond)
	waitForScheduler(t, "runs after shortening", func() bool { return runs.Load() > paused })

	scheduler.Update(true, 0)
	if _, interval := scheduler.State(); interval != 24*time.Hour {
		t.Fatalf("non-positive interval = %v, want 24h", interval)
	}
}

func TestOrganizerSchedulerCloseStopsAndCancelsRun(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	scheduler := newOrganizerScheduler(context.Background(), true, time.Millisecond, func(ctx context.Context) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		close(cancelled)
	})
	<-started
	scheduler.Close()
	select {
	case <-cancelled:
	default:
		t.Fatal("Close returned before the run in progress was cancelled")
	}
	scheduler.Close() // idempotent
	scheduler.Update(true, time.Millisecond)
}
