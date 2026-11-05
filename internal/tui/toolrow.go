package tui

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func toolHeaderName(name string) string {
	switch name {
	case "edit":
		return "Edit"
	case "write":
		return "Write"
	case "read":
		return "Read"
	case "grep":
		return "Grep"
	case "glob":
		return "Glob"
	case "list":
		return "List"
	case "web_fetch":
		return "Fetch"
	case "bash":
		return "Bash"
	case "subagent":
		return "Subagent"
	case "subagent_steer":
		return "Steer"
	case "todowrite":
		return "Plan"
	case "remember":
		return "Remember"
	case "forget":
		return "Forget"
	case "browser_exec":
		return "Browser"
	case "computer_exec":
		return "Computer"
	}
	return name
}

func toolSubject(name, args string) string {
	var m map[string]any
	_ = json.Unmarshal([]byte(args), &m)
	get := func(k string) string { s, _ := m[k].(string); return s }
	s := ""
	switch name {
	case "bash":

		s = strings.Join(strings.Fields(get("command")), " ")
	case "read", "write", "edit", "list":
		s = get("path")
		if s == "" && name == "list" {
			s = "."
		}
	case "grep", "glob":
		s = get("pattern")
		if p := get("path"); p != "" && s != "" {
			s += " in " + p
		}
	case "web_fetch":
		s = fetchSubject(get("url"))
	case "subagent":
		if s = get("description"); s == "" {
			s = firstLine(get("prompt"))
		}
	case "subagent_steer":
		s = get("id")
	case "browser_exec", "computer_exec":
		s = browserStepLabel(args)
	}
	if s == "" {
		s = truncLine(oneLine(args), 60)
	}
	return s
}

// fetchSubject shows a fetched URL as host and path, without scheme or query.
func fetchSubject(raw string) string {
	s := strings.TrimSpace(raw)
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		s = u.Host + strings.TrimSuffix(u.EscapedPath(), "/")
	}
	return ansi.Truncate(s, 60, "…")
}

func queuedSubject(name, args string) string {
	if name == "subagent" {
		return toolSubject("subagent", args)
	}
	return firstLine(args)
}

// toolHeaderRow is a finished tool call in the one grammar every state shares:
// glyph, tool name, subject. Only the glyph says how it went; the subject keeps
// its colour whether the call passed or failed.
func toolHeaderRow(name, args string, failed bool) string {
	glyph := textMutedStyle.Render(glyphs.ToolDone)
	if failed {
		glyph = errorTitleStyle.Render(glyphs.Fail)
	}
	return toolRowText(glyph, name, toolSubject(name, args), textSecondaryStyle)
}

// toolRowText lays out "glyph  Name  subject"; tag is the batch position
// (" 2/3") when several calls of one tool run together.
func toolRowText(glyph, name, subject string, subjectStyle lipgloss.Style, tag ...string) string {
	head := toolNameStyle.Render(toolHeaderName(name))
	if len(tag) > 0 && tag[0] != "" {
		head += textMutedStyle.Render(tag[0])
	}
	row := glyph + " " + head
	if subject != "" {
		row += "  " + subjectStyle.Render(subject)
	}
	return row
}

// trailingSpaceRE matches the spaces a word wrap leaves at the end of a line,
// including ones tucked before closing escape sequences.
var trailingSpaceRE = regexp.MustCompile(` +((?:\x1b\[[0-9;]*m)*)$`)

// withMeta right-aligns a meta cell (state, duration) on a row. A row too long
// for the terminal wraps with its continuation indented under the tool name
// rather than losing its tail: the command is what the reader checks.
func withMeta(row, meta string, width int) string {
	width = max(width, 1)
	mw := lipgloss.Width(meta)
	rw := lipgloss.Width(row)
	switch {
	case meta == "" && rw <= width:
		return row
	case meta != "" && rw+2+mw <= width:
		return row + strings.Repeat(" ", width-rw-mw) + meta
	}
	const indent = 2
	lines := strings.Split(wrap(row, max(width-indent, 1)), "\n")
	for i := range lines {
		lines[i] = trailingSpaceRE.ReplaceAllString(lines[i], "$1")
		if i > 0 {
			lines[i] = strings.Repeat(" ", indent) + lines[i]
		}
	}
	if meta != "" {
		last := len(lines) - 1
		if lw := lipgloss.Width(lines[last]); lw+2+mw <= width {
			lines[last] += strings.Repeat(" ", width-lw-mw) + meta
		} else {
			lines = append(lines, strings.Repeat(" ", max(width-mw, 0))+meta)
		}
	}
	return strings.Join(lines, "\n")
}

