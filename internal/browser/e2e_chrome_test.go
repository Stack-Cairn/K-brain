package browser

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/process"
)

// lockedBuffer collects a child's output while the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startTestChrome starts bin on profile with remote debugging on a port it picks
// itself and returns that port, read from the profile's DevToolsActivePort. A
// port chosen beforehand can be taken by another process before Chrome binds it.
//
// Cleanup kills Chrome's whole process tree and waits for it to end. It is
// registered after profile's TempDir, so it runs first: Chrome's helper
// processes otherwise keep writing into the profile while it is removed.
func startTestChrome(t *testing.T, bin, profile string, args ...string) int {
	t.Helper()
	// Configure needs a context command; cleanup stops it, not the context.
	cmd := exec.CommandContext(context.Background(), bin, append([]string{"--remote-debugging-port=0", "--user-data-dir=" + profile}, args...)...)
	output := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = output, output
	process.Configure(cmd, false)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = process.Kill(cmd)
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
			t.Errorf("chrome %d did not exit after kill", cmd.Process.Pid)
			return
		}
		deadline := time.Now().Add(10 * time.Second)
		for !processTreeGone(cmd.Process.Pid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	})
	deadline := time.Now().Add(45 * time.Second)
	for {
		if data, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort")); err == nil {
			if port, _, err := parseDevToolsActivePort(data); err == nil {
				return port
			}
		}
		select {
		case <-exited:
			t.Fatalf("chrome exited before it listened (%v):\n%s", waitErr, output.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("chrome wrote no DevToolsActivePort in 45s:\n%s", output.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
