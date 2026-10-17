package backend

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func providerSourceFixture(providers map[string]config.Provider) ProviderImageSource {
	cfg := config.Default()
	cfg.Providers = providers
	return ProviderImageSource{Settings: NewSettingsStore(cfg, func(*config.Config) error { return nil })}
}

func TestImageModelsPicksImageModelsOnly(t *testing.T) {
	p := config.Provider{
		ActiveModels: []string{"gpt-5.6", "gpt-image-2.5", "gpt-image-2"},
		Models:       []config.PiModel{{ID: "gemini-3-pro-image"}, {ID: "doubao-seedream-5-0"}, {ID: "claude-x"}, {ID: "gpt-image-2"}},
	}
	got := strings.Join(ImageModels(p), ",")
	if got != "gpt-image-2.5,gpt-image-2,gemini-3-pro-image,doubao-seedream-5-0" {
		t.Fatalf("image models: %s", got)
	}
}

func TestProviderPlatformDetection(t *testing.T) {
	for _, tc := range []struct {
		p    config.Provider
		want string
	}{
		{config.Provider{API: "openai-responses", BaseURL: "https://api-cn.rolldek.com/v1"}, imaging.PlatformRollDek},
		{config.Provider{API: "openai-completions", BaseURL: "https://ark.cn-beijing.volces.com/api/v3"}, imaging.PlatformVolcengine},
		{config.Provider{API: "google-generative-ai", BaseURL: "https://generativelanguage.googleapis.com/v1beta"}, imaging.PlatformGoogle},
		{config.Provider{API: "openai-responses", BaseURL: "https://relay.example/v1"}, imaging.PlatformOpenAI},
	} {
		if got := providerPlatform(tc.p); got != tc.want {
			t.Errorf("%s: got %s want %s", tc.p.BaseURL, got, tc.want)
		}
	}
}

func TestProviderSourceSkipsProvidersWithoutKeyOrImageModels(t *testing.T) {
	s := providerSourceFixture(map[string]config.Provider{
		"rolldek": {Name: "RollDek", API: "openai-responses", BaseURL: "https://api-cn.rolldek.com/v1/", APIKey: "k", ActiveModels: []string{"gpt-image-2"}},
		"nokey":   {Name: "NoKey", BaseURL: "https://a/v1", ActiveModels: []string{"gpt-image-2"}},
		"chat":    {Name: "Chat", BaseURL: "https://b/v1", APIKey: "k", ActiveModels: []string{"gpt-5.6"}},
	})
	set := s.Current()
	if len(set.Profiles) != 1 {
		t.Fatalf("profiles: %+v", set.Profiles)
	}
	p := set.Profiles[0]
	if p.Name != "RollDek" || p.ProviderID != "rolldek" || p.Platform != imaging.PlatformRollDek || p.BaseURL != "https://api-cn.rolldek.com/v1" || !p.APIKeyConfigured {
		t.Fatalf("profile: %+v", p)
	}
	if p.ID != providerProfileID("rolldek") || !strings.HasPrefix(imaging.ProfileTargetID(p.ID, "gpt-image-2"), "it_") {
		t.Fatal("ids not stable")
	}
}

func TestProviderKeyIsResolvedAtRequestTime(t *testing.T) {
	providers := map[string]config.Provider{"p": {Name: "P", BaseURL: "https://a/v1", APIKey: "first", ActiveModels: []string{"gpt-image-2"}, CustomHeaders: []config.CustomHeader{{Key: "X-A", Value: "1"}}}}
	s := providerSourceFixture(providers)
	s.Resolve = func(_ context.Context, expr string) (string, error) { return expr, nil }
	id := providerProfileID("p")
	scope := imaging.Scope{OwnerID: "local", BackendID: "local"}
	if v, err := s.ResolverFor(id).ResolveSecret(context.Background(), scope, "apiKey"); err != nil || v != "first" {
		t.Fatalf("key: %q %v", v, err)
	}
	cfg := s.Settings.Snapshot()
	p := cfg.Providers["p"]
	p.APIKey = "second"
	cfg.Providers["p"] = p
	s.Settings.cfg = cfg
	if v, _ := s.ResolverFor(id).ResolveSecret(context.Background(), scope, "apiKey"); v != "second" {
		t.Fatalf("edited key not picked up: %q", v)
	}
	if h := s.HeadersFor(id); h["X-A"] != "1" {
		t.Fatalf("headers: %v", h)
	}
	if _, err := s.ResolverFor(id).ResolveSecret(context.Background(), imaging.Scope{OwnerID: "x", BackendID: "local"}, "apiKey"); err == nil {
		t.Fatal("foreign scope resolved a key")
	}
}

func TestImageProfilesRouteIsReadOnlyAndHidesKeys(t *testing.T) {
	s := imageHTTPFixture(t)
	s.image.Profiles = NewProviderImageProfiles(providerSourceFixture(map[string]config.Provider{
		"rolldek": {Name: "RollDek", BaseURL: "https://api-cn.rolldek.com/v1", APIKey: "sk-provider-secret", ActiveModels: []string{"gpt-image-2"}},
	}))
	w := imageHTTPRequest(s, "GET", "/v1/image/profiles", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "sk-provider-secret") {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var v struct {
		Profiles []struct {
			ProviderID string `json:"providerId"`
			Targets    []struct {
				ID    string `json:"id"`
				Ready bool   `json:"ready"`
			} `json:"targets"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || len(v.Profiles) != 1 || v.Profiles[0].ProviderID != "rolldek" || !v.Profiles[0].Targets[0].Ready {
		t.Fatalf("decoded: %+v %v", v, err)
	}
	if w := imageHTTPRequest(s, "POST", "/v1/image/profiles", strings.NewReader(`{}`)); w.Code == 200 || w.Code == 201 {
		t.Fatalf("write accepted: %d", w.Code)
	}
}