package tui

import (
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/browser"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestBrowserDriverPalette(t *testing.T) {
	defer func() { browser.Driver = browser.DriverRod }()
	m := &model{cfg: &config.Config{}}
	var found *paletteItem
	for i, it := range m.paletteItems() {
		if it.title == "Browser driver" {
			found = &m.paletteItems()[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no Browser driver palette row")
	}
	if got := found.dynDesc(m); got == "" {
		t.Fatal("empty description")
	}
	pp := found.panel(m)
	if pp == nil || pp.kind != panelBrowser {
		t.Fatal("panel must be panelBrowser")
	}
	if len(pp.list) != 2 {
		t.Fatalf("want 2 drivers, got %v", pp.list)
	}
	m.switchBrowserDriver(browser.DriverChromedp)
	if browser.Driver != browser.DriverChromedp {
		t.Fatalf("switch failed: %q", browser.Driver)
	}
}
