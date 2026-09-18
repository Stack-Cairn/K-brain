package routing

import (
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func ResolvedProvider(cfg *config.Config, model, prov string) string {
	if prov != "" {
		return prov
	}
	if mdl := cfg.Models[model]; len(mdl.Providers) > 0 {
		return mdl.Providers[0]
	}
	cats := config.LoadCatalogs()
	for name := range cfg.Providers {
		if cat, ok := cats[name]; ok && cat.Find(model) != nil {
			return name
		}
	}
	return ""
}

func SubModelFor(cfg *config.Config, model, provider string) (agent.SubModel, error) {
	route, err := ResolveRoute(cfg, model, provider, false)
	if err != nil {
		return agent.SubModel{}, err
	}
	return agent.SubModel{Client: route.Client, Model: route.APIModel,
		ContextLimit: route.ContextLimit, MaxTokens: route.MaxOutput}, nil
}

func TaskDefaultFor(cfg *config.Config, provider string) (agent.SubModel, error) {
	tm, explicit := cfg.TaskModel, cfg.TaskModel != ""
	if !explicit {
		tm = config.DefaultTaskModel
	}
	o, err := SubModelFor(cfg, tm, cfg.TaskProvider)
	if err == nil {
		return o, nil
	}
	if explicit {
		return agent.SubModel{}, err
	}
	if id := catalogSuffixMatch(tm); id != "" {
		if o, err2 := SubModelFor(cfg, id, ""); err2 == nil {
			return o, nil
		}
	}
	return agent.SubModel{}, nil
}

func catalogSuffixMatch(name string) string {
	for _, cat := range config.LoadCatalogs() {
		for _, mi := range cat.Models {
			if strings.HasSuffix(mi.ID, "/"+name) {
				return mi.ID
			}
		}
	}
	return ""
}
