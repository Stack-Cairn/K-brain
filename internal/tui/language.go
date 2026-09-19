package tui

import (
	"github.com/Stack-Cairn/K-brain/internal/i18n"
)

func (m *model) language() string {
	if m.cfg == nil {
		return i18n.English
	}
	return i18n.Normalize(m.cfg.Language)
}

func (m *model) tr(text string) string { return i18n.Text(m.language(), text) }

func (m *model) setLanguage(language string) bool {
	if !i18n.Valid(language) {
		m.append(errStyle.Render(m.tr("usage: /language [zh_cn|zh_tw|en]")))
		return false
	}
	if m.cfg == nil {
		m.append(errStyle.Render(m.tr("language: configuration unavailable")))
		return false
	}
	previous := m.cfg.Language
	m.cfg.Language = language
	if err := m.cfg.Save(); err != nil {
		m.cfg.Language = previous
		m.append(errStyle.Render(m.tr("language: could not save: ") + err.Error()))
		return false
	}
	m.input.Placeholder = m.tr(inputPlaceholder)
	m.syncInputPlaceholder()
	m.menu = nil
	m.refreshVP()
	m.append(dimStyle.Render(m.tr("Interface language saved: ") + language))
	return true
}

func (m *model) languageCommand(args []string) {
	if len(args) == 0 {
		m.openPaletteOn("Language")
		return
	}
	if len(args) != 1 {
		m.append(errStyle.Render(m.tr("usage: /language [zh_cn|zh_tw|en]")))
		return
	}
	m.setLanguage(args[0])
}

func (m *model) languagePanel() *ppanel {
	pp := &ppanel{kind: panelLanguage, title: "Language", list: []string{i18n.Chinese, i18n.TraditionalChinese, i18n.English}}
	for i, language := range pp.list {
		if m.language() == language {
			pp.midx = i
			break
		}
	}
	return pp
}

func languageLabel(language string) string {
	if language == i18n.Chinese {
		return "zh_cn  简体中文"
	}
	if language == i18n.TraditionalChinese {
		return "zh_tw  繁體中文"
	}
	return "en     English"
}
