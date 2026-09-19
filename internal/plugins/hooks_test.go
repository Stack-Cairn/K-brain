package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/hooks"
)

func TestRunHookUsesEventProjectAndPayload(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	project, worktree := t.TempDir(), t.TempDir()
	dir := filepath.Join(project, ".k-brain", "plugins", "probe")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	command := `printf '%s' "$K_BRAIN_PLUGIN_PAYLOAD" > event.json`
	if runtime.GOOS == "windows" {
		command = `powershell -NoProfile -Command "[System.IO.File]::WriteAllText('event.json', [Environment]::GetEnvironmentVariable('K_BRAIN_PLUGIN_PAYLOAD'))"`
	}
	manifest := Manifest{Name: "probe", Command: []string{"unused"}, Hooks: map[string]string{"SessionStart": command}, Enabled: new(true)}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	m, err := New(project)
	if err != nil {
		t.Fatal(err)
	}
	event := hooks.Event{Name: "SessionStart", SessionID: "session-a", CWD: worktree}
	if err := m.RunHook(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(worktree, "event.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got hooks.Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got != event {
		t.Fatalf("payload = %+v, want %+v", got, event)
	}
	if _, err := os.Stat(filepath.Join(project, "event.json")); !os.IsNotExist(err) {
		t.Fatal("hook ran in manager project instead of event project")
	}
}
