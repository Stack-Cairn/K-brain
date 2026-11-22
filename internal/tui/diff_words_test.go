package tui

import (
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
