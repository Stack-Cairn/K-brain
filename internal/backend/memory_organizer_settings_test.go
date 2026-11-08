package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/memoryruntime"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

type organizerFixture struct {
	configPath string
	saves      int
	saveErr    error
	settings   *SettingsStore
	server     *Server
}

// newOrganizerServer starts a backend whose scheduled organizer uses flag as
// its -memory-organizer-interval and whose settings persist beside configPath.
func newOrganizerServer(t *testing.T, root string, flag time.Duration) *organizerFixture {
	t.Helper()
	fx := &organizerFixture{configPath: filepath.Join(root, "config.json")}
	fx.settings = NewSettingsStore(&config.Config{}, func(*config.Config) error {
		fx.saves++
		return fx.saveErr
	})
	if err := fx.settings.UseMemoryOrganizerFile(fx.configPath); err != nil {
		t.Fatal(err)
	}
	sessions, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.Close() })
	memoryStore, err := memory.OpenStore(filepath.Join(root, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{
		Store: sessions, EventDir: filepath.Join(root, "events"), MemoryRoot: memoryStore.Root(), DefaultCWD: root,
		Settings: fx.settings, Token: healthTestToken,
		Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
			return agent.New(&scriptedClient{}, "model", 128, ""), nil
		},
		MemoryRuntimeFactory: func(context.Context, string, protocol.ModelRef) (*memoryruntime.Runtime, error) {
			return memoryruntime.New(memoryruntime.Config{Store: memoryStore, ResolveModel: func(context.Context, string) (ai.Client, string, error) {
				return nil, "", errors.New("model execution is outside this test")
			}})
		},
		MemoryOrganizerInterval: flag,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	fx.server = server
	return fx
}

type organizerView struct {
	Enabled       bool    `json:"enabled"`
	IntervalHours float64 `json:"intervalHours"`
}

func (fx *organizerFixture) request(t *testing.T, method, body string) (int, organizerView, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	w := imageHTTPRequest(fx.server, method, "/v1/settings", reader)
	var out struct {
		MemoryOrganizer *organizerView `json:"memoryOrganizer"`
	}
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.MemoryOrganizer == nil {
			t.Fatalf("settings body without memoryOrganizer: %v %s", err, w.Body.String())
		}
		return w.Code, *out.MemoryOrganizer, w.Body.String()
	}
	return w.Code, organizerView{}, w.Body.String()
}

func (fx *organizerFixture) schedule(t *testing.T) (bool, time.Duration) {
	t.Helper()
	if fx.server.organizerScheduler == nil {
		t.Fatal("organizer scheduler not running")
	}
	return fx.server.organizerScheduler.State()
}

func TestMemoryOrganizerSettingFlagPrecedence(t *testing.T) {
	// No saved schedule: the flag applies; a non-positive flag means 24h.
	for flag, want := range map[time.Duration]float64{3 * time.Hour: 3, -1: 24} {
		fx := newOrganizerServer(t, t.TempDir(), flag)
		code, view, body := fx.request(t, http.MethodGet, "")
		if code != http.StatusOK || !view.Enabled || view.IntervalHours != want {
			t.Fatalf("flag %v: GET = %d %+v %s", flag, code, view, body)
		}
		if enabled, interval := fx.schedule(t); !enabled || interval != time.Duration(want*float64(time.Hour)) {
			t.Fatalf("flag %v: scheduler = %v %v", flag, enabled, interval)
		}
	}

	// A saved schedule beats the flag after a restart.
	root := t.TempDir()
	if err := config.SaveMemoryOrganizer(filepath.Join(root, "config.json"), config.MemoryOrganizerConfig{Enabled: false, IntervalHours: 48}); err != nil {
		t.Fatal(err)
	}
	fx := newOrganizerServer(t, root, 3*time.Hour)
	if code, view, _ := fx.request(t, http.MethodGet, ""); code != http.StatusOK || view.Enabled || view.IntervalHours != 48 {
		t.Fatalf("saved schedule GET = %d %+v", code, view)
	}
	if enabled, interval := fx.schedule(t); enabled || interval != 48*time.Hour {
		t.Fatalf("saved schedule scheduler = %v %v", enabled, interval)
	}
}

