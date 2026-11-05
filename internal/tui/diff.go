package tui

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/Stack-Cairn/K-brain/internal/process"
	"github.com/Stack-Cairn/K-brain/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

type diffOutput struct {
	data      []byte
	truncated bool
}

func (b *diffOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (1 << 20) - len(b.data)
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return n, nil
}

func gitDiff(ctx context.Context, root string, flags []string) (string, error) {
	args := []string{"--no-pager", "diff", "--no-ext-diff", "--no-textconv", "--no-color"}
	for _, flag := range flags {
		switch flag {
		case "--staged", "--cached", "--stat":
			args = append(args, flag)
		default:
			return "", fmt.Errorf("usage: /diff [--staged] [--stat]")
		}
	}
	cmd := exec.CommandContext(ctx, "git", append(args, "--")...)
	cmd.Dir = root
	process.Configure(cmd, false)
	cmd.WaitDelay = time.Second
	var out diffOutput
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	text := string(out.data)
	if err != nil {
		return "", fmt.Errorf("git diff: %w\n%s", err, tools.TruncateTail(text))
	}
	if strings.TrimSpace(text) == "" {
		text = "(no tracked changes; untracked files are not included)"
	}
	if out.truncated {
		text += "\n(diff truncated at 1 MiB)"
	}
	return text, nil
}

func (m *model) diffCommand(flags []string) tea.Cmd {
	for _, flag := range flags {
		if flag != "--staged" && flag != "--cached" && flag != "--stat" {
			m.append(errStyle.Render("usage: /diff [--staged] [--stat]"))
			return nil
		}
	}
	root, err := currentRoot()
	if err != nil {
		m.append(errStyle.Render("/diff: " + err.Error()))
		return nil
	}
	flags = append([]string(nil), flags...)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		text, err := gitDiff(ctx, root, flags)
		if err != nil {
			text = err.Error()
		}
		return shellDoneMsg{cmd: "git diff", out: text, localOnly: true}
	}
}

// ── Diff rendering ───────────────────────────────────────────────────────

// renderDiff draws a diff in the edit tool's "<line> <mark> <text>" row
// format (the format of edit and write results and of tools.EditPreview.Diff)
// at width cells, every row shown: a line-number gutter, the +/- sign, and
// the code coloured by the syntax of path's extension. Added and removed rows
// keep their sign and, where the terminal has background colour, a tint
// under the syntax colours; elsewhere the sign and a line colour carry the
// meaning. Long rows wrap under the code, not under the gutter.
func renderDiff(diff, path string, width int) string {
	return renderDiffFolded(diff, path, width, 0)
}

// renderDiffFolded is renderDiff showing at most maxRows rows (0: all),
// with a "⋯ +N lines · ctrl+e" row for the rest.
func renderDiffFolded(diff, path string, width, maxRows int) string {
	width = max(width, minRenderWidth)
	rows := strings.Split(strings.TrimRight(diff, "\n"), "\n")
	shown := rows
	if maxRows > 0 && len(rows) > maxRows {
		shown = rows[:maxRows]
	}
	parsed := make([]diffRow, len(shown))
	numW := 1
	for i, l := range shown {
		parsed[i] = parseDiffRow(l)
		numW = max(numW, len(parsed[i].num))
	}
	tinted := backgroundsActive()
	code := highlightDiff(parsed, codeLexer("", path), tinted)
	out := make([]string, 0, len(parsed)+1)
	for i, r := range parsed {
		out = append(out, renderDiffRow(r, code[i], numW, tinted, width))
	}
	if n := len(rows) - len(shown); n > 0 {
		out = append(out, foldHint(n, "lines"))
	}
	return strings.Join(out, "\n")
}

// diffSpan is a run of code in one style.
type diffSpan struct {
	text  string
	style lipgloss.Style
}

// diffBase is the background a row's cells sit on.
func diffBase(kind byte, tinted bool) lipgloss.Style {
	if tinted {
		switch kind {
		case '+':
			return diffAddStyle
		case '-':
			return diffDelStyle
		}
	}
	return lipgloss.NewStyle()
}

// highlightDiff colours every row's code. The old side (context and removed
// rows) and the new side (context and added rows) are each lexed as one
// stretch of code, so a construct spanning rows — a block comment, a raw
// string — colours as it does in the file. Without a background tint, added
// and removed rows take the line colour instead of syntax colours.
func highlightDiff(rows []diffRow, lexer chroma.Lexer, tinted bool) [][]diffSpan {
	out := make([][]diffSpan, len(rows))
	side := func(keep func(byte) bool, use func(byte) bool) {
		var idx []int
		var code []string
		for i, r := range rows {
			if r.kind != 0 && keep(r.kind) {
				idx = append(idx, i)
				code = append(code, strings.ReplaceAll(r.code, "\t", "    "))
			}
		}
		toks := lexLines(strings.Join(code, "\n"), lexer, len(code))
		for j, i := range idx {
			if !use(rows[i].kind) {
				continue
			}
			base := diffBase(rows[i].kind, tinted)
			var spans []diffSpan
			for _, t := range toks[j] {
				st := base
				if t.ok {
					st = synStyle(base, t.id)
				}
				spans = append(spans, diffSpan{t.text, st})
			}
			out[i] = spans
		}
	}
	side(func(k byte) bool { return k != '+' }, func(k byte) bool { return k == '-' })
	side(func(k byte) bool { return k != '-' }, func(k byte) bool { return k != '-' })
	if !tinted {
		for i, r := range rows {
			if r.kind == '+' || r.kind == '-' {
				out[i] = []diffSpan{{strings.ReplaceAll(r.code, "\t", "    "), signStyleFor(r.kind)}}
			}
		}
	}
	return out
}

