package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func TestWordDiffMarksChangedWords(t *testing.T) {
	for _, tc := range []struct {
		a, b   string
		am, bm []string
	}{
		{"v, ok := cache.Get(key, width)", "v, ok := cache.Get(key, height)", []string{"width"}, []string{"height"}},
		{"if !ok {", "if !ok || v.stale {", nil, []string{"|| v.stale "}},
		{"return a + b", "return a - b", []string{"+"}, []string{"-"}},
		// Nothing in common worth marking, and nothing changed.
		{"foo(bar)", "x := 1 + 2 * y", nil, nil},
		{"same line", "same line", nil, nil},
	} {
		am, bm := wordDiff(tc.a, tc.b)
		got := func(s string, ms [][2]int) []string {
			var out []string
			for _, m := range ms {
				out = append(out, s[m[0]:m[1]])
			}
			return out
		}
		if g := got(tc.a, am); strings.Join(g, "|") != strings.Join(tc.am, "|") {
			t.Errorf("%q → %q: old marks %q, want %q", tc.a, tc.b, g, tc.am)
		}
		if g := got(tc.b, bm); strings.Join(g, "|") != strings.Join(tc.bm, "|") {
			t.Errorf("%q → %q: new marks %q, want %q", tc.a, tc.b, g, tc.bm)
		}
	}
}

// Rows pair in order within a removed run and the added run right after
// it; unpaired rows and context get no marks.
func TestIntralineMarksPairRuns(t *testing.T) {
	rows := []diffRow{
		{kind: ' ', code: "ctx"},
		{kind: '-', code: "a := width"},
		{kind: '-', code: "b := width"},
		{kind: '+', code: "a := height"},
		{kind: '+', code: "b := height"},
		{kind: '+', code: "c := extra"},
		{kind: '-', code: "lonely removal"},
	}
	marks := intralineMarks(rows, func(i int) string { return rows[i].code })
	for i, want := range []int{0, 1, 1, 1, 1, 0, 0} {
		if len(marks[i]) != want {
			t.Errorf("row %d (%q): %d marks, want %d", i, rows[i].code, len(marks[i]), want)
		}
	}
}

// The changed word gets the stronger tint on true colour, reverse video on
// 16 colours and in mono, and the text never changes.
func TestDiffWordEmphasisPerMode(t *testing.T) {
	const d = "4 - \tv := cache.Get(key, width)\n4 + \tv := cache.Get(key, height)"
	defer SetLightTheme(false)
	for _, tc := range []struct {
		name    string
		profile termenv.Profile
		mono    bool
		want    string
	}{
		{"truecolor", termenv.TrueColor, false, "48;2;107;42;47"},
		{"16", termenv.ANSI, false, "7"},
		{"mono", termenv.Ascii, true, "7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore := useProfile(t, tc.profile, true)
			defer restore()
			SetLightTheme(false)
			if tc.mono {
				setMonoMode(true, true)
				defer setMonoMode(false, true)
			}
			out := renderDiff(d, "main.go", 80)
			plain := ansi.Strip(out)
			if !strings.Contains(plain, "cache.Get(key, width)") || !strings.Contains(plain, "cache.Get(key, height)") {
				t.Fatalf("the text must stay intact:\n%s", plain)
			}
			del := strings.Split(out, "\n")[0]
			i := strings.Index(del, "width")
			if i < 0 {
				t.Fatalf("no plain \"width\" run in %q", del)
			}
			sgr := del[strings.LastIndex(del[:i], "\x1b["):i]
			if !strings.Contains(sgr, tc.want) {
				t.Fatalf("\"width\" is drawn with %q, want it to carry %q", sgr, tc.want)
			}
		})
	}
}

// Every built-in theme variant tints changed words with its own emphasis
// colours in truecolor, and falls back to reverse video where backgrounds
// are not drawn: on 16 colours and in mono.
func TestDiffWordEmphasisPerTheme(t *testing.T) {
	const d = "4 - \tv := cache.Get(key, width)\n4 + \tv := cache.Get(key, height)"
	defer SetLightTheme(false)
	defer activateTheme(builtinThemes[0])
	sgrBefore := func(t *testing.T, row, word string) string {
		t.Helper()
		i := strings.Index(row, word)
		if i < 0 {
			t.Fatalf("no plain %q run in %q", word, row)
		}
		return row[strings.LastIndex(row[:i], "\x1b["):i]
	}
	for _, th := range builtinThemes {
		for _, light := range []bool{false, true} {
			if (light && th.light == nil) || (!light && th.dark == nil) {
				continue
			}
			p := th.variant(light)
			name := th.name + map[bool]string{false: "-dark", true: "-light"}[light]
			t.Run(name, func(t *testing.T) {
				for _, mode := range []struct {
					name    string
					profile termenv.Profile
					mono    bool
				}{
					{"truecolor", termenv.TrueColor, false},
					{"16", termenv.ANSI, false},
					{"mono", termenv.Ascii, true},
				} {
					restore := useProfile(t, mode.profile, !light)
					activateTheme(th)
					SetLightTheme(light)
					if mode.mono {
						setMonoMode(true, true)
					}
					rows := strings.Split(renderDiff(d, "main.go", 80), "\n")
					del, add := sgrBefore(t, rows[0], "width"), sgrBefore(t, rows[1], "height")
					if mode.mono {
						setMonoMode(false, true)
					}
					restore()
					if mode.name == "truecolor" {
						for _, c := range []struct {
							sgr string
							id  tokenID
						}{{del, tokDiffDelEmphBG}, {add, tokDiffAddEmphBG}} {
							if !sgrBGNear(c.sgr, p[c.id].hex) {
								t.Errorf("%s: emphasis drawn with %q, want the background %s", mode.name, c.sgr, p[c.id].hex)
							}
						}
						continue
					}
					for _, sgr := range []string{del, add} {
						params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(sgr, "\x1b["), "m"), ";")
						reverse, bg := false, false
						for _, p := range params {
							reverse = reverse || p == "7"
							bg = bg || strings.HasPrefix(p, "4") && len(p) == 2 || strings.HasPrefix(p, "10") && len(p) == 3
						}
						if !reverse || bg {
							t.Errorf("%s: emphasis drawn with %q, want reverse video and no background", mode.name, sgr)
						}
					}
				}
			})
		}
	}
}

// sgrBGNear reports whether sgr sets a truecolor background within one
// step per channel of "#rrggbb" (the renderer rounds through float RGB).
func sgrBGNear(sgr, hex string) bool {
	var r, g, b int
	fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
	_, rest, ok := strings.Cut(sgr, "48;2;")
	if !ok {
		return false
	}
	var gr, gg, gb int
	if n, _ := fmt.Sscanf(rest, "%d;%d;%d", &gr, &gg, &gb); n != 3 {
		return false
	}
	near := func(a, b int) bool { return a-b <= 1 && b-a <= 1 }
	return near(r, gr) && near(g, gg) && near(b, gb)
}
