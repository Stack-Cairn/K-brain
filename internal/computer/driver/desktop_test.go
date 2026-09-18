package driver

import (
	"os/exec"
	"strings"
	"testing"
)

func TestDesktopAdapters(t *testing.T) {
	python := "python3"
	if path, err := exec.LookPath("python"); err == nil {
		python = path
	}
	path, err := exec.LookPath(python)
	if err != nil {
		t.Skip("Python 3 not installed")
	}
	cmd := exec.CommandContext(t.Context(), path, "-I", "desktop_test.py")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("desktop adapter tests: %v\n%s", err, output)
	}
}

func TestEmbeddedDesktopScripts(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		script, err := desktopScript(platform)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(script, "def dispatch(") || !strings.Contains(script, "expectedRevision") {
			t.Fatalf("incomplete %s adapter", platform)
		}
	}
	if _, err := desktopScript("other"); err == nil {
		t.Fatal("unknown platform accepted")
	}
}
