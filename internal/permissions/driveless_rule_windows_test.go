//go:build windows

package permissions

import (
	"path/filepath"
	"testing"
)

// The tools resolve a drive-rooted path against the current drive, so "/p/secret/keys" reaches the
// gate as <drive>:\p\secret\keys. A deny rule spelled "/p/secret/**" has to cover that file.
func TestDrivelessDenyRuleCoversResolvedPathOnWindows(t *testing.T) {
	resolved, err := filepath.Abs("/p/secret/keys")
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{ProjectRoot: filepath.VolumeName(resolved) + `\proj`, AllowReadOnly: true}
	d := Evaluate(Request{Tool: "grep", Command: resolved}, opts, deny("Grep(/p/secret/**)"))
	if d.Effect != Deny {
		t.Errorf("request %q under Grep(/p/secret/**) = %+v, want deny", resolved, d)
	}
}
