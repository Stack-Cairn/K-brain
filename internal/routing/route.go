package routing

import (
	"fmt"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

type Route struct {
	ModelName    string
	ProviderName string
	Provider     config.Provider
	Model        config.Model
	APIModel     string
	Client       ai.Client
	ContextLimit int
	MaxOutput    int
}

func ResolveRoute(cfg *config.Config, modelName, providerName string, optional bool) (Route, error) {
	if cfg == nil {
		return Route{}, fmt.Errorf("configuration is nil")
	}
	prov, mdl, apiID, err := ResolveWithRefresh(cfg, modelName, providerName)
	if err != nil {
		return Route{}, err
	}
	modelName, providerName = SelectionNames(cfg, mdl, modelName, providerName)
	if modelName == "" {
		modelName = apiID
	}
	if providerName == "" {
		return Route{}, fmt.Errorf("no provider configured for model %q", modelName)
	}
	var client ai.Client
	if optional {
		client, err = ClientForProviderOptional(prov, providerName, cfg.MaxRetries)
	} else {
		client, err = ClientForProvider(prov, providerName, cfg.MaxRetries)
	}
	if err != nil {
		return Route{}, err
	}
	catalogs := config.LoadCatalogs()
	ctxLimit, maxOut := TokenLimits(mdl, catalogs[providerName], apiID)
	return Route{
		ModelName: modelName, ProviderName: providerName,
		Provider: prov, Model: mdl, APIModel: apiID,
		Client: client, ContextLimit: ctxLimit, MaxOutput: maxOut,
	}, nil
}

func (r Route) Configured() bool {
	key, _ := r.Provider.ResolveKey()
	return strings.TrimSpace(r.Provider.BaseURL) != "" && strings.TrimSpace(key) != ""
}
