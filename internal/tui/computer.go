package tui

import (
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/computer"
)

func (m *model) computerConsent(app string) bool {
	m.append(dimStyle.Render("◎ computer_exec wants to drive " + app + " — approve with `computer.allow` in config or re-run after granting"))
	return false
}

var (
	_ = computer.ApprovalNeeded{}
	_ = strings.Contains
)
