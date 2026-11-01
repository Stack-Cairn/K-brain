package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// KB-3 review, round 4 guard: OtherDir compares the cleaned cwd the tool will run in
// with the working directory, so every spelling of another directory is "other" and
// every spelling of the working directory is not; the tool and GateRequestFor agree.
func TestReviewKB3R4CwdSpellings(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "vendor-a")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), root), []WorkspaceRoot{{Path: root, Access: "write"}})
	cases := []struct {
		cwd   string
		other bool
	}{
		{"./vendor-a", true},
		{"vendor-a/", true},
		{"vendor-a/..", false},
		{"./", false},
		{root + string(filepath.Separator), false},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, struct {
			cwd   string
			other bool
		}{strings.ToUpper(root), false}, struct {
			cwd   string
			other bool
		}{"VENDOR-A", true})
	}
	for _, c := range cases {
		var got []GateRequest
		gated := WithGate(ctx, func(req GateRequest) (GateDecision, string) {
			got = append(got, req)
			return GateReject, "stop here"
		})
		args := map[string]any{"command": "git status --short", "cwd": c.cwd}
		_, _ = findToolForTest(LiveAgentCatalog(), "Bash").Run(gated, mustJSON(args))
		if len(got) != 1 || got[0].OtherDir != c.other {
			t.Errorf("Bash cwd=%q: gate requests %+v, want OtherDir=%v", c.cwd, got, c.other)
		}
		if req := GateRequestFor(ctx, "Bash", mustJSON(args)); req.OtherDir != c.other {
			t.Errorf("GateRequestFor(Bash cwd=%q).OtherDir = %v, want %v", c.cwd, req.OtherDir, c.other)
		}
	}
}
