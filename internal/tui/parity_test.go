package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestPromptTemplateIntegration(t *testing.T) {
	home, root := t.TempDir(), chdir(t)
	t.Setenv("K_BRAIN_HOME", home)
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	global := filepath.Join(home, "prompts")
	project := filepath.Join(root, ".k-brain", "prompts")
	write(global, "audit.md", "Global $1")
	write(global, "model.md", "Do not shadow builtins")
	write(project, "audit.md", "Project $1")
	m := compactCmdModel()
	m.loadPrompts(false)
	if len(m.promptCatalog.items) != 1 || m.promptCatalog.items[0].Body != "Global $1" {
		t.Fatalf("untrusted catalog: %+v", m.promptCatalog)
	}
	if err := config.Trust(root); err != nil {
		t.Fatal(err)
	}
	m.loadPrompts(false)
	if m.promptCatalog.items[0].Body != "Project $1" {
		t.Fatal("trusted project should override global")
	}
	_, cands := m.promptCompletions("/aud")
	if len(cands) != 1 || cands[0].Text != "/audit" {
		t.Fatalf("completion: %+v", cands)
	}
	before := len(m.agent.Messages)
	m.command(`/audit "two words"`)
	if m.input.Value() != "Project two words" || m.busy || len(m.agent.Messages) != before {
		t.Fatal("template must expand into editor without sending")
	}
	m.busy = true
	m.command("/audit busy")
	if m.input.Value() != "Project busy" || len(m.queue) != 0 {
		t.Fatal("busy expansion should stay in editor")
	}
	write(project, "audit.md", "Updated $1")
	m.command("/prompts refresh")
	m.command("/audit now")
	if m.input.Value() != "Updated now" {
		t.Fatal("refresh did not reload")
	}
	m.command(`/audit "unclosed`)
	if !strings.Contains(lastBlock(m), "unclosed quote") {
		t.Fatal("missing argument error")
	}
	write(project, "audit.md", strings.Repeat("line\n", 10001))
	m.command("/prompts refresh")
	m.input.SetValue("keep draft")
	m.command("/audit")
	if m.input.Value() != "keep draft" || !strings.Contains(lastBlock(m), "editor limits") {
		t.Fatal("oversized template silently truncated")
	}
	m.command("/prompts invalid")
	if !strings.Contains(lastBlock(m), "usage:") {
		t.Fatal("missing usage")
	}
}

