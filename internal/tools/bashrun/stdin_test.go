package bashrun

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunFeedsStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	res := Run(context.Background(), Options{Command: "cat", Shell: "sh", Dir: t.TempDir(), Timeout: 5 * time.Second, Stdin: strings.NewReader("hello stdin")})
	if res.Exit != "" || strings.TrimSpace(res.Output) != "hello stdin" {
		t.Fatalf("result %+v", res)
	}
	// Without Stdin the command reads the null device and does not wait.
	res = Run(context.Background(), Options{Command: "cat", Shell: "sh", Dir: t.TempDir(), Timeout: 5 * time.Second})
	if res.Exit != "" || res.TimedOut || strings.TrimSpace(res.Output) != "" {
		t.Fatalf("result %+v", res)
	}
}

func TestRunUnreadStdinDoesNotHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	// A large input the command never reads, held open by a background
	// descendant, must not keep Run waiting.
	start := time.Now()
	res := Run(context.Background(), Options{Command: "sleep 3 >/dev/null 2>&1 & exit 0", Shell: "sh", Dir: t.TempDir(), Timeout: 10 * time.Second, Stdin: strings.NewReader(strings.Repeat("x", 1<<20))})
	if res.Exit != "" || res.TimedOut {
		t.Fatalf("result %+v", res)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Run took %v", took)
	}
}
