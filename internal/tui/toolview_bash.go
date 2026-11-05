package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The shell family: bash and the process tools. The header carries the
// command and an exit badge ("exit 1 · 2.8s", "timed out · 30.0s"); the body
// shows the first and the last lines of the output with the middle folded,
// since a command's verdict is usually at the end.

const (
	shellHeadLines = 2
	shellTailLines = 3
)

var (
	timedOutNoteRE = regexp.MustCompile(`\n?\(command timed out\)\s*$`)
	fullOutputRE   = regexp.MustCompile(`^\[full output \(\d+ bytes\): .*\]$`)
)

// cleanShellOutput drops the status notes the shell tool appends to its
// output for the model ("(exit: exit status 1)", "(command timed out)") once
// the header's badge says the same.
func cleanShellOutput(text string, run *toolRunMeta) string {
	if run == nil {
		return text
	}
	if run.exitCode != nil || run.timedOut || run.cancelled {
		text = strings.TrimRight(exitNoteRE.ReplaceAllString(text, ""), "\n")
	}
	if run.timedOut {
		text = strings.TrimRight(timedOutNoteRE.ReplaceAllString(text, ""), "\n")
	}
	return text
}

// renderShellBody is a command's output on an elbow: everything when short or
// expanded, else its head and tail around a fold.
func renderShellBody(b *block, text string, width int) string {
	text = cleanShellOutput(text, b.run)
	elbow := textMutedStyle.Render("  " + glyphs.Result + " ")
	if strings.TrimSpace(text) == "" {
		return elbow + textMutedStyle.Render("(no output)")
	}
	lines := strings.Split(text, "\n")
	style := textSecondaryStyle
	if o, _ := toolOutcomeOf(b); o != outcomeOK || strings.HasPrefix(text, "Error") {
		// A failing command's output is what the reader came for: plain
		// text, not the quiet secondary colour.
		style = lipgloss.NewStyle()
	}
	type row struct {
		text string
		fold int
	}
	var rows []row
	if !b.expanded && len(lines) > shellHeadLines+shellTailLines+1 {
		for _, l := range lines[:shellHeadLines] {
			rows = append(rows, row{text: l})
		}
		rows = append(rows, row{fold: len(lines) - shellHeadLines - shellTailLines})
		for _, l := range lines[len(lines)-shellTailLines:] {
			rows = append(rows, row{text: l})
		}
	} else {
		for _, l := range lines {
			rows = append(rows, row{text: l})
		}
	}
	out := make([]string, 0, len(rows))
	for i, r := range rows {
		if r.fold > 0 {
			out = append(out, foldHint(r.fold, "lines"))
			continue
		}
		lead := elbow
		if i > 0 {
			lead = "    "
		}
		st := style
		if fullOutputRE.MatchString(r.text) {
			st = textHintStyle
		}
		out = append(out, wrapHanging(lead, st.Render(strings.ReplaceAll(r.text, "\t", "    ")), width))
	}
	return strings.Join(out, "\n")
}
