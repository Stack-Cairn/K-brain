package routing

import (
	"slices"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func DefaultEffortFor(catalogs map[string]config.Catalog, provName, modelID, pinned string) string {
	if pinned != "" {
		return pinned
	}

	if c, ok := catalogs[provName]; ok {
		if mi := c.Find(modelID); mi != nil {
			levels := c.Efforts(modelID)
			if slices.Contains(levels, "low") {
				return "low"
			}
			for _, e := range levels {
				if e != "" {
					return e
				}
			}
			return ""
		}
	}
	return "low"
}

// DefaultEfforts are the reasoning effort levels offered when the catalog lists none
// for a model; "" is off (no effort parameter is sent).
var DefaultEfforts = []string{"", "low", "medium", "high"}

// EffortsFor lists the reasoning effort levels the model supports, "" (off) first.
func EffortsFor(catalogs map[string]config.Catalog, provName, modelID string) []string {
	if c, ok := catalogs[provName]; ok {
		if levels := c.Efforts(modelID); len(levels) > 1 {
			return levels
		}
	}
	return DefaultEfforts
}

// ParseEffort matches s against levels as returned by EffortsFor; "off" selects no
// reasoning effort.
func ParseEffort(levels []string, s string) (string, bool) {
	if s == "off" {
		return "", true
	}
	for _, e := range levels[1:] {
		if s == e {
			return e, true
		}
	}
	return "", false
}
