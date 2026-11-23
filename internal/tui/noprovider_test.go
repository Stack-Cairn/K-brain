package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

// With only the placeholder provider (no baseUrl), the footer says so before
// anything is sent, and a send fails with a banner naming the provider, the
// file and the setting to fill in.
func TestUnconfiguredProviderIsNamed(t *testing.T) {
	m, _ := stClock(t)
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	m.cfg.Providers["demo"] = config.Provider{Name: "demo", API: "openai-completions"}
	m.provName = "demo"
	m.Update(mkWinSize(120, 30))
	if footer := ansi.Strip(m.View()); !strings.Contains(footer, "(not configured)") {
		t.Fatalf("footer should flag the unconfigured provider:\n%s", footer)
	}
	m.appendTurnError(fmt.Errorf("stream: %w", ai.ErrNoBaseURL))
	path, _ := config.Path()
	plain := ansi.Strip(m.View())
	for _, want := range []string{"No model provider configured", `Provider "demo" has no baseUrl`, "providers.demo.baseUrl"} {
		if !strings.Contains(strings.Join(strings.Fields(plain), " "), want) {
			t.Errorf("missing %q in\n%s", want, plain)
		}
	}
	if !strings.Contains(m.noProviderCause(), path) {
		t.Errorf("cause should name %s: %q", path, m.noProviderCause())
	}

	m.provName = "inference"
	m.Update(mkWinSize(120, 30))
	if strings.Contains(ansi.Strip(m.View()), "(not configured)") {
		t.Fatal("a configured provider must not be flagged")
	}
}
