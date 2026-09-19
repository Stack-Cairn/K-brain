package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/sandbox"
)

type Provider struct {
	Name                 string `json:"name,omitempty"`
	BaseURL              string `json:"baseUrl"`
	API                  string `json:"api"`
	APIKey               string `json:"apiKey"`
	PromptCachingEnabled *bool  `json:"promptCachingEnabled,omitempty"`
	PromptCacheRetention string `json:"promptCacheRetention,omitempty"`
	CacheSessionAffinity *bool  `json:"cacheSessionAffinity,omitempty"`
	CacheControlFormat   string `json:"cacheControlFormat,omitempty"`

	Models []PiModel `json:"models"`
}

type PiModel struct {
	ID             string          `json:"id"`
	Name           string          `json:"name,omitempty"`
	API            string          `json:"api,omitempty"`
	Reasoning      bool            `json:"reasoning,omitempty"`
	Input          []string        `json:"input,omitempty"`
	ContextWindow  int             `json:"contextWindow,omitempty"`
	MaxTokens      int             `json:"maxTokens,omitempty"`
	SamplingParams *SamplingParams `json:"samplingParams,omitempty"`
}

func (p Provider) Key() string {
	k, _ := p.ResolveKey()
	return k
}

func (p Provider) ResolveKey() (string, error) {
	return p.ResolveKeyContext(context.Background())
}

func (p Provider) ResolveKeyContext(ctx context.Context) (string, error) {
	if p.APIKey != "" {
		k, err := ResolveSecretContext(ctx, p.APIKey)
		if err != nil {
			return "", fmt.Errorf("provider %q apiKey: %w", p.Name, err)
		}
		return k, nil
	}
	return "", nil
}

type Model struct {
	Name      string   `json:"name,omitempty"`
	Providers []string `json:"providers"`
	ID        string   `json:"id,omitempty"`

	Context int `json:"context,omitempty"`

	MaxOut int `json:"maxOut,omitempty"`

	Vision bool `json:"vision,omitempty"`

	SamplingParams *SamplingParams `json:"samplingParams,omitempty"`
}

type SamplingParams struct {
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
}

func (m Model) ContextWindow() int {
	return m.Context
}

const DefaultCompactModel = "model1"

const DefaultCompactPct = 50

type Config struct {
	Language        string `json:"language,omitempty"`
	DefaultModel    string `json:"defaultModel"`
	DefaultProvider string `json:"defaultProvider,omitempty"`
	DefaultEffort   string `json:"defaultEffort,omitempty"`
	CompactModel    string `json:"compactModel,omitempty"`
	CompactProvider string `json:"compactProvider,omitempty"`
	CompactPct      int    `json:"compactPct,omitempty"`
	TaskModel       string `json:"taskModel,omitempty"`
	TaskProvider    string `json:"taskProvider,omitempty"`
	Theme           string `json:"theme,omitempty"`
	Mouse           *bool  `json:"mouse,omitempty"`
	Thinking        *bool  `json:"thinking,omitempty"`
	CollapsePaste   *bool  `json:"collapsePaste,omitempty"`
	GoalMaxRounds   int    `json:"goalMaxRounds,omitempty"`

	WorktreeSubagents *bool `json:"worktreeSubagents,omitempty"`
	MaxRetries        int   `json:"maxRetries,omitempty"`

	Experimental []string            `json:"experimental,omitempty"`
	Providers    map[string]Provider `json:"providers"`

	allowEmptySave bool
	Models         map[string]Model `json:"-"`

	MCPServers map[string]MCPServer `json:"mcp,omitempty"`

	LSPServers map[string]LSPServer `json:"lsp,omitempty"`

	Browser BrowserConfig `json:"browser,omitzero"`

	Computer ComputerConfig    `json:"computer,omitzero"`
	Sandbox  SandboxConfig     `json:"sandbox,omitzero"`
	Hooks    map[string][]Hook `json:"hooks,omitempty"`
}

type SandboxConfig struct {
	Mode     string   `json:"mode,omitempty"`
	Backend  string   `json:"backend,omitempty"`
	Network  *bool    `json:"network,omitempty"`
	Writable []string `json:"writable,omitempty"`
	ReadOnly []string `json:"readOnly,omitempty"`
}

func (s SandboxConfig) Policy(root string) *sandbox.Policy {
	network := false
	if s.Network != nil {
		network = *s.Network
	}
	if root == "" {
		root = sandbox.RootFromEnv()
	}
	return sandbox.New(s.Mode, s.Backend, root, network, s.Writable, s.ReadOnly)
}

