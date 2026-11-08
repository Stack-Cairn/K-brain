//go:build !windows

package stackdump

import (
	"os"
	"testing"
	"time"
)

func TestSIGUSR1WritesADumpAndKeepsRunning(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", dir)
	got := make(chan string, 1)
	stop := OnSignal(func(path string, err error) {
		if err != nil {
			t.Error(err)
		}
		got <- path
	})
	defer stop()
	if err := Signal(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	select {
	case path := <-got:
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no dump after SIGUSR1")
	}
}
