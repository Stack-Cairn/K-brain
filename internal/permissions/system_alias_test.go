package permissions

import (
	"reflect"
	"runtime"
	"testing"
)

func TestSystemPathAliases(t *testing.T) {
	cases := []struct {
		goos, target string
		want         []string
	}{
		{"darwin", "/private/var/folders/x/a.png", []string{"/private/var/folders/x/a.png", "/var/folders/x/a.png"}},
		{"darwin", "/var/folders/x/a.png", []string{"/var/folders/x/a.png", "/private/var/folders/x/a.png"}},
		{"darwin", "/tmp", []string{"/tmp", "/private/tmp"}},
		{"darwin", "/private/etc/hosts", []string{"/private/etc/hosts", "/etc/hosts"}},
		{"darwin", "/variable/a", []string{"/variable/a"}},
		{"darwin", "/private/varx/a", []string{"/private/varx/a"}},
		{"darwin", "/Users/u/var/a", []string{"/Users/u/var/a"}},
		{"linux", "/private/var/a", []string{"/private/var/a"}},
		{"linux", "/var/a", []string{"/var/a"}},
	}
	for _, c := range cases {
		if got := systemPathAliases(c.target, c.goos); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %s: got %q, want %q", c.goos, c.target, got, c.want)
		}
	}
}

// On macOS a rule written with the /var spelling holds for the resolved
// /private/var path and the other way round.
func TestPathRuleMatchesMacOSPrivateSpelling(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS /private symlinks")
	}
	rules := RuleSet{Scope: ScopeUser, Rules: []StoredRule{
		{Rule: "Image(/var/folders/x/**)", Effect: Deny},
		{Rule: "Write(/private/tmp/out/**)", Effect: Allow},
	}}
	opts := Options{ProjectRoot: "/Users/u/proj", AllowReadOnly: true}
	if d := Evaluate(Request{Tool: "Image", Command: "/private/var/folders/x/a.png"}, opts, rules); d.Effect != Deny {
		t.Errorf("resolved spelling not denied: %+v", d)
	}
	if d := Evaluate(Request{Tool: "Image", Command: "/var/folders/x/a.png"}, opts, rules); d.Effect != Deny {
		t.Errorf("literal spelling not denied: %+v", d)
	}
	if d := Evaluate(Request{Tool: "Write", Command: "/tmp/out/a.txt"}, opts, rules); d.Effect != Allow || d.Source != SourceRule {
		t.Errorf("/tmp spelling not allowed by /private/tmp rule: %+v", d)
	}
	if d := Evaluate(Request{Tool: "Write", Command: "/private/tmpx/a.txt"}, opts, rules); d.Effect == Allow {
		t.Errorf("unrelated path allowed: %+v", d)
	}
}
