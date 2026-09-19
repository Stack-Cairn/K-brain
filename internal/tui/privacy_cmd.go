package tui

import (
	"strconv"
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
		status := privacy.Snapshot()
		m.append(dimStyle.Render("privacy gateway: " + privacyState() + " — rules: " + strconv.Itoa(status.Rules) + ", mappings: " + strconv.Itoa(status.Remembered) + ", body cap: " + formatBytes(status.MaxBodyBytes)))
		return
	default:
		m.append(errStyle.Render("usage: /privacy [on|off|toggle|status]"))
		return
	}
	m.append(dimStyle.Render("privacy gateway: " + privacyState() + " — local masking on requests, automatic restoration on responses"))
}

func formatBytes(n int64) string {
	if n >= 1<<20 && n%(1<<20) == 0 {
		return strconv.FormatInt(n/(1<<20), 10) + " MiB"
	}
	if n >= 1<<10 && n%(1<<10) == 0 {
		return strconv.FormatInt(n/(1<<10), 10) + " KiB"
	}
	return strconv.FormatInt(n, 10) + " B"
}

func privacyState() string {
	if privacy.Enabled() {
		return "on"
	}
	return "off"
}
