package config

import (
	"maps"
	"reflect"
	"slices"
)

// Metadata returns the provider-independent part of m: everything except the
// provider list, ID and per-provider overrides.
func (m Model) Metadata() Model {
	m.Providers, m.ID, m.ProviderMetadata = nil, "", nil
	return m
}

// ForProvider returns m with the metadata the given provider declared for it.
// Models are keyed globally by ID, so two providers serving the same ID may
// disagree on context window, max output or modalities; the shared record holds
// one variant and ProviderMetadata holds the rest.
func (m Model) ForProvider(provider string) Model {
	override, ok := m.ProviderMetadata[provider]
	if !ok {
		return m
	}
	out := override.Metadata()
	out.Providers, out.ID, out.ProviderMetadata = m.Providers, m.ID, m.ProviderMetadata
	return out
}

// SetProviderMetadata records meta as provider's view of m, storing an override
// only when it differs from the shared metadata.
func (m *Model) SetProviderMetadata(provider string, meta Model) {
	meta = meta.Metadata()
	overrides := maps.Clone(m.ProviderMetadata)
	if reflect.DeepEqual(meta, m.Metadata()) {
		delete(overrides, provider)
	} else {
		if overrides == nil {
			overrides = map[string]Model{}
		}
		overrides[provider] = meta
	}
	if len(overrides) == 0 {
		overrides = nil
	}
	m.ProviderMetadata = overrides
}

// DropProvider removes provider from m. When the remaining providers all carry
// overrides, the first one is promoted to the shared metadata so the record
// stays as small as possible.
func (m *Model) DropProvider(provider string) {
	m.Providers = slices.DeleteFunc(slices.Clone(m.Providers), func(p string) bool { return p == provider })
	overrides := maps.Clone(m.ProviderMetadata)
	delete(overrides, provider)
	m.ProviderMetadata = overrides
	if len(overrides) == 0 {
		m.ProviderMetadata = nil
		return
	}
	for _, p := range m.Providers {
		if _, ok := overrides[p]; !ok {
			return
		}
	}
	if len(m.Providers) == 0 {
		m.ProviderMetadata = nil
		return
	}
	promoted := m.ForProvider(m.Providers[0])
	promoted.ProviderMetadata = nil
	for _, p := range m.Providers {
		promoted.SetProviderMetadata(p, overrides[p])
	}
	*m = promoted
}
