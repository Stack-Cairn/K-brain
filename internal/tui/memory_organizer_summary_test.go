package tui

import (
	"os"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// The TUI shows the organizer schedule LiveAgent saved through /v1/settings.
func TestMemoryOrganizerSummaryFollowsSavedSchedule(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", "")
	t.Setenv("K_BRAIN_HOME", home)
	configPath, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	if got := memoryOrganizerSummary(); got != "organizer: every 24h (backend default)" {
		t.Fatalf("default = %q", got)
	}
	if err := config.SaveMemoryOrganizer(configPath, config.MemoryOrganizerConfig{Enabled: true, IntervalHours: 6}); err != nil {
		t.Fatal(err)
	}
	if got := memoryOrganizerSummary(); got != "organizer: every 6h (LiveAgent memory settings)" {
		t.Fatalf("saved = %q", got)
	}
	if err := config.SaveMemoryOrganizer(configPath, config.MemoryOrganizerConfig{Enabled: false, IntervalHours: 6}); err != nil {
		t.Fatal(err)
	}
	if got := memoryOrganizerSummary(); got != "organizer: off (LiveAgent memory settings)" {
		t.Fatalf("disabled = %q", got)
	}
	if err := os.WriteFile(config.MemoryOrganizerPath(configPath), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := memoryOrganizerSummary(); got != "organizer: saved schedule unreadable, backend default applies" {
		t.Fatalf("unreadable = %q", got)
	}
}
