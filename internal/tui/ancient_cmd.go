package tui

import "strings"

func (m *model) ancientCommand(args []string) {
	action := "toggle"
	if len(args) > 0 {
		action = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch action {
	case "ancient", "on", "enable", "enabled":
		m.ancientMode = true
	case "normal", "off", "disable", "disabled":
		m.ancientMode = false
	case "toggle":
		m.ancientMode = !m.ancientMode
	default:
		m.append(errStyle.Render("usage: /ancient [on|off|toggle]"))
		return
	}
	for i := range m.blocks {
		m.blocks[i].stale = true
	}
	m.refreshVP()
	if m.ancientMode {
		m.append(dimStyle.Render("ancient mode: on — vertical text, top-to-bottom and right-to-left"))
	} else {
		m.append(dimStyle.Render("ancient mode: off"))
	}
}
