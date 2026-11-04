package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rulesTestEnv isolates HOME and the K-brain data dir and returns a project root that
// ends the ancestor walk.
func rulesTestEnv(t *testing.T) (home, root string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return home, root
}

func writeRule(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func scopes(files []PromptFile) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Scope)
	}
	return out
}

func TestInstructionFilesLoadClaudeCompatFilesInOrder(t *testing.T) {
	home, root := rulesTestEnv(t)
	child := filepath.Join(root, "pkg")
	writeRule(t, filepath.Join(home, ".claude", "CLAUDE.md"), "- User claude rule\n")
	writeRule(t, filepath.Join(root, "AGENTS.md"), "- Root agents rule\n")
	writeRule(t, filepath.Join(root, "CLAUDE.md"), "- Root claude rule\n")
	writeRule(t, filepath.Join(root, ".claude", "CLAUDE.md"), "- Root dot-claude rule\n")
	writeRule(t, filepath.Join(root, "CLAUDE.local.md"), "- Root local rule\n")
	writeRule(t, filepath.Join(root, ".liveagent", "brain.md"), "- Root brain rule\n")
	writeRule(t, filepath.Join(child, "CLAUDE.md"), "- Child claude rule\n")

	files := InstructionFiles(child)
	want := []string{
		"user CLAUDE.md",
		"project AGENTS.md",
		"project CLAUDE.md",
		"project .claude/CLAUDE.md",
		"project CLAUDE.local.md",
		"project",
		"project CLAUDE.md",
	}
	if got := scopes(files); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("scopes = %q, want %q", got, want)
	}
	if !files[0].User || files[1].User {
		t.Fatalf("only ~/.claude/CLAUDE.md is a user file: %+v", files[:2])
	}
	if files[len(files)-1].Text != "- Child claude rule" {
		t.Fatalf("deepest file should come last: %+v", files[len(files)-1])
	}
	if got := ProjectPromptFiles(child); len(got) != len(files)-1 || got[0].Scope != "project AGENTS.md" {
		t.Fatalf("ProjectPromptFiles should drop user files: %q", scopes(got))
	}
}

