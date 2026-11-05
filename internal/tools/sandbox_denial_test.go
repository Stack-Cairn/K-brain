package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/sandbox"
)

// TestShellToolsExplainSandboxDenials runs a write the platform sandbox refuses
// through both shell tools and checks the hint reaches the model's text and
// the structured result.
func TestShellToolsExplainSandboxDenials(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policy, err := sandbox.ForRun(nil, sandbox.RunRequest{Root: root, Network: true})
	if err != nil {
		t.Skipf("platform sandbox unavailable: %v", err)
	}
	// Temporary directories are writable by design; write under the user cache.
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no user cache directory")
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Skip(err)
	}
	outside, err := os.MkdirTemp(cache, "kbrain-denial-test-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	if outside, err = filepath.EvalSymlinks(outside); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "denied.txt")
	args, _ := json.Marshal(map[string]string{"command": "echo x > " + target})
	ctx := sandbox.WithPolicy(WithWorkingDir(context.Background(), root), policy)
	for _, tool := range []string{"bash", "Bash"} {
		t.Run(tool, func(t *testing.T) {
			ts := All()
			if tool == "Bash" {
				ts = LiveAgentCatalog()
			}
			result := ExecuteResult(ctx, ts, tool, args, false)
			t.Logf("text %q", result.Text)
			d := result.SandboxDenial
			if d == nil || d.Kind != "write" || d.Path != target || d.SuggestedRoot != outside {
				t.Fatalf("denial %+v, want a write to %s", d, target)
			}
			if !strings.Contains(result.Text, d.Hint()) {
				t.Fatalf("text lacks the hint: %q", result.Text)
			}
			if _, err := os.Stat(target); err == nil {
				t.Fatal("the sandbox let the write through")
			}
		})
	}
	// A failure the sandbox does not explain carries no hint.
	plain, _ := json.Marshal(map[string]string{"command": "echo nope >&2; exit 3"})
	if result := ExecuteResult(ctx, All(), "bash", plain, false); result.SandboxDenial != nil || strings.Contains(result.Text, "[sandbox]") {
		t.Fatalf("unrelated failure explained: %+v", result)
	}
}
