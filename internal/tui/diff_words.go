package tui

// Intraline diff emphasis.
//
// A run of removed rows followed directly by a run of added rows is paired
// row by row, and the words that differ within each pair are drawn on a
// stronger tint than the rest of the row, so a one-word edit in a long line
// stands out. Pairs that share too little are left alone: emphasising most
// of a rewritten line says nothing the row tint does not. Without background
// colour (16 colours, mono) the changed words are drawn in reverse video
// instead; with no attributes at all (plain text) nothing is added, so the
// text itself never changes.

import (
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// diffAddEmphStyle and diffDelEmphStyle carry the changed-word tints;
// buildStyles sets them.
var diffAddEmphStyle, diffDelEmphStyle lipgloss.Style

// maxWordDiffCells bounds the token grid of one pair, so a minified line
// costs nothing.
const maxWordDiffCells = 40000

// intralineMarks returns, per row, the byte ranges of its code (tabs
// expanded, as highlightDiff draws it) that differ from its paired row.
func intralineMarks(rows []diffRow, code func(int) string) [][][2]int {
	marks := make([][][2]int, len(rows))
	for i := 0; i < len(rows); {
		if rows[i].kind != '-' {
			i++
			continue
		}
		del := i
		for i < len(rows) && rows[i].kind == '-' {
			i++
		}
		add := i
		for i < len(rows) && rows[i].kind == '+' {
			i++
		}
		nDel, nAdd := add-del, i-add
		for k := range min(nDel, nAdd) {
			a, b := wordDiff(code(del+k), code(add+k))
			marks[del+k], marks[add+k] = a, b
		}
	}
	return marks
}

// diffTokens splits s into words, runs of space, and single other runes,
// as byte offsets.
func diffTokens(s string) [][2]int {
	var toks [][2]int
	class := func(r rune) int {
		switch {
		case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			return 1
		case unicode.IsSpace(r):
			return 2
		}
		return 0
	}
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		c, j := class(r), i+n
		for c != 0 && j < len(s) {
			r2, n2 := utf8.DecodeRuneInString(s[j:])
			if class(r2) != c {
				break
			}
			j += n2
		}
		toks = append(toks, [2]int{i, j})
		i = j
	}
	return toks
}

// wordDiff is the changed byte ranges of a and b, by a longest common
// subsequence of their tokens; nil for both when the lines share too little
// (or too much: nothing changed) to be worth marking.
func wordDiff(a, b string) (am, bm [][2]int) {
	ta, tb := diffTokens(a), diffTokens(b)
	if len(ta) == 0 || len(tb) == 0 || len(ta)*len(tb) > maxWordDiffCells {
		return nil, nil
	}
	tok := func(s string, t [2]int) string { return s[t[0]:t[1]] }
	// lcs[i][j] is the common length of ta[i:] and tb[j:].
	lcs := make([][]int32, len(ta)+1)
	for i := range lcs {
		lcs[i] = make([]int32, len(tb)+1)
	}
	for i := len(ta) - 1; i >= 0; i-- {
		for j := len(tb) - 1; j >= 0; j-- {
			if tok(a, ta[i]) == tok(b, tb[j]) {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	keepA, keepB := make([]bool, len(ta)), make([]bool, len(tb))
	same := 0
	for i, j := 0, 0; i < len(ta) && j < len(tb); {
		switch {
		case tok(a, ta[i]) == tok(b, tb[j]):
			keepA[i], keepB[j] = true, true
			same += ta[i][1] - ta[i][0]
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			i++
		default:
			j++
		}
	}
	// Mark only a pair that is largely the same line: the shared bytes make
	// up at least 40% of both together.
	if same == len(a) && same == len(b) || 5*2*same < 2*(len(a)+len(b)) {
		return nil, nil
	}
	return changedRanges(a, ta, keepA), changedRanges(b, tb, keepB)
}

// changedRanges merges the unkept tokens into ranges, bridging a gap that
// is only spaces so "a b" changed to "c d" marks one run, not two.
func changedRanges(s string, toks [][2]int, keep []bool) [][2]int {
	var out [][2]int
	for i, t := range toks {
		if keep[i] {
			continue
		}
		if n := len(out); n > 0 {
			gap := s[out[n-1][1]:t[0]]
			if gap == "" || isBlank(gap) {
				out[n-1][1] = t[1]
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

func isBlank(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// emphasizeSpans splits spans at the marked byte ranges and draws the
// marked parts with emph's style for their span's style.
func emphasizeSpans(spans []diffSpan, marks [][2]int, emph func(*lipgloss.Style) *lipgloss.Style) []diffSpan {
	if len(marks) == 0 {
		return spans
	}
	out := make([]diffSpan, 0, len(spans)+2*len(marks))
	pos, mi := 0, 0
	for _, sp := range spans {
		start, end := pos, pos+len(sp.text)
		pos = end
		at := start
		for at < end {
			for mi < len(marks) && marks[mi][1] <= at {
				mi++
			}
			if mi == len(marks) || marks[mi][0] >= end {
				out = append(out, diffSpan{sp.text[at-start:], sp.style})
				break
			}
			m := marks[mi]
			if m[0] > at {
				out = append(out, diffSpan{sp.text[at-start : m[0]-start], sp.style})
				at = m[0]
			}
			stop := min(m[1], end)
			out = append(out, diffSpan{sp.text[at-start : stop-start], emph(sp.style)})
			at = stop
		}
	}
	return out
}

// diffEmphasis is how a changed word is drawn on a row of kind: a stronger
// tint where backgrounds show, reverse video where they do not.
func diffEmphasis(kind byte, tinted bool) func(lipgloss.Style) lipgloss.Style {
	if !tinted {
		return func(st lipgloss.Style) lipgloss.Style { return st.Reverse(true) }
	}
	bg := diffAddEmphStyle.GetBackground()
	if kind == '-' {
		bg = diffDelEmphStyle.GetBackground()
	}
	return func(st lipgloss.Style) lipgloss.Style { return st.Background(bg) }
}
