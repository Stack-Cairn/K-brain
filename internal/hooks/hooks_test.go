package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func hookCommand(path string) (string, string) {
	if runtime.GOOS == "windows" {
		return "[Environment]::GetEnvironmentVariable('K_BRAIN_HOOK_EVENT') | Set-Content -LiteralPath '" + strings.ReplaceAll(path, "'", "''") + "'", "powershell"
	}
	return "printf '%s' \"$K_BRAIN_HOOK_EVENT\" > '" + strings.ReplaceAll(path, "'", "'\\''") + "'", "sh"
}

func TestRunnerExportsEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event.json")
	command, shell := hookCommand("event.json")
	r := New(map[string][]config.Hook{"UserPromptSubmit": {{Command: command, Shell: shell}}})
	event := Event{Name: "UserPromptSubmit", SessionID: "abc123", Prompt: "hello", CWD: filepath.Dir(path)}
	if err := r.Run(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("hook payload is not JSON: %v (%q)", err, data)
	}
	if got.Name != event.Name || got.SessionID != event.SessionID || got.Prompt != event.Prompt || got.CWD != event.CWD {
		t.Fatalf("event = %+v, want %+v", got, event)
	}
}

func TestRunnerStopsOnFailure(t *testing.T) {
	command := "exit 7"
	shell := "sh"
	if runtime.GOOS == "windows" {
		command, shell = "exit 7", "powershell"
	}
	r := New(map[string][]config.Hook{"PreToolUse": {{Command: command, Shell: shell}}})
	if err := r.Run(context.Background(), Event{Name: "PreToolUse"}); err == nil {
		t.Fatal("failed hook returned nil")
	}
}

func TestParseHooks(t *testing.T) {
	items, err := Parse([]byte(`{"PreToolUse":[{"command":"echo ok","timeout":3}]}`))
	if err != nil || len(items["PreToolUse"]) != 1 || items["PreToolUse"][0].Timeout != 3 {
		t.Fatalf("parsed hooks = %+v, err=%v", items, err)
	}
	if _, err := Parse([]byte(`{"Stop":[{"command":""}]}`)); err == nil {
		t.Fatal("empty command accepted")
	}
}