type Hook struct {
	Command string `json:"command"`
	Shell   string `json:"shell,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

type ComputerConfig struct {
	Allow []string `json:"allow,omitempty"`

	Deny []string `json:"deny,omitempty"`

	DefaultDeny *bool `json:"defaultDeny,omitempty"`

	Enabled *bool `json:"enabled,omitempty"`
}

type BrowserConfig struct {
	Mode string `json:"mode,omitempty"`

	CDPURL string `json:"cdpUrl,omitempty"`

	AllowPrivateURLs bool `json:"allowPrivateUrls,omitempty"`

	Enabled *bool `json:"enabled,omitempty"`
}

type LSPServer struct {
	Command     []string          `json:"command,omitempty"`
	Extensions  []string          `json:"extensions,omitempty"`
	RootMarkers []string          `json:"rootMarkers,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
}

type MCPServer struct {
	Command        []string          `json:"command,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	URL            string            `json:"url,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Enabled        *bool             `json:"enabled,omitempty"`
	Note           string            `json:"note,omitempty"`
	StartupTimeout int               `json:"startupTimeout,omitempty"`
	ToolTimeout    int               `json:"toolTimeout,omitempty"`
}

func Dir() (string, error) {
	if d := os.Getenv("K_BRAIN_HOME"); d != "" {
		return d, os.MkdirAll(d, 0o700)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".k-brain")
	return dir, os.MkdirAll(dir, 0o700)
}

func path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func Exists() bool {
	p, err := path()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

func setupDonePath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "setup.done"), nil
}

func SetupDone() bool {
	p, err := setupDonePath()
	if err != nil {
		return true
	}
	_, err = os.Stat(p)
	return err == nil
}

func MarkSetupDone() {
	p, err := setupDonePath()
	if err != nil {
		return
	}
	_ = os.WriteFile(p, []byte("ok\n"), 0o600)
}

func parseConfigJSONC(data []byte, cfg *Config) error {
	stripped, err := stripJSONC(data)
	if err != nil {
		return err
	}
	raw := bytes.TrimSpace(stripped)
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return err
	}
	if root == nil {
		return fmt.Errorf("config must be a Pi-style JSON object")
	}
	if _, ok := root["providers"]; !ok && looksLikePiProvider(root) {
		var provider Provider
		if err := decodeConfig(raw, &provider); err != nil {
			return err
		}
		name := strings.TrimSpace(provider.Name)
		if name == "" {
			name = "default"
		}
		*cfg = Config{Providers: map[string]Provider{name: provider}}
	} else {
		if _, legacy := root["models"]; legacy {
			return fmt.Errorf("top-level models is not supported; put models arrays inside providers")
		}
		if err := decodeConfig(raw, cfg); err != nil {
			return err
		}
	}
	for event, hooks := range cfg.Hooks {
		if strings.TrimSpace(event) == "" {
			return fmt.Errorf("hook event name cannot be empty")
		}
		switch event {
		case "SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop":
		default:
			return fmt.Errorf("unsupported hook event %q", event)
		}
		for i, hook := range hooks {
			if strings.TrimSpace(hook.Command) == "" {
				return fmt.Errorf("hook %s[%d] command cannot be empty", event, i)
			}
			if hook.Timeout < 0 {
				return fmt.Errorf("hook %s[%d] timeout must be non-negative", event, i)
			}
		}
	}
	if cfg.Sandbox.Mode != "" && cfg.Sandbox.Mode != "off" && cfg.Sandbox.Mode != "disabled" && cfg.Sandbox.Mode != "workspace" && cfg.Sandbox.Mode != "strict" {
		return fmt.Errorf("sandbox.mode must be off, workspace, or strict")
	}
	if cfg.Sandbox.Backend != "" && cfg.Sandbox.Backend != "auto" && cfg.Sandbox.Backend != "bwrap" && cfg.Sandbox.Backend != "bubblewrap" && cfg.Sandbox.Backend != "seatbelt" && cfg.Sandbox.Backend != "wsl" {
		return fmt.Errorf("unsupported sandbox.backend %q", cfg.Sandbox.Backend)
	}
	cfg.Models = make(map[string]Model)
	for _, name := range slices.Sorted(maps.Keys(cfg.Providers)) {
		provider := cfg.Providers[name]
		if provider.API != "" && provider.API != "openai-completions" && provider.API != "openai-responses" && provider.API != "anthropic-messages" {
			return fmt.Errorf("provider %q: unsupported API %q; use openai-completions, openai-responses, or anthropic-messages with baseUrl and apiKey", name, provider.API)
		}
		for _, pm := range provider.Models {
			if strings.TrimSpace(pm.ID) == "" {
				return fmt.Errorf("provider %q: each model needs an id", name)
			}
			if pm.API != "" && pm.API != "openai-completions" && pm.API != "openai-responses" && pm.API != "anthropic-messages" {
				return fmt.Errorf("model %q: unsupported API %q", pm.ID, pm.API)
			}
			if pm.ContextWindow < 0 || pm.MaxTokens < 0 {
				return fmt.Errorf("model %q: contextWindow and maxTokens must be non-negative", pm.ID)
			}
			m, exists := cfg.Models[pm.ID]
			if !exists {
				m = Model{ID: pm.ID, Name: pm.Name, Context: pm.ContextWindow,
					MaxOut: pm.MaxTokens, Vision: slices.Contains(pm.Input, "image"),
					SamplingParams: pm.SamplingParams}
			}
			if !slices.Contains(m.Providers, name) {
				m.Providers = append(m.Providers, name)
			}
			cfg.Models[pm.ID] = m
			if cfg.DefaultModel == "" {
				cfg.DefaultModel = pm.ID
			}
		}
		provider.Models = nil
		cfg.Providers[name] = provider
	}
	return nil
}

