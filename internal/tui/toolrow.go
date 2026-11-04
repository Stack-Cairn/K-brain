package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	toolHeadStyle = lipgloss.NewStyle().Bold(true)

	diffAddStyle = lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "194", Dark: "22"})
	diffDelStyle = lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "224", Dark: "52"})
)

func toolHeaderName(name string) string {
	switch name {
	case "edit":
		return "Update"
	case "write":
		return "Write"
	case "read":
		return "Read"
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
	case "read", "write", "edit":
		s = get("path")
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

func queuedSubject(name, args string) string {
	if name == "subagent" {
		return toolSubject("subagent", args)
	}
	return firstLine(args)
}

func toolHeaderRow(name, args string, failed bool) string {
	style := toolHeadStyle
	if failed {
		style = errStyle
	}
	return style.Render("● " + toolHeaderName(name) + "(" + toolSubject(name, args) + ")")
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

func renderDiffResult(diff, rest string, expanded bool, width int) string {
	added, removed := diffCounts(diff)
	var b strings.Builder
	b.WriteString(dimStyle.Render("  ⎿ ") + diffSummary(added, removed))
	rows := strings.Split(diff, "\n")
	shown := rows
	if !expanded && len(rows) > diffPreviewRows {
		shown = rows[:diffPreviewRows]
	}
	for _, l := range shown {
		b.WriteString("\n" + renderDiffLine(l, width))
	}
	if n := len(rows) - len(shown); n > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("\n    … +%d more (ctrl+e)", n)))
	}

	if trail := strings.TrimSpace(strings.Join(strings.Split(rest, "\n")[1:], "\n")); trail != "" {
		b.WriteString("\n" + wrap(dimStyle.Render("  "+strings.ReplaceAll(trail, "\n", "\n  ")), width))
	}
	return b.String()
}

func renderDiffLine(l string, width int) string {
	row := ansi.Truncate("    "+l, max(width, 8), "…")
	switch diffLineKind(l) {
	case '+':
		return diffAddStyle.Render(row + strings.Repeat(" ", max(width-lipgloss.Width(row), 0)))
	case '-':
		return diffDelStyle.Render(row + strings.Repeat(" ", max(width-lipgloss.Width(row), 0)))
	}
	return dimStyle.Render(row)
}

// quietResultTool reports tools whose output is read by the model rather than
// the user; their results fold to a line count.
func quietResultTool(name string) bool {
	switch name {
	case "read", "grep", "glob", "list":
		return true
	}
	return false
}

func quietResultUnit(name string, n int) string {
	unit := "line"
	switch name {
	case "grep":
		unit = "match"
	case "glob", "list":
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
// finished headers plus their results.
func toolFamily(k blockKind) bool {
	return k == blockTool || k == blockToolRun || k == blockToolQueued
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
	prev, cur := blocks[j].kind, blocks[i].kind
	if cur == blockTool {
		return prev == blockToolRun
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
