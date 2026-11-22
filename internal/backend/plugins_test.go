package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/resources"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func writePlugin(t *testing.T, dir string, manifest map[string]any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// pluginFixture has a user plugin and a workspace plugin in an untrusted
// folder.
type pluginFixture struct {
	home, workdir string
	server        *Server
}

func newPluginFixture(t *testing.T) *pluginFixture {
	t.Helper()
	isolateUserDir(t)
	t.Setenv("K_BRAIN_HOME", "")
	home, err := config.Dir()
	if err != nil {
		t.Fatal(err)
	}
	workdir, err := resources.CanonicalWorkdir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writePlugin(t, filepath.Join(home, "plugins", "notes"), map[string]any{
		"name": "notes", "version": "1.2.0", "description": "Note taking", "command": []string{"notes"}, "enabled": true,
		"prompt": "USER-PLUGIN-PROMPT", "tools": []map[string]any{{"name": "add", "description": "add a note"}},
	})
	writePlugin(t, filepath.Join(workdir, ".liveagent", "plugins", "build"), map[string]any{
		"name": "build", "command": []string{"build"}, "enabled": true, "prompt": "WORKSPACE-PLUGIN-PROMPT",
		"hooks": map[string]string{"SessionStart": "true", "Stop": ""}, "tools": []map[string]any{{"name": "run", "description": "run the build"}},
	})
	return &pluginFixture{home: home, workdir: workdir, server: &Server{token: "plugin-token", plugins: NewPluginHost()}}
}