func TestMemoryOrganizerSettingPersistsAndReschedules(t *testing.T) {
	root := t.TempDir()
	fx := newOrganizerServer(t, root, 3*time.Hour)
	revision := fx.settings.Revision()

	code, view, body := fx.request(t, http.MethodPut, `{"memoryOrganizer":{"intervalHours":6}}`)
	if code != http.StatusOK || !view.Enabled || view.IntervalHours != 6 {
		t.Fatalf("PUT interval = %d %+v %s", code, view, body)
	}
	if enabled, interval := fx.schedule(t); !enabled || interval != 6*time.Hour {
		t.Fatalf("scheduler after PUT = %v %v", enabled, interval)
	}
	// Only the organizer changed: config.json and the settings revision stay.
	if fx.saves != 0 || fx.settings.Revision() != revision {
		t.Fatalf("organizer-only PUT saved config %d times, revision %d -> %d", fx.saves, revision, fx.settings.Revision())
	}
	saved, ok, err := config.LoadMemoryOrganizer(fx.configPath)
	if err != nil || !ok || saved != (config.MemoryOrganizerConfig{Enabled: true, IntervalHours: 6}) {
		t.Fatalf("saved file = %+v %v %v", saved, ok, err)
	}

	if code, view, _ = fx.request(t, http.MethodPut, `{"memoryOrganizer":{"enabled":false}}`); code != http.StatusOK || view.Enabled || view.IntervalHours != 6 {
		t.Fatalf("PUT disable = %d %+v", code, view)
	}
	if enabled, interval := fx.schedule(t); enabled || interval != 6*time.Hour {
		t.Fatalf("scheduler after disable = %v %v", enabled, interval)
	}
	if code, view, _ = fx.request(t, http.MethodGet, ""); code != http.StatusOK || view.Enabled || view.IntervalHours != 6 {
		t.Fatalf("GET after disable = %d %+v", code, view)
	}

	// A request with other settings saves config.json once as well.
	if code, view, _ = fx.request(t, http.MethodPut, `{"memory":{"enabled":true},"memoryOrganizer":{"enabled":true,"intervalHours":168}}`); code != http.StatusOK || !view.Enabled || view.IntervalHours != 168 {
		t.Fatalf("combined PUT = %d %+v", code, view)
	}
	if fx.saves != 1 || fx.settings.Revision() != revision+1 {
		t.Fatalf("combined PUT saves=%d revision=%d", fx.saves, fx.settings.Revision())
	}
	if enabled, interval := fx.schedule(t); !enabled || interval != 168*time.Hour {
		t.Fatalf("scheduler after combined PUT = %v %v", enabled, interval)
	}

	// Restarting with a different flag keeps the saved schedule.
	if err := fx.server.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := newOrganizerServer(t, root, time.Hour)
	if code, view, _ = restarted.request(t, http.MethodGet, ""); code != http.StatusOK || !view.Enabled || view.IntervalHours != 168 {
		t.Fatalf("GET after restart = %d %+v", code, view)
	}
	if enabled, interval := restarted.schedule(t); !enabled || interval != 168*time.Hour {
		t.Fatalf("scheduler after restart = %v %v", enabled, interval)
	}
}

func TestMemoryOrganizerSettingRejectsInvalidAndKeepsState(t *testing.T) {
	fx := newOrganizerServer(t, t.TempDir(), 3*time.Hour)
	for _, body := range []string{
		`{"memoryOrganizer":{}}`,
		`{"memoryOrganizer":{"intervalHours":0}}`,
		`{"memoryOrganizer":{"intervalHours":0.5}}`,
		`{"memoryOrganizer":{"intervalHours":169}}`,
		`{"memoryOrganizer":{"intervalHours":"24"}}`,
		`{"memoryOrganizer":{"enabled":true,"intervalMinutes":5}}`,
	} {
		if code, _, resp := fx.request(t, http.MethodPut, body); code != http.StatusBadRequest {
			t.Fatalf("%s: status %d %s", body, code, resp)
		}
	}
	if _, err := os.Stat(config.MemoryOrganizerPath(fx.configPath)); !os.IsNotExist(err) {
		t.Fatalf("rejected update wrote the schedule file: %v", err)
	}
	if enabled, interval := fx.schedule(t); !enabled || interval != 3*time.Hour {
		t.Fatalf("scheduler after rejected updates = %v %v", enabled, interval)
	}

	// A config save failure rolls the schedule file back.
	fx.saveErr = errors.New("disk full")
	if code, _, _ := fx.request(t, http.MethodPut, `{"memory":{"enabled":false},"memoryOrganizer":{"intervalHours":12}}`); code != http.StatusInternalServerError {
		t.Fatalf("failed save status = %d", code)
	}
	if _, ok, _ := config.LoadMemoryOrganizer(fx.configPath); ok {
		t.Fatal("schedule file left behind after the config save failed")
	}
	if code, view, _ := fx.request(t, http.MethodGet, ""); code != http.StatusOK || view.IntervalHours != 3 {
		t.Fatalf("GET after failed save = %d %+v", code, view)
	}
	if enabled, interval := fx.schedule(t); !enabled || interval != 3*time.Hour {
		t.Fatalf("scheduler after failed save = %v %v", enabled, interval)
	}
}

func TestMemoryOrganizerSettingWithoutPersistence(t *testing.T) {
	store := NewSettingsStore(&config.Config{}, func(*config.Config) error { return nil })
	server := &Server{token: healthTestToken, settings: store}
	w := imageHTTPRequest(server, http.MethodPut, "/v1/settings", strings.NewReader(`{"memoryOrganizer":{"enabled":false}}`))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	w = imageHTTPRequest(server, http.MethodGet, "/v1/settings", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"memoryOrganizer":{"enabled":true,"intervalHours":24}`) {
		t.Fatalf("GET = %d %s", w.Code, w.Body.String())
	}
}
