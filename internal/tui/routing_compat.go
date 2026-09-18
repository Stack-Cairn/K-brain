package tui

import (
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/routing"
)

func DefaultEffortFor(c map[string]config.Catalog, provider, model, pinned string) string {
	return routing.DefaultEffortFor(c, provider, model, pinned)
}
