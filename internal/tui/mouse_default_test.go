package tui

import (
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestMouseDefaultsOn(t *testing.T) {
	cfg := config.Default()
	if cfg.Mouse != nil {
		t.Fatalf("default config must not set mouse (nil = on), got %v", *cfg.Mouse)
	}
	b := false
	cfg2 := &config.Config{Mouse: &b}
	if cfg2.Mouse == nil || *cfg2.Mouse {
		t.Fatal("explicit false should stay off")
	}
}
