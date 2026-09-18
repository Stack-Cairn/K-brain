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
