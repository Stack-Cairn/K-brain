package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

// ProviderImageSource turns the chat provider configuration into image
// services: every provider with an API key becomes one profile, and its image
// models (by name) become the models the workbench can pick. The user keeps a
// single place to manage base URLs and keys.
type ProviderImageSource struct {
	Settings *SettingsStore
	// Resolve expands key expressions such as env references; nil uses the
	// same resolver chat requests use.
	Resolve func(context.Context, string) (string, error)
}

// imageModelName recognises image-generation model ids. Providers do not
// declare output modalities, so the name is the only portable signal.
var imageModelName = regexp.MustCompile(`(?i)(image|imagen|seedream|seededit|dall-?e|banana|flux|midjourney|jimeng|cogview|kolors|wanx|stable-?diffusion|sdxl|\bsd\d)`)

func providerProfileID(providerID string) string {
	sum := sha256.Sum256([]byte("provider:" + providerID))
	return "ip_" + hex.EncodeToString(sum[:8])
}

func providerPlatform(p config.Provider) string {
	if p.API == "google-generative-ai" {
		return imaging.PlatformGoogle
	}
	host := ""
	if u, err := url.Parse(p.BaseURL); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	switch {
	case strings.Contains(host, "rolldek"):
		return imaging.PlatformRollDek
	case strings.Contains(host, "volces.com") || strings.Contains(host, "volcengine"):
		return imaging.PlatformVolcengine
	}
	return imaging.PlatformOpenAI
}

// ImageModels lists a provider's image models: enabled ones first in the
// user's order, then the remaining known ones.
func ImageModels(p config.Provider) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] || !imageModelName.MatchString(id) {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, id := range p.ActiveModels {
		add(id)
	}
	for _, m := range p.Models {
		add(m.ID)
	}
	return out
}

func (s ProviderImageSource) providers() (map[string]config.Provider, uint64) {
	cfg := s.Settings.Snapshot()
	if cfg == nil {
		return nil, 0
	}
	return cfg.Providers, s.Settings.Revision()
}

func (s ProviderImageSource) Current() imaging.ImageProfileSet {
	providers, revision := s.providers()
	ids := make([]string, 0, len(providers))
	for id := range providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	set := imaging.ImageProfileSet{SchemaVersion: imaging.ProfileSchemaVersion, Revision: revision}
	for _, id := range ids {
		p := providers[id]
		models := ImageModels(p)
		if strings.TrimSpace(p.APIKey) == "" || len(models) == 0 {
			continue
		}
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = id
		}
		set.Profiles = append(set.Profiles, imaging.ImageProfile{
			ID: providerProfileID(id), Name: name, Kind: "builtin", Platform: providerPlatform(p),
			BaseURL: strings.TrimRight(p.BaseURL, "/"), Enabled: true, Models: models, SelectedModel: models[0],
			APIKeyConfigured: true, APIProxy: p.UseSystemProxy, ProviderID: id,
		})
	}
	return set
}

func (s ProviderImageSource) provider(profileID string) (config.Provider, bool) {
	providers, _ := s.providers()
	for id, p := range providers {
		if providerProfileID(id) == profileID {
			return p, true
		}
	}
	return config.Provider{}, false
}

func (s ProviderImageSource) Configured(profileID string) bool {
	p, ok := s.provider(profileID)
	return ok && strings.TrimSpace(p.APIKey) != ""
}

func (s ProviderImageSource) resolve(ctx context.Context, expr string) (string, error) {
	if s.Resolve != nil {
		return s.Resolve(ctx, expr)
	}
	return config.ResolveSecretContext(ctx, expr)
}

// ResolverFor reads the key at request time, so a key edited in provider
// settings takes effect on the next generation without a restart.
func (s ProviderImageSource) ResolverFor(profileID string) imaging.SecretResolver {
	return providerKeyResolver{source: s, profileID: profileID}
}

func (s ProviderImageSource) HeadersFor(profileID string) map[string]string {
	p, ok := s.provider(profileID)
	if !ok || len(p.CustomHeaders) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, h := range p.CustomHeaders {
		v, err := s.resolve(context.Background(), h.Value)
		if err == nil && !strings.ContainsAny(v, "\r\n") && strings.TrimSpace(h.Key) != "" {
			out[h.Key] = v
		}
	}
	return out
}

type providerKeyResolver struct {
	source    ProviderImageSource
	profileID string
}

func (r providerKeyResolver) ResolveSecret(ctx context.Context, scope imaging.Scope, _ string) (string, error) {
	if checkImageScope(scope) != nil {
		return "", imageError(imaging.CodeForbidden)
	}
	p, ok := r.source.provider(r.profileID)
	if !ok {
		return "", imageError(imaging.CodeAuthRequired)
	}
	v, err := r.source.resolve(ctx, p.APIKey)
	if err != nil || v == "" || strings.ContainsAny(v, "\r\n") {
		return "", imageError(imaging.CodeAuthRequired)
	}
	return v, nil
}