func extractDiff(result string) (diff, rest string) {
	before, tail, found := strings.Cut(result, "\n```diff\n")
	if !found {
		return "", result
	}
	body, after, found := strings.Cut(tail, "\n```")
	if !found {
		return "", result
	}
	rest = before
	if after = strings.TrimPrefix(after, "\n"); after != "" {
		rest += "\n" + after
	}
	return body, rest
}

func diffLineKind(line string) byte {
	rest := strings.TrimLeft(line, "0123456789")
	if rest != line {
		rest = strings.TrimPrefix(rest, " ")
	}
	switch {
	case strings.HasPrefix(rest, "+ ") || rest == "+":
		return '+'
	case strings.HasPrefix(rest, "- ") || rest == "-":
		return '-'
	}
	return ' '
}

func diffCounts(diff string) (added, removed int) {
	for l := range strings.SplitSeq(diff, "\n") {
		switch diffLineKind(l) {
		case '+':
			added++
		case '-':
			removed++
		}
	}
	return added, removed
}

func diffSummary(added, removed int) string {
	plural := func(n int) string {
		if n == 1 {
			return "line"
		}
		return "lines"
	}
	switch {
	case added > 0 && removed > 0:
		return fmt.Sprintf("Added %d %s, removed %d %s", added, plural(added), removed, plural(removed))
	case added > 0:
		return fmt.Sprintf("Added %d %s", added, plural(added))
	case removed > 0:
		return fmt.Sprintf("Removed %d %s", removed, plural(removed))
	}
	return "No lines changed"
}

const diffPreviewRows = 30

// diffRow is one parsed row of an edit diff: "12 + code", "+ code" or a
// trailing note such as "… +3 more lines".
type diffRow struct {
	num  string
	kind byte // '+', '-', ' ', or 0 for a note
	code string
}

func parseDiffRow(l string) diffRow {
	rest := strings.TrimLeft(l, "0123456789")
	num := l[:len(l)-len(rest)]
	if num != "" {
		if !strings.HasPrefix(rest, " ") {
			return diffRow{code: l}
		}
		rest = rest[1:]
	}
	switch {
	case strings.HasPrefix(rest, "+ ") || rest == "+":
		return diffRow{num, '+', strings.TrimPrefix(rest[1:], " ")}
	case strings.HasPrefix(rest, "- ") || rest == "-":
		return diffRow{num, '-', strings.TrimPrefix(rest[1:], " ")}
	case strings.HasPrefix(rest, "  ") || rest == " ":
		return diffRow{num, ' ', strings.TrimPrefix(rest, "  ")}
	}
	if num == "" {
		return diffRow{code: l}
	}
	return diffRow{num, ' ', rest}
}

var diffPathRE = regexp.MustCompile(`[\w./-]+\.[A-Za-z0-9]+`)

// diffCountsCell is "+2 −1": the added count in state.ok, removed in state.error.
func diffCountsCell(added, removed int) string {
	var parts []string
	if added > 0 {
		parts = append(parts, growStyle.Render(fmt.Sprintf("+%d", added)))
	}
	if removed > 0 {
		parts = append(parts, errStyle.Render(fmt.Sprintf("%s%d", glyphs.Minus, removed)))
	}
	if len(parts) == 0 {
		return textMutedStyle.Render(diffSummary(0, 0))
	}
	return strings.Join(parts, " ")
}

