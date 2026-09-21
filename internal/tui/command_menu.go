package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m *model) commandArgumentHint() string {
	value := m.input.Value()
	if m.namePrompt != nil || m.iactive != nil || !strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\n\r\t") {
		return ""
	}
	name := strings.TrimRight(value, " ")
	if strings.Contains(name, " ") {
		return ""
	}
	for _, template := range m.promptCatalog.items {
		if "/"+template.Name == name {
			return template.ArgumentHint
		}
	}
	if entry := registryFind(name); entry != nil {
		return entry.Args
	}
	return ""
}

func (m *model) inputArgumentView(view string) string {
	hint := m.commandArgumentHint()
	if hint == "" || m.input.ShowLineNumbers || m.input.LineInfo().CharOffset != utf8.RuneCountInString(m.input.Value()) {
		return view
	}
	lines := strings.Split(view, "\n")
	width := lipgloss.Width(lines[0])
	start := lipgloss.Width(m.input.Prompt) + lipgloss.Width(m.input.Value()) + 1
	if start >= width || m.input.LineInfo().RowOffset != 0 {
		return view
	}
	hint = ansi.Truncate(strings.Join(strings.Fields(hint), " "), width-start, "…")
	end := start + lipgloss.Width(hint)
	lines[0] = ansi.Cut(lines[0], 0, start) + dimStyle.Render(hint) + ansi.Cut(lines[0], end, width)
	return strings.Join(lines, "\n")
}

func (m *model) menuView() string {
	if m.menu == nil || len(m.menu.cands) == 0 {
		return ""
	}
	width := max(m.width, 1)
	start := max(m.menu.idx-menuRows+1, 0)
	end := min(start+menuRows, len(m.menu.cands))
	nameWidth := 0
	for _, candidate := range m.menu.cands {
		nameWidth = max(nameWidth, lipgloss.Width(candidate.Text))
	}
	nameWidth = min(nameWidth, max(width-2, 0))
	var b strings.Builder
	for i := start; i < end; i++ {
		candidate := m.menu.cands[i]
		name := ansi.Truncate(candidate.Text, nameWidth, "…")
		line := name
		if descriptionWidth := width - 2 - nameWidth - 2; descriptionWidth > 0 {
			line += strings.Repeat(" ", nameWidth-lipgloss.Width(name)+2)
			description := ansi.Truncate(strings.Join(strings.Fields(candidate.Desc), " "), descriptionWidth, "…")
			if i == m.menu.idx {
				line += description
			} else {
				line += dimStyle.Render(description)
			}
		}
		if i == m.menu.idx {
			line = selectedRowStyle.Width(width).Render("› " + line)
		} else {
			line = "  " + line
		}
		b.WriteString(ansi.Truncate(line, width, "…"))
		b.WriteByte('\n')
	}
	footer := fmt.Sprintf("  %d/%d", m.menu.idx+1, len(m.menu.cands))
	b.WriteString(dimStyle.Render(ansi.Truncate(footer, width, "…")))
	return b.String()
}