func (f *pluginFixture) request(t *testing.T, method, path, body string, status int) pluginList {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer plugin-token")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	var out pluginList
	if status == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func pluginByID(list pluginList, id string) *pluginView {
	for i := range list.Plugins {
		if list.Plugins[i].ID == id {
			return &list.Plugins[i]
		}
	}
	return nil
}

func TestPluginRoutesRespectFolderTrust(t *testing.T) {
	f := newPluginFixture(t)
	query := "/v1/plugins?workdir=" + url.QueryEscape(f.workdir)
	list := f.request(t, http.MethodGet, query, "", http.StatusOK)
	if list.Trusted || pluginByID(list, "build") != nil {
		t.Fatalf("untrusted workspace listed its plugins: %+v", list)
	}
	notes := pluginByID(list, "notes")
	if notes == nil || notes.Source != "user" || !notes.Enabled || notes.Version != "1.2.0" ||
		!slices.Equal(notes.Contributes.Tools, []string{"plugin_notes_add"}) || !notes.Contributes.Prompt {
		t.Fatalf("user plugin = %+v", notes)
	}
	if err := config.Trust(f.workdir); err != nil {
		t.Fatal(err)
	}
	list = f.request(t, http.MethodGet, query, "", http.StatusOK)
	build := pluginByID(list, "build")
	if !list.Trusted || build == nil || build.Source != "workspace" || !slices.Equal(build.Contributes.Hooks, []string{"SessionStart"}) {
		t.Fatalf("trusted listing = %+v", list)
	}
	// Removing trust hides the workspace plugin again.
	if err := config.Untrust(f.workdir); err != nil {
		t.Fatal(err)
	}
	if list = f.request(t, http.MethodGet, query, "", http.StatusOK); pluginByID(list, "build") != nil {
		t.Fatal("workspace plugin listed after trust was revoked")
	}
}

func TestPluginEnableDisableInstallRemove(t *testing.T) {
	f := newPluginFixture(t)
	if err := config.Trust(f.workdir); err != nil {
		t.Fatal(err)
	}
	body := `{"workdir":` + strconvQuote(f.workdir) + `}`
	start := f.request(t, http.MethodGet, "/v1/plugins", "", http.StatusOK).Revision
	list := f.request(t, http.MethodPost, "/v1/plugins/build/disable", body, http.StatusOK)
	if p := pluginByID(list, "build"); p == nil || p.Enabled || list.Revision <= start {
		t.Fatalf("disable: %+v", list)
	}
	list = f.request(t, http.MethodPost, "/v1/plugins/build/enable", body, http.StatusOK)
	if p := pluginByID(list, "build"); p == nil || !p.Enabled {
		t.Fatalf("enable: %+v", list)
	}
	f.request(t, http.MethodPost, "/v1/plugins/missing/enable", body, http.StatusNotFound)
	f.request(t, http.MethodDelete, "/v1/plugins/build?workdir="+url.QueryEscape(f.workdir), "", http.StatusConflict)

	src := filepath.Join(t.TempDir(), "lint")
	writePlugin(t, src, map[string]any{"name": "lint", "command": []string{"lint"}})
	f.request(t, http.MethodPost, "/v1/plugins/install", `{"path":"relative/dir"}`, http.StatusBadRequest)
	list = f.request(t, http.MethodPost, "/v1/plugins/install", `{"path":`+strconvQuote(src)+`}`, http.StatusOK)
	if p := pluginByID(list, "lint"); p == nil || p.Source != "user" || p.Enabled {
		t.Fatalf("install: %+v", list)
	}
	f.request(t, http.MethodPost, "/v1/plugins/install", `{"path":`+strconvQuote(src)+`}`, http.StatusConflict)
	list = f.request(t, http.MethodDelete, "/v1/plugins/lint", "", http.StatusOK)
	if pluginByID(list, "lint") != nil {
		t.Fatalf("remove: %+v", list)
	}
	if _, err := os.Stat(filepath.Join(f.home, "plugins", "lint")); !os.IsNotExist(err) {
		t.Fatalf("removed plugin still on disk: %v", err)
	}
	f.request(t, http.MethodPost, "/v1/plugins/reload", "", http.StatusOK)
	f.request(t, http.MethodGet, "/v1/plugins?workdir=relative", "", http.StatusBadRequest)
	f.request(t, http.MethodPut, "/v1/plugins", "", http.StatusNotFound)
	(&pluginFixture{server: &Server{token: "plugin-token"}}).request(t, http.MethodGet, "/v1/plugins", "", http.StatusNotImplemented)
}

func strconvQuote(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

func TestPluginListLongPollsForChanges(t *testing.T) {
	f := newPluginFixture(t)
	revision := f.request(t, http.MethodGet, "/v1/plugins", "", http.StatusOK).Revision
	// Nothing changes: the wait times out with the same revision.
	started := time.Now()
	if got := f.request(t, http.MethodGet, "/v1/plugins?since="+itoa(revision)+"&wait=1", "", http.StatusOK).Revision; got != revision {
		t.Fatalf("idle revision = %d, want %d", got, revision)
	}
	if time.Since(started) < 900*time.Millisecond {
		t.Fatal("long poll returned before its wait elapsed")
	}
	done := make(chan pluginList, 1)
	go func() {
		done <- f.request(t, http.MethodGet, "/v1/plugins?since="+itoa(revision)+"&wait=30", "", http.StatusOK)
	}()
	time.Sleep(100 * time.Millisecond)
	f.request(t, http.MethodPost, "/v1/plugins/notes/disable", "", http.StatusOK)
	select {
	case list := <-done:
		if list.Revision <= revision || pluginByID(list, "notes").Enabled {
			t.Fatalf("woken listing = %+v", list)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("long poll was not woken by the change")
	}
	// A change made outside the backend (the kn CLI, by hand) advances the
	// revision on the next listing.
	revision = f.request(t, http.MethodGet, "/v1/plugins", "", http.StatusOK).Revision
	writePlugin(t, filepath.Join(f.home, "plugins", "extra"), map[string]any{"name": "extra", "command": []string{"x"}})
	if list := f.request(t, http.MethodGet, "/v1/plugins", "", http.StatusOK); list.Revision <= revision || pluginByID(list, "extra") == nil {
		t.Fatalf("external change: %+v", list)
	}
}

func itoa(n uint64) string { return strconv.FormatUint(n, 10) }

// pluginModel records the system prompt and tool names of each request.
type pluginModel struct {
	scriptedClient
	mu      sync.Mutex
	prompts []string
	tools   [][]string
}

func (c *pluginModel) Clone() ai.Client { return c }
func (c *pluginModel) Stream(_ context.Context, request ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	var names []string
	for _, tool := range request.Tools {
		names = append(names, tool.Function.Name)
	}
	c.mu.Lock()
	c.prompts = append(c.prompts, request.Messages[0].Content)
	c.tools = append(c.tools, names)
	c.mu.Unlock()
	text("ok")
	return ai.Message{Role: "assistant", Content: "ok", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

func (c *pluginModel) last() (string, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.prompts[len(c.prompts)-1], c.tools[len(c.tools)-1]
}

func TestPluginsReachSessionsAndFollowTrustPerTurn(t *testing.T) {
	f := newPluginFixture(t)
	model := &pluginModel{}
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	backendServer, err := New(Options{Store: store, EventDir: t.TempDir(), Plugins: f.server.plugins,
		Models: []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}},
		Factory: func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
			return agent.New(model, m.Model, 1024, "system", agent.WithSystemPromptResolver(func() (string, error) { return "system", nil })), nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(backendServer)
	t.Cleanup(func() { httpServer.Close(); _ = backendServer.Close() })
	var sess protocol.Session
	resp := postJSON(t, http.DefaultClient, httpServer.URL+"/v1/sessions", protocol.CreateSessionRequest{CWD: f.workdir, Model: protocol.ModelRef{Provider: "fixture", Model: "fixture-model"}}, &sess)
	resp.Body.Close()
	turn := func(prompt string) (string, []string) {
		t.Helper()
		var run protocol.RunAccepted
		resp := postJSON(t, http.DefaultClient, httpServer.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: prompt, Prompt: prompt}, &run)
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("run status %d", resp.StatusCode)
		}
		waitForRunTerminal(t, httpServer.URL, sess.ID, run.RunID)
		return model.last()
	}
	prompt, names := turn("untrusted")
	if !strings.Contains(prompt, "USER-PLUGIN-PROMPT") || strings.Contains(prompt, "WORKSPACE-PLUGIN-PROMPT") ||
		!slices.Contains(names, "plugin_notes_add") || slices.Contains(names, "plugin_build_run") {
		t.Fatalf("untrusted turn: prompt %q tools %v", prompt, names)
	}
	if err := config.Trust(f.workdir); err != nil {
		t.Fatal(err)
	}
	prompt, names = turn("trusted")
	if !strings.Contains(prompt, "WORKSPACE-PLUGIN-PROMPT") || !slices.Contains(names, "plugin_build_run") {
		t.Fatalf("trusted turn: prompt %q tools %v", prompt, names)
	}
	f.request(t, http.MethodPost, "/v1/plugins/notes/disable", "", http.StatusOK)
	prompt, names = turn("disabled")
	if strings.Contains(prompt, "USER-PLUGIN-PROMPT") || slices.Contains(names, "plugin_notes_add") {
		t.Fatalf("disabled plugin still offered: prompt %q tools %v", prompt, names)
	}
}

func TestPluginToolsAskPermissionAndRecheckState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture plugin is a shell script")
	}
	f := newPluginFixture(t)
	dir := filepath.Join(f.home, "plugins", "echo")
	writePlugin(t, dir, map[string]any{"name": "echo", "command": []string{"./run.sh"}, "enabled": true, "tools": []map[string]any{{"name": "ping"}}})
	script := "#!/bin/sh\nread line\necho '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":\"pong\"}}'\n"
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	var ping func(context.Context, json.RawMessage) (string, error)
	for _, tool := range f.server.plugins.Tools(f.workdir) {
		if tool.Def.Function.Name == "plugin_echo_ping" {
			ping = tool.Run
		}
	}
	if ping == nil {
		t.Fatal("plugin tool not offered")
	}
	var asked []string
	gate := func(decision tools.GateDecision) context.Context {
		return tools.WithGate(t.Context(), func(req tools.GateRequest) (tools.GateDecision, string) {
			asked = append(asked, req.Tool)
			if decision == tools.GateReject {
				return decision, "rejected by the user"
			}
			return decision, ""
		})
	}
	if _, err := ping(gate(tools.GateReject), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("rejected call: %v", err)
	}
	if out, err := ping(gate(tools.GateAllowOnce), json.RawMessage(`{}`)); err != nil || out != "pong" {
		t.Fatalf("allowed call: %q %v", out, err)
	}
	if !slices.Equal(asked, []string{"plugin_echo_ping", "plugin_echo_ping"}) {
		t.Fatalf("gate saw %v", asked)
	}
	f.request(t, http.MethodPost, "/v1/plugins/echo/disable", "", http.StatusOK)
	if _, err := ping(gate(tools.GateAllowOnce), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("call after disable: %v", err)
	}
}
