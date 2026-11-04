package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/hooks"
)

func TestRunFiresSessionEndHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell hook")
	}
	runFixture(t, "hi", nil)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "end.json")
	cfg.Hooks = map[string][]config.Hook{hooks.SessionEnd: {{Command: `printf '%s' "$K_BRAIN_HOOK_EVENT" > '` + strings.ReplaceAll(out, "'", `'\''`) + `'`, Shell: "sh"}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := runCapture(t, "", "--quiet", "hello"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("SessionEnd hook did not run: %v", err)
	}
	var event hooks.Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if event.Name != hooks.SessionEnd || event.Reason != "exit" || event.SessionID == "" {
		t.Fatalf("SessionEnd payload: %s", data)
	}
}
