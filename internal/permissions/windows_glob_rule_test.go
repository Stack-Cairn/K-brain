package permissions

import (
	"runtime"
	"testing"
)

// A Windows path rule is naturally written with backslashes. Before, the \ in
// front of ** or *.go was read as a glob escape, so D:\work\** matched only a
// file literally named "work*" and the deny silently covered nothing. Windows
// file names cannot contain * or ?, so on Windows \* and \? are a separator
// followed by a glob; \[ and \] stay escapes for Suggest's `[id]` rules.
func TestWindowsBackslashRulesKeepGlobs(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows separators")
	}
	root := `D:\work`
	for _, tc := range []struct{ rule, command string }{
		{`Read(D:\work\**)`, `D:\work\a\b.txt`},
		{`Read(D:\work\**)`, `D:\work\b.txt`},
		{`Read(d:\WORK\**)`, `D:\work\b.txt`},
		{`Read(D:\work\*.go)`, `D:\work\x.go`},
		{`Read(D:\work\?.txt)`, `D:\work\a.txt`},
		{`Read(D:\work\**\*.env)`, `D:\work\sub\deep\secret.env`},
		{`Read(**\*.env)`, `D:\work\sub\secret.env`},
		{`Edit(src\**)`, `D:\work\src\pkg\a.go`},
		{`Edit(src\*\a.go)`, `D:\work\src\pkg\a.go`},
		{`Edit(\\host\share\work\**)`, `\\host\share\work\a.go`},
		{`Edit(\\?\D:\work\**)`, `D:\work\a.go`},
		{`Edit(\\?\UNC\host\share\work\**)`, `\\host\share\work\a.go`},
	} {
		req := Request{Tool: toolOf(tc.rule), Command: tc.command}
		opts := Options{ProjectRoot: root}
		if d := Evaluate(req, opts, deny(tc.rule)); d.Effect != Deny {
			t.Errorf("deny %s vs %s: %+v, want deny", tc.rule, tc.command, d)
		}
		if d := Evaluate(req, opts, allow(tc.rule)); d.Effect != Allow {
			t.Errorf("allow %s vs %s: %+v, want allow", tc.rule, tc.command, d)
		}
	}
	// The globs still have their usual reach.
	for _, tc := range []struct{ rule, command string }{
		{`Read(D:\work\*.go)`, `D:\work\sub\x.go`},
		{`Read(D:\work\?.txt)`, `D:\work\ab.txt`},
		{`Read(D:\work\**)`, `D:\workshop\a.txt`},
		{`Read(D:\work\**)`, `D:\work`},
		{`Edit(src\**)`, `D:\work\other\a.go`},
	} {
		if d := Evaluate(Request{Tool: toolOf(tc.rule), Command: tc.command}, Options{ProjectRoot: root}, deny(tc.rule)); d.Effect == Deny {
			t.Errorf("deny %s reached %s", tc.rule, tc.command)
		}
	}
	// \[ and \] keep escaping, so a bracketed folder name in the form Suggest
	// writes stays literal.
	literal := `Edit(D:/work/app/\[id\]/page.tsx)`
	if d := Evaluate(Request{Tool: "Edit", Command: `D:\work\app\[id]\page.tsx`}, Options{ProjectRoot: root}, deny(literal)); d.Effect != Deny {
		t.Errorf("deny %s missed the bracketed folder: %+v", literal, d)
	}
	if d := Evaluate(Request{Tool: "Edit", Command: `D:\work\app\i\page.tsx`}, Options{ProjectRoot: root}, deny(literal)); d.Effect == Deny {
		t.Errorf("deny %s treated [id] as a character class", literal)
	}
}

// Elsewhere * and ? are legal in file names, so \* stays a literal star.
func TestBackslashStarStaysLiteralOutsideWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file names")
	}
	rule := `Read(/work/\*)`
	if d := Evaluate(Request{Tool: "Read", Command: "/work/*"}, Options{ProjectRoot: "/work"}, deny(rule)); d.Effect != Deny {
		t.Errorf("deny %s missed the file named *: %+v", rule, d)
	}
	if d := Evaluate(Request{Tool: "Read", Command: "/work/a.txt"}, Options{ProjectRoot: "/work"}, deny(rule)); d.Effect == Deny {
		t.Errorf("deny %s matched an ordinary file", rule)
	}
}

func toolOf(rule string) string {
	for i := range rule {
		if rule[i] == '(' {
			return rule[:i]
		}
	}
	return rule
}
