package routing

import (
	"context"
	"fmt"

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
	return SubModelForContext(context.Background(), cfg, model, provider)
}

func SubModelForContext(ctx context.Context, cfg *config.Config, model, provider string) (agent.SubModel, error) {
	route, err := ResolveRouteContext(ctx, cfg, model, provider, false)
	if err != nil {
		return agent.SubModel{}, err
	}
	return agent.SubModel{Client: route.Client, Model: route.APIModel, Provider: route.ProviderName,
		ContextLimit: route.ContextLimit, MaxTokens: route.MaxOutput, Vision: route.Vision}, nil
}

func TaskDefaultFor(cfg *config.Config) (agent.SubModel, error) {
	return TaskDefaultForContext(context.Background(), cfg)
}

func TaskDefaultForContext(ctx context.Context, cfg *config.Config) (agent.SubModel, error) {
	if cfg == nil {
		return agent.SubModel{}, fmt.Errorf("configuration is nil")
	}
	if err := ctx.Err(); err != nil {
		return agent.SubModel{}, err
	}
	if cfg.TaskModel == "" {
		return agent.SubModel{}, nil
	}
	return SubModelForContext(ctx, cfg, cfg.TaskModel, cfg.TaskProvider)
}
