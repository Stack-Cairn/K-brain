package mcp

import (
	"fmt"
	"hash/fnv"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

type ServerConfig struct {
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`

	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	Enabled        *bool  `json:"enabled,omitempty"`
	Note           string `json:"note,omitempty"`
	StartupTimeout int    `json:"startupTimeout,omitempty"`
	ToolTimeout    int    `json:"toolTimeout,omitempty"`

	Source string `json:"-"`
}

func (c ServerConfig) Remote() bool { return c.URL != "" }

func (c ServerConfig) Disabled() bool { return c.Enabled != nil && !*c.Enabled }

func (c ServerConfig) StartupTimeoutDuration() time.Duration {
	if c.StartupTimeout > 0 {
		return time.Duration(c.StartupTimeout) * time.Second
	}
	return 30 * time.Second
}

func (c ServerConfig) ToolTimeoutDuration() time.Duration {
	if c.ToolTimeout > 0 {
		return time.Duration(c.ToolTimeout) * time.Second
	}
	return 60 * time.Second
}

func (c ServerConfig) Valid() string {
	switch {
	case c.Remote() && len(c.Command) > 0:
		return "both command and url set"
	case !c.Remote() && len(c.Command) == 0:
		return "neither command nor url set"
	case c.Remote() && c.URL != "" && !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://"):
		return "url must start with http:// or https://"
	}
	return ""
}

var notNameChar = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sanitize(s string) string {
	if s == "" {
		return "_"
	}
	return notNameChar.ReplaceAllString(s, "_")
}

func serverKey(name string) string {
	if name != "" && notNameChar.FindStringIndex(name) == nil && !strings.Contains(name, "__") {
		return name
	}
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(name))
	return fmt.Sprintf("%s_%08x", strings.ReplaceAll(sanitize(name), "_", "-"), sum.Sum32())
}

func ToolName(server, tool string) string {
	return "mcp__" + serverKey(server) + "__" + sanitize(tool)
}

func ParseToolName(name string) (srvKey, tool string, ok bool) {
	rest, found := strings.CutPrefix(name, "mcp__")
	if !found {
		return "", "", false
	}
	srvKey, tool, found = strings.Cut(rest, "__")
	if !found || tool == "" {
		return "", "", false
	}
	return srvKey, tool, true
}

func Merge(kBrain, codex, claude, claudeGlobal map[string]ServerConfig) map[string]ServerConfig {
	out := make(map[string]ServerConfig, len(kBrain)+len(codex)+len(claude)+len(claudeGlobal))
	maps.Copy(out, claudeGlobal)
	maps.Copy(out, claude)
	maps.Copy(out, codex)
	maps.Copy(out, kBrain)
	return out
}

type ImportPolicy struct {
	Claude ImportSourcePolicy
	Codex  ImportSourcePolicy
}

type ImportSourcePolicy struct {
	Enabled bool
	Only    map[string]bool
	Exclude map[string]bool
}

func ImportPolicyFrom(imp *config.MCPImport) ImportPolicy {
	convert := func(s *config.MCPImportSource) ImportSourcePolicy {
		p := ImportSourcePolicy{Enabled: true}
		if s == nil {
			return p
		}
		if s.Enabled != nil {
			p.Enabled = *s.Enabled
		}
		if len(s.Only) > 0 {
			p.Only = make(map[string]bool, len(s.Only))
			for _, n := range s.Only {
				p.Only[n] = true
			}
		}
		if len(s.Exclude) > 0 {
			p.Exclude = make(map[string]bool, len(s.Exclude))
			for _, n := range s.Exclude {
				p.Exclude[n] = true
			}
		}
		return p
	}
	if imp == nil {
		return ImportPolicy{Claude: convert(nil), Codex: convert(nil)}
	}
	return ImportPolicy{Claude: convert(imp.Claude), Codex: convert(imp.Codex)}
}

func (p ImportSourcePolicy) Admits(name string) bool {
	if !p.Enabled {
		return false
	}
	if p.Exclude[name] {
		return false
	}
	if len(p.Only) > 0 && !p.Only[name] {
		return false
	}
	return true
}

type Filtered struct {
	Merged  map[string]ServerConfig
	Blocked map[string]ServerConfig
	Sources map[string]string
	Errs    map[string]error
}

func setSource(src map[string]ServerConfig, path string) {
	for name, c := range src {
		c.Source = path
		src[name] = c
	}
}

func LoadMergedFiltered(cwd string, kBrainCfg map[string]ServerConfig, policy ImportPolicy) Filtered {
	errs := map[string]error{}
	claudeGlobalPath := ClaudeGlobalPath()
	claudeGlobal, err := LoadClaude(claudeGlobalPath)
	if err != nil && !os.IsNotExist(err) {
		errs[claudeGlobalPath] = err
	}
	claudePath := filepath.Join(cwd, ".mcp.json")
	claude, err := LoadClaude(claudePath)
	if err != nil && !os.IsNotExist(err) {
		errs[claudePath] = err
	}
	codexPath := CodexPath()
	codex, err := LoadCodex(codexPath)
	if err != nil && !os.IsNotExist(err) {
		errs[codexPath] = err
	}
	setSource(claudeGlobal, claudeGlobalPath)
	setSource(claude, claudePath)
	setSource(codex, codexPath)
	setSource(kBrainCfg, kBrainConfigPath())
	blocked := map[string]ServerConfig{}
	split := func(src map[string]ServerConfig, p ImportSourcePolicy) map[string]ServerConfig {
		kept := make(map[string]ServerConfig, len(src))
		for name, c := range src {
			if !p.Admits(name) {
				if _, owned := kBrainCfg[name]; !owned {
					off := false
					c.Enabled = &off
					if c.Note != "" {
						c.Note = "blocked by mcpImport config — " + c.Note
					} else {
						c.Note = "blocked by mcpImport config"
					}
					blocked[name] = c
				}
				continue
			}
			kept[name] = c
		}
		return kept
	}
	claudeGlobalKept := split(claudeGlobal, policy.Claude)
	claudeKept := split(claude, policy.Claude)
	codexKept := split(codex, policy.Codex)
	sources := make(map[string]string, len(kBrainCfg)+len(codex)+len(claude)+len(claudeGlobal))
	for name := range kBrainCfg {
		sources[name] = "k-brain"
	}
	for name := range claudeGlobal {
		sources[name] = "~/.claude.json"
	}
	for name := range claude {
		sources[name] = ".mcp.json"
	}
	for name := range codex {
		sources[name] = "codex"
	}
	return Filtered{
		Merged:  Merge(kBrainCfg, codexKept, claudeKept, claudeGlobalKept),
		Blocked: blocked,
		Sources: sources,
		Errs:    errs,
	}
}

func LoadMerged(cwd string, kBrainCfg map[string]ServerConfig) (map[string]ServerConfig, map[string]error) {
	_ = cwd
	return FromConfigured(kBrainCfg), nil
}

func FromConfigured(kBrainCfg map[string]ServerConfig) map[string]ServerConfig {
	merged := make(map[string]ServerConfig, len(kBrainCfg))
	for name, server := range kBrainCfg {
		server.Source = "k-brain"
		merged[name] = server
	}
	return merged
}

func LoadConfigured(kBrainCfg map[string]ServerConfig) Filtered {
	sources := make(map[string]string, len(kBrainCfg))
	for name := range kBrainCfg {
		sources[name] = "k-brain"
	}
	return Filtered{Merged: FromConfigured(kBrainCfg), Sources: sources}
}

var CodexPath = defaultCodexPath

var ClaudeGlobalPath = defaultClaudeGlobalPath

func kBrainConfigPath() string {
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "config.json")
}

func FromConfigMap(in map[string]config.MCPServer) map[string]ServerConfig {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]ServerConfig, len(in))
	for name, c := range in {
		out[name] = ServerConfig{
			Command:        c.Command,
			Env:            c.Env,
			Cwd:            c.Cwd,
			URL:            c.URL,
			Headers:        c.Headers,
			Enabled:        c.Enabled,
			Note:           c.Note,
			StartupTimeout: c.StartupTimeout,
			ToolTimeout:    c.ToolTimeout,
		}
	}
	return out
}

func defaultCodexPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "config.toml")
}

func defaultClaudeGlobalPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude.json")
}
