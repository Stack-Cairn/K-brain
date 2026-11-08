package backend

import (
	"errors"
	"fmt"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// settingsMemoryOrganizer is the effective organizer schedule in /v1/settings:
// the saved setting, else the startup flag, else enabled every 24 hours.
type settingsMemoryOrganizer struct {
	Enabled       bool    `json:"enabled"`
	IntervalHours float64 `json:"intervalHours"`
	interval      time.Duration
}

// settingsMemoryOrganizerUpdate changes the schedule; an omitted field keeps
// its effective value, and the saved result always holds both.
type settingsMemoryOrganizerUpdate struct {
	Enabled       *bool    `json:"enabled,omitempty"`
	IntervalHours *float64 `json:"intervalHours,omitempty"`
}

var errOrganizerPersistence = errors.New("memory organizer settings persistence is unavailable")

// UseMemoryOrganizerFile makes the store persist memoryOrganizer beside the
// given config file (config.MemoryOrganizerPath) and loads a saved schedule.
// An unreadable file is reported and ignored: the startup default applies
// until the next successful save replaces it.
func (s *SettingsStore) UseMemoryOrganizerFile(configPath string) error {
	saved, ok, err := config.LoadMemoryOrganizer(configPath)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.organizerPath = configPath
	s.organizer, s.organizerSaved = saved, ok
	return err
}

// savedMemoryOrganizerLocked is the schedule saved through /v1/settings, if any.
func (s *SettingsStore) savedMemoryOrganizerLocked() (config.MemoryOrganizerConfig, bool) {
	return s.organizer, s.organizerSaved
}

func (s *SettingsStore) savedMemoryOrganizer() (config.MemoryOrganizerConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.savedMemoryOrganizerLocked()
}

func (s *Server) organizerFlagDefault() time.Duration {
	if s.organizerDefault <= 0 {
		return config.DefaultMemoryOrganizerInterval
	}
	return s.organizerDefault
}

// memoryOrganizerFrom resolves precedence: saved setting > startup flag > 24h.
func (s *Server) memoryOrganizerFrom(saved config.MemoryOrganizerConfig, ok bool) settingsMemoryOrganizer {
	if ok {
		return settingsMemoryOrganizer{Enabled: saved.Enabled, IntervalHours: saved.IntervalHours, interval: saved.Interval()}
	}
	interval := s.organizerFlagDefault()
	return settingsMemoryOrganizer{Enabled: true, IntervalHours: interval.Hours(), interval: interval}
}

// memoryOrganizerSetting is the effective schedule.
func (s *Server) memoryOrganizerSetting() settingsMemoryOrganizer {
	if s.settings == nil {
		return s.memoryOrganizerFrom(config.MemoryOrganizerConfig{}, false)
	}
	return s.memoryOrganizerFrom(s.settings.savedMemoryOrganizer())
}

// nextMemoryOrganizerLocked merges an update into the effective schedule and
// validates the result. Callers hold s.settings.mu.
func (s *Server) nextMemoryOrganizerLocked(update settingsMemoryOrganizerUpdate) (config.MemoryOrganizerConfig, error) {
	if update.Enabled == nil && update.IntervalHours == nil {
		return config.MemoryOrganizerConfig{}, fmt.Errorf("memoryOrganizer requires enabled or intervalHours")
	}
	current := s.memoryOrganizerFrom(s.settings.savedMemoryOrganizerLocked())
	next := config.MemoryOrganizerConfig{Enabled: current.Enabled, IntervalHours: current.IntervalHours}
	if update.Enabled != nil {
		next.Enabled = *update.Enabled
	}
	if update.IntervalHours != nil {
		next.IntervalHours = *update.IntervalHours
	}
	if err := next.Validate(); err != nil {
		return config.MemoryOrganizerConfig{}, err
	}
	return next, nil
}

// rescheduleMemoryOrganizer hands the effective schedule to the running
// organizer loop, if this backend runs one.
func (s *Server) rescheduleMemoryOrganizer() {
	schedule := s.memoryOrganizerSetting()
	s.organizerMu.Lock()
	defer s.organizerMu.Unlock()
	if s.organizerScheduler != nil {
		s.organizerScheduler.Update(schedule.Enabled, schedule.interval)
	}
}

// settingsView is the GET/PUT /v1/settings body: the config projection plus
// the server-owned memory organizer schedule.
func (s *Server) settingsView(cfg *config.Config) settingsProjection {
	out := projectSettings(cfg)
	organizer := s.memoryOrganizerSetting()
	out.MemoryOrganizer = &organizer
	return out
}
