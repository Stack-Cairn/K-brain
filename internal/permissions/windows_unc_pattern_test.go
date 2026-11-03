package permissions

import (
	"runtime"
	"testing"
)

// Every prefix of the extended spellings parses without panicking.
func TestTrimExtendedPatternShortInputs(t *testing.T) {
	for _, full := range []string{`\\?\UNC\h\s\x`, `//?/unc/h/s/x`, `\\?\UNC/h`, `//?/UNC\h`, `\\?\C:\x`, `//?/c:/x`} {
		for i := 0; i <= len(full); i++ {
			_, _ = ParseRule("Image(" + full[:i] + ")")
			_ = trimExtendedPattern(full[:i])
		}
	}
}

func TestExtendedUNCRuleEdges(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path namespaces")
	}
	for _, tc := range []struct {
		rule, command string
		want          Effect
	}{
		// Mixed separators after the prefix.
		{`Image(//?/unc\host\share\work/**)`, `\\host\share\work\a.png`, Deny},
		{`Image(\\?\UNC/host/share/work/**)`, `\\?\UNC\host\share\work\a.png`, Deny},
		// A deny that matched before keeps matching.
		{`Image(//?/UNC/host/share/work/**)`, `\\?\UNC\host\share\work\a.png`, Deny},
		{`Image(//?/UNC/host/share/work/**)`, `\\host\share\work\a.png`, Deny},
		// Not the UNC prefix: left alone, does not widen to \\host.
		{`Image(\\?\UNCx\host\share/**)`, `\\host\share\a.png`, ""},
		{`Image(//?/UNCx/host/share/**)`, `\\host\share\a.png`, ""},
		// A prefix with nothing after it matches nothing.
		{`Image(\\?\UNC\)`, `\\host\share\a.png`, ""},
		{`Image(//?/UNC/)`, `\\host\share\a.png`, ""},
	} {
		d := Evaluate(Request{Tool: "Image", Command: tc.command}, Options{ProjectRoot: `D:\work`}, deny(tc.rule))
		if d.Effect != tc.want {
			t.Errorf("%s vs %q: %+v, want %q", tc.rule, tc.command, d, tc.want)
		}
	}
}
