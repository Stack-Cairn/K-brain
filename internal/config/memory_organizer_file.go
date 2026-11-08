package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"
)

// memoryOrganizerSuffix names the file beside a config file that keeps the
// backend memory organizer schedule saved through /v1/settings. Like the memory
// switch it stays out of config.json, so releases that decode config.json with
// DisallowUnknownFields still start after a rollback. Only an explicit save
// writes it; config saves never touch it, so a process saving from a stale
// config copy cannot undo a schedule set elsewhere.
const memoryOrganizerSuffix = ".memory-organizer.json"

const (
	// MemoryOrganizerMinHours and MemoryOrganizerMaxHours bound a saved interval.
	MemoryOrganizerMinHours = 1
	MemoryOrganizerMaxHours = 168
	// DefaultMemoryOrganizerInterval applies when neither a saved setting nor a
	// startup flag gives one.
	DefaultMemoryOrganizerInterval = 24 * time.Hour
)

// MemoryOrganizerConfig is a saved organizer schedule. Both fields are always
// written; a file missing either is treated as unreadable.
type MemoryOrganizerConfig struct {
	Enabled       bool    `json:"enabled"`
	IntervalHours float64 `json:"intervalHours"`
}

// Interval converts the saved hours to a duration.
func (m MemoryOrganizerConfig) Interval() time.Duration {
	return time.Duration(m.IntervalHours * float64(time.Hour))
}

// Validate checks the saved interval range (1 to 168 hours, inclusive).
func (m MemoryOrganizerConfig) Validate() error {
	if math.IsNaN(m.IntervalHours) || m.IntervalHours < MemoryOrganizerMinHours || m.IntervalHours > MemoryOrganizerMaxHours {
		return fmt.Errorf("memoryOrganizer.intervalHours must be between %d and %d", MemoryOrganizerMinHours, MemoryOrganizerMaxHours)
	}
	return nil
}

// MemoryOrganizerPath is the organizer schedule file for a config file.
func MemoryOrganizerPath(configPath string) string { return configPath + memoryOrganizerSuffix }

// LoadMemoryOrganizer reads the saved schedule for a config file. saved is
// false when no schedule was ever saved; an unreadable or invalid file is an
// error and leaves saved false, so callers fall back to the startup default.
func LoadMemoryOrganizer(configPath string) (m MemoryOrganizerConfig, saved bool, err error) {
	data, err := os.ReadFile(MemoryOrganizerPath(configPath))
	if errors.Is(err, os.ErrNotExist) {
		return MemoryOrganizerConfig{}, false, nil
	}
	if err != nil {
		return MemoryOrganizerConfig{}, false, err
	}
	var wire struct {
		Enabled       *bool    `json:"enabled"`
		IntervalHours *float64 `json:"intervalHours"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return MemoryOrganizerConfig{}, false, fmt.Errorf("parse %s: %w", MemoryOrganizerPath(configPath), err)
	}
	if wire.Enabled == nil || wire.IntervalHours == nil {
		return MemoryOrganizerConfig{}, false, fmt.Errorf("parse %s: enabled and intervalHours are required", MemoryOrganizerPath(configPath))
	}
	m = MemoryOrganizerConfig{Enabled: *wire.Enabled, IntervalHours: *wire.IntervalHours}
	if err := m.Validate(); err != nil {
		return MemoryOrganizerConfig{}, false, err
	}
	return m, true, nil
}

// SaveMemoryOrganizer atomically writes the schedule file for a config file.
func SaveMemoryOrganizer(configPath string, m MemoryOrganizerConfig) error {
	if err := m.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	p := MemoryOrganizerPath(configPath)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".memory-organizer-*")
	if err != nil {
		return err
	}
	return replaceConfigFile(f, p, append(data, '\n'))
}

// RestoreMemoryOrganizer puts back a schedule file state captured before a save:
// saved=false removes the file.
func RestoreMemoryOrganizer(configPath string, m MemoryOrganizerConfig, saved bool) error {
	if !saved {
		if err := os.Remove(MemoryOrganizerPath(configPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return SaveMemoryOrganizer(configPath, m)
}
