package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestForegroundReportCapped(t *testing.T) {
	long := strings.Repeat("x", subagentReportCap+5000)
	srv, _ := modelRecorder(t, long)
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "parent-model", 100, "sys")
	out, err := findTool(t, ag, "subagent").Run(context.Background(),
		json.RawMessage(`{"prompt":"go"}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(out) > len(long) {
		t.Fatalf("report should be capped at %d bytes, got %d", subagentReportCap, len(out))
	}
	if !strings.Contains(out, "report truncated") {
		t.Fatalf("capped report should carry a truncation marker, got tail %q", out[len(out)-120:])
	}
	if !strings.HasPrefix(out, strings.Repeat("x", 100)) {
		t.Fatal("capped report should keep the report's head")
	}
}

func TestForegroundReportUnderCapPassesThrough(t *testing.T) {
	srv, _ := modelRecorder(t, "short report")
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "parent-model", 100, "sys")
	out, err := findTool(t, ag, "subagent").Run(context.Background(),
		json.RawMessage(`{"prompt":"go"}`))
	if err != nil || out != "short report" {
		t.Fatalf("short report should pass through verbatim, got %q, %v", out, err)
	}
}

func TestTaskSlug(t *testing.T) {
	cases := []struct{ desc, want string }{
		{"Survey context growth in pi + oh-my-pi", "survey-context-growth-in-pi-3"},
		{"Fix the bug!", "fix-the-bug-3"},
		{"", "sub-3"},
		{"!!!", "sub-3"},
		{"a b c d e f g", "a-b-c-d-e-3"},
	}
	for _, c := range cases {
		if got := taskSlug(c.desc, 3); got != c.want {
			t.Errorf("taskSlug(%q,3) = %q, want %q", c.desc, got, c.want)
		}
	}
	if n := taskIDNum(taskSlug("survey pi", 42)); n != 42 {
		t.Errorf("taskIDNum should recover the trailing counter, got %d", n)
	}
	if n := taskIDNum("task-7"); n != 7 {
		t.Errorf("taskIDNum on legacy id: got %d", n)
	}
}

func TestStartBackgroundSlugID(t *testing.T) {
	srv, _ := modelRecorder(t, "ok")
	defer srv.Close()
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	task := ag.StartBackground("Survey context growth in codex", "p", SubModel{})
	<-task.Done
	if !strings.HasPrefix(task.ID, "survey-context-growth-in-codex-") {
		t.Fatalf("task id should be a description slug, got %q", task.ID)
	}
}

func TestBackgroundWorktreeRegistersBeforeProvisioning(t *testing.T) {
	if os.Getenv("K_BRAIN_SKIP_WORKTREE_TEST") == "1" {
		t.Skip("skipped via K_BRAIN_SKIP_WORKTREE_TEST")
	}
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "init")
	t.Chdir(repo)

	srv, _ := modelRecorder(t, "done")
	defer srv.Close()
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.WorktreeSubagents = true

	registeredBeforeReturn := false
	ag.Tasks().OnChange = func(*BackgroundTask) { registeredBeforeReturn = true }

	out, err := findTool(t, ag, "subagent").Run(ctx, json.RawMessage(`{"prompt":"go","background":true}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !registeredBeforeReturn {
		t.Fatal("the task row must register before the tool call returns (spawn lag)")
	}
	tasks := ag.Tasks().List()
	if len(tasks) != 1 {
		t.Fatalf("expected the task registered before the call returned, got %d", len(tasks))
	}
	if !strings.Contains(out, "worktree") {
		t.Fatalf("with isolation on, the result should name the worktree: %q", out)
	}
	found := false

	<-tasks[0].Done
	for _, msg := range tasks[0].sub.Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, "git worktree at") {
			found = true
		}
	}
	if !found {
		t.Fatal("worktree path should reach the subagent as its first steered message")
	}
	ag.Tasks().Cancel(tasks[0].ID)
}

func TestStartBackgroundCapturesSubModel(t *testing.T) {
	srv, _ := modelRecorder(t, "ok")
	defer srv.Close()

	parent := New(ai.New(srv.URL, "k"), "parent-m", 100, "sys")
	parent.TaskDefault = SubModel{Client: ai.New(srv.URL, "k"), Model: "sub-default-m"}

	def := parent.StartBackground("uses default", "p", SubModel{})
	<-def.Done
	if def.SubModel != "sub-default-m" {
		t.Fatalf("default route: SubModel = %q, want sub-default-m", def.SubModel)
	}

	ov := parent.StartBackground("uses override", "p", SubModel{Client: ai.New(srv.URL, "k"), Model: "sub-override-m"})
	<-ov.Done
	if ov.SubModel != "sub-override-m" {
		t.Fatalf("override route: SubModel = %q, want sub-override-m", ov.SubModel)
	}

	bare := New(ai.New(srv.URL, "k"), "parent-m", 100, "sys")
	own := bare.StartBackground("uses parent", "p", SubModel{})
	<-own.Done
	if own.SubModel != "parent-m" {
		t.Fatalf("parent route: SubModel = %q, want parent-m", own.SubModel)
	}
}
