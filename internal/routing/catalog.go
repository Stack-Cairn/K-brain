package routing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func CatalogModels(infos []ai.ModelInfo) []config.ModelInfoLite {
	lites := make([]config.ModelInfoLite, len(infos))
	for i, mi := range infos {
		lites[i] = config.ModelInfoLite{
			ID:                  mi.ID,
			ContextLength:       mi.ContextLength,
			MaxCompletionTokens: mi.MaxCompletionTokens,
			ReasoningEfforts:    mi.ReasoningEfforts,
			InputModalities:     mi.InputModalities,
		}
		if mi.Pricing != nil {
			lites[i].InPrice, lites[i].OutPrice, lites[i].CacheReadPrice = mi.Pricing.Rates()
		}
	}
	return lites
}

func RefreshCatalogs(cfg *config.Config, force bool) map[string]config.Catalog {
	cats := config.LoadCatalogs()
	dirty := false
	for name, prov := range cfg.Providers {
		if strings.TrimSpace(prov.BaseURL) == "" {
			continue
		}
		if c, ok := cats[name]; ok && !force && !c.Stale() && c.BaseURL == prov.BaseURL {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		client, err := ClientForProvider(prov, name, cfg.MaxRetries)
		if err != nil {
			cancel()
			config.LogEvent("catalog.fetch", name+" skipped: "+err.Error())
			continue
		}
		infos, err := client.Models(ctx)
		cancel()
		if err != nil {
			config.LogEvent("catalog.fetch", name+" failed: "+err.Error())
			continue
		}
		config.LogEvent("catalog.fetch", fmt.Sprintf("%s ok: %d models", name, len(infos)))
		cats[name] = config.Catalog{FetchedAt: time.Now(), BaseURL: prov.BaseURL, Models: CatalogModels(infos)}
		dirty = true
	}
	if dirty {
		_ = config.SaveCatalogs(cats)
	}
	return cats
}

func ResolveWithRefresh(cfg *config.Config, modelName, provName string) (config.Provider, config.Model, string, error) {
	prov, mdl, id, err := cfg.Resolve(modelName, provName)
	var unknown *config.UnknownModelError
	if !errors.As(err, &unknown) {
		return prov, mdl, id, err
	}
	config.LogEvent("catalog.fetch", fmt.Sprintf("startup resolve missed %q — force-refreshing catalogs", unknown.Model))
	RefreshCatalogs(cfg, true)
	if prov, mdl, id, rerr := cfg.Resolve(modelName, provName); rerr == nil {
		return prov, mdl, id, nil
	}
	return config.Provider{}, config.Model{}, "", err
}
