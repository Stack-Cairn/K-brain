package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSystemPromptAppendsUserBrain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)

	p := Build(t.TempDir(), time.Now())
	if !strings.Contains(p, "never force-push") {
		t.Fatal("built-in operating rules must always be present")
	}
	if !strings.Contains(p, "Standing instructions from the user") || !strings.Contains(p, "You are K-brain (氪脑)") {
		t.Fatal("a fresh install should append the English K-brain brain prompt")
	}

	if err := os.WriteFile(filepath.Join(home, "brain.md"), []byte("- Always pnpm, never npm.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p = Build(t.TempDir(), time.Now())
	if !strings.Contains(p, "never force-push") {
		t.Fatal("built-in rules survive a user brain.md")
	}
	if !strings.Contains(p, "Standing instructions from the user") || !strings.Contains(p, "Always pnpm") {
		t.Fatalf("user instructions should append:\n%s", p)
	}
}

func TestSystemPromptEnvBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)

	now := time.Date(2026, 8, 28, 19, 21, 11, 0, time.Local)
	p := Build("/tmp/work", now)

	for _, want := range []string{
		"<env>",
		"Working directory: /tmp/work",
		"Current date/time: Fri Aug 28, 2026 19:21:11",
		"User: ",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("env block should contain %q:\n%s", want, p)
		}
	}
	if !strings.Contains(p, " (UTC") {
		t.Fatalf("date/time should carry a UTC offset:\n%s", p)
	}
}

func TestWithWorkingDirectoryUpdatesOnlyEnvironment(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	p := Build("/old", time.Now()) + "\nUser note: /old\n  Working directory: /old\n"
	want := strings.Replace(p, "\n<env>\n  Working directory: /old\n", "\n<env>\n  Working directory: /new\n", 1)
	if got := WithWorkingDirectory(p, "/new"); got != want {
		t.Fatal("working directory update changed unrelated prompt content")
	}
	for _, custom := range []string{"", "custom instructions", "\n<env>\n  Working directory: unfinished"} {
		if got := WithWorkingDirectory(custom, "/new"); got != custom {
			t.Fatalf("custom prompt changed: %q", got)
		}
	}
}
