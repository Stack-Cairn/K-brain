package winpath

import (
	"runtime"
	"testing"
)

func TestNormalize(t *testing.T) {
	if runtime.GOOS != "windows" {
		for _, p := range []string{`\\?\D:\a`, `/home/a\b:c`} {
			if got, err := Normalize(p); err != nil || got != p {
				t.Errorf("off Windows %q -> %q, %v; want unchanged", p, got, err)
			}
		}
		return
	}
	for in, want := range map[string]string{
		`D:\work\a.png`:                  `D:\work\a.png`,
		`work\a.png`:                     `work\a.png`,
		`\\server\share\a.png`:           `\\server\share\a.png`,
		`\\?\D:\work\a.png`:              `D:\work\a.png`,
		`\\?\d:\work\.codex-artifacts\a`: `D:\work\.codex-artifacts\a`,
		`\\?\D:\`:                        `D:\`,
		`\\?\D:\work\`:                   `D:\work\`,
		`\\?\UNC\server\share\a.png`:     `\\server\share\a.png`,
		`\\?\unc\server\share`:           `\\server\share`,
		`\\?\UNC\localhost\C$\work\`:     `\\localhost\C$\work\`,
	} {
		if got, err := Normalize(in); err != nil || got != want {
			t.Errorf("%q -> %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		`\\.\D:\a`, `\??\D:\a`, `//?/D:/a`, `\\?/D:\a`, `/\?\D:\a`,
		`\\?\Volume{01234567-89ab-cdef-0123-456789abcdef}\a`, `\\?\GLOBALROOT\Device\a`,
		`\\?\D:`, `\\?\D:a`, `\\?\D:\a\\b`, `\\?\D:\a/b`, `\\?\D:\a\..\b`, `\\?\D:\.\a`,
		`\\?\D:\a.`, `\\?\D:\a `, `\\?\D:\a:s`, `\\?\D:\aux.txt`, `\\?\D:\LPT1`, `\\?\D:\a` + "\x01",
		`\\?\UNC\`, `\\?\UNC\server`, `\\?\UNC\server\`, `\\?\UNC\\share\a`, `\\?\UNC\server\share\..`,
	} {
		if got, err := Normalize(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
	if got := Simplify(`\\.\pipe\x`); got != `\\.\pipe\x` {
		t.Errorf("Simplify changed a refused spelling: %q", got)
	}
}
