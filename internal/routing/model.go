package routing

import (
	"slices"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func (r Route) AgentModel() agent.ModelConfig {
	m := agent.ModelConfig{Client: r.Client, ID: r.APIModel, Name: r.ModelName, Provider: r.ProviderName, MaxTokens: r.MaxOutput, ContextLimit: r.ContextLimit}
	m.Vision = r.Vision
	m.NativeWebSearch = r.Provider.NativeWebSearchEnabled
	if sp := r.Model.SamplingParams; sp != nil {
		m.Temperature, m.TopP = sp.Temperature, sp.TopP
	}
	return m
}

// SupportsVision reports whether the model accepts image input on provider.
//
// Input modalities stored in the configuration are a deliberate setting (the
// model editor's override, or what the desktop host pushed), and they win over
// the provider's /models catalog: relays often list a vision model as text-only,
// and that override exists precisely to correct them. Without stored
// modalities the catalog decides, then the configured vision flag.
func SupportsVision(cfg *config.Config, modelName, modelID string, catalogs map[string]config.Catalog, provider string) bool {
	var configured config.Model
	if cfg != nil {
		configured = cfg.Models[modelName].ForProvider(provider)
	}
	if len(configured.InputModalities) > 0 {
		return slices.Contains(configured.InputModalities, "image")
	}
	if vision, found := catalogs[provider].SupportsVision(modelID); found {
		return vision
	}
	return configured.Vision
}
