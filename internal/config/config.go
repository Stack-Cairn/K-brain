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

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/datapath"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
)

type Provider struct {
	Name                   string               `json:"name,omitempty"`
	Type                   string               `json:"type,omitempty"`
	BaseURL                string               `json:"baseUrl"`
	IsFullURL              bool                 `json:"isFullUrl,omitempty"`
	ModelsURL              string               `json:"modelsUrl,omitempty"`
	API                    string               `json:"api"`
	APIKey                 string               `json:"apiKey"`
	CustomHeaders          []CustomHeader       `json:"customHeaders,omitempty"`
	ActiveModels           []string             `json:"activeModels"`
	ModelOrder             []string             `json:"modelOrder,omitempty"`
	RequestFormat          string               `json:"requestFormat,omitempty"`
	Reasoning              string               `json:"reasoning,omitempty"`
	PromptCacheHintMode    string               `json:"promptCacheHintMode,omitempty"`
	NativeWebSearchEnabled bool                 `json:"nativeWebSearchEnabled,omitempty"`
	UseSystemProxy         bool                 `json:"useSystemProxy,omitempty"`
	Metadata               map[string]any       `json:"metadata,omitempty"`
	PromptCachingEnabled   *bool                `json:"promptCachingEnabled,omitempty"`
	PromptCacheRetention   string               `json:"promptCacheRetention,omitempty"`
	RetryPolicy            map[string]any       `json:"retryPolicy,omitempty"`
	UsageQuery             map[string]any       `json:"usageQuery,omitempty"`
	CacheSessionAffinity   *bool                `json:"cacheSessionAffinity,omitempty"`
	CacheControlFormat     string               `json:"cacheControlFormat,omitempty"`
	CacheCapabilities      ai.CacheCapabilities `json:"cacheCapabilities,omitzero"`

	Models []PiModel `json:"models"`
}

type CustomHeader struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type PiModel struct {
	ID               string   `json:"id"`
	Name             string   `json:"name,omitempty"`
	DisplayName      string   `json:"displayName,omitempty"`
	OwnedBy          string   `json:"ownedBy,omitempty"`
	LimitsSource     string   `json:"limitsSource,omitempty"`
	OutputModalities []string `json:"outputModalities,omitempty"`
	// ImageAdapter explicitly selects an image protocol; "disabled" opts out of discovery.
	ImageAdapter    string          `json:"imageAdapter,omitempty"`
	InputModalities []string        `json:"inputModalities,omitempty"`
	API             string          `json:"api,omitempty"`
	Reasoning       bool            `json:"reasoning,omitempty"`
	Input           []string        `json:"input,omitempty"`
	ContextWindow   int             `json:"contextWindow,omitempty"`
	MaxTokens       int             `json:"maxTokens,omitempty"`
	SamplingParams  *SamplingParams `json:"samplingParams,omitempty"`
}

