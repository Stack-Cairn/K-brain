package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrainCreatesEmptyUserPromptAndStripsComments(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())

	if got := BrainInstructions(); got != "" {
		t.Fatalf("a fresh user brain should be empty, got %q", got)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("K_BRAIN_HOME"), "brain.md"))
	if err != nil || len(data) != 0 {
		t.Fatalf("user brain should be an empty placeholder: %v\n%s", err, data)
	}
	if got := SystemInstructions(); !strings.Contains(got, "You are K-brain (氪脑)") {
		t.Fatalf("a fresh system prompt should contain the English K-brain prompt, got %q", got)
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

func TestProjectPromptFilesLoadAncestorInstructions(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "packages", "app")
	if err := os.MkdirAll(filepath.Join(child, ".k-brain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# root\n- Root rule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, ".k-brain", "brain.md"), []byte("- App rule\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := ProjectPromptFiles(child)
	if len(files) != 2 {
		t.Fatalf("files = %+v, want two project prompt files", files)
	}
	if files[0].Text != "- Root rule" || files[1].Text != "- App rule" {
		t.Fatalf("files = %+v, want root-to-child order", files)
	}
}

func TestSystemPromptCanBeCustomized(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	if got := SystemInstructions(); !strings.Contains(got, "You are K-brain (氪脑)") {
		t.Fatalf("system seed should contain the default prompt, got %q", got)
	}
	if err := os.WriteFile(SystemPath(), []byte("You are a custom K-brain system agent.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := SystemInstructions(); got != "You are a custom K-brain system agent." {
		t.Fatalf("custom system prompt = %q", got)
	}
}
