package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// KB-3: reading the output of a process or terminal the session started is a poll;
// the gate request says so, both from the tool itself and from GateRequestFor (the
// AI review hook), so the permission rules can treat it as read-only.
func TestProcessAndTerminalReadsAreGatedAsPolls(t *testing.T) {
	root := t.TempDir()
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), root), []WorkspaceRoot{{Path: root, Access: "write"}})
	catalog := LiveAgentCatalog()
	managed := findToolForTest(catalog, "ManagedProcess")
	out, err := managed.Run(ctx, json.RawMessage(`{"action":"start","command":"printf hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	id := fieldValue(out, "process_id")
	defer func() {
		_, _ = findToolForTest(catalog, "ProcessStop").Run(ctx, mustJSON(map[string]any{"session_id": id}))
	}()

	type seen struct {
		tool, command string
		poll          bool
	}
	run := func(tool string, args map[string]any) []seen {
		var got []seen
		gated := WithGate(ctx, func(req GateRequest) (GateDecision, string) {
			got = append(got, seen{req.Tool, req.Command, req.Poll})
			return GateAllowOnce, ""
		})
		_, _ = findToolForTest(catalog, tool).Run(gated, mustJSON(args))
		return got
	}
	for _, c := range []struct {
		name string
		tool string
		args map[string]any
		poll bool
	}{
		{"ProcessWait", "ProcessWait", map[string]any{"session_id": id, "yield_time_ms": 2000}, true},
		{"status", "ManagedProcess", map[string]any{"action": "status", "process_id": id}, true},
		{"status of all", "ManagedProcess", map[string]any{"action": "status"}, true},
		{"read_log", "ManagedProcess", map[string]any{"action": "read_log", "process_id": id}, true},
		{"wait", "ManagedProcess", map[string]any{"action": "wait", "process_id": id, "yield_time_ms": 2000}, true},
		{"ReadTerminal", "ReadTerminal", map[string]any{"session_id": "term-unknown"}, true},
		{"start", "ManagedProcess", map[string]any{"action": "start", "command": "printf again"}, false},
		{"stop", "ProcessStop", map[string]any{"session_id": id}, false},
	} {
		got := run(c.tool, c.args)
		if len(got) == 0 {
			t.Fatalf("%s: the gate was not consulted", c.name)
		}
		for _, g := range got {
			if g.poll != c.poll {
				t.Errorf("%s: gate request %+v, want poll=%v", c.name, g, c.poll)
			}
		}
	}
}

func TestGateRequestForMarksPolls(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		tool    string
		args    string
		command string
		poll    bool
	}{
		{"ProcessWait", `{"session_id":"bash-1"}`, "bash-1", true},
		{"ProcessWait", `{"process_id":"mp-2"}`, "mp-2", true},
		{"ManagedProcess", `{"action":"status","process_id":"mp-1"}`, "mp-1", true},
		{"ManagedProcess", `{"action":"status"}`, "status", true},
		{"ManagedProcess", `{"action":"read_log","process_id":"mp-1"}`, "mp-1", true},
		{"ManagedProcess", `{"action":"wait","process_id":"mp-1"}`, "mp-1", true},
		{"ReadTerminal", `{"session_id":"term-1"}`, "term-1", true},
		{"ManagedProcess", `{"action":"start","command":"npm test"}`, "npm test", false},
		{"ManagedProcess", `{"action":"stop","process_id":"mp-1"}`, "mp-1", false},
		{"ProcessStop", `{"session_id":"mp-1"}`, "mp-1", false},
	} {
		req := GateRequestFor(ctx, c.tool, json.RawMessage(c.args))
		if req.Poll != c.poll || req.Command != c.command {
			t.Errorf("GateRequestFor(%s, %s) = command %q poll %v, want %q %v", c.tool, c.args, req.Command, req.Poll, c.command, c.poll)
		}
	}
}

func TestCwdArgumentMarksOtherDir(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "vendor-a")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), root), []WorkspaceRoot{{Path: root, Access: "write"}})
	for _, c := range []struct {
		cwd   string
		other bool
	}{{"", false}, {".", false}, {root, false}, {"vendor-a", true}, {sub, true}} {
		var got []GateRequest
		gated := WithGate(ctx, func(req GateRequest) (GateDecision, string) {
			got = append(got, req)
			return GateReject, "stop here"
		})
		args := map[string]any{"command": "git status --short"}
		if c.cwd != "" {
			args["cwd"] = c.cwd
		}
		_, _ = findToolForTest(LiveAgentCatalog(), "Bash").Run(gated, mustJSON(args))
		if len(got) != 1 || got[0].OtherDir != c.other {
			t.Errorf("Bash cwd=%q: gate requests %+v, want OtherDir=%v", c.cwd, got, c.other)
		}
		if req := GateRequestFor(ctx, "Bash", mustJSON(args)); req.OtherDir != c.other {
			t.Errorf("GateRequestFor(Bash cwd=%q).OtherDir = %v, want %v", c.cwd, req.OtherDir, c.other)
		}
		start := map[string]any{"action": "start", "command": "git status --short"}
		if c.cwd != "" {
			start["cwd"] = c.cwd
		}
		if req := GateRequestFor(ctx, "ManagedProcess", mustJSON(start)); req.OtherDir != c.other {
			t.Errorf("GateRequestFor(ManagedProcess start cwd=%q).OtherDir = %v, want %v", c.cwd, req.OtherDir, c.other)
		}
	}
}
