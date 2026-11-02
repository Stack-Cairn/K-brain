package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/permissions"
)

// uncExtended returns the \\?\UNC\localhost\C$\... spelling of a local path, which is
// what Rust's fs::canonicalize prints for a project on a network share. Skips when the
// administrative share is unavailable.
func uncExtended(t *testing.T, local string) (extended, plain string) {
	t.Helper()
	if runtime.GOOS != "windows" || len(local) < 3 || local[1] != ':' {
		t.Skip("Windows drive path required")
	}
	tail := strings.ToUpper(local[:1]) + "$" + local[2:]
	extended, plain = `\\?\UNC\localhost\`+tail, `\\localhost\`+tail
	if _, err := os.Stat(extended); err != nil {
		t.Skipf("administrative share unavailable: %v", err)
	}
	return extended, plain
}

// A workspace whose root and working directory arrive in the \\?\UNC\ spelling keeps
// working: relative targets, the root's own spelling and the default process cwd.
func TestLiveAgentPathExtendedUNCWorkspaceStillResolves(t *testing.T) {
	local := t.TempDir()
	root, plain := uncExtended(t, local)
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkingDir(WithWorkspaceRoots(context.Background(), []WorkspaceRoot{{Path: root, Access: "write"}}), root)
	got, err := liveagentPath(ctx, "a.txt", "read", "Read", false)
	if err != nil {
		t.Errorf("relative Read in an extended UNC workspace: %v", err)
	} else if !strings.EqualFold(got, filepath.Join(plain, "a.txt")) {
		t.Errorf("relative Read resolved to %q, want the plain UNC spelling", got)
	}
	if _, err := liveagentPath(ctx, filepath.Join(root, "a.txt"), "write", "Write", false); err != nil {
		t.Errorf("Write of the root's own spelling: %v", err)
	}
	if _, err := liveagentPath(ctx, WorkingDir(ctx), "write", "ManagedProcess", true); err != nil {
		t.Errorf("default ManagedProcess cwd: %v", err)
	}
}

// A read-only root configured in the \\?\UNC\ spelling still vetoes a plain UNC write.
func TestLiveAgentPathReadOnlyExtendedUNCRootVetoesPlainWrite(t *testing.T) {
	local := t.TempDir()
	ro := filepath.Join(local, "ro")
	if err := os.MkdirAll(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	_, plainWork := uncExtended(t, local)
	extRO, plainRO := uncExtended(t, ro)
	ctx := WithWorkspaceRoots(context.Background(), []WorkspaceRoot{{Path: plainWork, Access: "write"}, {Path: extRO, Access: "read"}})
	target := filepath.Join(plainRO, "a.txt")
	if got, err := liveagentPath(ctx, target, "write", "Write", false); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("write %q into read-only root %q: got %q err=%v", target, extRO, got, err)
	}
}

// The TUI write tool gates and writes one path. Every Windows spelling of a denied file
// is either denied by the rule or refused before the gate; none lands in the directory.
func TestWriteToolDenySeesEveryWindowsSpelling(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path namespaces")
	}
	root := t.TempDir()
	secret := filepath.Join(root, "secret")
	if err := os.MkdirAll(secret, 0o755); err != nil {
		t.Fatal(err)
	}
	rules := permissions.RuleSet{Scope: permissions.ScopeUser, Rules: []permissions.StoredRule{{Rule: "Edit(" + filepath.ToSlash(secret) + "/**)", Effect: permissions.Deny}}}
	ctx := WithGate(WithWorkingDir(context.Background(), root), func(req GateRequest) (GateDecision, string) {
		if permissions.Evaluate(permissions.Request{Tool: req.Tool, Command: req.Command}, permissions.Options{ProjectRoot: root}, rules).Effect == permissions.Deny {
			return GateReject, "denied by rule"
		}
		return GateAllowOnce, ""
	})
	file := filepath.Join(secret, "a.txt")
	for _, spelling := range []string{file, `\\?\` + file, `\\.\` + file, `//?/` + filepath.ToSlash(file), `//./` + filepath.ToSlash(file)} {
		for _, tool := range []Tool{writeTool(), editTool()} {
			_ = os.WriteFile(file, []byte("before"), 0o600)
			_, err := tool.Run(ctx, mustJSON(map[string]any{"path": spelling, "content": "leak", "old_string": "before", "new_string": "leak"}))
			if data, _ := os.ReadFile(file); err == nil || string(data) != "before" {
				t.Errorf("%s via %q passed the deny rule (err=%v, file holds %q)", tool.Def.Function.Name, spelling, err, data)
			}
		}
	}
}
