package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// SettingsStore publishes immutable snapshots only after persistence succeeds.
type SettingsStore struct {
	revision uint64
	mu       sync.RWMutex
	cfg      *config.Config
	save     func(*config.Config) error
}

func NewSettingsStore(cfg *config.Config, save func(*config.Config) error) *SettingsStore {
	return &SettingsStore{cfg: cfg.Snapshot(), save: save, revision: 1}
}

func (s *SettingsStore) Revision() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}

func (s *SettingsStore) Snapshot() *config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Snapshot()
}

type settingsProvider struct {
	CacheCapabilities      ai.CacheCapabilities  `json:"cacheCapabilities"`
	CacheSessionAffinity   *bool                 `json:"cacheSessionAffinity,omitempty"`
	CacheControlFormat     string                `json:"cacheControlFormat,omitempty"`
	ID                     string                `json:"id"`
	Name                   string                `json:"name"`
	Type                   string                `json:"type,omitempty"`
	API                    string                `json:"api"`
	BaseURL                string                `json:"baseUrl"`
	IsFullURL              bool                  `json:"isFullUrl,omitempty"`
	ModelsURL              string                `json:"modelsUrl,omitempty"`
	APIKeyConfigured       bool                  `json:"apiKeyConfigured"`
	CustomHeaders          []config.CustomHeader `json:"customHeaders,omitempty"`
	ModelOrder             []string              `json:"modelOrder,omitempty"`
	ActiveModels           []string              `json:"activeModels"`
	RequestFormat          string                `json:"requestFormat,omitempty"`
	Reasoning              string                `json:"reasoning,omitempty"`
	PromptCachingEnabled   *bool                 `json:"promptCachingEnabled,omitempty"`
	PromptCacheHintMode    string                `json:"promptCacheHintMode,omitempty"`
	PromptCacheRetention   string                `json:"promptCacheRetention,omitempty"`
	NativeWebSearchEnabled bool                  `json:"nativeWebSearchEnabled"`
	UseSystemProxy         bool                  `json:"useSystemProxy,omitempty"`
	RetryPolicy            map[string]any        `json:"retryPolicy,omitempty"`
	UsageQuery             map[string]any        `json:"usageQuery,omitempty"`
	Metadata               map[string]any        `json:"metadata,omitempty"`
	Models                 []settingsModel       `json:"models"`
}
type settingsModel struct {
	Provider        string   `json:"provider"`
	ID              string   `json:"id"`
	Name            string   `json:"name,omitempty"`
	DisplayName     string   `json:"displayName,omitempty"`
	OwnedBy         string   `json:"ownedBy,omitempty"`
	LimitsSource    string   `json:"limitsSource,omitempty"`
	ContextWindow   int      `json:"contextWindow,omitempty"`
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	MaxOutputToken  int      `json:"maxOutputToken,omitempty"`
	InputModalities []string `json:"inputModalities,omitempty"`
	Vision          bool     `json:"vision,omitempty"`
}
type settingsProjection struct {
	Computer        config.ComputerConfig `json:"computer"`
	Version         string                `json:"version"`
	Mode            string                `json:"mode"`
	DefaultModel    string                `json:"defaultModel"`
	DefaultProvider string                `json:"defaultProvider"`
	Providers       []settingsProvider    `json:"providers"`
	Models          []settingsModel       `json:"models"`
}
type settingsModelUpdate struct {
	Provider        string    `json:"provider,omitempty"`
	ID              string    `json:"id"`
	Name            *string   `json:"name,omitempty"`
	DisplayName     *string   `json:"displayName,omitempty"`
	OwnedBy         *string   `json:"ownedBy,omitempty"`
	LimitsSource    *string   `json:"limitsSource,omitempty"`
	ContextWindow   *int      `json:"contextWindow,omitempty"`
	MaxOutputTokens *int      `json:"maxOutputTokens,omitempty"`
	MaxOutputToken  *int      `json:"maxOutputToken,omitempty"`
	InputModalities *[]string `json:"inputModalities,omitempty"`
	Vision          *bool     `json:"vision,omitempty"`
}
type settingsProviderUpdate struct {
	CacheCapabilities      *ai.CacheCapabilities `json:"cacheCapabilities,omitempty"`
	CacheSessionAffinity   *bool                 `json:"cacheSessionAffinity,omitempty"`
	CacheControlFormat     *string               `json:"cacheControlFormat,omitempty"`
	ID                     string                `json:"id"`
	Name                   string                `json:"name"`
	Type                   string                `json:"type,omitempty"`
	API                    string                `json:"api,omitempty"`
	BaseURL                string                `json:"baseUrl"`
	IsFullURL              *bool                 `json:"isFullUrl,omitempty"`
	ModelsURL              *string               `json:"modelsUrl,omitempty"`
	APIKey                 *string               `json:"apiKey,omitempty"`
	Key                    *string               `json:"key,omitempty"`
	APIKeyConfigured       bool                  `json:"apiKeyConfigured,omitempty"`
	ClearAPIKey            bool                  `json:"clearApiKey,omitempty"`
	CustomHeaders          []config.CustomHeader `json:"customHeaders,omitempty"`
	ModelOrder             []string              `json:"modelOrder,omitempty"`
	ActiveModels           []string              `json:"activeModels,omitempty"`
	RequestFormat          string                `json:"requestFormat,omitempty"`
	Reasoning              string                `json:"reasoning,omitempty"`
	PromptCachingEnabled   *bool                 `json:"promptCachingEnabled,omitempty"`
	PromptCacheHintMode    string                `json:"promptCacheHintMode,omitempty"`
	PromptCacheRetention   string                `json:"promptCacheRetention,omitempty"`
	NativeWebSearchEnabled *bool                 `json:"nativeWebSearchEnabled,omitempty"`
	UseSystemProxy         *bool                 `json:"useSystemProxy,omitempty"`
	RetryPolicy            map[string]any        `json:"retryPolicy,omitempty"`
	UsageQuery             map[string]any        `json:"usageQuery,omitempty"`
	Metadata               map[string]any        `json:"metadata,omitempty"`
	Models                 []settingsModelUpdate `json:"models"`
}
type settingsUpdate struct {
	Computer        *config.ComputerConfig   `json:"computer,omitempty"`
	DefaultModel    *string                  `json:"defaultModel,omitempty"`
	DefaultProvider *string                  `json:"defaultProvider,omitempty"`
	Providers       []settingsProviderUpdate `json:"providers,omitempty"`
	DeleteProviders []string                 `json:"deleteProviders,omitempty"`
}

func publicBaseURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

func projectSettings(cfg *config.Config) settingsProjection {
	out := settingsProjection{Version: protocol.Version, Mode: "kbrain", DefaultModel: cfg.DefaultModel, DefaultProvider: cfg.DefaultProvider, Providers: []settingsProvider{}, Models: []settingsModel{}}
	out.Computer = cfg.Computer
	for id, p := range cfg.Providers {
		pv := settingsProvider{ID: id, Name: p.Name, Type: p.Type, API: p.API, BaseURL: publicBaseURL(p.BaseURL), IsFullURL: p.IsFullURL, ModelsURL: publicModelsURL(p.ModelsURL), APIKeyConfigured: p.APIKey != "", CustomHeaders: publicHeaders(p.CustomHeaders), ModelOrder: slices.Clone(p.ModelOrder), ActiveModels: slices.Clone(p.ActiveModels), RequestFormat: p.RequestFormat, Reasoning: p.Reasoning, PromptCachingEnabled: p.PromptCachingEnabled, PromptCacheHintMode: p.PromptCacheHintMode, PromptCacheRetention: p.PromptCacheRetention, NativeWebSearchEnabled: p.NativeWebSearchEnabled, UseSystemProxy: p.UseSystemProxy, RetryPolicy: p.RetryPolicy, UsageQuery: publicMetadata(p.UsageQuery), Metadata: publicMetadata(p.Metadata), Models: []settingsModel{}}
		pv.CacheCapabilities, pv.CacheSessionAffinity, pv.CacheControlFormat = p.CacheCapabilities, p.CacheSessionAffinity, p.CacheControlFormat
		for modelID, shared := range cfg.Models {
			if !slices.Contains(shared.Providers, id) {
				continue
			}
			m := shared.ForProvider(id)
			mv := settingsModel{Provider: id, ID: modelID, Name: m.Name, DisplayName: m.DisplayName, OwnedBy: m.OwnedBy, LimitsSource: m.LimitsSource, ContextWindow: m.Context, MaxOutputTokens: m.MaxOut, MaxOutputToken: m.MaxOut, InputModalities: slices.Clone(m.InputModalities), Vision: m.Vision}
			// Provider settings must expose disabled models so editing and saving a
			// provider cannot delete them. The top-level catalog remains active-only.
			pv.Models = append(pv.Models, mv)
			if p.ModelActive(modelID) {
				out.Models = append(out.Models, mv)
			}
		}
		if pv.ActiveModels == nil {
			for _, m := range pv.Models {
				pv.ActiveModels = append(pv.ActiveModels, m.ID)
			}
		}
		sort.Slice(pv.Models, func(i, j int) bool { return pv.Models[i].ID < pv.Models[j].ID })
		out.Providers = append(out.Providers, pv)
	}
	sort.Slice(out.Providers, func(i, j int) bool { return out.Providers[i].ID < out.Providers[j].ID })
	sort.Slice(out.Models, func(i, j int) bool {
		if out.Models[i].Provider == out.Models[j].Provider {
			return out.Models[i].ID < out.Models[j].ID
		}
		return out.Models[i].Provider < out.Models[j].Provider
	})
	return out
}

