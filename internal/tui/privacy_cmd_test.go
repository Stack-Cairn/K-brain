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

func TestPrivacyStatusShowsLocalLimits(t *testing.T) {
	privacy.SetEnabled(true)
	defer privacy.SetEnabled(false)
	_, _ = privacy.MaskText("contact alice@example.com")
	m := compactCmdModel()
	m.command("/privacy status")
	got := lastBlock(m)
	for _, part := range []string{"privacy gateway: on", "rules:", "mappings:", "body cap: 32 MiB"} {
		if !strings.Contains(got, part) {
			t.Fatalf("status missing %q: %s", part, got)
		}
	}
}
