package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func cdFixture(t *testing.T) (*model, string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("K_BRAIN_HOME", filepath.Join(root, "home"))
	oldDir, newDir := filepath.Join(root, "old"), filepath.Join(root, "new")
	for _, dir := range []string{oldDir, newDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(oldDir)
	oldDir = cwd()
	newDir, err := filepath.EvalSymlinks(newDir)
	if err != nil {
		t.Fatal(err)
	}
	m := shellModel()
	m.agent.WorkingDir = oldDir
	m.sandboxPolicy = sandbox.New("strict", "auto", oldDir, false, []string{"cache"}, []string{"readonly"})
	m.agent.SandboxPolicy = m.sandboxPolicy
	return m, oldDir, newDir
}

func TestCdUpdatesExecutionDirectoryAndIsolatesPolicy(t *testing.T) {
	m, oldDir, newDir := cdFixture(t)
	m.sysPrompt = "Custom prompt\n<env>\n  Working directory: " + oldDir + "\n</env>\nHistory: " + oldDir
	m.agent.Messages = []ai.Message{{Role: "system", Content: m.sysPrompt}}
	original := m.sandboxPolicy
	m.cdCommand(newDir)
	if cwd() != newDir || m.agent.WorkingDir != newDir {
		t.Fatalf("directory mismatch: process=%q agent=%q want=%q", cwd(), m.agent.WorkingDir, newDir)
	}
	if m.sandboxPolicy.Root != newDir || m.agent.SandboxPolicy != m.sandboxPolicy {
		t.Fatal("agent and TUI sandbox roots are not synchronized")
	}
	if original.Root != oldDir || original == m.sandboxPolicy {
		t.Fatal("directory change mutated a shared policy")
	}
	m.sandboxPolicy.Writable[0] = "changed"
	m.sandboxPolicy.ReadOnly[0] = "changed"
	if original.Writable[0] != "cache" || original.ReadOnly[0] != "readonly" {
		t.Fatal("directory change shares policy slices")
	}
	if !strings.Contains(m.sysPrompt, "Working directory: "+newDir+"\n") || !strings.Contains(m.sysPrompt, "History: "+oldDir) || m.agent.Messages[0].Content != m.sysPrompt {
		t.Fatal("directory update changed custom prompt text or left stale environment information")
	}
}

func TestCdKeepsDirectoryWhileWorkIsActive(t *testing.T) {
	for _, state := range []string{"turn", "subagent", "followup"} {
		t.Run(state, func(t *testing.T) {
			m, oldDir, newDir := cdFixture(t)
			switch state {
			case "turn":
				m.busy = true
			case "subagent":
				m.agent.RestoreTask(agent.BackgroundTask{ID: "task", Status: agent.TaskRunning})
			case "followup":
				m.agent.RestoreTask(agent.BackgroundTask{ID: "task", Status: agent.TaskDone, FollowingUp: true})
			}
			m.cdCommand(newDir)
			if cwd() != oldDir || m.agent.WorkingDir != oldDir || m.sandboxPolicy.Root != oldDir {
				t.Fatal("active work allowed directory change")
			}
			m.cdCommand("")
			if len(m.blocks) < 2 {
				t.Fatal("bare /cd should remain available")
			}
		})
	}
}

func TestCdFailedPathPreservesExecutionDirectory(t *testing.T) {
	m, oldDir, newDir := cdFixture(t)
	policy := m.sandboxPolicy
	m.cdCommand(filepath.Join(newDir, "missing"))
	if cwd() != oldDir || m.agent.WorkingDir != oldDir || m.sandboxPolicy != policy {
		t.Fatal("failed chdir changed execution configuration")
	}
}

type cdReadClient struct {
	ai.Client
	calls  int
	result string
}

func (c *cdReadClient) Stream(_ context.Context, req ai.Request, _ func(string), _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.calls++
	if c.calls == 1 {
		call := ai.ToolCall{ID: "read-cwd", Type: "function"}
		call.Function.Name = "read"
		call.Function.Arguments = `{"path":"where.txt"}`
		return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}}, ai.Usage{}, nil
	}
	for _, msg := range req.Messages {
		if msg.Role == "tool" {
			c.result = msg.Content
		}
	}
	return ai.Message{Role: "assistant", Content: "done"}, ai.Usage{}, nil
}

