package config

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMemoryOrganizerFileRoundTrip(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if _, saved, err := LoadMemoryOrganizer(configPath); saved || err != nil {
		t.Fatalf("missing file: saved=%v err=%v", saved, err)
	}
	want := MemoryOrganizerConfig{Enabled: false, IntervalHours: 1.5}
	if err := SaveMemoryOrganizer(configPath, want); err != nil {
		t.Fatal(err)
	}
	got, saved, err := LoadMemoryOrganizer(configPath)
	if err != nil || !saved || got != want {
		t.Fatalf("load = %+v saved=%v err=%v", got, saved, err)
	}
	if got.Interval() != 90*time.Minute {
		t.Fatalf("interval = %v", got.Interval())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("saving the schedule touched config.json: %v", err)
	}
	if err := RestoreMemoryOrganizer(configPath, MemoryOrganizerConfig{}, false); err != nil {
		t.Fatal(err)
	}
	if _, saved, _ := LoadMemoryOrganizer(configPath); saved {
		t.Fatal("restore to unsaved left the file")
	}
}

func TestMemoryOrganizerFileValidation(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	for _, hours := range []float64{0, 0.99, 168.01, -1, math.NaN(), math.Inf(1)} {
		if err := SaveMemoryOrganizer(configPath, MemoryOrganizerConfig{Enabled: true, IntervalHours: hours}); err == nil {
			t.Fatalf("saved intervalHours %v", hours)
		}
	}
	for _, hours := range []float64{1, 24, 168} {
		if err := SaveMemoryOrganizer(configPath, MemoryOrganizerConfig{Enabled: true, IntervalHours: hours}); err != nil {
			t.Fatalf("intervalHours %v: %v", hours, err)
		}
	}
	for _, body := range []string{`{"enabled":true}`, `{"intervalHours":5}`, `{"enabled":true,"intervalHours":500}`, `not json`} {
		if err := os.WriteFile(MemoryOrganizerPath(configPath), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, saved, err := LoadMemoryOrganizer(configPath); saved || err == nil {
			t.Fatalf("%s: saved=%v err=%v", body, saved, err)
		}
	}
}
