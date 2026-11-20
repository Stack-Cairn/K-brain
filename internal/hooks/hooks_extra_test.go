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

func skipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell hook")
	}
}

func TestMatcherSelectsTools(t *testing.T) {
	for _, tc := range []struct {
		matcher string
		match   []string
		skip    []string
	}{
		{"", []string{"bash", "read"}, nil},
		{"*", []string{"bash", "mcp__x__y"}, nil},
		{"bash", []string{"bash"}, []string{"bashful", "rebash", "read"}},
		{"edit|write", []string{"edit", "write"}, []string{"editor", "bash"}},
		{"mcp__.*", []string{"mcp__github__search"}, []string{"bash"}},
	} {
		match, err := config.CompileHookMatcher(tc.matcher)
		if err != nil {
			t.Fatalf("%q: %v", tc.matcher, err)
		}
		h := hook{match: match}
		for _, name := range tc.match {
			if !h.matches(Event{Name: PreToolUse, ToolName: name}) {
				t.Errorf("matcher %q did not match %q", tc.matcher, name)
			}
		}
		for _, name := range tc.skip {
			if h.matches(Event{Name: PostToolUse, ToolName: name}) {
				t.Errorf("matcher %q matched %q", tc.matcher, name)
			}
		}
	}
}

func TestMatcherSkipsHookAtRuntime(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	r := New(map[string][]config.Hook{PostToolUse: {
		{Command: "touch bash.ran", Matcher: "bash"},
		{Command: "touch edit.ran", Matcher: "edit|write"},
		{Command: "touch all.ran"},
	}})
	if err := r.Run(context.Background(), Event{Name: PostToolUse, ToolName: "write", CWD: dir}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"bash.ran": false, "edit.ran": true, "all.ran": true} {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", name, err == nil, want)
		}
	}
}

func TestParseValidatesMatcher(t *testing.T) {
	items, err := Parse([]byte(`{"PreToolUse":[{"command":"echo ok","matcher":"bash|edit"}]}`))
	if err != nil || items[PreToolUse][0].Matcher != "bash|edit" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if _, err := Parse([]byte(`{"PostToolUse":[{"command":"echo ok","matcher":"("}]}`)); err == nil || !strings.Contains(err.Error(), "matcher") {
		t.Fatalf("bad pattern accepted: %v", err)
	}
	if _, err := Parse([]byte(`{"Stop":[{"command":"echo ok","matcher":"bash"}]}`)); err == nil {
		t.Fatal("matcher on a non-tool event accepted")
	}
	if _, err := Parse([]byte(`{"Stop":[{"command":"echo ok","matcher":"*"}]}`)); err == nil {
		t.Fatal("matcher on a non-tool event accepted")
	}
	if _, err := Parse([]byte(`{"PostToolUseFailure":[{"command":"echo ok","matcher":"*"}]}`)); err != nil {
		t.Fatal(err)
	}
}

func TestEventOnStdin(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	r := New(map[string][]config.Hook{PreToolUse: {{Command: "cat > stdin.json; printf '%s' \"$K_BRAIN_HOOK_EVENT\" > env.json"}}})
	event := Event{Name: PreToolUse, ToolName: "bash", ToolArgs: `{"command":"ls"}`, CWD: dir}
	if err := r.Run(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.ReadFile(filepath.Join(dir, "stdin.json"))
	if err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "env.json"))
	if string(stdin) != string(env) {
		t.Fatalf("stdin %q differs from env %q", stdin, env)
	}
	var got Event
	if err := json.Unmarshal(stdin, &got); err != nil || got.ToolName != "bash" || got.ToolArgs != event.ToolArgs {
		t.Fatalf("stdin event %+v err=%v", got, err)
	}
}

func TestLargeEventIgnoredStdin(t *testing.T) {
	skipWindows(t)
	// A hook that never reads a payload larger than a pipe buffer still finishes.
	// (The env copy keeps the payload under the per-variable size limit.)
	r := New(map[string][]config.Hook{PostToolUse: {{Command: "exit 0", Timeout: 5}}})
	event := Event{Name: PostToolUse, ToolResult: strings.Repeat("x", 100_000), CWD: t.TempDir()}
	if err := r.Run(context.Background(), event); err != nil {
		t.Fatal(err)
	}
}

