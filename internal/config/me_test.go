package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMeSeedsTemplateAndStripsComments(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())

	if got := MeInstructions(); got != "" {
		t.Fatalf("a fresh seed is all comments — nothing to inject, got %q", got)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("K_BRAIN_HOME"), "me.md"))
	if err != nil || !strings.Contains(string(data), "# Your standing instructions") {
		t.Fatalf("seed file should exist with the template: %v\n%s", err, data)
	}

	os.WriteFile(filepath.Join(os.Getenv("K_BRAIN_HOME"), "me.md"),
		[]byte("# hi\n\n- Always pnpm.\n- Ask before force-push.\n"), 0o644)
	got := MeInstructions()
	if !strings.Contains(got, "- Always pnpm.") || strings.Contains(got, "# hi") {
		t.Fatalf("instructions should carry user lines only:\n%s", got)
	}

	if !strings.Contains(MeSeed, "/me opens this file") {
		t.Fatal("seed should tell the user how to edit")
	}
}
