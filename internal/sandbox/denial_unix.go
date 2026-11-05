//go:build unix

package sandbox

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// hostWritable reports whether the user running the backend could write path,
// or create it in its nearest existing directory, without the sandbox.
func hostWritable(path string) bool {
	for dir := path; ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(dir); err == nil {
			return unix.Access(dir, unix.W_OK) == nil
		}
		if filepath.Dir(dir) == dir {
			return false
		}
	}
}
