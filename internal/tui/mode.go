package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m *model) cyclePermissionMode() {
	switch m.permissionMode {
	case "plan":
		m.setPermissionMode("auto_review")
	case "auto_review":
		m.setPermissionMode("always")
	case "always":
		m.setPermissionMode("normal")
	default:
		m.setPermissionMode("plan")
	}
}

func (m *model) setPermissionMode(mode string) {
	if mode == "plan" && m.permissionMode != "plan" {
		// Approving a plan returns to the mode the plan was made from.
		m.cardsState().prePlan = m.permissionMode
	}
	m.permissionMode = mode
	if m.agent != nil {
		m.agent.SetPlanMode(mode == "plan")
	}
	// No feedback line: the mode chip in the footer already shows the change.
}

func (m *model) permissionModeLabel() string { return m.modeLabel(m.permissionMode) }

// modeLabel is a permission mode's display name.
func (m *model) modeLabel(mode string) string {
	switch mode {
	case "plan":
		return m.tr("Plan")
	case "always":
		return m.tr("Always allow")
	case "auto_review":
		return m.tr("Auto review")
	case "auto_edit":
		return m.tr("Auto edit")
	case "full_access":
		return m.tr("Full access")
	default:
		return m.tr("Normal")
	}
}

// modeChip renders the permission mode as a filled badge, followed, when the
// row has room, by a quieter badge for what the mode changes beyond asking:
// plan mode's read-only tools, auto edit's unprompted edits, and a configured
// AI reviewer. The chip keeps permissionModeLabel's exact text so it stays
// translated. Its colour is the risk: allow-everything modes sit on the error
// red so the default "always" mode is never mistaken for a cautious one.
// Without colour the chip keeps its shape as a bracketed label, and the risky
// ones stay reversed.
func (m *model) modeChip() string {
	style := modeChipNormal
	switch m.permissionMode {
	case "plan":
		style = modeChipPlan
	case "always", "full_access":
		style = modeChipAlways
	case "auto_review", "auto_edit":
		style = modeChipReview
	}
	label := m.permissionModeLabel()
	chip := style.Render(label)
	if monoMode {
		chip = style.Render("[" + label + "]")
	}
	if extra := m.modeBadge(); extra != "" && m.width >= 60 {
		chip += " " + extra
	}
	return chip
}

// modeBadge is the second badge: what the current mode does besides asking,
// or "" when the chip says it all.
func (m *model) modeBadge() string {
	var parts []string
	tone := tokTextSecondary
	switch m.permissionMode {
	case "plan":
		parts, tone = append(parts, m.tr("read-only")), tokAccent
	case "auto_edit":
		parts, tone = append(parts, m.tr("edits allowed")), tokStateWarn
	}
	if m.permissionMode != "always" && m.permissionMode != "auto_review" && m.cfg != nil && m.cfg.AIApproval.Enabled() {
		review := m.tr("AI review")
		if m.cfg.AIApproval.Mode == "auto" {
			review = m.tr("AI auto-approve")
		}
		parts = append(parts, review)
	}
	if len(parts) == 0 {
		return ""
	}
	text := chromeGlyphs(strings.Join(parts, " · "))
	switch {
	case monoMode:
		return "[" + text + "]"
	case backgroundsActive():
		return lipgloss.NewStyle().Padding(0, 1).Foreground(tokColor(tone)).Background(tokColor(tokSurfaceRaised)).Render(text)
	}
	return fgTok(tone).Render(text)
}

func (m *model) permissionCommand(args []string) {
	if len(args) == 0 {
		m.cyclePermissionMode()
		return
	}
	if len(args) == 1 {
		switch args[0] {
		case "normal", "plan", "always", "auto_review", "auto_edit", "full_access":
			m.setPermissionMode(args[0])
			return
		}
	}
	m.append(errStyle.Render(m.tr("usage: /permissions [normal|plan|auto_review|auto_edit|full_access|always]")))
}
