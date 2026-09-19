package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLanguageRoundTrip(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	for _, language := range []string{"zh_cn", "zh_tw", "en"} {
		cfg := Default()
		cfg.Language = language
		if err := cfg.Save(); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load()
		if err != nil || loaded.Language != language {
			t.Fatalf("%+v %v", loaded, err)
		}
	}
}

func TestLanguagePreservedWhenProvidersEmpty(t *testing.T) {
	root := t.TempDir()
	t.Setenv("K_BRAIN_HOME", root)
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"language":"zh_cn"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil || cfg.Language != "zh_cn" {
		t.Fatalf("%+v %v", cfg, err)
	}
}
