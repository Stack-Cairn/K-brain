package permissions

import (
	"runtime"
	"testing"
)

// A remembered Edit rule must cover the request it was made for. \?\D:\... is a
// root-relative path whose first component is "?", joined under the project root.
// Before escapeGlob stopped escaping ? on Windows, Suggest wrote \? and slashPath
// turned it into "/?", so the rule never matched. (A real \\?\ path that
// winpath.Simplify refuses is left out: tools.go rejects it with ErrNamespace
// before any edit happens.)
func TestReviewSuggestedExtendedPathRuleStillMatches(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows extended paths")
	}
	opts := Options{ProjectRoot: `D:\work`}
	for _, command := range []string{
		`\?\D:\work\dot.\a.txt`,
	} {
		req := Request{Tool: "Edit", Command: command}
		rules := Suggest(req, opts)
		if len(rules) != 1 {
			t.Errorf("Suggest(%s) = %v", command, rules)
			continue
		}
		parsed, err := ParseRule(rules[0])
		if err != nil {
			t.Errorf("%s: %v", rules[0], err)
			continue
		}
		stored := parsed.String() // what AddRule writes
		t.Logf("%s: suggested %s, stored %s", command, rules[0], stored)
		if d := Evaluate(req, opts, allow(stored)); d.Effect != Allow {
			t.Errorf("remembered %s does not cover %s: %+v", stored, command, d)
		}
	}
}
