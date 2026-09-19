package mcp

import (
	"fmt"
	"hash/fnv"
	"maps"
	"regexp"
	"slices"
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

func FromConfigMap(in map[string]config.MCPServer) map[string]ServerConfig {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]ServerConfig, len(in))
	for name, c := range in {
		var enabled *bool
		if c.Enabled != nil {
			enabled = new(*c.Enabled)
		}
		out[name] = ServerConfig{
			Command:        slices.Clone(c.Command),
			Env:            maps.Clone(c.Env),
			Cwd:            c.Cwd,
			URL:            c.URL,
			Headers:        maps.Clone(c.Headers),
			Enabled:        enabled,
			Note:           c.Note,
			StartupTimeout: c.StartupTimeout,
			ToolTimeout:    c.ToolTimeout,
			Source:         "k-brain",
		}
	}
	return out
}