func (p Provider) ModelActive(id string) bool {
	return p.ActiveModels == nil || slices.Contains(p.ActiveModels, id)
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
	Name             string   `json:"name,omitempty"`
	DisplayName      string   `json:"displayName,omitempty"`
	OwnedBy          string   `json:"ownedBy,omitempty"`
	LimitsSource     string   `json:"limitsSource,omitempty"`
	OutputModalities []string `json:"outputModalities,omitempty"`
	// ImageAdapter explicitly selects an image protocol; "disabled" opts out of discovery.
	ImageAdapter    string   `json:"imageAdapter,omitempty"`
	InputModalities []string `json:"inputModalities,omitempty"`
	Providers       []string `json:"providers"`
	ID              string   `json:"id,omitempty"`

	Context int `json:"context,omitempty"`

	MaxOut int `json:"maxOut,omitempty"`

	Vision bool `json:"vision,omitempty"`

	SamplingParams *SamplingParams `json:"samplingParams,omitempty"`

	// ProviderMetadata holds per-provider metadata that differs from the
	// shared fields above; use ForProvider to read a provider's view of the model.
	ProviderMetadata map[string]Model `json:"providerMetadata,omitempty"`
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
	imageConfigRaw     json.RawMessage
	imageConfigInvalid bool

	// Image is optional. Public JSON is redacted; marshalConfig uses private disk types.
	Image           *imaging.ImageSettings `json:"image,omitempty"`
	Language        string                 `json:"language,omitempty"`
	DefaultModel    string                 `json:"defaultModel"`
	DefaultProvider string                 `json:"defaultProvider,omitempty"`
	DefaultEffort   string                 `json:"defaultEffort,omitempty"`
	CompactModel    string                 `json:"compactModel,omitempty"`
	CompactProvider string                 `json:"compactProvider,omitempty"`
	CompactPct      int                    `json:"compactPct,omitempty"`
	TaskModel       string                 `json:"taskModel,omitempty"`
	TaskProvider    string                 `json:"taskProvider,omitempty"`
	Theme           string                 `json:"theme,omitempty"`
	Mouse           *bool                  `json:"mouse,omitempty"`
	Thinking        *bool                  `json:"thinking,omitempty"`
	CollapsePaste   *bool                  `json:"collapsePaste,omitempty"`
	GoalMaxRounds   int                    `json:"goalMaxRounds,omitempty"`

	WorktreeSubagents *bool `json:"worktreeSubagents,omitempty"`
	MaxRetries        int   `json:"maxRetries,omitempty"`

	Experimental []string            `json:"experimental,omitempty"`
	Providers    map[string]Provider `json:"providers"`
	// ProviderOrder is the user's display order. It is persisted per provider inside
	// metadata (see providerOrderMetadataKey); empty means providers sort by ID.
	ProviderOrder []string `json:"-"`

	allowEmptySave bool
	Models         map[string]Model `json:"-"`

	MCPServers map[string]MCPServer `json:"mcp,omitempty"`

	LSPServers map[string]LSPServer `json:"lsp,omitempty"`

	Browser BrowserConfig `json:"browser,omitzero"`

	// Memory is persisted in the memorySwitchSuffix file, never in config.json; the key is still
	// read so values written by earlier builds of the switch carry over.
	Memory MemoryConfig `json:"memory,omitzero"`
	// memoryDisk is the switch file as of this config's last Load or Save. Save leaves the
	// switch alone when Memory still matches it, so a process saving an unrelated setting
	// from a stale copy never undoes a switch another process set.
	memoryDisk     memoryDiskState
	memoryDiskPath string

	AIApproval AIApprovalConfig  `json:"aiApproval,omitzero"`
	Computer   ComputerConfig    `json:"computer,omitzero"`
	Sandbox    SandboxConfig     `json:"sandbox,omitzero"`
	Hooks      map[string][]Hook `json:"hooks,omitempty"`

	RulesCompat RulesCompatConfig `json:"rulesCompat,omitzero"`
}

// RulesCompatConfig toggles discovery of instruction files written for other agent
// harnesses. Both sources default to on; set a field to false to opt out.
//
//	claude: CLAUDE.md, CLAUDE.local.md, .claude/CLAUDE*.md, ~/.claude/CLAUDE.md, .claude/skills
//	cursor: .cursor/rules/*.md|*.mdc and the legacy .cursorrules
type RulesCompatConfig struct {
	Claude *bool `json:"claude,omitempty"`
	Cursor *bool `json:"cursor,omitempty"`
}

func (r RulesCompatConfig) ClaudeEnabled() bool { return r.Claude == nil || *r.Claude }

func (r RulesCompatConfig) CursorEnabled() bool { return r.Cursor == nil || *r.Cursor }

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

// HookEvents lists the lifecycle events a hooks entry may name.
var HookEvents = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure",
	"PreCompact", "PostCompact", "Stop", "SessionEnd",
}