func TestStopBlockAndContextReplies(t *testing.T) {
	skipWindows(t)
	quote := func(s string) string { return "printf '%s\\n' '" + s + "'" }
	for _, tc := range []struct {
		name, event string
		commands    []string
		block       bool
		reason      string
		context     string
	}{
		{"stop block", Stop, []string{quote(`{"decision":"block","reason":"tests not run"}`)}, true, "tests not run", ""},
		{"stop block after log", Stop, []string{quote("checking") + "; " + quote(`{"decision":"block","reason":"lint"}`)}, true, "lint", ""},
		{"stop first block wins", Stop, []string{quote(`{"decision":"block","reason":"one"}`), quote(`{"decision":"block","reason":"two"}`)}, true, "one", ""},
		{"stop approve", Stop, []string{quote(`{"decision":"approve"}`)}, false, "", ""},
		{"stop plain", Stop, []string{quote("block")}, false, "", ""},
		{"block ignored elsewhere", PostToolUse, []string{quote(`{"decision":"block","reason":"x"}`)}, false, "", ""},
		{"deny is not a stop block", Stop, []string{quote(`{"decision":"deny","reason":"x"}`)}, false, "", ""},
		{"prompt context", UserPromptSubmit, []string{quote(`{"additionalContext":"branch is main"}`)}, false, "", "branch is main"},
		{"tool context joined", PostToolUse, []string{quote(`{"additionalContext":"one"}`), quote("plain"), quote(`{"additionalContext":"two"}`)}, false, "", "one\n\ntwo"},
		{"failure context", PostToolUseFailure, []string{quote(`{"additionalContext":"retry later"}`)}, false, "", "retry later"},
		{"context ignored on stop", Stop, []string{quote(`{"additionalContext":"x"}`)}, false, "", ""},
		{"context ignored pre tool", PreToolUse, []string{quote(`{"additionalContext":"x"}`)}, false, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := make([]config.Hook, 0, len(tc.commands))
			for _, c := range tc.commands {
				list = append(list, config.Hook{Command: c})
			}
			out, err := New(map[string][]config.Hook{tc.event: list}).RunOutcome(context.Background(), Event{Name: tc.event, CWD: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if out.Block != tc.block || out.Reason != tc.reason || out.AdditionalContext != tc.context {
				t.Fatalf("outcome %+v, want block=%v reason=%q context=%q", out, tc.block, tc.reason, tc.context)
			}
		})
	}
}

func TestFailingStopHookDoesNotBlock(t *testing.T) {
	skipWindows(t)
	r := New(map[string][]config.Hook{Stop: {{Command: `printf '{"decision":"block","reason":"x"}'; exit 3`}}})
	out, err := r.RunOutcome(context.Background(), Event{Name: Stop, CWD: t.TempDir()})
	if err == nil || out.Block {
		t.Fatalf("outcome %+v err=%v", out, err)
	}
}

func TestAdditionalContextBounded(t *testing.T) {
	text := strings.Repeat("é", MaxAdditionalContext)
	got := boundContext(text)
	if len(got) > MaxAdditionalContext || !strings.HasSuffix(got, "[additional context truncated]") || !strings.HasPrefix(got, "éé") {
		t.Fatalf("bounded length %d suffix %q", len(got), got[len(got)-40:])
	}
	if strings.ContainsRune(got, '�') {
		t.Fatal("cut inside a rune")
	}
	if boundContext("short") != "short" {
		t.Fatal("short text changed")
	}
}

func TestStopHookActivePayload(t *testing.T) {
	data, _ := json.Marshal(Event{Name: Stop, StopHookActive: true})
	if string(data) != `{"event":"Stop","stopHookActive":true}` {
		t.Fatalf("payload %s", data)
	}
	if data, _ := json.Marshal(Event{Name: Stop}); string(data) != `{"event":"Stop"}` {
		t.Fatalf("payload %s", data)
	}
}

func TestLargeEventStillRunsHookAndStdinIsComplete(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "stdin.json")
	big := strings.Repeat("x", 1<<20)
	r := New(map[string][]config.Hook{PostToolUse: {{Command: "cat > " + out + " && printf %s \"$K_BRAIN_HOOK_EVENT\" | wc -c > " + out + ".env"}}})
	if err := r.Run(context.Background(), Event{Name: PostToolUse, CWD: dir, ToolName: "bash", ToolResult: big}); err != nil {
		t.Fatalf("hook with a 1 MiB event failed: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var got Event
	if err := json.Unmarshal(data, &got); err != nil || got.ToolResult != big {
		t.Fatalf("stdin event incomplete: err=%v len=%d", err, len(got.ToolResult))
	}
	env := envPayload(Event{Name: PostToolUse, ToolResult: big}, data)
	if len(env) > MaxEnvPayload || !strings.Contains(env, `"truncated":true`) {
		t.Fatalf("env payload len=%d, want <= %d with truncated flag", len(env), MaxEnvPayload)
	}
}
