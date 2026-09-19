package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	if Trusted(first) {
		t.Fatal("should not be trusted initially")
	}
	if err := Trust(first); err != nil {
		t.Fatal(err)
	}
	if !Trusted(first) || Trusted(second) {
		t.Fatal("trust must be scoped to one project")
	}
	if !Trusted(filepath.Join(first, "nested")) {
		t.Fatal("a trusted project should cover its descendants")
	}
	data, err := os.ReadFile(filepath.Join(home, "trusted_folders.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "[folders.") || !strings.Contains(text, "trusted = true") || !strings.Contains(text, "decided_at = ") {
		t.Fatalf("unexpected trust TOML:\n%s", text)
	}
	if strings.Contains(text, "trusted.json") {
		t.Fatal("legacy JSON trust format must not be written")
	}
}

func TestTrustDoesNotMergeProjects(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	first, second := filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "second")
	if err := Trust(first); err != nil {
		t.Fatal(err)
	}
	if err := Trust(second); err != nil {
		t.Fatal(err)
	}
	if !Trusted(first) || !Trusted(second) {
		t.Fatal("both project records should remain trusted")
	}
}
