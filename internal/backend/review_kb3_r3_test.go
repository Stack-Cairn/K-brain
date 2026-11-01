package backend

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// cwdClient makes one Bash call with the tool's own cwd argument.
type cwdClient struct {
	scriptedClient
	lock sync.Mutex
	done bool
	args map[string]any
}

func (c *cwdClient) Stream(_ context.Context, req ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if last := req.Messages[len(req.Messages)-1]; last.Role != "tool" && !c.done {
		c.done = true
		raw, _ := json.Marshal(c.args)
		call := ai.ToolCall{ID: "cwd-call-1", Type: "function"}
		call.Function.Name, call.Function.Arguments = "Bash", string(raw)
		return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}}, ai.Usage{}, nil
	}
	if text != nil {
		text("done")
	}
	return ai.Message{Role: "assistant", Content: "done", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

// KB-3 review, round 3, P1: git after cd asks now, but the Bash tool takes the
// directory as an argument (cwd). The gate sees only "git status", which is
// read-only, so git runs in the other repository and its core.fsmonitor program
// runs without a prompt: the same effect as `cd vendor-evil && git status`.
func TestReviewKB3R3BashCwdArgumentRunsOtherRepoHookWithoutPrompt(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("pwsh"); err != nil {
			t.Skip("pwsh not on PATH")
		}
		t.Setenv("K_BRAIN_SHELL", "pwsh.exe")
	} else {
		t.Setenv("K_BRAIN_SHELL", "bash")
	}
	client := &cwdClient{args: map[string]any{"command": "git status --short", "cwd": "vendor-evil"}}
	base, _ := newRulesServer(t, client)
	sess := createTestSession(t, base)
	evil := filepath.Join(sess.CWD, "vendor-evil")
	if err := os.MkdirAll(evil, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "core.fsmonitor", "echo pwned >fsmonitor-ran.txt; exit 1;"}} {
		cmd := exec.Command(git, args...)
		cmd.Dir = evil
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	events, prompts := runRules(t, base, sess.ID, "normal", func(protocol.PermissionRequest) protocol.PermissionDecision {
		return protocol.PermissionDecision{Decision: "reject"}
	})
	_, statErr := os.Stat(filepath.Join(evil, "fsmonitor-ran.txt"))
	if len(prompts) == 0 {
		t.Fatalf("Bash{command: git status, cwd: vendor-evil} ran without a prompt (auto_resolved=%+v, hook ran=%v); git in another directory must ask", autoResolved(events), statErr == nil)
	}
	if statErr == nil {
		t.Fatalf("the rejected command still ran the repository's core.fsmonitor program")
	}
}
