package tui

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestAskYN(t *testing.T) {
	cases := []struct {
		input string
		def   bool
		want  bool
	}{
		{"\n", true, true},
		{"\n", false, false},
		{"y\n", false, true},
		{"yes\n", false, true},
		{"Y\n", false, true},
		{"n\n", true, false},
		{"No\n", true, false},
		{"garbage\n\n", true, true},
		{"garbage\nnonsense\n", false, false},
		{"", false, false},
	}
	for _, tc := range cases {
		r := bufio.NewReader(strings.NewReader(tc.input))
		var out bytes.Buffer
		if got := askYN(r, &out, "q?", tc.def); got != tc.want {
			t.Errorf("askYN(%q, def=%v) = %v, want %v", tc.input, tc.def, got, tc.want)
		}
	}
}

func driveWizard(t *testing.T, input string) *config.Config {
	t.Helper()
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)

	cfg := config.Default()
	var output bytes.Buffer
	if err := runSetupWizard(cfg, strings.NewReader(input), &output); err != nil {
		t.Fatalf("runSetupWizard: %v", err)
	}
	for _, removed := range []string{"import", "claude", "codex"} {
		if strings.Contains(strings.ToLower(output.String()), removed) {
			t.Fatalf("setup offers a removed integration: %s", output.String())
		}
	}
	body, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(`"mcpImport"`)) {
		t.Fatal("setup wrote removed import settings")
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	return saved
}

func TestWizardDefaultsOptOut(t *testing.T) {

	saved := driveWizard(t, "\n\n\n")
	if saved.Thinking != nil {
		t.Fatalf("default thinking answer should leave the block absent, got %v", *saved.Thinking)
	}
	if len(saved.Providers) != 1 || saved.Providers["demo"].APIKey != "" {
		t.Fatal("setup must leave the API provider template unchanged")
	}
}

func TestWizardThinkingOn(t *testing.T) {

	saved := driveWizard(t, "y\n")
	if saved.Thinking != nil && !*saved.Thinking {
		t.Fatal("thinking should remain enabled")
	}
}

func TestWizardThinkingOff(t *testing.T) {

	saved := driveWizard(t, "n\nn\nn\n")
	if saved.Thinking == nil || *saved.Thinking {
		t.Fatalf("thinking off should persist as false, got %+v", saved.Thinking)
	}
}
