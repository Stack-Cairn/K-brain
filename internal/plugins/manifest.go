package plugins

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type Manifest struct {
	Name        string            `json:"name"`
	Version     string            `json:"version,omitempty"`
	Description string            `json:"description,omitempty"`
	Prompt      string            `json:"prompt,omitempty"`
	Command     []string          `json:"command"`
	Tools       []Tool            `json:"tools,omitempty"`
	Hooks       map[string]string `json:"hooks,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
}

type Plugin struct {
	Manifest
	Dir     string
	Enabled bool
	Err     error
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func Load(dir string) (Plugin, error) {
	data, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return Plugin{}, err
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Plugin{}, fmt.Errorf("plugin.json: %w", err)
	}
	if !namePattern.MatchString(m.Name) {
		return Plugin{}, fmt.Errorf("invalid plugin name %q", m.Name)
	}
	if len(m.Command) == 0 || strings.TrimSpace(m.Command[0]) == "" {
		return Plugin{}, fmt.Errorf("plugin %q command is required", m.Name)
	}
	for _, t := range m.Tools {
		if !namePattern.MatchString(t.Name) {
			return Plugin{}, fmt.Errorf("plugin %q has invalid tool name %q", m.Name, t.Name)
		}
		if len(t.InputSchema) == 0 {
			t.InputSchema = json.RawMessage(`{"type":"object"}`)
		}
		var schema any
		if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
			return Plugin{}, fmt.Errorf("plugin %q tool %q inputSchema: %w", m.Name, t.Name, err)
		}
	}
	enabled := false
	if m.Enabled != nil {
		enabled = *m.Enabled
	}
	return Plugin{Manifest: m, Dir: dir, Enabled: enabled}, nil
}
