package config

import (
	"encoding/json"
	"maps"
	"slices"
)

// modelOverridesMetadataKey stores, inside Provider.Metadata, the models whose metadata this
// provider declares differently from the shared record.
//
// The provider's own "models" list always carries the shared metadata. K-brain builds that
// predate per-provider metadata reject a config whose providers disagree on a shared model ID
// and use DisallowUnknownFields, so the per-provider variants live in the free-form metadata
// map they already accept. An older build then loads the config with the shared values instead
// of refusing to start; this build re-applies the overrides.
const modelOverridesMetadataKey = "kbrainModelMetadataOverrides"

func piModelFor(id string, model Model) PiModel {
	pm := PiModel{
		ID: id, Name: model.Name, DisplayName: model.DisplayName, OwnedBy: model.OwnedBy,
		LimitsSource: model.LimitsSource, InputModalities: model.InputModalities,
		ContextWindow: model.ContextWindow(), MaxTokens: model.MaxOut, SamplingParams: model.SamplingParams,
	}
	if model.Vision {
		pm.Input = []string{"text", "image"}
	}
	return pm
}

func metadataFromPiModel(pm PiModel) Model {
	return Model{
		Name: pm.Name, DisplayName: pm.DisplayName, OwnedBy: pm.OwnedBy, LimitsSource: pm.LimitsSource,
		InputModalities: pm.InputModalities, Context: pm.ContextWindow, MaxOut: pm.MaxTokens,
		Vision:         slices.Contains(pm.Input, "image") || slices.Contains(pm.InputModalities, "image"),
		SamplingParams: pm.SamplingParams,
	}
}

// takeModelOverrides removes the overrides entry from the provider metadata and decodes it.
// The entry is a storage detail, so it is never kept in memory or exposed through settings.
func takeModelOverrides(provider *Provider) (map[string]PiModel, error) {
	raw, ok := provider.Metadata[modelOverridesMetadataKey]
	if !ok {
		return nil, nil
	}
	metadata := maps.Clone(provider.Metadata)
	delete(metadata, modelOverridesMetadataKey)
	if len(metadata) == 0 {
		metadata = nil
	}
	provider.Metadata = metadata
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var overrides map[string]PiModel
	if err := json.Unmarshal(data, &overrides); err != nil {
		return nil, err
	}
	return overrides, nil
}

// withModelOverrides returns the provider metadata to persist, carrying the overrides entry
// when there is at least one override.
func withModelOverrides(metadata map[string]any, overrides map[string]PiModel) map[string]any {
	out := maps.Clone(metadata)
	delete(out, modelOverridesMetadataKey)
	if len(overrides) > 0 {
		if out == nil {
			out = map[string]any{}
		}
		out[modelOverridesMetadataKey] = overrides
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
