//go:build windows

package permissions_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/permissions"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func set(effect permissions.Effect, rules ...string) permissions.RuleSet {
	s := permissions.RuleSet{Scope: permissions.ScopeUser}
	for _, r := range rules {
		s.Rules = append(s.Rules, permissions.StoredRule{Rule: r, Effect: effect})
	}
	return s
}

func drives(t *testing.T) (process, other string) {
	abs, err := filepath.Abs(`\`)
	if err != nil {
		t.Fatal(err)
	}
	process = filepath.VolumeName(abs)
	other = "C:"
	if strings.EqualFold(process, "C:") {
		other = "D:"
	}
	return
}

// A session whose workspace is on another drive than the process: the tool resolves "/p/secret/keys"
// on the workspace's drive, and the deny rule has to cover that file.
func TestDrivelessDenyFollowsWorkspaceDrive(t *testing.T) {
	_, other := drives(t)
	root := other + `\proj`
	ctx := tools.WithWorkingDir(context.Background(), root)
	resolved := tools.GateRequestFor(ctx, "edit", []byte(`{"path":"/p/secret/keys"}`)).Command
	d := permissions.Evaluate(permissions.Request{Tool: "edit", Command: resolved}, permissions.Options{ProjectRoot: root}, set(permissions.Deny, "Edit(/p/secret/**)"))
	t.Logf("resolved=%s decision=%+v", resolved, d)
	if d.Effect != permissions.Deny {
		t.Errorf("workspace %s: edit /p/secret/keys resolves to %s, Edit(/p/secret/**) = %+v, want deny", root, resolved, d)
	}
}

// The same allow rule, in that session, must not release the process drive's copy, which "/p" does not name there.
func TestDrivelessAllowFollowsWorkspaceDrive(t *testing.T) {
	process, other := drives(t)
	root := other + `\proj`
	target := process + `\p\x`
	d := permissions.Evaluate(permissions.Request{Tool: "edit", Command: target}, permissions.Options{ProjectRoot: root}, set(permissions.Allow, "Edit(/p/**)"))
	t.Logf("decision=%+v", d)
	if d.Effect == permissions.Allow {
		t.Errorf("workspace %s: Edit(/p/**) allowed %s on another drive: %+v", root, target, d)
	}
}

// Rust's canonicalize hands out \?\ spellings; a process started in one has that current directory.
func TestDrivelessDenyWithExtendedProcessCwd(t *testing.T) {
	wd, _ := os.Getwd()
	dir := t.TempDir()
	if err := os.Chdir(w("@@?/") + dir); err != nil {
		t.Skip(err)
	}
	defer os.Chdir(wd)
	abs, _ := filepath.Abs(`\`)
	gw, _ := os.Getwd()
	t.Logf("Getwd=%s Abs=%s Vol=%s", gw, abs, filepath.VolumeName(abs))
	vol := filepath.VolumeName(dir)
	root := vol + `\proj`
	ctx := tools.WithWorkingDir(context.Background(), root)
	resolved := tools.GateRequestFor(ctx, "edit", []byte(`{"path":"/p/secret/keys"}`)).Command
	d := permissions.Evaluate(permissions.Request{Tool: "edit", Command: resolved}, permissions.Options{ProjectRoot: root}, set(permissions.Deny, "Edit(/p/secret/**)"))
	if d.Effect != permissions.Deny {
		t.Errorf("cwd %s: %s under Edit(/p/secret/**) = %+v, want deny", gw, resolved, d)
	}
}

func w(s string) string {
	bs := string(rune(92))
	s = strings.ReplaceAll(s, "@@?/", bs+bs+"?"+bs)
	return strings.ReplaceAll(s, "@@", bs+bs)
}
