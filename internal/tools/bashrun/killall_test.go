package bashrun

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/process"
)

func trackedCount() int {
	trackMu.Lock()
	defer trackMu.Unlock()
	return len(tracked)
}

func TestKillAllReapsChildren(t *testing.T) {
	done := make(chan Result, 1)
	go func() { done <- Run(context.Background(), Options{Command: "sleep 60"}) }()

	deadline := time.Now().Add(2 * time.Second)
	for trackedCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("child never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	trackMu.Lock()
	var cmd *exec.Cmd
	for _, c := range tracked {
		cmd = c
	}
	trackMu.Unlock()

	KillAll()

	if process.Alive(cmd) {
		t.Fatalf("process %d still alive after KillAll", cmd.Process.Pid)
	}
	select {
	case res := <-done:
		if !res.Killed {
			t.Fatalf("expected killed result, got %+v", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after KillAll")
	}
}

func TestBackgroundGrandchildDoesNotHang(t *testing.T) {
	done := make(chan Result, 1)
	go func() {
		done <- Run(context.Background(), Options{Command: "sleep 30 & echo started", Timeout: 10 * time.Second})
	}()
	select {
	case res := <-done:
		if res.TimedOut {
			t.Fatalf("background grandchild caused a timeout: %+v", res)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("run hung on a backgrounded grandchild")
	}
}
