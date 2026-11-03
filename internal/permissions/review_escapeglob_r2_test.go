package permissions

import (
	"path/filepath"
	"runtime"
	"testing"
)

// Each Suggest branch, with bracketed names, must round-trip through AddRule's
// normalization, cover the request it was made for, stay literal, and dedupe.
func TestReviewEscapeGlobSuggestBranchesRoundTrip(t *testing.T) {
	root := `/work`
	cases := []struct{ command, sibling string }{
		{`/work/app/[id]/page.tsx`, `/work/app/i/page.tsx`}, // dir branch
		{`/work/[x].txt`, `/work/x.txt`},                    // file in root branch
		{`/other/[a]/b.txt`, `/other/a/b.txt`},              // absolute branch
		{`/work/app/[[...slug]]/p.tsx`, `/work/app/s/p.tsx`},
	}
	if runtime.GOOS == "windows" {
		root = `D:\work`
		cases = []struct{ command, sibling string }{
			{`D:\work\app\[id]\page.tsx`, `D:\work\app\i\page.tsx`},
			{`D:\work\[x].txt`, `D:\work\x.txt`},
			{`E:\other\[a]\b.txt`, `E:\other\a\b.txt`},
			{`D:\work\app\[[...slug]]\p.tsx`, `D:\work\app\s\p.tsx`},
			{`\\host\share\[a]\b.txt`, `\\host\share\a\b.txt`},
			{`\\?\D:\work\app\[id]\page.tsx`, `D:\work\app\i\page.tsx`},
		}
	}
	opts := Options{ProjectRoot: root}
	for _, tc := range cases {
		rules := Suggest(Request{Tool: "Edit", Command: tc.command}, opts)
		if len(rules) != 1 {
			t.Errorf("Suggest(%s) = %v", tc.command, rules)
			continue
		}
		p, err := ParseRule(rules[0])
		if err != nil {
			t.Errorf("%s: %v", rules[0], err)
			continue
		}
		stored := p.String()
		if again, _ := ParseRule(stored); again.String() != stored {
			t.Errorf("not idempotent: %s -> %s", stored, again.String())
		}
		if d := Evaluate(Request{Tool: "Edit", Command: tc.command}, opts, allow(stored)); d.Effect != Allow {
			t.Errorf("%s (stored %s) misses %s", rules[0], stored, tc.command)
		}
		if d := Evaluate(Request{Tool: "Edit", Command: tc.sibling}, opts, allow(stored)); d.Effect == Allow {
			t.Errorf("%s over-matches %s", stored, tc.sibling)
		}
		path := filepath.Join(t.TempDir(), "rules.json")
		a, err := AddRule(path, root, StoredRule{Rule: rules[0], Effect: Allow})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := AddRule(path, root, StoredRule{Rule: Suggest(Request{Tool: "Edit", Command: tc.command}, opts)[0], Effect: Allow})
		if a.ID != b.ID {
			t.Errorf("duplicate stored entries for %s", rules[0])
		}
	}
}

// Off Windows * and ? are legal in file names and must stay literal.
func TestReviewEscapeGlobPosixStarStaysLiteral(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file names")
	}
	opts := Options{ProjectRoot: "/work"}
	rules := Suggest(Request{Tool: "Edit", Command: "/work/a*b/c?.txt"}, opts)
	if len(rules) != 1 {
		t.Fatalf("Suggest = %v", rules)
	}
	if d := Evaluate(Request{Tool: "Edit", Command: "/work/aXXb/c.txt"}, opts, allow(rules[0])); d.Effect == Allow {
		t.Errorf("%s treats * as a glob", rules[0])
	}
	if d := Evaluate(Request{Tool: "Edit", Command: "/work/a*b/x.txt"}, opts, allow(rules[0])); d.Effect != Allow {
		t.Errorf("%s misses its own folder", rules[0])
	}
}
