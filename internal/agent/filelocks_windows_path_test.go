package agent

import (
	"path/filepath"
	"runtime"
	"testing"
)

// write and edit lock the file they mutate; \\?\D:\a and D:\a are one file and must
// share one lock, or two calls spelled differently write it concurrently.
func TestPathLockKeyIgnoresExtendedSpelling(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path namespaces")
	}
	file := filepath.Join(t.TempDir(), "a.txt")
	if got, want := canonicalPathKey(`\\?\`+file), canonicalPathKey(file); got != want {
		t.Fatalf("lock key %q, want %q", got, want)
	}
}
