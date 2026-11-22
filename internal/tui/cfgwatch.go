package tui

import (
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

type cfgSyncMsg struct {
	mod      time.Time
	theme    string
	language *string
	// loaded carries the memory switch with the disk state Load saw; nil skips it.
	loaded *config.Config
}

type cfgSyncTick struct{}

func (m *model) watchConfig() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return cfgSyncTick{} })
}

func (m *model) cfgSync() (tea.Model, tea.Cmd) {
	if m.prog == nil {
		return m, nil
	}
	// Theme files are re-read when they change, except while the picker is
	// previewing one.
	if m.palette == nil || m.palette.top() == nil || m.palette.top().kind != panelTheme {
		m.refreshCustomThemes(false)
	}
	dir, err := config.Dir()
	if err != nil {
		return m, m.watchConfig()
	}
	fi, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil || !fi.ModTime().After(m.cfgMod) {
		return m, m.watchConfig()
	}
	cfg, err := config.Load()
	if err != nil {
		return m, m.watchConfig()
	}
	return m, tea.Sequence(
		func() tea.Msg {
			return cfgSyncMsg{mod: fi.ModTime(), theme: cfg.Theme, language: &cfg.Language, loaded: cfg}
		},
		m.watchConfig(),
	)
}

func (m *model) applyCfgSync(msg cfgSyncMsg) {
	m.cfgMod = msg.mod
	if msg.language != nil && m.cfg != nil && m.cfg.Language != *msg.language {
		m.cfg.Language = *msg.language
		m.input.Placeholder = m.tr(inputPlaceholder)
		m.syncInputPlaceholder()
		m.menu = nil
		m.refreshVP()
	}
	if m.cfg != nil && msg.loaded != nil {
		// The switch is changed in LiveAgent; follow it like theme and language.
		on := msg.loaded.Memory.IsEnabled()
		changed := m.cfg.Memory.IsEnabled() != on
		m.cfg.AdoptMemory(msg.loaded)
		if changed {
			// Like the backend, the agent takes the switch at a turn boundary: tools this
			// turn already offered the model keep working. Only an agent turn waits, since
			// only it ends with turnDoneMsg; /review and goal formulation do not run one.
			if m.agent != nil && m.agent.TurnRunning() {
				m.memoryPending = &on
			} else {
				m.applyMemorySwitch(on)
			}
		}
	}
	if _, pinned := m.cfgExtra["density"]; !pinned && msg.loaded != nil && m.cfg != nil && msg.loaded.Density != m.cfg.Density {
		m.cfg.Density = msg.loaded.Density
		m.applyDensity()
	}
	if _, pinned := m.cfgExtra["theme"]; pinned {
		return
	}
	if msg.theme != m.cfg.Theme {
		m.cfg.Theme = msg.theme
		m.themeHow = m.applyTheme(msg.theme)
		m.refreshVP()
	}
}

// applyMemorySwitch hands the switch LiveAgent set to the agent and says so.
func (m *model) applyMemorySwitch(on bool) {
	m.memoryPending = nil
	if m.agent != nil {
		m.agent.SetMemoryEnabled(on)
	}
	if on {
		m.append(dimStyle.Render("◐ memory was turned back on in LiveAgent"))
	} else {
		m.append(dimStyle.Render(memoryOffNotice))
	}
}
