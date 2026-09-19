package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrainSeedsKBrainPromptAndStripsComments(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())

	if got := BrainInstructions(); !strings.Contains(got, "You are K-brain (氪脑)") {
		t.Fatalf("a fresh seed should contain the English K-brain prompt, got %q", got)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("K_BRAIN_HOME"), "brain.md"))
	if err != nil || !strings.Contains(string(data), "name: system-agent") || !strings.Contains(string(data), "<work_policy>") {
		t.Fatalf("seed file should exist with the template: %v\n%s", err, data)
	}

	if err := os.WriteFile(filepath.Join(os.Getenv("K_BRAIN_HOME"), "brain.md"),
		[]byte("# hi\n\n- Always pnpm.\n- Ask before force-push.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := BrainInstructions()
	if !strings.Contains(got, "- Always pnpm.") || strings.Contains(got, "# hi") {
		t.Fatalf("instructions should carry user lines only:\n%s", got)
	}
	if strings.Contains(got, "me.md") {
		t.Fatal("brain instructions must not mention the removed me.md file")
	}
}
