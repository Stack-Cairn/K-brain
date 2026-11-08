package datapath

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Under `go test` the data directory must never be the developer's real one:
// tests that log (k-brain.log), save settings or cache provider state would
// otherwise write into ~/.liveagent. A directory outside the system temporary
// directory is taken to be a real one and replaced by a temporary directory
// private to the test process. Tests that point HOME or LIVEAGENT_HOME at
// t.TempDir() are unaffected.

var (
	testDataOnce sync.Once
	testDataDir  string
	testDataErr  error
)

// testSafe returns the home and overrides UserDir resolves under test.
func testSafe(home, preferred, legacy string) (string, string, string) {
	if TestRedirect(home, preferred, legacy) == "" {
		return home, preferred, legacy
	}
	testDataOnce.Do(func() {
		testDataDir, testDataErr = os.MkdirTemp("", "kbrain-test-data-")
	})
	if testDataErr != nil {
		// Without a temporary directory there is no safe place: fail
		// resolution rather than fall back to the real one.
		return "", "", ""
	}
	return home, testDataDir, ""
}

// TestRedirect reports the directory UserDir would use, when a test process
// must not use it because it lies outside the temporary directory; "" when it
// is safe.
func TestRedirect(home, preferred, legacy string) string {
	dir := strings.TrimSpace(preferred)
	if dir == "" {
		dir = strings.TrimSpace(legacy)
	}
	if dir == "" {
		if home == "" {
			return ""
		}
		dir = filepath.Join(home, LiveAgentDirName)
	}
	if InTempDir(dir) {
		return ""
	}
	return dir
}

// InTempDir reports whether path lies inside the system temporary directory,
// comparing both the paths as written and with symbolic links resolved (on
// macOS the temporary directory is reached through /var -> /private/var).
func InTempDir(path string) bool {
	tmp := os.TempDir()
	if tmp == "" || path == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	if within(abs, filepath.Clean(tmp)) {
		return true
	}
	return within(resolveExisting(abs), resolveExisting(tmp))
}

func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// resolveExisting resolves symbolic links in the longest existing prefix of
// path and appends the rest unchanged.
func resolveExisting(path string) string {
	rest := ""
	for p := filepath.Clean(path); ; {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Clean(path)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}