func (s *Server) handleModels(w http.ResponseWriter) {
	if s.settings == nil {
		writeJSON(w, http.StatusOK, map[string]any{"version": protocol.Version, "models": s.models})
		return
	}
	projection := projectSettings(s.settings.Snapshot())
	models := make([]map[string]any, 0, len(projection.Models))
	for _, m := range projection.Models {
		models = append(models, map[string]any{"provider": m.Provider, "model": m.ID, "name": m.Name, "contextWindow": m.ContextWindow, "maxOutputTokens": m.MaxOutputTokens, "vision": m.Vision})
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": protocol.Version, "models": models, "defaultModel": protocol.ModelRef{Provider: projection.DefaultProvider, Model: projection.DefaultModel}})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeJSONError(w, http.StatusNotImplemented, "backend settings are unavailable")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, projectSettings(s.settings.Snapshot()))
		return
	}
	if r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var update settingsUpdate
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&update); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid settings request")
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeJSONError(w, http.StatusBadRequest, "expected one settings object")
		return
	}
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	next := s.settings.cfg.Snapshot()
	if err := applySettings(next, update); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.settings.save == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "settings persistence is unavailable")
		return
	}
	if err := s.settings.save(next); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not persist settings")
		return
	}
	s.settings.cfg = next
	s.settings.revision++
	writeJSON(w, http.StatusOK, projectSettings(next))
}

