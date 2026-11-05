package testhome

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/datapath"
)

var realData string

func TestMain(m *testing.M) {
	// A developer who exports LIVEAGENT_HOME or K_BRAIN_HOME has their real data directory here.
	realData = filepath.Join(os.TempDir(), "kbrain-real-data-sentinel")
	os.Setenv("LIVEAGENT_HOME", realData)
	code := Run(m)
	os.RemoveAll(realData)
	os.Exit(code)
}

func TestRunIsolatesTheDataDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := datapath.Resolve(home, os.Getenv("LIVEAGENT_HOME"), os.Getenv("K_BRAIN_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	if dir == realData {
		t.Fatalf("data directory is still the real one: %s", dir)
	}
}
