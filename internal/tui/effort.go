package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/charmbracelet/x/ansi"
)

var defaultEfforts = []string{"", "low", "medium", "high"}

var effortCands = []cand{
	{"off", "No reasoning effort parameter sent"},
	{"low", "Fast, shallow reasoning"},
	{"medium", "Balanced reasoning"},
	{"high", "Deep reasoning, slower"},
}

func (m *model) effortsFor() []string {
	return effortsIn(m.catalogs, m.provName, m.agent.Model)
}

func effortsIn(catalogs map[string]config.Catalog, provName, modelID string) []string {
	if c, ok := catalogs[provName]; ok {
		if levels := c.Efforts(modelID); len(levels) > 1 {
			return levels
		}
	}
	return defaultEfforts
}

func nextEffort(levels []string, cur string) string {
	for i, e := range levels {
		if e == cur {
			return levels[(i+1)%len(levels)]
		}
	}
	return levels[0]
}

func effortLabel(e string) string {
	if e == "" {
		return "off"
	}
	return e
}

func effortDescription(level string) string {
	switch strings.ToLower(effortLabel(level)) {
	case "off", "none":
		return "No reasoning"
	case "minimal":
		return "Light reasoning"
	case "low":
		return "Fast reasoning"
	case "medium":
		return "Balanced reasoning"
	case "high":
		return "Deep reasoning"
	case "xhigh", "max":
		return "Maximum depth"
	default:
		return "Custom reasoning"
	}
}

func effortBadge(level string, active bool) string {
	mark := "○"
	if active {
		mark = "●"
	}
	text := mark + " " + effortLabel(level)
	if active {
		return accentStyle.Render(text)
	}
	return dimStyle.Render(text)
}

func (m *model) effortPanelView(pp *ppanel) string {
	var b strings.Builder
	current := effortLabel(m.agent.Effort)
	b.WriteString(accentStyle.Render("  Reasoning mode") + "\n")
	b.WriteString(dimStyle.Render("  current  ") + effortBadge(m.agent.Effort, true) + "  " + dimStyle.Render(effortDescription(current)) + "\n\n")

	maxWidth := m.width - 4
	if maxWidth < 28 {
		maxWidth = 28
	}
	for i, level := range pp.levels {
		active := effortLabel(level) == current
		marker := " "
		if i == pp.lidx {
			marker = "›"
		}
		state := "○"
		if active {
			state = "●"
		}
		line := fmt.Sprintf(" %s %s %-8s  │  %s", marker, state, effortLabel(level), effortDescription(level))
		if active {
			line += "  current"
		}
		line = ansi.Truncate(line, maxWidth, "…")
		if i == pp.lidx {
			b.WriteString(accentStyle.Render(line))
		} else if active {
			b.WriteString(botStyle.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n" + shortcutStyle.Render("  ↑↓ navigate  ·  enter apply  ·  ←→ cycle  ·  esc close"))
	return b.String()
}

func parseEffort(levels []string, s string) (string, bool) {
	if s == "off" {
		return "", true
	}
	for _, e := range levels[1:] {
		if s == e {
			return e, true
		}
	}
	return "", false
}

func effortCandsFor(levels []string) []cand {
	out := make([]cand, 0, len(levels))
	for _, e := range levels {
		out = append(out, cand{effortLabel(e), ""})
	}
	return out
}

func (m *model) updateCatalogs(cats map[string]config.Catalog) {
	m.catalogs = cats
	if n := m.contextLimitFor(m.provName, m.agent.Model); n > 0 && n != m.agent.ContextLimit {
		m.agent.ContextLimit = n
	}
	if !slices.Contains(m.effortsFor(), m.agent.Effort) {
		m.resetEffort("")
		m.append(dimStyle.Render("✦ effort reset to off: not supported by " + m.agent.Model))
	}
}