func decodeConfig(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func looksLikePiProvider(v map[string]json.RawMessage) bool {
	for _, key := range []string{"name", "api", "baseUrl", "apiKey"} {
		if _, ok := v[key]; ok {
			return true
		}
	}
	return false
}

func (c *Config) fingerprint() string {
	return fmt.Sprintf("providers=%d models=%d default=%q compact=%q",
		len(c.Providers), len(c.Models), c.DefaultModel, c.CompactModel)
}

func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		cfg := Default()
		logf("config.load", "missing file, writing defaults (%s)", cfg.fingerprint())
		return cfg, cfg.Save()
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := parseConfigJSONC(data, &cfg); err != nil {
		logf("config.load", "PARSE FAILURE %s: %v (%d bytes)", p, err, len(data))
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}

	if len(cfg.Providers) == 0 && len(cfg.Models) == 0 {
		logf("config.load", "CLOBBERED/EMPTY config detected (%d bytes on disk), attempting recovery", len(data))
		if bak, err := os.ReadFile(p + ".bak"); err == nil {
			var restored Config
			if parseConfigJSONC(bak, &restored) == nil && (len(restored.Providers) > 0 || len(restored.Models) > 0) {
				logf("config.load", "restored from .bak (%s)", restored.fingerprint())
				if len(restored.MCPServers) == 0 && len(cfg.MCPServers) > 0 {
					restored.MCPServers = cfg.MCPServers
				}
				if cfg.Language != "" {
					restored.Language = cfg.Language
				}
				return &restored, restored.Save()
			}
		}
		def := Default()
		if cfg.Language != "" {
			def.Language = cfg.Language
		}
		def.MCPServers = cfg.MCPServers
		logf("config.load", "no usable .bak; regenerated defaults (%s), keeping %d mcp entries", def.fingerprint(), len(cfg.MCPServers))
		return def, def.Save()
	}
	logf("config.load", "ok (%s)", cfg.fingerprint())
	return &cfg, nil
}

func (c *Config) Save() error {
	p, err := path()
	if err != nil {
		return err
	}
	if len(c.Providers) == 0 && len(c.Models) == 0 && !c.allowEmptySave {
		if existing, err := os.ReadFile(p); err == nil {
			var cur Config
			if parseConfigJSONC(existing, &cur) == nil && (len(cur.Providers) > 0 || len(cur.Models) > 0) {
				logf("config.save", "REFUSED empty overwrite of healthy config (disk had providers=%d models=%d)", len(cur.Providers), len(cur.Models))
				return fmt.Errorf("refusing to overwrite %s: existing config has providers/models but the value being saved is empty", p)
			}
		}
	}
	data, err := marshalConfig(c)
	if err != nil {
		return err
	}

	if existing, err := os.ReadFile(p); err == nil && len(existing) > 0 {
		var cur Config
		if parseConfigJSONC(existing, &cur) == nil {
			logf("config.save", "before=(%s) after=(%s)", cur.fingerprint(), c.fingerprint())
		} else {
			logf("config.save", "before=(unparseable, %d bytes) after=(%s)", len(existing), c.fingerprint())
		}

		_ = os.WriteFile(p+".bak", existing, 0o600)
	} else {
		logf("config.save", "first write (%s)", c.fingerprint())
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		logf("config.save", "write tmp failed: %v", err)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		logf("config.save", "rename failed: %v", err)
		return err
	}
	if c.allowEmptySave {

		_ = os.Remove(p + ".bak")
		c.allowEmptySave = false
	}
	return nil
}

