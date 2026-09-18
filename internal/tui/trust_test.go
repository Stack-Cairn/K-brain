package tui

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestTrustGate(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	wd, _ := os.Getwd()
	if err := config.Trust(wd); err != nil {
		t.Fatal(err)
	}
	ok, err := checkTrust(bufio.NewReader(strings.NewReader("")))
	if err != nil || !ok {
		t.Fatalf("trusted cwd should pass: %v %v", ok, err)
	}
}