func TestCdNextAgentTurnReadsNewDirectory(t *testing.T) {
	m, oldDir, newDir := cdFixture(t)
	writeRewindFile(t, oldDir, "where.txt", "old-directory")
	writeRewindFile(t, newDir, "where.txt", "new-directory")
	client := &cdReadClient{Client: ai.New("http://unused", "test")}
	m.agent = agent.New(client, "model1", 100, "system")
	m.agent.WorkingDir = oldDir
	m.agent.SandboxPolicy = m.sandboxPolicy
	m.cdCommand(newDir)
	if _, err := m.agent.Turn(context.Background(), "read where.txt", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(client.result, "new-directory") || strings.Contains(client.result, "old-directory") {
		t.Fatalf("tool read did not use new directory: %q", client.result)
	}
}

func TestShellUsesCapturedExecutionDirectory(t *testing.T) {
	m, oldDir, newDir := cdFixture(t)
	writeRewindFile(t, oldDir, "where.txt", "old-directory")
	writeRewindFile(t, newDir, "where.txt", "new-directory")
	command := testShellCommand(t, "cat where.txt", "Get-Content -Raw where.txt")
	if err := os.Chdir(newDir); err != nil {
		t.Fatal(err)
	}
	m.runShell("!!" + command)
	if got := lastBlock(m); !strings.Contains(got, "old-directory") {
		t.Fatalf("shell ignored agent directory: %q", got)
	}
	if got := shellExecAt(command, oldDir); !strings.Contains(got, "old-directory") {
		t.Fatalf("shell ignored captured directory: %q", got)
	}
}

func TestCdSnapshotsStayWithinRepository(t *testing.T) {
	for _, sameRepo := range []bool{false, true} {
		t.Run(map[bool]string{false: "different-repository", true: "same-repository"}[sameRepo], func(t *testing.T) {
			m, oldDir, newDir := cdFixture(t)
			git(t, oldDir, "init", "-q")
			if sameRepo {
				newDir = filepath.Join(oldDir, "nested")
				if err := os.MkdirAll(newDir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			store, err := session.Open(filepath.Join(filepath.Dir(oldDir), "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			m.store = store
			m.sessionID, err = store.Create(oldDir, "model1", "provider1")
			if err != nil {
				t.Fatal(err)
			}
			m.snapshots = map[int]string{1: strings.Repeat("1", 40)}
			if err := store.SetSnapshot(m.sessionID, 1, m.snapshots[1]); err != nil {
				t.Fatal(err)
			}
			m.cdCommand(newDir)
			want := 0
			if sameRepo {
				want = 1
			}
			if len(m.snapshots) != want || len(store.Snapshots(m.sessionID)) != want {
				t.Fatal("workspace snapshots were not scoped to the repository")
			}
			meta, _, err := store.Load(m.sessionID)
			if err != nil || meta.CWD != oldDir {
				t.Fatalf("original session project changed: %+v, %v", meta, err)
			}
		})
	}
}

func TestCdSnapshotSaveFailureRollsBack(t *testing.T) {
	m, oldDir, newDir := cdFixture(t)
	store, err := session.Open(filepath.Join(filepath.Dir(oldDir), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	m.store = store
	m.sessionID = "test-session"
	m.snapshots = map[int]string{1: strings.Repeat("1", 40)}
	store.Close()
	m.cdCommand(newDir)
	if cwd() != oldDir || m.agent.WorkingDir != oldDir || m.sandboxPolicy.Root != oldDir || len(m.snapshots) != 1 {
		t.Fatal("snapshot save failure did not preserve original directory and snapshots")
	}
}
