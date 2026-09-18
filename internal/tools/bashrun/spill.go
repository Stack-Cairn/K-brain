package bashrun

import (
	"fmt"
	"os"
	"path/filepath"
)

func Spill(output string) string {
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("k-brain-bash-%d", os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	f, err := os.CreateTemp(dir, "*.log")
	if err != nil {
		return ""
	}
	if _, err := f.WriteString(output); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return ""
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return ""
	}
	return f.Name()
}
