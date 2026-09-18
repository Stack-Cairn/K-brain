package mcp

import (
	"encoding/json"
	"fmt"
	"os"
)

type claudeFile struct {
	MCPServers map[string]claudeServer `json:"mcpServers"`
}

type claudeServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Enabled *bool             `json:"enabled"`
	Timeout int               `json:"timeout"`
}

func ParseClaude(data []byte) (map[string]ServerConfig, error) {
	var f claudeFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse .mcp.json: %w", err)
	}
	out := make(map[string]ServerConfig, len(f.MCPServers))
	for name, s := range f.MCPServers {
		c := ServerConfig{
			Env:     s.Env,
			Cwd:     s.Cwd,
			URL:     s.URL,
			Headers: s.Headers,
			Enabled: s.Enabled,
		}
		if s.Command != "" {
			c.Command = append([]string{s.Command}, s.Args...)
		}
		switch s.Type {
		case "sse":
			disabled := false
			c.Enabled = &disabled
			c.Note = "claude sse transport is legacy and unsupported — switch the server to streamable http (type: \"http\")"
		case "http", "streamable-http", "":

		case "stdio":

		default:
			c.Note = fmt.Sprintf("unknown claude transport type %q — assumed from command/url fields", s.Type)
		}
		if s.Timeout > 0 {
			c.StartupTimeout = s.Timeout
			c.ToolTimeout = s.Timeout
		}
		out[name] = c
	}
	return out, nil
}

func LoadClaude(path string) (map[string]ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseClaude(data)
}
