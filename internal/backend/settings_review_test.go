package backend

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestSettingsClearKeyAndDeleteModel(t *testing.T) {
	cfg := &config.Config{DefaultModel: "m", DefaultProvider: "p", Providers: map[string]config.Provider{"p": {API: "openai-completions", BaseURL: "https://api.example/v1", APIKey: "secret"}}, Models: map[string]config.Model{"m": {ID: "m", Providers: []string{"p"}, Context: 100}}}
	var saved *config.Config
	store := NewSettingsStore(cfg, func(next *config.Config) error { saved = next.Snapshot(); return nil })
	server := &Server{settings: store}
	clear := httptest.NewRecorder()
	server.ServeHTTP(clear, httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(`{"providers":[{"id":"p","api":"openai-completions","baseUrl":"https://api.example/v1","clearApiKey":true,"models":[]}]}`)))
	if clear.Code != http.StatusOK || saved.Providers["p"].APIKey != "" || len(saved.Models) != 0 {
		t.Fatalf("clear/delete = %d %#v", clear.Code, saved)
	}
	if strings.Contains(clear.Body.String(), "secret") {
		t.Fatal("key leaked")
	}
}

func TestSettingsBlankKeyRetainsKey(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.Provider{"p": {API: "openai-completions", BaseURL: "https://api.example/v1", APIKey: "secret"}}, Models: map[string]config.Model{"m": {ID: "m", Providers: []string{"p"}}}}
	var saved *config.Config
	store := NewSettingsStore(cfg, func(next *config.Config) error { saved = next.Snapshot(); return nil })
	server := &Server{settings: store}
	req := httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(`{"providers":[{"id":"p","api":"openai-completions","baseUrl":"https://api.example/v1","models":[{"id":"m"}]}]}`))
	out := httptest.NewRecorder()
	server.ServeHTTP(out, req)
	if out.Code != http.StatusOK || saved.Providers["p"].APIKey != "secret" {
		t.Fatalf("blank key cleared: %d %#v", out.Code, saved)
	}
}

func putSettings(t *testing.T, server *httptest.Server, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, server.URL+"/v1/settings", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	out, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatal(err)
	}
	return out.StatusCode, string(data)
}

func providerModel(t *testing.T, projection settingsProjection, provider, id string) settingsModel {
	t.Helper()
	for _, p := range projection.Providers {
		if p.ID != provider {
			continue
		}
		for _, m := range p.Models {
			if m.ID == id {
				return m
			}
		}
	}
	t.Fatalf("model %s/%s not projected", provider, id)
	return settingsModel{}
}

func TestSettingsAcceptsPerProviderSharedModelMetadata(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.Provider{"a": {API: "openai-completions", BaseURL: "https://a.invalid/v1"}, "b": {API: "openai-completions", BaseURL: "https://b.invalid/v1"}}, Models: map[string]config.Model{"m": {ID: "m", Providers: []string{"a", "b"}, Context: 100}}}
	var saved *config.Config
	store := NewSettingsStore(cfg, func(next *config.Config) error { saved = next.Snapshot(); return nil })
	server := httptest.NewServer(&Server{settings: store})
	defer server.Close()
	if status, body := putSettings(t, server, `{"providers":[{"id":"a","api":"openai-completions","baseUrl":"https://a.invalid/v1","models":[{"id":"m","contextWindow":200,"maxOutputTokens":30,"inputModalities":["text","image"]}]}]}`); status != http.StatusOK {
		t.Fatalf("update = %d %s", status, body)
	}
	got := saved.Models["m"]
	if !slices.Equal(got.Providers, []string{"b", "a"}) {
		t.Fatalf("providers = %v", got.Providers)
	}
	if a := got.ForProvider("a"); a.Context != 200 || a.MaxOut != 30 || !a.Vision {
		t.Fatalf("a = %+v", a)
	}
	if b := got.ForProvider("b"); b.Context != 100 || b.MaxOut != 0 || b.Vision {
		t.Fatalf("b changed: %+v", b)
	}
	projection := projectSettings(saved)
	if a := providerModel(t, projection, "a", "m"); a.ContextWindow != 200 || a.MaxOutputTokens != 30 || !a.Vision {
		t.Fatalf("projected a = %+v", a)
	}
	if b := providerModel(t, projection, "b", "m"); b.ContextWindow != 100 || b.MaxOutputTokens != 0 || b.Vision {
		t.Fatalf("projected b = %+v", b)
	}
}

