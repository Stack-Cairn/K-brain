package permissions

import (
	"runtime"
	"testing"
)

// \\?\D:\... names the same file as D:\..., so a path rule written either way
// must match a request spelled either way, and a deny must not slip past.
func TestPathRulesSeeThroughExtendedDriveSpelling(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path namespaces")
	}
	for _, tc := range []struct{ rule, command, root string }{
		{"Image(D:/work/**)", `\\?\D:\work\shots\a.png`, `D:\work`},
		{"Image(shots/**)", `\\?\D:\work\shots\a.png`, `D:\work`},
		{"Image(shots/**)", `D:\work\shots\a.png`, `\\?\D:\work`},
		{"Image(shots/**)", `shots\a.png`, `\\?\D:\work`},
		{`Image(\\?\D:/work/**)`, `D:\work\shots\a.png`, `D:\work`},
		{`Image(\\?\UNC\host\share\work/**)`, `\\host\share\work\a.png`, `D:\work`},
		{`Image(//?/unc/host/share/work/**)`, `\\?\UNC\host\share\work\a.png`, `D:\work`},
		{`Image(//host/share/work/**)`, `\\?\UNC\host\share\work\a.png`, `D:\work`},
	} {
		d := Evaluate(Request{Tool: "Image", Command: tc.command}, Options{ProjectRoot: tc.root}, deny(tc.rule))
		if d.Effect != Deny {
			t.Errorf("%s vs %q (root %q): %+v, want deny", tc.rule, tc.command, tc.root, d)
		}
	}
}