func marshalConfig(c *Config) ([]byte, error) {

	wire := *c
	wire.Models = nil
	wire.Providers = piProviders(c)
	body, err := json.MarshalIndent(&wire, "", "  ")
	if err != nil {
		return nil, err
	}
	header := "// k-brain configuration — JSONC: comments and trailing commas are allowed.\n" +
		"// providers use Pi's provider shape: name, api, models, baseUrl, and apiKey.\n" +
		"// defaultModel/defaultProvider pick a configured model route.\n"
	out := append([]byte(header), body...)
	return append(out, '\n'), nil
}

func piProviders(c *Config) map[string]Provider {
	providers := make(map[string]Provider, len(c.Providers))
	for name, provider := range c.Providers {
		p := provider
		p.Models = make([]PiModel, 0)
		for _, modelName := range slices.Sorted(maps.Keys(c.Models)) {
			model := c.Models[modelName]
			if !slices.Contains(model.Providers, name) {
				continue
			}
			id := model.ID
			if id == "" {
				id = modelName
			}
			pm := PiModel{ID: id, Name: model.Name, ContextWindow: model.ContextWindow(),
				MaxTokens: model.MaxOut, SamplingParams: model.SamplingParams}
			if model.Vision {
				pm.Input = []string{"text", "image"}
			}
			p.Models = append(p.Models, pm)
		}
		providers[name] = p
	}
	return providers
}

func (c *Config) Resolve(model, provider string) (Provider, Model, string, error) {
	if model == "" {
		model = c.DefaultModel
	}
	m, ok := c.Models[model]
	if !ok {

		var err error
		m, provider, err = c.resolveFromCatalog(model, provider)
		if err != nil {
			return Provider{}, Model{}, "", err
		}
	}
	if provider == "" {
		provider = c.DefaultProvider
	}
	if provider == "" && len(m.Providers) > 0 {
		provider = m.Providers[0]
	}
	p, ok := c.Providers[provider]
	if !ok {
		return Provider{}, Model{}, "", fmt.Errorf("unknown provider %q (providers: %s)", provider, keys(c.Providers))
	}
	id := m.ID
	if id == "" {
		id = model
	}
	return p, m, id, nil
}

type UnknownModelError struct {
	Model string
	known string
}

func (e *UnknownModelError) Error() string {
	return fmt.Sprintf("unknown model %q (configured: %s; catalog models are listed by /model)", e.Model, e.known)
}

func (c *Config) resolveFromCatalog(model, provider string) (Model, string, error) {
	type hit struct {
		prov string
		mi   *ModelInfoLite
	}
	var hits []hit
	for name, cat := range LoadCatalogs() {
		if provider != "" && name != provider {
			continue
		}
		if _, ok := c.Providers[name]; !ok {
			continue
		}
		if mi := cat.Find(model); mi != nil {
			hits = append(hits, hit{name, mi})
		}
	}
	if len(hits) == 0 {
		return Model{}, "", &UnknownModelError{Model: model, known: keys(c.Models)}
	}
	if len(hits) > 1 {

		for _, h := range hits {
			if h.prov == c.DefaultProvider {
				hits = []hit{h}
				break
			}
		}
	}
	if len(hits) > 1 {
		names := make([]string, len(hits))
		for i, h := range hits {
			names[i] = h.prov
		}
		return Model{}, "", fmt.Errorf("model %q is advertised by multiple providers (%s); pass a provider to disambiguate (-p / /model %s <provider>)",
			model, strings.Join(names, ", "), model)
	}
	h := hits[0]
	m := Model{
		Providers: []string{h.prov},
		ID:        model,
		Context:   h.mi.ContextLength,
		MaxOut:    h.mi.MaxCompletionTokens,
	}
	if slices.Contains(h.mi.InputModalities, "image") {
		m.Vision = true
	}
	return m, h.prov, nil
}

func (c *Config) Snapshot() *Config {
	snap := *c
	snap.Providers = make(map[string]Provider, len(c.Providers))
	maps.Copy(snap.Providers, c.Providers)
	snap.Models = make(map[string]Model, len(c.Models))
	maps.Copy(snap.Models, c.Models)
	return &snap
}

func keys[V any](m map[string]V) string {
	s := ""
	for k := range m {
		if s != "" {
			s += ", "
		}
		s += k
	}
	return s
}

func Default() *Config {
	return &Config{
		Language:     "en",
		DefaultModel: "model1",
		CompactModel: DefaultCompactModel,
		Providers: map[string]Provider{
			"demo": {
				Name:    "demo",
				API:     "openai-completions",
				BaseURL: "",
				APIKey:  "",
			},
		},
		Models: map[string]Model{
			"model1": {Providers: []string{"demo"}, Context: 128000, MaxOut: 8192},
			"model2": {Providers: []string{"demo"}, Context: 128000, MaxOut: 8192},
		},
	}
}