// renderDiffResult draws an edit's diff with a line-number gutter. Added and
// removed rows keep their +/- sign and, where the terminal has background
// colour, a tint under syntax-coloured code; elsewhere the sign and a line
// colour carry the meaning. Long rows wrap under the code, not the gutter.
func renderDiffResult(diff, rest string, expanded bool, width int) string {
	added, removed := diffCounts(diff)
	var b strings.Builder
	b.WriteString(textMutedStyle.Render("  "+glyphs.Result+" ") + diffCountsCell(added, removed) + textMutedStyle.Render("  "+diffSummary(added, removed)))
	rows := strings.Split(diff, "\n")
	shown := rows
	if !expanded && len(rows) > diffPreviewRows {
		shown = rows[:diffPreviewRows]
	}
	numW := 1
	parsed := make([]diffRow, len(shown))
	for i, l := range shown {
		parsed[i] = parseDiffRow(l)
		numW = max(numW, len(parsed[i].num))
	}
	filename := ""
	if first, _, _ := strings.Cut(rest, "\n"); first != "" {
		filename = diffPathRE.FindString(first)
	}
	lexer := codeLexer("", filename)
	for _, r := range parsed {
		b.WriteString("\n" + renderDiffRow(r, numW, lexer, width))
	}
	if n := len(rows) - len(shown); n > 0 {
		b.WriteString("\n" + textHintStyle.Render(fmt.Sprintf("    %s +%d more lines · ctrl+e", glyphs.Fold, n)))
	}

	if trail := strings.TrimSpace(strings.Join(strings.Split(rest, "\n")[1:], "\n")); trail != "" {
		b.WriteString("\n" + wrap(dimStyle.Render("  "+strings.ReplaceAll(trail, "\n", "\n  ")), width))
	}
	return b.String()
}

// renderDiffLine renders a single raw diff row on its own (gutter sized to it).
func renderDiffLine(l string, width int) string {
	r := parseDiffRow(l)
	return renderDiffRow(r, max(len(r.num), 1), nil, width)
}