func TestInstructionFilesDedupSymlinksCopiesAndImportShims(t *testing.T) {
	_, root := rulesTestEnv(t)
	writeRule(t, filepath.Join(root, "AGENTS.md"), "# Rules\n- Shared rule\n")
	// Same file through a symlink.
	if err := os.Symlink("AGENTS.md", filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Identical text under another name.
	writeRule(t, filepath.Join(root, ".claude", "CLAUDE.md"), "- Shared rule\n")
	// Claude Code import shim pointing at an already loaded file.
	writeRule(t, filepath.Join(root, "CLAUDE.local.md"), "@AGENTS.md\n")

	files := InstructionFiles(root)
	if len(files) != 1 || files[0].Scope != "project AGENTS.md" {
		t.Fatalf("files = %+v, want only AGENTS.md", files)
	}
}

func TestInstructionFilesKeepDistinctAgentsAndClaudeInSameDir(t *testing.T) {
	_, root := rulesTestEnv(t)
	writeRule(t, filepath.Join(root, "AGENTS.md"), "- Agents rule\n")
	writeRule(t, filepath.Join(root, "CLAUDE.md"), "@AGENTS.md\n- Claude-only rule\n")
	files := InstructionFiles(root)
	if len(files) != 2 || files[1].Text != "@AGENTS.md\n- Claude-only rule" {
		t.Fatalf("files = %+v, want AGENTS.md and CLAUDE.md", files)
	}
}

func TestInstructionFilesCursorRules(t *testing.T) {
	_, root := rulesTestEnv(t)
	rules := filepath.Join(root, ".cursor", "rules")
	writeRule(t, filepath.Join(rules, "always.mdc"), "---\ndescription: Always on\nglobs:\nalwaysApply: true\n---\n- Always rule\n")
	writeRule(t, filepath.Join(rules, "scoped.mdc"), "---\ndescription: \"TypeScript style\"\nglobs: src/**/*.ts, src/**/*.tsx\nalwaysApply: false\n---\n- Scoped rule\n")
	writeRule(t, filepath.Join(rules, "listed.mdc"), "---\nglobs:\n  - \"*.go\"\n  - cmd/**\n---\n- Listed rule\n")
	writeRule(t, filepath.Join(rules, "manual.mdc"), "---\nalwaysApply: false\n---\n- Manual rule\n")
	writeRule(t, filepath.Join(rules, "nested", "plain.md"), "- Plain rule\n")
	writeRule(t, filepath.Join(rules, "notes.txt"), "- Not a rule\n")
	writeRule(t, filepath.Join(root, ".cursorrules"), "- Legacy rule\n")

	files := InstructionFiles(root)
	var inlined []string
	var index PromptFile
	for _, f := range files {
		if f.Scope == "project Cursor rules index" {
			index = f
			continue
		}
		inlined = append(inlined, f.Text)
	}
	if got := strings.Join(inlined, "|"); got != "- Legacy rule|- Always rule|- Plain rule" {
		t.Fatalf("inlined = %q", got)
	}
	for _, want := range []string{
		filepath.Join(rules, "scoped.mdc") + " — TypeScript style (applies to files matching: src/**/*.ts, src/**/*.tsx)",
		filepath.Join(rules, "listed.mdc") + " (applies to files matching: *.go, cmd/**)",
		filepath.Join(rules, "manual.mdc") + " (manual: apply only when the user asks for it)",
	} {
		if !strings.Contains(index.Text, want) {
			t.Fatalf("index missing %q:\n%s", want, index.Text)
		}
	}
	if strings.Contains(index.Text, "Scoped rule") || strings.Contains(index.Text, "notes.txt") {
		t.Fatalf("index must list rules, not inline them:\n%s", index.Text)
	}
}

func TestInstructionFilesHonorRulesCompatOptOut(t *testing.T) {
	home, root := rulesTestEnv(t)
	writeRule(t, filepath.Join(os.Getenv("LIVEAGENT_HOME"), "config.json"),
		"// comment\n{\"providers\":{},\"rulesCompat\":{\"claude\":false,\"cursor\":false}}\n")
	writeRule(t, filepath.Join(home, ".claude", "CLAUDE.md"), "- User claude rule\n")
	writeRule(t, filepath.Join(root, "AGENTS.md"), "- Agents rule\n")
	writeRule(t, filepath.Join(root, "CLAUDE.md"), "- Claude rule\n")
	writeRule(t, filepath.Join(root, ".cursor", "rules", "a.mdc"), "---\nalwaysApply: true\n---\n- Cursor rule\n")

	files := InstructionFiles(root)
	if len(files) != 1 || files[0].Scope != "project AGENTS.md" {
		t.Fatalf("files = %+v, want only AGENTS.md when compat is off", files)
	}
}

func TestInstructionFilesCapCompatFiles(t *testing.T) {
	_, root := rulesTestEnv(t)
	line := strings.Repeat("x", 99) + "\n"
	writeRule(t, filepath.Join(root, "CLAUDE.md"), strings.Repeat(line, (maxCompatPromptFileBytes/len(line))+50))
	files := InstructionFiles(root)
	if len(files) != 1 {
		t.Fatalf("files = %+v", files)
	}
	text := files[0].Text
	if len(text) > maxCompatPromptFileBytes+200 || !strings.Contains(text, "(Truncated at 64 KiB. Read "+filepath.Join(root, "CLAUDE.md")) {
		t.Fatalf("oversized CLAUDE.md should be truncated with a pointer, len=%d tail=%q", len(text), text[len(text)-120:])
	}
}

func TestInstructionFilesListCompatFilesPastTotalBudget(t *testing.T) {
	_, root := rulesTestEnv(t)
	rules := filepath.Join(root, ".cursor", "rules")
	per := maxCompatPromptFileBytes - 1024
	n := maxCompatPromptTotalBytes/per + 1
	for i := range n {
		body := strings.Repeat(string(rune('a'+i))+"\n", per/2)
		writeRule(t, filepath.Join(rules, string(rune('a'+i))+".md"), body)
	}
	files := InstructionFiles(root)
	if len(files) != n {
		t.Fatalf("got %d files, want %d", len(files), n)
	}
	last := files[len(files)-1]
	if !strings.HasPrefix(last.Text, "(Not inlined: compatibility rules exceed the 256 KiB budget.") {
		t.Fatalf("last file should be listed, not inlined: %q", last.Text[:min(len(last.Text), 80)])
	}
}

func TestRulesCompatRoundTripsThroughConfig(t *testing.T) {
	var cfg Config
	if err := parseConfigJSONC([]byte(`{"providers":{},"rulesCompat":{"cursor":false}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.RulesCompat.ClaudeEnabled() || cfg.RulesCompat.CursorEnabled() {
		t.Fatalf("rulesCompat = %+v", cfg.RulesCompat)
	}
	data, err := marshalConfig(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"rulesCompat"`) || !strings.Contains(string(data), `"cursor": false`) {
		t.Fatalf("saved config dropped rulesCompat:\n%s", data)
	}
	var empty Config
	data, err = marshalConfig(&empty)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "rulesCompat") {
		t.Fatalf("default rulesCompat should be omitted:\n%s", data)
	}
}
