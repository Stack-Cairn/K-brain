package plugins

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
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
	Command     []string          `json:"command,omitempty"`
	Tools       []Tool            `json:"tools,omitempty"`
	Hooks       map[string]string `json:"hooks,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
	// UI declares static web panels a graphical client may show. They are
	// plain files under the plugin directory, served read-only and rendered in
	// a sandboxed frame; nothing in them runs in K-brain itself.
	UI *UI `json:"ui,omitempty"`
}

// UI is the "ui" section of plugin.json.
type UI struct {
	// Root is the directory, relative to the plugin directory, that holds
	// every file a panel may load; "ui" when empty. Nothing outside it is
	// served.
	Root   string  `json:"root,omitempty"`
	Panels []Panel `json:"panels"`
}

// Panel is one UI panel.
type Panel struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Entry is the panel's HTML page, relative to Root.
	Entry string `json:"entry"`
	// Placement says where a client shows the panel: PlacementSidePane or
	// PlacementSettings.
	Placement string `json:"placement"`
}

const (
	PlacementSidePane = "sidePane"
	PlacementSettings = "settings"
	// MaxPanels bounds the panels one plugin may declare.
	MaxPanels = 8
	// DefaultUIRoot is UI.Root when the manifest leaves it empty.
	DefaultUIRoot = "ui"
)

// UIRoot is the plugin's UI directory relative to its directory.
func (m Manifest) UIRoot() string {
	if m.UI == nil || m.UI.Root == "" {
		return DefaultUIRoot
	}
	return m.UI.Root
}

// CleanRelativePath reports whether p is a slash-separated relative path
// whose segments are all ordinary names: no empty, "." or ".." segments, no
// dot files, backslashes, colons or control characters. Such a path can only
// name something below the directory it is resolved against.
func CleanRelativePath(p string) bool {
	if p == "" || len(p) > 512 {
		return false
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == "" || strings.HasPrefix(segment, ".") {
			return false
		}
		for _, r := range segment {
			if r < 0x20 || r == 0x7f || r == '\\' || r == ':' {
				return false
			}
		}
	}
	return true
}

func validateUI(name string, ui *UI) error {
	if ui == nil {
		return nil
	}
	if ui.Root != "" && !CleanRelativePath(ui.Root) {
		return fmt.Errorf("plugin %q ui.root must be a relative path inside the plugin", name)
	}
	if len(ui.Panels) > MaxPanels {
		return fmt.Errorf("plugin %q declares more than %d ui panels", name, MaxPanels)
	}
	seen := map[string]bool{}
	for _, panel := range ui.Panels {
		if !namePattern.MatchString(panel.ID) || seen[panel.ID] {
			return fmt.Errorf("plugin %q has an invalid or duplicate ui panel id %q", name, panel.ID)
		}
		seen[panel.ID] = true
		if title := strings.TrimSpace(panel.Title); title == "" || utf8.RuneCountInString(title) > 80 {
			return fmt.Errorf("plugin %q ui panel %q needs a title of at most 80 characters", name, panel.ID)
		}
		ext := strings.ToLower(path.Ext(panel.Entry))
		if !CleanRelativePath(panel.Entry) || (ext != ".html" && ext != ".htm") {
			return fmt.Errorf("plugin %q ui panel %q entry must be an .html file inside the ui root", name, panel.ID)
		}
		if panel.Placement != PlacementSidePane && panel.Placement != PlacementSettings {
			return fmt.Errorf("plugin %q ui panel %q placement must be %q or %q", name, panel.ID, PlacementSidePane, PlacementSettings)
		}
	}
	return nil
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
	// Fields this version does not know are ignored, so a plugin written for
	// a newer version (or carrying fields for other tools) still loads; the
	// fields it relies on are validated below.
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&m); err != nil {
		return Plugin{}, fmt.Errorf("plugin.json: %w", err)
	}
	if !namePattern.MatchString(m.Name) {
		return Plugin{}, fmt.Errorf("invalid plugin name %q", m.Name)
	}
	// The command runs the plugin's tools; a plugin with only hooks, a prompt
	// or UI panels needs none.
	if (len(m.Command) == 0 && len(m.Tools) > 0) || (len(m.Command) > 0 && strings.TrimSpace(m.Command[0]) == "") {
		return Plugin{}, fmt.Errorf("plugin %q command is required", m.Name)
	}
	if err := validateUI(m.Name, m.UI); err != nil {
		return Plugin{}, err
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