func renderDiffRow(r diffRow, numW int, lexer chroma.Lexer, width int) string {
	if r.kind == 0 {
		note := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(r.code), "…"))
		return textHintStyle.Render(ansi.Truncate("    "+glyphs.Fold+" "+note, max(width, 8), "…"))
	}
	const lead = "  "
	gutter := lead + strings.Repeat(" ", numW-len(r.num)) + r.num + "  "
	prefixW := len(gutter) + 2
	codeW := max(width-prefixW, 4)

	base := lipgloss.NewStyle()
	tinted := backgroundsActive()
	sign := " "
	lineStyle := lipgloss.NewStyle()
	switch r.kind {
	case '+':
		sign = growStyle.Render("+")
		lineStyle = growStyle
		if tinted {
			base = diffAddStyle
		}
	case '-':
		sign = errStyle.Render("-")
		lineStyle = errStyle
		if tinted {
			base = diffDelStyle
		}
	}
	var code string
	switch {
	case r.kind == ' ' || tinted:
		code = strings.Join(highlightLines(r.code, lexer, base), " ")
	default:
		code = lineStyle.Render(strings.ReplaceAll(r.code, "\t", "    "))
	}
	pieces := strings.Split(ansi.Hardwrap(code, codeW, true), "\n")
	var out []string
	for i, p := range pieces {
		g := withBG(gutterStyle, base).Render(gutter)
		s := withBG(lipgloss.NewStyle(), base).Render(" ")
		if i == 0 {
			out = append(out, g+withBG(signStyleFor(r.kind), base).Render(ansi.Strip(sign))+s+p)
		} else {
			out = append(out, base.Render(strings.Repeat(" ", prefixW))+p)
		}
	}
	for i, l := range out {
		if r.kind != ' ' && tinted {
			out[i] = l + base.Render(strings.Repeat(" ", max(width-lipgloss.Width(l), 0)))
		}
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

// quietResultTool reports tools whose output is read by the model rather than
// the user; their results fold to a line count.
func quietResultTool(name string) bool {
	switch name {
	case "read", "grep", "glob", "list", "web_fetch":
		return true
	}
	return false
}

var (
	searchMatchesRe = regexp.MustCompile(`(?m)^matches=(\d+)`)
	searchTotalRe   = regexp.MustCompile(`(?m)total=(\d+)`)
	fetchTotalRe    = regexp.MustCompile(`(?m)^total_chars=(\d+)$`)
)

// quietResultSummary is the folded line for a read or search result. Searches
// report their own counts in a header; output lines would count the header too.
func quietResultSummary(name, text string, lines int) string {
	if name == "web_fetch" {
		return fetchResultSummary(text)
	}
	n := lines
	re := searchTotalRe
	if name == "grep" {
		re = searchMatchesRe
	}
	if name != "read" {
		if m := re.FindStringSubmatch(text); m != nil {
			n, _ = strconv.Atoi(m[1])
		}
	}
	return fmt.Sprintf("%d %s", n, quietResultUnit(name, n))
}

// fetchResultSummary counts the characters a fetch returned, after its header and
// blank line, and the page's total when that is more.
func fetchResultSummary(text string) string {
	_, body, _ := strings.Cut(text, "\n\n")
	n := utf8.RuneCountInString(body)
	unit := "chars"
	if n == 1 {
		unit = "char"
	}
	if m := fetchTotalRe.FindStringSubmatch(text); m != nil {
		if total, _ := strconv.Atoi(m[1]); total > n {
			return fmt.Sprintf("%d of %d chars", n, total)
		}
	}
	return fmt.Sprintf("%d %s", n, unit)
}

func quietResultUnit(name string, n int) string {
	unit := "line"
	switch name {
	case "grep":
		unit = "match"
	case "glob":
		unit = "file"
	case "list":
		unit = "entry"
	}
	if n == 1 {
		return unit
	}
	switch unit {
	case "match":
		return "matches"
	case "entry":
		return "entries"
	}
	return unit + "s"
}

// toolFamily reports blocks that belong to a tool call: queued, running and
// finished headers plus their results. A resumed session seeds call headers as
// plain text rows tagged with the tool name.
func toolFamily(b block) bool {
	return b.kind == blockTool || b.kind == blockToolRun || b.kind == blockToolQueued || toolHead(b)
}

func toolHead(b block) bool {
	return b.kind == blockToolRun || (b.kind == blockText && b.toolName != "")
}

// gluedToPrev reports whether block i sits directly under the previous visible
// block with no blank row: a result under its header, and back-to-back tool
// calls, read as one compact group.
func gluedToPrev(blocks []block, i int) bool {
	j := i - 1
	for j >= 0 && blocks[j].y0 < 0 && blocks[j].kind == blockBanner {
		j--
	}
	if j < 0 {
		return false
	}
	prev, cur := blocks[j], blocks[i]
	if cur.kind == blockTool {
		return toolHead(prev)
	}
	return toolFamily(cur) && toolFamily(prev)
}

// dropLeadSpaces removes up to n leading spaces from s, skipping over any ANSI
// escape sequences in front of them (glamour opens lines with empty styled
// spans, so a plain TrimPrefix misses its margin).
func dropLeadSpaces(s string, n int) string {
	var b strings.Builder
	i := 0
	for i < len(s) && n > 0 {
		if s[i] == '\x1b' {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
					j++
				}
			}
			j = min(j+1, len(s))
			b.WriteString(s[i:j])
			i = j
			continue
		}
		if s[i] != ' ' {
			break
		}
		i++
		n--
	}
	b.WriteString(s[i:])
	return b.String()
}

// exitNoteRE is the note a shell command that exited non-zero ends its output
// with, "(exit: exit status 1)", possibly wrapped once more in parentheses.
var exitNoteRE = regexp.MustCompile(`\(+exit: [^\n]*\)+\s*$`)

// toolResultFailed reports whether a tool result is a failure: an error from
// the tool itself, or a command that exited non-zero.
func toolResultFailed(result string) bool {
	if strings.HasPrefix(result, "Error:") {
		return true
	}
	return exitNoteRE.MatchString(result) && !strings.Contains(result, "(exit: cancelled)")
}
