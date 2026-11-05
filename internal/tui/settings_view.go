package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/browser"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/i18n"
)

// The settings modal is a registry of the settings kn already has, grouped
// by category. Every row reads its value live and changes it through the
// same setter its slash command or palette entry uses, so nothing here adds
// a config key or a second way to store one.

// settingDef is one setting.
type settingDef struct {
	section string
	name    string
	desc    string
	// command is the slash command or key that changes it elsewhere.
	command string
	value   func(m *model) string
	// step changes the value one notch (←/→, Enter when there is no open).
	step func(m *model, delta int)
	// open runs on Enter instead, for settings edited in their own picker.
	open func(m *model) tea.Cmd
	// toggle runs on Enter for on/off settings whose change needs a command.
	toggle func(m *model) tea.Cmd
}

// permissionModes is the order ←/→ walks the permission modes in.
var permissionModes = []string{"normal", "plan", "auto_review", "auto_edit", "full_access", "always"}

// themeNames are the themes /theme and the settings modal accept.
var themeNames = []string{"auto", "light", "dark", "mono"}

// cycle returns the entry delta steps from cur in list, wrapping round.
func cycle(list []string, cur string, delta int) string {
	if len(list) == 0 {
		return cur
	}
	i := slices.Index(list, cur)
	if i < 0 {
		if delta < 0 {
			return list[len(list)-1]
		}
		return list[0]
	}
	return list[(i+delta+len(list))%len(list)]
}

func (m *model) currentThemeName() string {
	if m.cfg == nil || m.cfg.Theme == "" {
		return "auto"
	}
	return m.cfg.Theme
}

func settingDefs() []settingDef {
	return []settingDef{
		{
			section: "Display", name: "Theme", desc: "colour scheme for the transcript and chrome", command: "/theme",
			value: func(m *model) string { return m.currentThemeName() },
			step:  func(m *model, d int) { m.pickTheme(cycle(themeNames, m.currentThemeName(), d)) },
		},
		{
			section: "Display", name: "Language", desc: "interface language", command: "/language",
			value: func(m *model) string { return m.language() },
			step: func(m *model, d int) {
				m.setLanguage(cycle([]string{i18n.English, i18n.Chinese, i18n.TraditionalChinese}, m.language(), d))
			},
		},
		{
			section: "Display", name: "Thinking tokens", desc: "show model reasoning in the transcript", command: palHintThinking,
			value: func(m *model) string { return onOff(m.showThinking) },
			step:  func(m *model, _ int) { m.setThinking(!m.showThinking) },
		},
		{
			section: "Display", name: "Mouse capture", desc: "wheel scroll and click-to-copy inside kn", command: "/mouse",
			value: func(m *model) string { return onOff(m.mouseOn) },
			toggle: func(m *model) tea.Cmd {
				_, cmd := m.command("/mouse")
				return cmd
			},
		},
		{
			section: "Display", name: "Ancient mode", desc: "classical vertical layout for replies", command: "/ancient",
			value: func(m *model) string { return onOff(m.ancientMode) },
			step:  func(m *model, _ int) { m.ancientCommand([]string{"toggle"}) },
		},
		{
			section: "Display", name: "Browser driver", desc: "automation engine that drives Chrome", command: "K_BRAIN_BROWSER_DRIVER",
			value: func(m *model) string { return browser.Driver },
			step:  func(m *model, d int) { m.switchBrowserDriver(cycle(browser.Drivers, browser.Driver, d)) },
		},
		{
			section: "Agent", name: "Model", desc: "model and route for new turns", command: "/model",
			value: func(m *model) string { return m.modelName + "@" + m.provName },
			open: func(m *model) tea.Cmd {
				m.openModelPicker(false)
				return nil
			},
		},
		{
			section: "Agent", name: "Reasoning effort", desc: "how hard the model thinks before answering", command: "/effort",
			value: func(m *model) string { return effortLabel(m.agent.Effort) },
			step: func(m *model, d int) {
				if d < 0 {
					m.setEffort(prevEffort(m.effortsFor(), m.agent.Effort))
					return
				}
				m.setEffort(nextEffort(m.effortsFor(), m.agent.Effort))
			},
		},
		{
			section: "Agent", name: "Permission mode", desc: "what tools may do without asking", command: "/permissions · Shift+Tab",
			value: func(m *model) string { return m.permissionModeLabel() },
			step: func(m *model, d int) {
				cur := m.permissionMode
				if cur == "" {
					cur = "normal"
				}
				m.setPermissionMode(cycle(permissionModes, cur, d))
			},
		},
		{
			section: "Agent", name: "Subagent model", desc: "model subagents run on", command: "config taskModel",
			value: func(m *model) string {
				if m.cfg == nil || m.cfg.TaskModel == "" {
					return "current model"
				}
				return m.cfg.TaskModel
			},
			open: func(m *model) tea.Cmd {
				m.openPaletteOn("Subagent model")
				return nil
			},
		},
		{
			section: "Session", name: "Compaction model", desc: "model that summarises long conversations", command: "/compact <model>",
			value: func(m *model) string {
				if m.compactModel == "" {
					return "default (" + config.DefaultCompactModel + ")"
				}
				return m.compactModel
			},
			open: func(m *model) tea.Cmd {
				m.openPaletteOn("Compaction model")
				return nil
			},
		},
		{
			section: "Session", name: "Compaction level", desc: "auto-compact at this share of the context window", command: "←/→",
			value: func(m *model) string { return fmt.Sprintf("%d%%", m.compactPct()) },
			step:  func(m *model, d int) { m.setCompactPct(m.compactPct() + 10*d) },
		},
		{
			section: "Session", name: "Goal rounds", desc: "rounds a /goal may run before it stops", command: "/goal rounds <n>",
			value: func(m *model) string { return fmt.Sprintf("%d", m.goalMaxRounds()) },
		},
		{
			section: "Session", name: "Privacy gateway", desc: "mask secrets in requests, restore them in replies", command: "/privacy",
			value: func(m *model) string { return privacyState() },
			step:  func(m *model, _ int) { m.privacyCommand([]string{"toggle"}) },
		},
	}
}

