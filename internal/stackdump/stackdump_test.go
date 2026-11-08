package stackdump

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWriteInDumpsEveryGoroutine(t *testing.T) {
	dir := t.TempDir()
	block := make(chan struct{})
	defer close(block)
	go func() { <-block }()
	path, err := WriteIn(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"reason: test", "goroutine ", "TestWriteInDumpsEveryGoroutine"} {
		if !strings.Contains(text, want) {
			t.Errorf("dump lacks %q", want)
		}
	}
	if got := List(dir); len(got) != 1 || got[0] != path {
		t.Fatalf("List = %v", got)
	}
}

func TestWriteInKeepsTheNewestDumps(t *testing.T) {
	dir := t.TempDir()
	for range keep + 5 {
		if _, err := WriteIn(dir, "prune"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // distinct millisecond timestamps
	}
	if n := len(List(dir)); n != keep {
		t.Fatalf("%d dumps kept, want %d", n, keep)
	}
}

func TestWatchdogDumpsOnceAStall(t *testing.T) {
	var stalled atomic.Bool
	stalled.Store(true)
	release := make(chan struct{})
	var dumps atomic.Int32
	dumped := make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &Watchdog{
		Name: "test loop", Interval: 10 * time.Millisecond, Timeout: 50 * time.Millisecond,
		Probe: func(ctx context.Context) {
			if stalled.Load() {
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
		},
		Write: func(reason string) (string, error) {
			dumps.Add(1)
			dumped <- reason
			return "dump", nil
		},
	}
	w.Start(ctx)
	select {
	case reason := <-dumped:
		if !strings.Contains(reason, "test loop") {
			t.Fatalf("reason %q", reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no dump for a stalled probe")
	}
	time.Sleep(200 * time.Millisecond)
	if n := dumps.Load(); n != 1 {
		t.Fatalf("%d dumps for one stall, want 1", n)
	}
	stalled.Store(false)
	close(release)
	time.Sleep(200 * time.Millisecond)
	if n := dumps.Load(); n != 1 {
		t.Fatalf("%d dumps after the loop recovered, want 1", n)
	}
}

func TestWatchdogIgnoresPausedTime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var dumps atomic.Int32
	w := &Watchdog{
		Name: "paused", Interval: 10 * time.Millisecond, Timeout: 50 * time.Millisecond,
		Probe:  func(ctx context.Context) { <-ctx.Done() },
		Paused: func() bool { return true },
		Write:  func(string) (string, error) { dumps.Add(1); return "", nil },
	}
	w.Start(ctx)
	time.Sleep(300 * time.Millisecond)
	if n := dumps.Load(); n != 0 {
		t.Fatalf("%d dumps while paused", n)
	}
}
