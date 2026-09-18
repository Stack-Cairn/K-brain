package routing

import "github.com/Stack-Cairn/K-brain/internal/config"

func SelectionNames(cfg *config.Config, mdl config.Model, modelName, provName string) (string, string) {
	if modelName == "" {
		modelName = cfg.DefaultModel
	}
	if provName == "" {
		provName = cfg.DefaultProvider
		if provName == "" && len(mdl.Providers) > 0 {
			provName = mdl.Providers[0]
		}
	}
	return modelName, provName
}