func applySettings(cfg *config.Config, update settingsUpdate) error {
	if update.Computer != nil {
		computer := *update.Computer
		if computer.Backend != "" && computer.Backend != "cua" && computer.Backend != "legacy" {
			return fmt.Errorf("computer.backend must be cua or legacy")
		}
		if computer.ApprovalPolicy != "" && computer.ApprovalPolicy != "ask" && computer.ApprovalPolicy != "allow" && computer.ApprovalPolicy != "deny" {
			return fmt.Errorf("computer.approvalPolicy must be ask, allow or deny")
		}
		if len(computer.Command) > 0 && strings.TrimSpace(computer.Command[0]) == "" {
			return fmt.Errorf("computer.command executable cannot be empty")
		}
		cfg.Computer = computer
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]config.Provider{}
	}
	if cfg.Models == nil {
		cfg.Models = map[string]config.Model{}
	}
	remove := func(id string) {
		for mid, m := range cfg.Models {
			m.DropProvider(id)
			if len(m.Providers) == 0 {
				delete(cfg.Models, mid)
			} else {
				cfg.Models[mid] = m
			}
		}
	}
	for _, id := range update.DeleteProviders {
		delete(cfg.Providers, id)
		remove(id)
	}
	seen := map[string]bool{}
	for _, in := range update.Providers {
		id := strings.TrimSpace(in.ID)
		if id == "" || seen[id] {
			return fmt.Errorf("provider IDs must be non-empty and unique")
		}
		seen[id] = true
		p, err := updateProvider(cfg.Providers[id], in)
		if err != nil {
			return err
		}
		if in.Models != nil {
			old := maps.Clone(cfg.Models)
			remove(id)
			ids := map[string]bool{}
			for _, item := range in.Models {
				mid := strings.TrimSpace(item.ID)
				if mid == "" || ids[mid] {
					return fmt.Errorf("models require unique IDs")
				}
				ids[mid] = true
				// Start from this provider's current view of the model (or the shared
				// metadata when the provider is new to it) and overlay the update.
				m := old[mid].ForProvider(id)
				if item.Name != nil {
					m.Name = *item.Name
				}
				if item.DisplayName != nil {
					m.DisplayName = *item.DisplayName
				}
				if item.OwnedBy != nil {
					m.OwnedBy = *item.OwnedBy
				}
				if item.LimitsSource != nil {
					m.LimitsSource = *item.LimitsSource
				}
				if item.ContextWindow != nil {
					if *item.ContextWindow < 0 {
						return fmt.Errorf("context window must be non-negative")
					}
					m.Context = *item.ContextWindow
				}
				mo := item.MaxOutputTokens
				if mo == nil {
					mo = item.MaxOutputToken
				}
				if mo != nil {
					if *mo < 0 {
						return fmt.Errorf("max output tokens must be non-negative")
					}
					m.MaxOut = *mo
				}
				if item.InputModalities != nil {
					m.InputModalities = slices.Clone(*item.InputModalities)
					m.Vision = slices.Contains(m.InputModalities, "image")
				}
				if item.Vision != nil {
					m.Vision = *item.Vision
				}
				// Other providers may share this model ID with different metadata;
				// record this provider's variant without touching theirs.
				rec, exists := cfg.Models[mid]
				if !exists {
					rec = m.Metadata()
				}
				rec.ID = mid
				rec.Providers = append(slices.Clone(rec.Providers), id)
				rec.SetProviderMetadata(id, m)
				cfg.Models[mid] = rec
			}
		}
		for _, mid := range p.ActiveModels {
			if m, ok := cfg.Models[mid]; !ok || !slices.Contains(m.Providers, id) {
				return fmt.Errorf("active model is not configured for provider")
			}
		}
		cfg.Providers[id] = p
	}
	if update.DefaultModel != nil {
		cfg.DefaultModel = strings.TrimSpace(*update.DefaultModel)
	}
	if update.DefaultProvider != nil {
		cfg.DefaultProvider = strings.TrimSpace(*update.DefaultProvider)
	}
	projection := projectSettings(cfg)
	if len(projection.Models) == 0 {
		cfg.DefaultModel = ""
		cfg.DefaultProvider = ""
	} else if !slices.ContainsFunc(projection.Models, func(m settingsModel) bool { return m.ID == cfg.DefaultModel && m.Provider == cfg.DefaultProvider }) {
		cfg.DefaultModel = projection.Models[0].ID
		cfg.DefaultProvider = projection.Models[0].Provider
	}
	return nil
}

// Refresh only at the next idle run boundary; SetModel preserves tasks and history.
func (s *Server) refreshSettingsLocked(rt *runtimeSession) error {
	if s.settings == nil {
		return nil
	}
	revision := s.settings.Revision()
	cfg := s.settings.Snapshot()
	m, ok := cfg.Models[rt.agent.ModelName]
	if !ok || !slices.Contains(m.Providers, rt.agent.Provider) {
		return fmt.Errorf("selected model is no longer configured")
	}
	if _, ok := cfg.Providers[rt.agent.Provider]; !ok {
		return fmt.Errorf("selected provider is no longer configured")
	}
	if rt.settingsRevision == revision {
		return nil
	}
	fresh, err := s.factory(context.Background(), rt.agent.WorkingDir, protocol.ModelRef{Provider: rt.agent.Provider, Model: rt.agent.ModelName})
	if err != nil {
		return fmt.Errorf("could not refresh model configuration")
	}
	err = rt.agent.SetModel(agent.ModelConfig{Client: fresh.Client, ID: fresh.Model, Name: rt.agent.ModelName, Provider: rt.agent.Provider, MaxTokens: fresh.MaxTokens, ContextLimit: fresh.ContextLimit, Vision: fresh.Vision, Temperature: fresh.Temperature, TopP: fresh.TopP})
	if err != nil {
		return err
	}
	rt.agent.TaskDefault = fresh.TaskDefault
	rt.agent.CompactClient, rt.agent.CompactModel, rt.agent.CompactProvider = fresh.CompactClient, fresh.CompactModel, fresh.CompactProvider
	rt.settingsRevision = revision
	return nil
}
