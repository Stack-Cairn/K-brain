//go:build !windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/datapath"
	"github.com/Stack-Cairn/K-brain/internal/stackdump"
)

func TestDebugStacksSignalsAProcessAndPrintsItsDump(t *testing.T) {
	stop := stackdump.OnSignal(nil)
	defer stop()
	var out bytes.Buffer
	if err := debugCLI([]string{"stacks", strconv.Itoa(os.Getpid())}, &out); err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(out.String())
	t.Cleanup(func() { _ = os.Remove(path) })
	if !datapath.InTempDir(path) {
		t.Fatalf("a test wrote a dump outside the temp dir: %s", path)
	}
	if !strings.HasPrefix(filepath.Base(path), stackdump.Prefix) {
		t.Fatalf("printed %q, want a dump path", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "SIGUSR1") || !strings.Contains(string(raw), "TestDebugStacksSignalsAProcessAndPrintsItsDump") {
		t.Fatalf("dump lacks the reason or this test's stack:\n%.400s", raw)
	}
}
