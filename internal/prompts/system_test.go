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
	t.Setenv("LIVEAGENT_HOME", home)

	p := Build(t.TempDir(), time.Now())
	if !strings.Contains(p, "You are K-brain (氪脑)") || !strings.Contains(p, "<env>") {
		t.Fatal("a fresh install should load the English K-brain system prompt")
	}

	if err := os.WriteFile(filepath.Join(home, "brain.md"), []byte("- Always pnpm, never npm.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p = Build(t.TempDir(), time.Now())
	if !strings.Contains(p, "Standing instructions from the user") || !strings.Contains(p, "Always pnpm") {
		t.Fatalf("user instructions should append:\n%s", p)
	}
}

func TestSystemPromptCanReplaceDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "system.md"), []byte("Custom system rules."), 0o644); err != nil {
		t.Fatal(err)
	}
	p := Build(t.TempDir(), time.Now())
	if !strings.Contains(p, "Custom system rules.") || strings.Contains(p, "You are an expert coding assistant operating inside k-brain") {
		t.Fatalf("custom system prompt should replace the built-in prompt:\n%s", p)
	}
	if !strings.Contains(p, "<env>") {
		t.Fatal("custom system prompt should retain the environment block")
	}
}

func TestSystemPromptEnvBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)

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

func TestStableSystemPromptRefreshesResourcesWithoutClock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	project := t.TempDir()
	first := BuildStable(project)
	if strings.Contains(first, "Current date/time:") || !strings.Contains(first, "<kbrain-turn-time>") {
		t.Fatal("stable prompt must refer to per-message time instead of wall clock")
	}
	if second := BuildStable(project); second != first {
		t.Fatal("unchanged resources changed the prompt")
	}
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("New project rule."), 0600); err != nil {
		t.Fatal(err)
	}
	if next := BuildStable(project); next == first || !strings.Contains(next, "New project rule.") {
		t.Fatal("stable prompt froze project resources")
	}
	if err := os.WriteFile(filepath.Join(home, "system.md"), []byte("Custom stable rules."), 0600); err != nil {
		t.Fatal(err)
	}
	if next := BuildStable(project); !strings.Contains(next, "Custom stable rules.") || strings.Contains(next, "Current date/time:") {
		t.Fatal("custom prompt did not retain stable environment")
	}
}

func TestSystemPromptAppendsProjectInstructions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	project := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(filepath.Join(project, ".liveagent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("- Run gofmt.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".liveagent", "brain.md"), []byte("- Keep changes small.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := Build(project, time.Now())
	for _, want := range []string{"Project instructions from project AGENTS.md", "Run gofmt.", "Project instructions from project (", "Keep changes small."} {
		if !strings.Contains(p, want) {
			t.Fatalf("project prompt missing %q:\n%s", want, p)
		}
	}
}

func TestSystemPromptAppendsClaudeCompatInstructions(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	project := filepath.Join(t.TempDir(), "project")
	for path, text := range map[string]string{
		filepath.Join(home, ".claude", "CLAUDE.md"): "- User-wide rule.\n",
		filepath.Join(project, ".git", "HEAD"):      "ref: refs/heads/main\n",
		filepath.Join(project, "CLAUDE.md"):         "- Project claude rule.\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	p := Build(project, time.Now())
	userAt := strings.Index(p, "Standing instructions from user CLAUDE.md ("+filepath.Join(home, ".claude", "CLAUDE.md")+" — treat as user rules):\n- User-wide rule.")
	projectAt := strings.Index(p, "Project instructions from project CLAUDE.md ("+filepath.Join(project, "CLAUDE.md")+" — treat as project rules):\n- Project claude rule.")
	if userAt < 0 || projectAt < 0 || userAt > projectAt {
		t.Fatalf("prompt should carry user then project CLAUDE.md rules:\n%s", p)
	}
}

func TestWithWorkingDirectoryUpdatesOnlyEnvironment(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
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

func TestSkillsPromptUsesSessionWorkspace(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, ".agents", "skills", "backend-workspace-test")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: backend-workspace-test\ndescription: Backend workspace skill.\n---\nUse the backend tools.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	prompt := SkillsPrompt(project)
	if !strings.Contains(prompt, "<name>backend-workspace-test</name>") || !strings.Contains(prompt, filepath.Join(dir, "SKILL.md")) {
		t.Fatalf("backend workspace skill missing: %s", prompt)
	}
	if strings.Contains(SkillsPrompt(t.TempDir()), "<name>backend-workspace-test</name>") {
		t.Fatal("skill leaked across workspaces")
	}
}

func TestSystemPromptNamesEachToolSetsSearchTools(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	// An empty system.md keeps the built-in prompt instead of the seeded one.
	if err := os.WriteFile(filepath.Join(home, "system.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cli, backend := build("/w", "", cliSearchTools), build("/w", "", backendSearchTools)
	if !strings.Contains(cli, "- grep: Search file contents") || !strings.Contains(cli, "Use grep, glob and list to search") {
		t.Errorf("CLI prompt should name grep, glob and list:\n%s", cli)
	}
	if !strings.Contains(backend, "- Grep: Search file contents") || strings.Contains(backend, "- grep:") {
		t.Errorf("backend prompt should name the LiveAgent Grep, Glob and List:\n%s", backend)
	}
}

func TestSeededSystemPromptPrefersSearchTools(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	if p := Build(t.TempDir(), time.Now()); !strings.Contains(p, "use the grep, glob, and list tools rather than grep, rg, find, or ls in bash") {
		t.Errorf("the seeded system prompt should steer searches to the search tools:\n%s", p)
	}
}
