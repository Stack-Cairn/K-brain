package tui

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/privacy"
)

func TestPrivacyCommandTogglesGateway(t *testing.T) {
	privacy.SetEnabled(false)
	defer privacy.SetEnabled(false)
	m := compactCmdModel()
	m.command("/privacy on")
	if !privacy.Enabled() || !strings.Contains(lastBlock(m), "privacy gateway: on") {
		t.Fatal("privacy gateway did not enable")
	}
	m.command("/privacy off")
	if privacy.Enabled() || !strings.Contains(lastBlock(m), "privacy gateway: off") {
		t.Fatal("privacy gateway did not disable")
	}
}
