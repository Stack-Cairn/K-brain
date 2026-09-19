package tui

import (
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/privacy"
)

func (m *model) privacyCommand(args []string) {
	action := "toggle"
	if len(args) > 0 {
		action = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch action {
	case "on", "enable", "enabled":
		privacy.SetEnabled(true)
	case "off", "disable", "disabled":
		privacy.SetEnabled(false)
	case "toggle":
		privacy.Toggle()
	case "status":
		m.append(dimStyle.Render("privacy gateway: " + privacyState()))
		return
	default:
		m.append(errStyle.Render("usage: /privacy [on|off|toggle|status]"))
		return
	}
	m.append(dimStyle.Render("privacy gateway: " + privacyState() + " — local masking on requests, automatic restoration on responses"))
}

func privacyState() string {
	if privacy.Enabled() {
		return "on"
	}
	return "off"
}