type Hook struct {
	Command string `json:"command"`
	Shell   string `json:"shell,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

type ComputerConfig struct {
	Backend        string   `json:"backend,omitempty"`
	Command        []string `json:"command,omitempty"`
	ApprovalPolicy string   `json:"approvalPolicy,omitempty"`

	Allow []string `json:"allow,omitempty"`

	Deny []string `json:"deny,omitempty"`

	DefaultDeny *bool `json:"defaultDeny,omitempty"`

	Enabled *bool `json:"enabled,omitempty"`
}

// MemoryConfig is the memory master switch. Off stops memory injection, hidden extraction
// and the agent memory tools; stored memories and the memory management API stay available.
type MemoryConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// IsEnabled defaults to true so configurations written before the switch keep memory on.
func (m MemoryConfig) IsEnabled() bool { return m.Enabled == nil || *m.Enabled }

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
	return datapath.UserDir()
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
		type configFields Config
		wire := struct {
			*configFields
			Image json.RawMessage `json:"image,omitempty"`
		}{configFields: (*configFields)(cfg)}
		if err := decodeConfig(raw, &wire); err != nil {
			return err
		}
		cfg.Image = nil
		cfg.imageConfigRaw = nil
		cfg.imageConfigInvalid = false
		if len(wire.Image) > 0 && !bytes.Equal(bytes.TrimSpace(wire.Image), []byte("null")) {
			image, imageErr := imaging.UnmarshalSettingsConfig(wire.Image)
			if imageErr == nil {
				imageErr = imaging.ValidateSettingsWithProviders(image, func(id string) bool { _, ok := cfg.Providers[id]; return ok })
			}
			if imageErr != nil {
				cfg.imageConfigRaw = bytes.Clone(wire.Image)
				cfg.imageConfigInvalid = true
			} else {
				cfg.Image = &image
			}
		}
	}
	for event, hooks := range cfg.Hooks {
		if strings.TrimSpace(event) == "" {
			return fmt.Errorf("hook event name cannot be empty")
		}
		if !slices.Contains(HookEvents, event) {
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
	if err := cfg.AIApproval.Validate(); err != nil {
		return err
	}
	if cfg.Sandbox.Mode != "" && cfg.Sandbox.Mode != "off" && cfg.Sandbox.Mode != "disabled" && cfg.Sandbox.Mode != "workspace" && cfg.Sandbox.Mode != "strict" {
		return fmt.Errorf("sandbox.mode must be off, workspace, or strict")
	}
	if cfg.Sandbox.Backend != "" && cfg.Sandbox.Backend != "auto" && cfg.Sandbox.Backend != "bwrap" && cfg.Sandbox.Backend != "bubblewrap" && cfg.Sandbox.Backend != "seatbelt" && cfg.Sandbox.Backend != "wsl" && cfg.Sandbox.Backend != "landlock" && cfg.Sandbox.Backend != "low-integrity" {
		return fmt.Errorf("unsupported sandbox.backend %q", cfg.Sandbox.Backend)
	}
	cfg.Models = make(map[string]Model)
	orderIndex := map[string]int{}
	cfg.ProviderOrder = nil
	for _, name := range slices.Sorted(maps.Keys(cfg.Providers)) {
		provider := cfg.Providers[name]
		if err := provider.CacheCapabilities.Validate(); err != nil {
			return fmt.Errorf("provider %q: %w", name, err)
		}
		if index, ok := takeProviderOrder(&provider); ok {
			orderIndex[name] = index
			cfg.ProviderOrder = append(cfg.ProviderOrder, name)
		}
		overrides, err := takeModelOverrides(&provider)
		if err != nil {
			return fmt.Errorf("provider %q: invalid %s: %w", name, modelOverridesMetadataKey, err)
		}
		if provider.CacheControlFormat != "" && provider.CacheControlFormat != "anthropic" {
			return fmt.Errorf("provider %q: unsupported cacheControlFormat", name)
		}
		if provider.API == "" {
			switch provider.Type {
			case "claude_code":
				provider.API = ai.APIMessages
			case "gemini":
				provider.API = ai.APIGemini
			case "xai":
				provider.API = ai.APIResponses
			case "codex":
				provider.API = provider.RequestFormat
				if provider.API == "" {
					provider.API = ai.APIResponses
				}
			case "deepseek":
				provider.API = ai.APIChatCompletions
			}
		}
		if !ai.SupportedAPI(provider.API) {
			return fmt.Errorf("provider %q: unsupported API %q; use openai-completions, openai-responses, anthropic-messages, or google-generative-ai with baseUrl and apiKey", name, provider.API)
		}
		for _, pm := range provider.Models {
			if strings.TrimSpace(pm.ID) == "" {
				return fmt.Errorf("provider %q: each model needs an id", name)
			}
			if !ai.SupportedAPI(pm.API) {
				return fmt.Errorf("model %q: unsupported API %q", pm.ID, pm.API)
			}
			if pm.ContextWindow < 0 || pm.MaxTokens < 0 {
				return fmt.Errorf("model %q: contextWindow and maxTokens must be non-negative", pm.ID)
			}

			shared := metadataFromPiModel(pm)
			meta := shared
			if override, ok := overrides[pm.ID]; ok {
				if override.ContextWindow < 0 || override.MaxTokens < 0 {
					return fmt.Errorf("model %q: contextWindow and maxTokens must be non-negative", pm.ID)
				}
				meta = metadataFromPiModel(override)
			}
			m, exists := cfg.Models[pm.ID]
			if !exists {
				m = shared
				m.ID = pm.ID
			}
			if !slices.Contains(m.Providers, name) {
				m.Providers = append(m.Providers, name)
			}
			// Providers may declare different metadata for a shared model ID; keep
			// each provider's variant instead of rejecting the whole config.
			m.SetProviderMetadata(name, meta)
			cfg.Models[pm.ID] = m
			if cfg.DefaultModel == "" {
				cfg.DefaultModel = pm.ID
			}
		}
		provider.Models = nil
		cfg.Providers[name] = provider
	}
	// Names were collected in ID order, so a stable sort breaks index ties by ID.
	slices.SortStableFunc(cfg.ProviderOrder, func(a, b string) int { return orderIndex[a] - orderIndex[b] })
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
		cfg.loadMemorySwitch(p)
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
				// A memory value left in the empty config by earlier builds still counts.
				if cfg.Memory.Enabled != nil {
					restored.Memory = cfg.Memory
				}
				restored.loadMemorySwitch(p)
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
		// A memory value left in the empty config by earlier builds still counts.
		if cfg.Memory.Enabled != nil {
			def.Memory = cfg.Memory
		}
		def.loadMemorySwitch(p)
		logf("config.load", "no usable .bak; regenerated defaults (%s), keeping %d mcp entries", def.fingerprint(), len(cfg.MCPServers))
		return def, def.Save()
	}
	cfg.loadMemorySwitch(p)
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
	restoreMemory := func() {}
	if c.memoryStateForPath(p).needsWrite(c.Memory) {
		restoreMemory, err = saveMemorySwitch(p, c.Memory)
		if err != nil {
			logf("config.save", "memory switch save failed: %v", err)
			return err
		}
	}
	staging, err := os.OpenFile(p+".tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		restoreMemory()
		logf("config.save", "write tmp failed: %v", err)
		return err
	}
	if err := replaceConfigFile(staging, p, data); err != nil {
		restoreMemory()
		logf("config.save", "atomic save failed: %v", err)
		return err
	}
	if c.allowEmptySave {

		_ = os.Remove(p + ".bak")
		c.allowEmptySave = false
	}
	c.memoryDisk = memoryDiskFor(c.Memory)
	c.memoryDiskPath = canonicalMemoryConfigPath(p)
	return nil
}

func marshalConfig(c *Config) ([]byte, error) {
	wire := *c
	wire.Models = nil
	wire.Memory = MemoryConfig{}
	wire.Providers = piProviders(c)
	var image json.RawMessage
	if c.imageConfigInvalid && c.Image == nil {
		image = bytes.Clone(c.imageConfigRaw)
	}
	if c.Image != nil {
		if err := imaging.ValidateSettingsWithProviders(*c.Image, func(id string) bool { _, ok := c.Providers[id]; return ok }); err != nil {
			return nil, fmt.Errorf("invalid image configuration")
		}
		var err error
		image, err = imaging.MarshalSettingsConfig(*c.Image)
		if err != nil {
			return nil, err
		}
	}
	body, err := json.MarshalIndent(struct {
		*Config
		Image json.RawMessage `json:"image,omitempty"`
	}{Config: &wire, Image: image}, "", "  ")
	if err != nil {
		return nil, err
	}
	var validated Config
	if err := parseConfigJSONC(body, &validated); err != nil {
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
	ordered := len(c.ProviderOrder) > 0
	for index, name := range c.OrderedProviderIDs() {
		provider := c.Providers[name]
		p := provider
		p.Models = make([]PiModel, 0)
		var overrides map[string]PiModel
		for _, modelName := range slices.Sorted(maps.Keys(c.Models)) {
			model := c.Models[modelName]
			if !slices.Contains(model.Providers, name) {
				continue
			}
			id := model.ID
			if id == "" {
				id = modelName
			}
			// Every provider lists the shared metadata so older K-brain builds, which
			// require identical metadata per model ID, can still load this file.
			p.Models = append(p.Models, piModelFor(id, model))
			if _, ok := model.ProviderMetadata[name]; ok {
				if overrides == nil {
					overrides = map[string]PiModel{}
				}
				overrides[id] = piModelFor(id, model.ForProvider(name))
			}
		}
		p.Metadata = withProviderOrder(withModelOverrides(provider.Metadata, overrides), index, ordered)
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
	return p, m.ForProvider(provider), id, nil
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

// ImageConfigUnavailable reports an isolated optional configuration failure.
// The original image document is retained privately for subsequent saves.
func (c *Config) ImageConfigUnavailable() bool {
	return c.imageConfigInvalid && c.Image == nil
}

func (c *Config) Snapshot() *Config {
	snap := *c
	snap.imageConfigRaw = bytes.Clone(c.imageConfigRaw)
	if c.Image != nil {
		image := imaging.CloneSettings(*c.Image)
		snap.Image = &image
	}
	snap.Providers = make(map[string]Provider, len(c.Providers))
	for id, provider := range c.Providers {
		data, _ := json.Marshal(provider)
		var cloned Provider
		_ = json.Unmarshal(data, &cloned)
		snap.Providers[id] = cloned
	}
	snap.ProviderOrder = slices.Clone(c.ProviderOrder)
	snap.Models = make(map[string]Model, len(c.Models))
	for id, model := range c.Models {
		data, _ := json.Marshal(model)
		var cloned Model
		_ = json.Unmarshal(data, &cloned)
		snap.Models[id] = cloned
	}
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