func TestCopyReplySelectionAndFiles(t *testing.T) {
	msgs := []ai.Message{
		{Role: "assistant", Content: "first"},
		{Role: "user", Content: "ignore"},
		{Role: "assistant", Content: ""},
		{Role: "tool", Content: "ignore"},
		{Role: "assistant", Content: "氪脑\nsecond"},
	}
	for _, tc := range []struct {
		n    int
		want string
	}{{1, "氪脑\nsecond"}, {2, "first"}} {
		got, err := assistantReply(msgs, tc.n)
		if err != nil || got != tc.want {
			t.Fatalf("%q %v", got, err)
		}
	}
	if _, err := assistantReply(msgs, 3); err == nil {
		t.Fatal("missing bounds error")
	}
	for _, tc := range []struct {
		arg  string
		n    int
		path string
	}{{"", 1, ""}, {"2", 2, ""}, {`2 "D:\My Files\reply.md"`, 2, `D:\My Files\reply.md`}, {"reply file.md", 1, "reply file.md"}} {
		n, path, err := parseCopyArgs(tc.arg)
		if err != nil || n != tc.n || path != tc.path {
			t.Fatalf("%q => %d %q %v", tc.arg, n, path, err)
		}
	}
	for _, arg := range []string{"0", "-1", "99999999999999999999999999"} {
		if _, _, err := parseCopyArgs(arg); err == nil {
			t.Fatalf("accepted %q", arg)
		}
	}
	m := shellModel()
	m.agent.Messages = msgs
	file := filepath.Join(t.TempDir(), "reply file.md")
	cmd := m.copyCommand(`"` + file + `"`)
	if cmd == nil {
		t.Fatal("no copy command")
	}
	if msg := cmd(); !strings.Contains(string(msg.(noticeMsg)), "Reply saved") {
		t.Fatalf("%v", msg)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "氪脑\nsecond" {
		t.Fatalf("%q %v", data, err)
	}
	if msg := m.copyCommand(file)(); !strings.Contains(string(msg.(noticeMsg)), "/copy:") {
		t.Fatalf("existing file should not be overwritten: %v", msg)
	}
}

func TestLocalShellDoesNotTouchContext(t *testing.T) {
	for _, busy := range []bool{false, true} {
		m := shellModel()
		m.busy = busy
		m.applyShellDone(shellDoneMsg{cmd: "echo private", out: "private output", localOnly: true})
		if len(m.agent.Messages) != 0 || !strings.Contains(lastBlock(m), "private output") {
			t.Fatal("local output leaked or not rendered")
		}
	}
}

func TestLocalShellExecution(t *testing.T) {
	m := shellModel()
	m.runShell("!!echo local-only-test")
	if len(m.agent.Messages) != 0 || !strings.Contains(lastBlock(m), "local-only-test") || strings.Contains(lastBlock(m), "not recognized") {
		t.Fatalf("local execution: %s", lastBlock(m))
	}
	m.runShell("!!")
	if len(m.agent.Messages) != 0 || !strings.Contains(lastBlock(m), "!! <command>") {
		t.Fatal("empty local command")
	}
}

func TestGitDiffCommand(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "--quiet")
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "file.txt")
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := gitDiff(context.Background(), root, nil)
	if err != nil || !strings.Contains(out, "-before") || !strings.Contains(out, "+after") {
		t.Fatalf("diff: %q %v", out, err)
	}
	out, err = gitDiff(context.Background(), root, []string{"--staged", "--stat"})
	if err != nil || !strings.Contains(out, "file.txt") {
		t.Fatalf("staged: %q %v", out, err)
	}
	if _, err := gitDiff(context.Background(), root, []string{"--output=bad"}); err == nil {
		t.Fatal("accepted arbitrary Git option")
	}
	if _, err := gitDiff(context.Background(), t.TempDir(), nil); err == nil {
		t.Fatal("expected non-repository error")
	}
	git("add", "file.txt")
	out, err = gitDiff(context.Background(), root, nil)
	if err != nil || !strings.Contains(out, "no tracked changes") {
		t.Fatalf("clean: %q %v", out, err)
	}
}

func TestPromptEnterWhileBusy(t *testing.T) {
	home := t.TempDir()
	chdir(t)
	t.Setenv("K_BRAIN_HOME", home)
	dir := filepath.Join(home, "prompts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "audit.md"), []byte("Review $1"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := busyQueueModel()
	m.input.SetValue("/audit project")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.queue) != 0 || m.input.Value() != "Review project" {
		t.Fatalf("queued=%v editor=%q", m.queue, m.input.Value())
	}
	if len(m.agent.Messages) != 0 {
		t.Fatal("expansion called model")
	}
	m = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.queue) != 1 || m.queue[0] != "Review project" {
		t.Fatalf("expanded prompt should enter normal queue: %v", m.queue)
	}
}

func TestDiffOutputBound(t *testing.T) {
	var out diffOutput
	data := []byte(strings.Repeat("x", (1<<20)+100))
	n, err := out.Write(data)
	if err != nil || n != len(data) || len(out.data) != 1<<20 || !out.truncated {
		t.Fatal("diff output limit failed")
	}
	if n, err := out.Write(data); n != len(data) || err != nil || len(out.data) != 1<<20 {
		t.Fatal("repeated overflow failed")
	}
}

func TestDiffTUIDoesNotTouchContext(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := chdir(t)
	cmd := exec.Command("git", "init", "--quiet", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	m := shellModel()
	_, run := m.command("/diff")
	if run == nil {
		t.Fatal("missing diff command")
	}
	m.Update(run())
	if len(m.agent.Messages) != 0 || !strings.Contains(lastBlock(m), "no tracked changes") {
		t.Fatalf("diff leaked: %s", lastBlock(m))
	}
}
