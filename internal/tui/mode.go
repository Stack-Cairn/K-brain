package tui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Stack-Cairn/K-brain/internal/config"
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
	m.savePermissionMode(mode)
	// No feedback line: the mode chip in the footer already shows the change.
}

// startModes are the permission modes a session can start in. Plan is left
// out: it is a detour that returns to the mode it was entered from.
var startModes = []string{"normal", "always", "auto_review", "auto_edit", "full_access"}

// initialPermissionMode is the mode a session starts in: --cautious asks for
// "normal" for this run; otherwise the folder's last choice, then the saved
// default, then "normal" for a first run and "always" for an install from
// before the mode was saved, so its behaviour does not change under it.
func initialPermissionMode(cfg *config.Config, wd string, cautious, firstRun bool) string {
	if cautious {
		return "normal"
	}
	if mode := config.ProjectPermissionMode(wd); slices.Contains(startModes, mode) {
		return mode
	}
	if cfg != nil && slices.Contains(startModes, cfg.PermissionMode) {
		return cfg.PermissionMode
	}
	if firstRun {
		return "normal"
	}
	return "always"
}

// savePermissionMode keeps mode as the folder's choice for the next session.
// Only a session started by Run saves (permWD set): tests and embedded models
// leave the user's files alone.
func (m *model) savePermissionMode(mode string) {
	if m.permWD == "" || !slices.Contains(startModes, mode) {
		return
	}
	if err := config.SetProjectPermissionMode(m.permWD, mode); err != nil {
		m.showNotice(m.tr("could not save the permission mode: ") + err.Error())
	}
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
	chip := m.modeChipLabel()
	if extra := m.modeBadge(); extra != "" && m.width >= 60 {
		chip += " " + extra
	}
	return chip
}

// modeChipLabel is the chip without the second badge: the part of the info
// row that is never cut, however narrow the terminal.
func (m *model) modeChipLabel() string {
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
	if monoMode {
		return style.Render("[" + label + "]")
	}
	return style.Render(label)
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
