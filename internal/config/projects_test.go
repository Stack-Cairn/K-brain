package config

import (
	"os"
	"testing"
)

func TestProjectGoalMaxRounds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	dir := t.TempDir()

	if n := ProjectGoalMaxRounds(dir); n != 0 {
		t.Fatalf("no override should be 0, got %d", n)
	}
	if err := SetProjectGoalMaxRounds(dir, 42); err != nil {
		t.Fatal(err)
	}
	if n := ProjectGoalMaxRounds(dir); n != 42 {
		t.Fatalf("override should be 42, got %d", n)
	}

	other := t.TempDir()
	if n := ProjectGoalMaxRounds(other); n != 0 {
		t.Fatalf("other project should be 0, got %d", n)
	}

	if err := SetProjectGoalMaxRounds(dir, 0); err != nil {
		t.Fatal(err)
	}
	if n := ProjectGoalMaxRounds(dir); n != 0 {
		t.Fatalf("cleared override should be 0, got %d", n)
	}
}

func TestGoalMaxRoundsConfigRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	cfg := Default()
	cfg.GoalMaxRounds = 250
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.GoalMaxRounds != 250 {
		t.Fatalf("goalMaxRounds should round-trip as 250, got %d", loaded.GoalMaxRounds)
	}
}

// The permission mode chosen in a folder is kept for that folder alone, next
// to its other per-folder settings.
func TestProjectPermissionMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	dir, other := t.TempDir(), t.TempDir()
	if err := SetProjectGoalMaxRounds(dir, 7); err != nil {
		t.Fatal(err)
	}
	if got := ProjectPermissionMode(dir); got != "" {
		t.Fatalf("no choice yet, got %q", got)
	}
	if err := SetProjectPermissionMode(dir, "auto_edit"); err != nil {
		t.Fatal(err)
	}
	if got := ProjectPermissionMode(dir); got != "auto_edit" {
		t.Fatalf("saved mode = %q", got)
	}
	if got := ProjectPermissionMode(other); got != "" {
		t.Fatalf("another folder picked up %q", got)
	}
	if ProjectGoalMaxRounds(dir) != 7 {
		t.Fatal("saving the mode lost the folder's goal rounds")
	}
	if err := SetProjectPermissionMode(dir, ""); err != nil || ProjectPermissionMode(dir) != "" {
		t.Fatalf("forgetting the mode: %v %q", err, ProjectPermissionMode(dir))
	}
}

// permissionMode survives a save and load, and an old config has none.
func TestPermissionModeConfigRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	cfg := Default()
	if cfg.PermissionMode != "" {
		t.Fatalf("default config carries %q; existing installs must keep starting in always", cfg.PermissionMode)
	}
	cfg.PermissionMode = "normal"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil || loaded.PermissionMode != "normal" {
		t.Fatalf("reloaded: %v %q", err, loaded.PermissionMode)
	}
}
