package config

import (
	"os"
	"path/filepath"
	"testing"
)

func unusableHome(t *testing.T) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("K_BRAIN_HOME", filepath.Join(f, "k-brain"))
}

func blockedPath(t *testing.T, name, how string) {
	t.Helper()
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if how == "tmp" {
		p += ".tmp"
	}
	if err := os.MkdirAll(filepath.Join(p, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestNoConfigDirIsNotFatal(t *testing.T) {
	unusableHome(t)

	if Trusted("/some/dir") {
		t.Error("Trusted should be false when the config dir is unusable")
	}
	if err := Trust("/some/dir"); err == nil {
		t.Error("Trust should report the unusable config dir")
	}
	if n := ProjectGoalMaxRounds("/some/dir"); n != 0 {
		t.Errorf("ProjectGoalMaxRounds = %d, want 0", n)
	}
	if err := SetProjectGoalMaxRounds("/some/dir", 5); err == nil {
		t.Error("SetProjectGoalMaxRounds should report the unusable config dir")
	}
	if cats := LoadCatalogs(); len(cats) != 0 {
		t.Errorf("LoadCatalogs = %v, want empty", cats)
	}
	if err := SaveCatalogs(map[string]Catalog{"p": {}}); err == nil {
		t.Error("SaveCatalogs should report the unusable config dir")
	}
	var v map[string]any
	if err := ReadJSON("state.json", &v); err == nil {
		t.Error("ReadJSON should report the unusable config dir")
	}
	if err := WriteJSON("state.json", map[string]int{}); err == nil {
		t.Error("WriteJSON should report the unusable config dir")
	}
	if _, err := Load(); err == nil {
		t.Error("Load should report the unusable config dir")
	}
	if err := Default().Save(); err == nil {
		t.Error("Save should report the unusable config dir")
	}
}

func TestTrustedRejectsCorruptFile(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	dir, _ := Dir()
	if err := os.WriteFile(filepath.Join(dir, "trusted.json"), []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if Trusted("/x") {
		t.Fatal("a corrupt trusted.json must not grant trust")
	}

	if err := Trust("/x"); err != nil {
		t.Fatal(err)
	}
	if !Trusted("/x") {
		t.Fatal("Trust should overwrite the corrupt file")
	}
}

func TestTrustReportsWriteFailures(t *testing.T) {
	t.Run("staging write", func(t *testing.T) {
		t.Setenv("K_BRAIN_HOME", t.TempDir())
		blockedPath(t, "trusted.json", "tmp")
		if err := Trust("/x"); err == nil {
			t.Fatal("Trust should report the failed staging write")
		}
	})
	t.Run("rename", func(t *testing.T) {
		t.Setenv("K_BRAIN_HOME", t.TempDir())
		blockedPath(t, "trusted.json", "target")
		if err := Trust("/x"); err == nil {
			t.Fatal("Trust should report the failed rename")
		}
		dir, _ := Dir()
		if _, err := os.Stat(filepath.Join(dir, "trusted.json.tmp")); !os.IsNotExist(err) {
			t.Fatal("a failed rename must clean up its staging file")
		}
	})
}

func TestProjectsReportWriteFailures(t *testing.T) {
	t.Run("staging write", func(t *testing.T) {
		t.Setenv("K_BRAIN_HOME", t.TempDir())
		blockedPath(t, "projects.json", "tmp")
		if err := SetProjectGoalMaxRounds("/x", 7); err == nil {
			t.Fatal("SetProjectGoalMaxRounds should report the failed staging write")
		}
	})
	t.Run("rename", func(t *testing.T) {
		t.Setenv("K_BRAIN_HOME", t.TempDir())
		blockedPath(t, "projects.json", "target")
		if err := SetProjectGoalMaxRounds("/x", 7); err == nil {
			t.Fatal("SetProjectGoalMaxRounds should report the failed rename")
		}
		dir, _ := Dir()
		if _, err := os.Stat(filepath.Join(dir, "projects.json.tmp")); !os.IsNotExist(err) {
			t.Fatal("a failed rename must clean up its staging file")
		}
	})
}

func TestConfigSaveReportsWriteFailures(t *testing.T) {
	t.Run("staging write", func(t *testing.T) {
		t.Setenv("K_BRAIN_HOME", t.TempDir())
		blockedPath(t, "config.json", "tmp")
		if err := Default().Save(); err == nil {
			t.Fatal("Save should report the failed staging write")
		}
	})
	t.Run("rename", func(t *testing.T) {
		t.Setenv("K_BRAIN_HOME", t.TempDir())
		blockedPath(t, "config.json", "target")
		if err := Default().Save(); err == nil {
			t.Fatal("Save should report the failed rename")
		}
		dir, _ := Dir()
		if _, err := os.Stat(filepath.Join(dir, "config.json.tmp")); !os.IsNotExist(err) {
			t.Fatal("a failed rename must clean up its staging file")
		}
	})
}

func TestSaveOverUnparseableConfig(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	dir, _ := Dir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Default().Save(); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(p + ".bak")
	if err != nil {
		t.Fatalf("the unparseable file should be backed up: %v", err)
	}
	if string(bak) != "{ this is not json" {
		t.Fatalf("backup content: %q", bak)
	}
	cfg, err := Load()
	if err != nil || cfg.DefaultModel != Default().DefaultModel {
		t.Fatalf("the replacement config should load: %v %+v", err, cfg)
	}
}

func TestLoadReportsUnreadableConfig(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	dir, _ := Dir()
	if err := os.MkdirAll(filepath.Join(dir, "config.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("Load should report an unreadable config file")
	}
}

func TestLoadKeepsMCPServersWhenRestoringBackup(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	dir, _ := Dir()
	p := filepath.Join(dir, "config.json")

	if err := os.WriteFile(p+".bak", []byte(`{
  "defaultModel": "kimi-k3",
  "providers": {
    "inference": {
      "baseUrl": "u",
      "api": "openai-completions",
      "models": [
        {
          "id": "kimi-k3"
        }
      ]
    }
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"mcp":{"fs":{"command":["srv"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 1 || cfg.DefaultModel != "kimi-k3" {
		t.Fatalf("should have restored from .bak: %+v", cfg)
	}
	if _, ok := cfg.MCPServers["fs"]; !ok {
		t.Fatalf("the mcp block must survive the restore: %+v", cfg.MCPServers)
	}
}

func TestCatalogMissesReturnZero(t *testing.T) {
	c := Catalog{Models: []ModelInfoLite{{ID: "known", ContextLength: 10}}}
	if got := c.ContextLength("unknown"); got != 0 {
		t.Errorf("ContextLength(unknown) = %d, want 0", got)
	}
	if got := c.Find("unknown"); got != nil {
		t.Errorf("Find(unknown) = %+v, want nil", got)
	}
}

func TestLoadCatalogsRejectsGarbage(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	dir, _ := Dir()
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	cats := LoadCatalogs()
	if cats == nil {
		t.Fatal("LoadCatalogs must never return nil")
	}
	cats["p"] = Catalog{}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte("[1,2]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadCatalogs(); got == nil || len(got) != 0 {
		t.Fatalf("a non-object models.json should yield an empty map, got %v", got)
	}
}

func TestWriteJSONReportsUnmarshalableValue(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	if err := WriteJSON("state.json", make(chan int)); err == nil {
		t.Fatal("an unmarshalable value should be an error")
	}
	dir, _ := Dir()
	if _, err := os.Stat(filepath.Join(dir, "state.json")); !os.IsNotExist(err) {
		t.Fatal("nothing should have been written")
	}
}