type lexTok struct {
	text string
	id   tokenID
	ok   bool
}

// lexLines splits code into n lines of syntax tokens; a nil lexer gives one
// plain token per line.
func lexLines(code string, lexer chroma.Lexer, n int) [][]lexTok {
	lines := make([][]lexTok, max(n, 1))
	plain := func() [][]lexTok {
		for i, l := range strings.Split(code, "\n") {
			if i < len(lines) && l != "" {
				lines[i] = []lexTok{{text: l}}
			}
		}
		return lines
	}
	if lexer == nil {
		return plain()
	}
	it, err := lexer.Tokenise(nil, code)
	if err != nil {
		return plain()
	}
	at := 0
	for t := it(); t != chroma.EOF; t = it() {
		id, ok := synToken(t.Type)
		for i, part := range strings.Split(t.Value, "\n") {
			if i > 0 {
				at++
			}
			if part != "" && at < len(lines) {
				lines[at] = append(lines[at], lexTok{part, id, ok})
			}
		}
	}
	return lines
}

// renderDiffRow lays one parsed row out at width: gutter, sign, code wrapped
// under itself, and the tint carried to the right edge.
func renderDiffRow(r diffRow, code []diffSpan, numW int, tinted bool, width int) string {
	if r.kind == 0 {
		note := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(r.code), "…"))
		return truncCells(textHintStyle.Render(chromeGlyphs("    "+glyphs.Fold+" "+note)), width)
	}
	base := diffBase(r.kind, tinted)
	gutter := "  " + strings.Repeat(" ", numW-len(r.num)) + r.num + "  "
	prefixW := len(gutter) + 2
	pieces := wrapDiffSpans(code, max(width-prefixW, 4))
	sign := " "
	if r.kind != ' ' {
		sign = string(r.kind)
	}
	out := make([]string, len(pieces))
	for i, p := range pieces {
		lead := base.Render(strings.Repeat(" ", prefixW))
		if i == 0 {
			lead = withBG(gutterStyle, base).Render(gutter) + withBG(signStyleFor(r.kind), base).Render(sign) + base.Render(" ")
		}
		l := lead + p
		if r.kind != ' ' && tinted {
			l += base.Render(strings.Repeat(" ", max(width-lipgloss.Width(l), 0)))
		}
		out[i] = l
	}
	return strings.Join(out, "\n")
}

func signStyleFor(kind byte) lipgloss.Style {
	switch kind {
	case '+':
		return growStyle
	case '-':
		return errStyle
	}
	return lipgloss.NewStyle()
}

// wrapDiffSpans word-wraps styled code to width cells, breaking after a
// space where it can and inside a word where it must, and renders each line.
// Every piece is styled on its own, so no colour leaks across a break.
func wrapDiffSpans(spans []diffSpan, width int) []string {
	type cell struct {
		r  rune
		w  int
		si int
	}
	var lines [][]cell
	var cur []cell
	curW, brk := 0, -1 // brk: index in cur just after the last usable space
	for si, sp := range spans {
		for _, r := range sp.text {
			w := runewidth.RuneWidth(r)
			if curW+w > width && len(cur) > 0 {
				if brk > 0 && brk < len(cur) {
					lines = append(lines, cur[:brk])
					cur = append([]cell(nil), cur[brk:]...)
				} else {
					lines = append(lines, cur)
					cur = nil
				}
				curW, brk = 0, -1
				word := false
				for i, c := range cur {
					curW += c.w
					if c.r != ' ' {
						word = true
					} else if word {
						brk = i + 1
					}
				}
			}
			cur = append(cur, cell{r, w, si})
			curW += w
			if r == ' ' && slices.ContainsFunc(cur[:len(cur)-1], func(c cell) bool { return c.r != ' ' }) {
				brk = len(cur)
			}
		}
	}
	lines = append(lines, cur)
	out := make([]string, len(lines))
	for i, l := range lines {
		var b strings.Builder
		for j := 0; j < len(l); {
			k := j
			var seg strings.Builder
			for k < len(l) && l[k].si == l[j].si {
				seg.WriteRune(l[k].r)
				k++
			}
			b.WriteString(spans[l[j].si].style.Render(seg.String()))
			j = k
		}
		out[i] = b.String()
	}
	return out
}
