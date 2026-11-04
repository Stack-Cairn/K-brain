package hooks

import (
	"context"
	"encoding/json"
	"errors"
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
	for _, name := range []string{PostToolUseFailure, PreCompact, PostCompact, SessionEnd} {
		if _, err := Parse([]byte(`{"` + name + `":[{"command":"echo ok"}]}`)); err != nil {
			t.Fatalf("%s rejected: %v", name, err)
		}
	}
	if _, err := Parse([]byte(`{"afterTurn":[{"command":"echo ok"}]}`)); err == nil {
		t.Fatal("unknown event accepted")
	}
}

func TestPreToolUseStructuredDecision(t *testing.T) {
	shell, quote := "sh", func(s string) string { return "printf '%s\\n' '" + s + "'" }
	if runtime.GOOS == "windows" {
		shell, quote = "powershell", func(s string) string { return "Write-Output '" + s + "'" }
	}
	for _, tc := range []struct {
		name, event, command, reason string
		denied                       bool
	}{
		{"deny", PreToolUse, quote(`{"decision":"deny","reason":"no network"}`), "no network", true},
		{"deny after log line", PreToolUse, quote("checking") + "; " + quote(`{"decision":"deny","reason":"blocked"}`), "blocked", true},
		{"deny without reason", PreToolUse, quote(`{"decision":"deny"}`), "denied by hook", true},
		{"allow", PreToolUse, quote(`{"decision":"allow","reason":"fine"}`), "", false},
		{"plain text", PreToolUse, quote("deny"), "", false},
		{"other event", PostToolUse, quote(`{"decision":"deny","reason":"ignored"}`), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New(map[string][]config.Hook{tc.event: {{Command: tc.command, Shell: shell}}})
			err := r.Run(context.Background(), Event{Name: tc.event, CWD: t.TempDir()})
			var denied *DeniedError
			if errors.As(err, &denied) != tc.denied || (tc.denied && err.Error() != tc.reason) || (!tc.denied && err != nil) {
				t.Fatalf("err = %v, want denied=%v reason=%q", err, tc.denied, tc.reason)
			}
		})
	}
}

func TestEventCompactAndSessionFields(t *testing.T) {
	data, err := json.Marshal(Event{Name: PostCompact, Trigger: "auto", MessagesBefore: 9, TokensBefore: 900, MessagesAfter: 3, TokensAfter: 120, Summary: "s"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"event":"PostCompact","trigger":"auto","messagesBefore":9,"tokensBefore":900,"messagesAfter":3,"tokensAfter":120,"summary":"s"}`
	if string(data) != want {
		t.Fatalf("payload %s, want %s", data, want)
	}
	if data, _ := json.Marshal(Event{Name: PreToolUse, ToolName: "bash"}); strings.Contains(string(data), "trigger") || strings.Contains(string(data), "reason") {
		t.Fatalf("existing payloads gained fields: %s", data)
	}
}
