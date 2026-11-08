// Package stackdump writes every goroutine's stack to a file in the K-brain
// log directory, without stopping the process, so a frozen kn or kn backend
// can be diagnosed: on SIGUSR1 (Unix), on GET /v1/debug/stacks (backend), and
// automatically when a watchdog sees the UI loop or the backend stall.
package stackdump

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// keep is how many dumps a log directory holds; older ones are removed.
const keep = 20

// Prefix starts every dump file name: stacks-<timestamp>-<pid>.txt.
const Prefix = "stacks-"

// Dir is the directory dumps are written to, the one k-brain.log is in.
func Dir() (string, error) { return config.Dir() }

// Capture returns every goroutine's stack.
func Capture() []byte {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) || len(buf) >= 256<<20 {
			return buf[:n]
		}
		buf = make([]byte, 2*len(buf))
	}
}

// Write dumps every goroutine's stack into Dir and returns the file's path.
// reason is recorded in the header and in k-brain.log.
func Write(reason string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return WriteIn(dir, reason)
}

// WriteIn is Write into dir.
func WriteIn(dir, reason string) (string, error) {
	stacks := Capture()
	now := time.Now()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s%s-%d.txt", Prefix, now.UTC().Format("20060102T150405.000Z"), os.Getpid())
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	exe, _ := os.Executable()
	_, err = fmt.Fprintf(f, "K-brain goroutine dump\nreason: %s\ntime: %s\npid: %d\nexecutable: %s\nargs: %q\ngo: %s %s/%s\ngoroutines: %d\n\n",
		reason, now.Format(time.RFC3339Nano), os.Getpid(), exe, os.Args, runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumGoroutine())
	if err == nil {
		_, err = f.Write(stacks)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	config.LogEvent("stacks", fmt.Sprintf("wrote %s (%s)", path, reason))
	prune(dir)
	return path, nil
}

// List returns the dumps in dir, oldest first.
func List(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), Prefix) && strings.HasSuffix(e.Name(), ".txt") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out) // the UTC timestamp sorts by time
	return out
}

func prune(dir string) {
	dumps := List(dir)
	for len(dumps) > keep {
		_ = os.Remove(dumps[0])
		dumps = dumps[1:]
	}
}
