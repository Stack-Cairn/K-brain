package computer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func ensureHelperBinary() (string, error) {
	if p := os.Getenv("K_BRAIN_COMPUTER_BIN"); p != "" {
		return p, nil
	}
	exe, err := os.Executable()
	if err == nil {
		name := "k-brain-computer"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		p := filepath.Join(filepath.Dir(exe), name)
		if st, statErr := os.Stat(p); statErr == nil && !st.IsDir() {
			return p, nil
		}
	}
	if p, err := exec.LookPath("k-brain-computer"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%w: build or install k-brain-computer beside k-brain", ErrUnsupportedPlatform)
}
