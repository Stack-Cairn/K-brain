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
		b.WriteString(dimStyle.Render(fmt.Sprintf("\n    … +%d more (ctrl+e or click to expand)", n)))
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