func settingRows(m *model) []surfaceRow {
	var rows []surfaceRow
	for _, d := range settingDefs() {
		d := d
		r := surfaceRow{
			section: d.section,
			key:     d.name,
			desc:    d.desc,
			value:   d.value(m),
			search:  d.command,
			muted:   d.step == nil && d.open == nil && d.toggle == nil,
			step:    d.step,
		}
		switch {
		case d.open != nil:
			r.enter = d.open
		case d.toggle != nil:
			r.enter = d.toggle
		}
		r.preview = func(m *model) []string {
			out := []string{cardTitleStyle.Render(m.tr(d.name)) + "  " + accentFgStyle.Render(chromeGlyphs(d.value(m)))}
			w, _ := m.surfaceSize()
			for _, l := range wrapPreview(chromeGlyphs(m.tr(d.desc)), w) {
				out = append(out, textSecondaryStyle.Render(l))
			}
			how := "read-only here"
			switch {
			case d.open != nil:
				how = "Enter opens its picker"
			case d.step != nil:
				how = "←/→ change · Enter next"
			case d.toggle != nil:
				how = "Enter toggles"
			}
			out = append(out, textMutedStyle.Render(chromeGlyphs(m.tr(how)+" · "+d.command)))
			return out
		}
		rows = append(rows, r)
	}
	return rows
}

// settingsCommand is /settings: the modal on an interactive screen, a plain
// listing otherwise.
func (m *model) settingsCommand(args []string) {
	if m.height <= 0 {
		var b strings.Builder
		section := ""
		for _, d := range settingDefs() {
			if d.section != section {
				section = d.section
				b.WriteString(m.tr(section) + "\n")
			}
			fmt.Fprintf(&b, "  %s: %s  (%s)\n", m.tr(d.name), d.value(m), d.command)
		}
		m.append(dimStyle.Render(strings.TrimRight(b.String(), "\n")))
		return
	}
	m.openSettingsFiltered(strings.Join(args, " "))
}

// openSettings opens the settings modal (F2).
func (m *model) openSettings() tea.Cmd {
	m.openSettingsFiltered("")
	return nil
}

func (m *model) openSettingsFiltered(filter string) {
	m.openSurface(&surfaceModal{
		kind:   "settings",
		title:  "Settings",
		filter: filter,
		tabs: []surfaceTab{{
			name:  "Settings",
			rows:  settingRows,
			hints: "↑/↓ select · ←/→ change · Enter toggle or open · Esc close",
		}},
	})
}
