package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestPermissionDigitsPickOptions(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want tools.GateDecision
	}{{"1", tools.GateAllowOnce}, {"2", tools.GateAllowAlways}} {
		m := compactCmdModel()
		m.perms = permRules{}
		reply := make(chan permAnswer, 1)
		m.permDialog = &permDialog{req: tools.GateRequest{Tool: "bash", Command: "go test ./...", Rule: tools.CommandRule("go test ./...")}, reply: reply}
		m.permKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.key)})
		if ans := <-reply; ans.decision != tc.want {
			t.Fatalf("key %s: decision %v, want %v", tc.key, ans.decision, tc.want)
		}
	}
	m := compactCmdModel()
	m.permDialog = &permDialog{req: tools.GateRequest{Tool: "bash", Command: "rm x"}, reply: make(chan permAnswer, 1)}
	m.permKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if !m.permDialog.rejecting {
		t.Fatal("3 should open the reject-with-reason prompt")
	}
}

// The approval card is boxed in the warning colour, keeps the whole command,
// numbers its options and fits any width.
func TestPermissionCardShape(t *testing.T) {
	restore := useProfile(t, termenv.TrueColor, true)
	defer restore()
	long := "go test -run 'TestGoldenViews|TestTokenContrast' -count=1 ./internal/tui/... ./internal/i18n/"
	for _, width := range []int{0, 30, 40, 80, 120} {
		m := compactCmdModel()
		m.width = width
		m.permDialog = &permDialog{req: tools.GateRequest{Tool: "bash", Command: long, Rule: tools.CommandRule(long)}, reply: make(chan permAnswer, 1)}
		out := m.permView()
		plain := ansi.Strip(out)
		lines := strings.Split(plain, "\n")
		if !strings.HasPrefix(lines[0], glyphs.Border.TopLeft) || !strings.HasPrefix(lines[len(lines)-1], glyphs.Border.BottomLeft) {
			t.Fatalf("width %d: approval should be a card:\n%s", width, plain)
		}
		for _, l := range lines {
			if got := lipgloss.Width(l); got != cardWidth(width) {
				t.Fatalf("width %d: card row is %d cells: %q", width, got, l)
			}
		}
		inside := strings.ReplaceAll(strings.ReplaceAll(plain, glyphs.Border.Left, ""), glyphs.Border.Right, "")
		if !strings.Contains(strings.Join(strings.Fields(inside), ""), strings.Join(strings.Fields(long), "")) {
			t.Fatalf("width %d: the command must be shown whole:\n%s", width, plain)
		}
		for _, opt := range []string{"1  Yes, once", "2  Yes, always allow", "3  No, and tell"} {
			if !strings.Contains(plain, opt) {
				t.Fatalf("width %d: missing option %q:\n%s", width, opt, plain)
			}
		}
		if !strings.Contains(plain, glyphs.User+" 1") {
			t.Fatalf("width %d: the selected option carries the caret:\n%s", width, plain)
		}
		if warn := styleSGR(fgTok(tokStateWarn)); !strings.HasPrefix(out, warn) {
			t.Fatalf("width %d: the border should be drawn in state.warn: %q", width, out[:min(len(out), 40)])
		}
	}
}

func TestQuestionCardShape(t *testing.T) {
	restore := useProfile(t, termenv.TrueColor, true)
	defer restore()
	m := compactCmdModel()
	m.width = 60
	m.askDialog = newAskDialog(tools.AskRequest{
		Question: "Which output format?",
		Options:  []tools.AskOption{{Label: "Lines", Description: "streams well"}, {Label: "Array"}},
	}, nil)
	out := m.askView()
	plain := ansi.Strip(out)
	for _, want := range []string{"? Question", "Which output format?", glyphs.User + " 1  Lines", "streams well", "2  Array", "3  type your own answer"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("question card missing %q:\n%s", want, plain)
		}
	}
	if acc := styleSGR(fgTok(tokAccent)); !strings.HasPrefix(out, acc) {
		t.Fatalf("the question card border should use the accent: %q", out[:min(len(out), 40)])
	}
}

func TestNoticesKeepTheirMeaningWithoutColour(t *testing.T) {
	restore := useProfile(t, termenv.Ascii, true)
	defer restore()
	out, ok := renderNotice("error: provider returned 529: overloaded, retrying in 4s", 30)
	if !ok {
		t.Fatal("error notices are restyled")
	}
	lines := strings.Split(ansi.Strip(out), "\n")
	if lines[0] != glyphs.Band+" Error" {
		t.Fatalf("error title row: %q", lines[0])
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, glyphs.Band+" ") || lipgloss.Width(l) > 30 {
			t.Fatalf("every error row hangs off the rail within the width: %q", l)
		}
	}
	if !strings.Contains(strings.Join(lines[1:], " "), "retrying in 4s") {
		t.Fatalf("the cause wraps rather than truncates: %q", lines)
	}

	out, ok = renderNotice("⚠ context is 82% full — /compact soon", 20)
	if !ok {
		t.Fatal("warnings are restyled")
	}
	lines = strings.Split(ansi.Strip(out), "\n")
	if !strings.HasPrefix(lines[0], glyphs.Warn+" ") || len(lines) < 2 || !strings.HasPrefix(lines[1], "  ") {
		t.Fatalf("a warning leads with the glyph and wraps under its text: %q", lines)
	}
	if _, ok := renderNotice("plain line", 40); ok {
		t.Fatal("ordinary text is left alone")
	}
}

// styleSGR is the escape a style opens with.
func styleSGR(s lipgloss.Style) string {
	out := s.Render("x")
	if i := strings.Index(out, "x"); i > 0 {
		return out[:i]
	}
	return ""
}