func TestSettingsImportsProviderWithDifferentSharedModelMetadata(t *testing.T) {
	cfg := &config.Config{DefaultModel: "m", DefaultProvider: "a", Providers: map[string]config.Provider{"a": {API: "openai-completions", BaseURL: "https://a.invalid/v1"}}, Models: map[string]config.Model{"m": {ID: "m", Name: "M", Providers: []string{"a"}, Context: 100, MaxOut: 10}}}
	var saved *config.Config
	store := NewSettingsStore(cfg, func(next *config.Config) error { saved = next.Snapshot(); return nil })
	server := httptest.NewServer(&Server{settings: store})
	defer server.Close()
	// Issue #21: adding a second provider that reports different metadata for an
	// existing model ID used to reject the whole provider.
	if status, body := putSettings(t, server, `{"providers":[{"id":"b","api":"openai-completions","baseUrl":"https://b.invalid/v1","models":[{"id":"m","name":"M (b)","contextWindow":400,"maxOutputTokens":40},{"id":"other","contextWindow":5}]}]}`); status != http.StatusOK {
		t.Fatalf("import = %d %s", status, body)
	}
	projection := projectSettings(saved)
	if a := providerModel(t, projection, "a", "m"); a.Name != "M" || a.ContextWindow != 100 || a.MaxOutputTokens != 10 {
		t.Fatalf("projected a = %+v", a)
	}
	if b := providerModel(t, projection, "b", "m"); b.Name != "M (b)" || b.ContextWindow != 400 || b.MaxOutputTokens != 40 {
		t.Fatalf("projected b = %+v", b)
	}
	if other := providerModel(t, projection, "b", "other"); other.ContextWindow != 5 {
		t.Fatalf("projected other = %+v", other)
	}
	_, a, _, err := saved.Resolve("m", "a")
	if err != nil || a.Context != 100 || a.MaxOut != 10 {
		t.Fatalf("resolve a = %+v %v", a, err)
	}
	_, b, _, err := saved.Resolve("m", "b")
	if err != nil || b.Context != 400 || b.MaxOut != 40 {
		t.Fatalf("resolve b = %+v %v", b, err)
	}

	// Removing b drops its override and leaves a untouched.
	if status, body := putSettings(t, server, `{"deleteProviders":["b"]}`); status != http.StatusOK {
		t.Fatalf("delete = %d %s", status, body)
	}
	if m := saved.Models["m"]; m.ProviderMetadata != nil || m.Context != 100 || !slices.Equal(m.Providers, []string{"a"}) {
		t.Fatalf("after delete = %+v", m)
	}
}

func TestSettingsStillRejectsInvalidSharedModelMetadata(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.Provider{"a": {API: "openai-completions", BaseURL: "https://a.invalid/v1"}, "b": {API: "openai-completions", BaseURL: "https://b.invalid/v1"}}, Models: map[string]config.Model{"m": {ID: "m", Providers: []string{"a", "b"}, Context: 100}}}
	var saved *config.Config
	store := NewSettingsStore(cfg, func(next *config.Config) error { saved = next.Snapshot(); return nil })
	server := httptest.NewServer(&Server{settings: store})
	defer server.Close()
	for _, body := range []string{
		`{"providers":[{"id":"a","api":"openai-completions","baseUrl":"https://a.invalid/v1","models":[{"id":"m","contextWindow":-1}]}]}`,
		`{"providers":[{"id":"a","api":"openai-completions","baseUrl":"https://a.invalid/v1","models":[{"id":"m","maxOutputTokens":-1}]}]}`,
		`{"providers":[{"id":"a","api":"openai-completions","baseUrl":"https://a.invalid/v1","models":[{"id":"m"},{"id":"m"}]}]}`,
	} {
		if status, out := putSettings(t, server, body); status != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", body, status, out)
		}
	}
	if saved != nil || !slices.Equal(store.Snapshot().Models["m"].Providers, []string{"a", "b"}) || store.Snapshot().Models["m"].Context != 100 {
		t.Fatalf("rejected update changed shared model: saved=%#v current=%#v", saved, store.Snapshot().Models["m"])
	}
}
