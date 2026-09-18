package tui

import (
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

type cfgSyncMsg struct {
	mod   time.Time
	theme string
}

type cfgSyncTick struct{}

func (m *model) watchConfig() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return cfgSyncTick{} })
}

func (m *model) cfgSync() (tea.Model, tea.Cmd) {
	if m.prog == nil {
		return m, nil
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
		func() tea.Msg { return cfgSyncMsg{mod: fi.ModTime(), theme: cfg.Theme} },
		m.watchConfig(),
	)
}

func (m *model) applyCfgSync(msg cfgSyncMsg) {
	m.cfgMod = msg.mod
	if _, pinned := m.cfgExtra["theme"]; pinned {
		return
	}
	if msg.theme != m.cfg.Theme {
		m.cfg.Theme = msg.theme
		m.themeHow = m.applyTheme(msg.theme)
		m.refreshVP()
	}
}
