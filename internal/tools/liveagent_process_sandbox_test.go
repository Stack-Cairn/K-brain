package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/sandbox"
)

func TestLiveAgentProcessStartWithIDSandboxPolicyInheritedByIsolatedContext(t *testing.T) {
	for _, isolated := range []bool{false, true} {
		t.Run("isolated="+strings.ToLower(strconvBool(isolated)), func(t *testing.T) {
			root := t.TempDir()
			ctx := sandbox.WithPolicy(context.Background(), &sandbox.Policy{
				Mode:    "strict",
				Backend: "liveagent-test-invalid-backend",
				Root:    root,
			})
			manager := newLiveAgentProcessManager()
			if _, err := manager.startWithID(ctx, "test", "printf should-not-run", root, "", time.Second, isolated, !isolated); err == nil {
				t.Fatal("startWithID bypassed the inherited sandbox policy")
			} else if !strings.Contains(err.Error(), "sandbox") && !strings.Contains(err.Error(), "backend") {
				t.Fatalf("unexpected sandbox error: %v", err)
			}
		})
	}
}

func TestLiveAgentBashYieldFailsClosedWhenSandboxWrapFails(t *testing.T) {
	root := t.TempDir()
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), root), []WorkspaceRoot{{Path: root, Access: "write"}})
	ctx = sandbox.WithPolicy(ctx, &sandbox.Policy{
		Mode:    "strict",
		Backend: "liveagent-test-invalid-backend",
		Root:    root,
	})

	_, err := findToolForTest(LiveAgentCatalog(), "Bash").Run(ctx, mustJSON(map[string]any{
		"command":       "printf should-not-run",
		"yield_time_ms": 1,
	}))
	if err == nil {
		t.Fatal("Bash yield succeeded after sandbox wrapping failed")
	}
	if !strings.Contains(err.Error(), "sandbox") && !strings.Contains(err.Error(), "backend") {
		t.Fatalf("unexpected sandbox error: %v", err)
	}
}

func strconvBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
