package routing

import (
	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func (r Route) AgentModel() agent.ModelConfig {
	m := agent.ModelConfig{Client: r.Client, ID: r.APIModel, Name: r.ModelName, Provider: r.ProviderName, MaxTokens: r.MaxOutput, ContextLimit: r.ContextLimit}
	m.Vision = r.Vision
	if sp := r.Model.SamplingParams; sp != nil {
		m.Temperature, m.TopP = sp.Temperature, sp.TopP
	}
	return m
}

func SupportsVision(cfg *config.Config, modelName, modelID string, catalogs map[string]config.Catalog, provider string) bool {
	if vision, found := catalogs[provider].SupportsVision(modelID); found {
		return vision
	}
	return cfg != nil && cfg.Models[modelName].Vision
}
