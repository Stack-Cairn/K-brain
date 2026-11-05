package backend

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/permissions"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func stubSandboxForRun(t *testing.T, fn func(*sandbox.Policy, sandbox.RunRequest) (*sandbox.Policy, error)) {
	t.Helper()
	old := sandboxForRun
	sandboxForRun = fn
	t.Cleanup(func() { sandboxForRun = old })
}

func TestNormalizeRunSandbox(t *testing.T) {
	options, err := normalizeRunOptions(&protocol.RunOptions{Sandbox: &protocol.RunSandbox{}}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.Sandbox == nil || options.Sandbox.Mode != "workspace" || options.Sandbox.Network {
		t.Fatalf("sandbox = %+v, want workspace offline", options.Sandbox)
	}
	if _, err := normalizeRunOptions(&protocol.RunOptions{Sandbox: &protocol.RunSandbox{Mode: "off"}}, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "options.sandbox.mode") {
		t.Fatalf("mode off accepted: %v", err)
	}
	in := &protocol.RunOptions{Sandbox: &protocol.RunSandbox{Mode: "workspace", Network: true}}
	options, err = normalizeRunOptions(in, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	options.Sandbox.Network = false
	if !in.Sandbox.Network {
		t.Fatal("normalized options share the caller's sandbox")
	}
	if options, _ = normalizeRunOptions(nil, t.TempDir(), nil); options.Sandbox != nil {
		t.Fatal("sandbox enabled without being requested")
	}
}

func TestRunSandboxGate(t *testing.T) {
	ask := func(prompted *bool) func(tools.GateRequest) (tools.GateDecision, string) {
		return func(tools.GateRequest) (tools.GateDecision, string) {
			*prompted = true
			return tools.GateReject, "asked"
		}
	}
	for _, tc := range []struct {
		name, mode, tool, policy string
		rule                     permissions.Decision
		sandboxed                bool
		allowed, prompted        bool
	}{
		{name: "normal bash asks without sandbox", mode: "normal", tool: "Bash", prompted: true},
		{name: "normal bash runs confined", mode: "normal", tool: "Bash", sandboxed: true, allowed: true},
		{name: "default mode runs confined", tool: "bash", sandboxed: true, allowed: true},
		{name: "auto_edit process runs confined", mode: "auto_edit", tool: "ManagedProcess", sandboxed: true, allowed: true},
		{name: "terminal runs confined", mode: "normal", tool: "TerminalSession", sandboxed: true, allowed: true},
		{name: "file write still asks", mode: "normal", tool: "Write", sandboxed: true, prompted: true},
		{name: "computer use still asks", mode: "auto_edit", tool: "computer_exec", sandboxed: true, prompted: true},
		{name: "mcp still asks", mode: "normal", tool: "mcp__x__y", sandboxed: true, prompted: true},
		{name: "per-tool ask wins", mode: "normal", tool: "Bash", policy: "ask", sandboxed: true, prompted: true},
		{name: "per-tool deny wins", mode: "normal", tool: "Bash", policy: "deny", sandboxed: true},
		{name: "deny rule wins", mode: "normal", tool: "Bash", rule: permissions.Decision{Effect: permissions.Deny, Rule: "Bash(rm *)"}, sandboxed: true},
		{name: "ask rule wins", mode: "normal", tool: "Bash", rule: permissions.Decision{Effect: permissions.Ask, Rule: "Bash(git push *)"}, sandboxed: true, prompted: true},
		{name: "auto_review keeps reviewing", mode: "auto_review", tool: "Bash", sandboxed: true, prompted: true},
		{name: "plan mode blocks", mode: "plan", tool: "Bash", sandboxed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := &protocol.RunOptions{PermissionMode: tc.mode}
			if tc.sandboxed {
				in.Sandbox = &protocol.RunSandbox{Mode: "workspace"}
			}
			options, err := normalizeRunOptions(in, t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.policy != "" {
				options.Tools = &protocol.ToolSelection{Policies: map[string]string{tc.tool: tc.policy}}
			}
			prompted := false
			decision, reason := runToolGate(options, tools.GateRequest{Tool: tc.tool}, tc.rule, ask(&prompted))
			if (decision == tools.GateAllowOnce) != tc.allowed || prompted != tc.prompted {
				t.Fatalf("decision=%v reason=%q prompted=%v", decision, reason, prompted)
			}
			if tc.allowed && reason != sandboxAllowReason {
				t.Fatalf("reason = %q", reason)
			}
		})
	}
}

func TestRunSandboxPolicyUsesWriteRoots(t *testing.T) {
	cwd, extra, read := t.TempDir(), t.TempDir(), t.TempDir()
	var got sandbox.RunRequest
	stubSandboxForRun(t, func(base *sandbox.Policy, req sandbox.RunRequest) (*sandbox.Policy, error) {
		got = req
		return &sandbox.Policy{Mode: "workspace", Root: req.Root}, nil
	})
	s := &Server{sandboxMasked: []string{"/data/liveagent"}, sandboxEnvKeep: []string{"NPM_TOKEN"}}
	configured := &sandbox.Policy{Mode: "workspace", Root: cwd}
	if p, err := s.runSandboxPolicy(configured, cwd, protocol.RunOptions{}); err != nil || p != configured {
		t.Fatalf("unsandboxed run changed the configured policy: %v %v", p, err)
	}
	options := protocol.RunOptions{
		Sandbox: &protocol.RunSandbox{Mode: "workspace", Network: true},
		WorkspaceRoots: []protocol.WorkspaceRoot{
			{Path: cwd, Access: "write"},
			{Path: extra, Access: "write"},
			{Path: read, Access: "read"},
		},
	}
	if _, err := s.runSandboxPolicy(configured, cwd, options); err != nil {
		t.Fatal(err)
	}
	if got.Root != cwd || !slices.Equal(got.Writable, []string{extra}) || !got.Network || !slices.Equal(got.Masked, []string{"/data/liveagent"}) || !slices.Equal(got.EnvKeep, []string{"NPM_TOKEN"}) {
		t.Fatalf("request = %+v", got)
	}
}

func TestSandboxedRunRefusedWhenUnavailable(t *testing.T) {
	stubSandboxForRun(t, func(*sandbox.Policy, sandbox.RunRequest) (*sandbox.Policy, error) {
		return nil, errors.New("sandbox is unavailable on this machine: no mechanism")
	})
	root := t.TempDir()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	client := &scriptedClient{response: "hello"}
	_, httpServer := newTestServer(t, store, filepath.Join(root, "events"), client)
	sess := createTestSession(t, httpServer.URL)
	resp := postJSON(t, http.DefaultClient, httpServer.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{
		ConversationID: sess.ID, ClientRequestID: "req-sandbox", Prompt: "hi",
		Options: &protocol.RunOptions{Sandbox: &protocol.RunSandbox{Mode: "workspace"}},
	}, nil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "options.sandbox: sandbox is unavailable") {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls != 0 {
		t.Fatalf("model called %d times for a refused run", calls)
	}
}

func TestHealthAdvertisesRunSandbox(t *testing.T) {
	caps := healthJSON(t, &Server{token: healthTestToken})["capabilities"].(map[string]any)
	got, ok := caps["run_sandbox"].(map[string]any)
	if !ok {
		t.Fatalf("run_sandbox = %#v", caps["run_sandbox"])
	}
	probe := sandbox.Probe()
	if got["supported"] != probe.Supported || got["version"] != runSandboxVersion || got["network_control"] != probe.NetworkControl {
		t.Fatalf("run_sandbox = %#v, probe %+v", got, probe)
	}
	if !probe.Supported && got["reason"] == "" {
		t.Fatal("unsupported sandbox without a reason")
	}
}
