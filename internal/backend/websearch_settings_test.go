package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/permissions"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const fixtureWebSearchKey = "fixture-web-search-key-42"

func clearWebSearchEnvBackend(t *testing.T) {
	t.Helper()
	for _, name := range []string{config.EnvWebSearchProvider, config.EnvWebSearchAPIKey, config.EnvWebSearchBaseURL,
		"TAVILY_API_KEY", "BRAVE_SEARCH_API_KEY", "BOCHA_API_KEY", "JINA_API_KEY"} {
		t.Setenv(name, "")
	}
}

type webSearchSettingsView struct {
	Provider         string   `json:"provider"`
	BaseURL          string   `json:"baseUrl"`
	MaxResults       int      `json:"maxResults"`
	APIKeyConfigured bool     `json:"apiKeyConfigured"`
	APIKeySource     string   `json:"apiKeySource"`
	Available        bool     `json:"available"`
	Reason           string   `json:"reason"`
	Providers        []string `json:"providers"`
}

type webSearchSettingsFixture struct {
	server *Server
	saved  *config.Config
}

func newWebSearchSettingsServer(t *testing.T, cfg *config.Config) *webSearchSettingsFixture {
	t.Helper()
	fx := &webSearchSettingsFixture{}
	fx.server = &Server{settings: NewSettingsStore(cfg, func(c *config.Config) error {
		fx.saved = c.Snapshot()
		return nil
	})}
	return fx
}

func (fx *webSearchSettingsFixture) do(t *testing.T, method, body string) (int, string, webSearchSettingsView) {
	t.Helper()
	req := httptest.NewRequest(method, "/v1/settings", strings.NewReader(body))
	out := httptest.NewRecorder()
	fx.server.ServeHTTP(out, req)
	var wire struct {
		WebSearch *webSearchSettingsView `json:"webSearch"`
	}
	if out.Code == http.StatusOK {
		if err := json.Unmarshal(out.Body.Bytes(), &wire); err != nil || wire.WebSearch == nil {
			t.Fatalf("settings body without webSearch: %v %s", err, out.Body.String())
		}
		return out.Code, out.Body.String(), *wire.WebSearch
	}
	return out.Code, out.Body.String(), webSearchSettingsView{}
}

func TestSettingsWebSearchRoundTrip(t *testing.T) {
	clearWebSearchEnvBackend(t)
	fx := newWebSearchSettingsServer(t, &config.Config{})

	code, _, view := fx.do(t, http.MethodGet, "")
	if code != http.StatusOK || view.Available || view.APIKeyConfigured || !strings.Contains(view.Reason, "no web search provider") {
		t.Fatalf("empty GET = %d %+v", code, view)
	}
	if !slices.Equal(view.Providers, config.WebSearchProviders) {
		t.Fatalf("providers = %v", view.Providers)
	}

	code, body, view := fx.do(t, http.MethodPut, `{"webSearch":{"provider":" Tavily ","apiKey":"`+fixtureWebSearchKey+`","maxResults":7}}`)
	if code != http.StatusOK {
		t.Fatalf("PUT = %d %s", code, body)
	}
	if strings.Contains(body, fixtureWebSearchKey) {
		t.Fatal("PUT response echoes the key")
	}
	if view.Provider != "tavily" || view.MaxResults != 7 || !view.APIKeyConfigured || view.APIKeySource != "config" || !view.Available || view.Reason != "" {
		t.Fatalf("PUT view = %+v", view)
	}
	if fx.saved == nil || fx.saved.WebSearch.APIKey != fixtureWebSearchKey || fx.saved.WebSearch.Provider != "tavily" {
		t.Fatalf("saved = %+v", fx.saved)
	}

	// The webSearch section of a GET body can be PUT back without losing the stored key.
	code, getBody, _ := fx.do(t, http.MethodGet, "")
	if code != http.StatusOK || strings.Contains(getBody, fixtureWebSearchKey) {
		t.Fatalf("GET = %d, leaks key: %t", code, strings.Contains(getBody, fixtureWebSearchKey))
	}
	var section struct {
		WebSearch json.RawMessage `json:"webSearch"`
	}
	if err := json.Unmarshal([]byte(getBody), &section); err != nil {
		t.Fatal(err)
	}
	code, body, view = fx.do(t, http.MethodPut, `{"webSearch":`+string(section.WebSearch)+`}`)
	if code != http.StatusOK || !view.APIKeyConfigured || fx.saved.WebSearch.APIKey != fixtureWebSearchKey || view.MaxResults != 7 {
		t.Fatalf("round trip = %d %s", code, body)
	}

	// An environment key takes over and is reported as such.
	t.Setenv("TAVILY_API_KEY", "env-key-not-shown")
	code, body, view = fx.do(t, http.MethodGet, "")
	if code != http.StatusOK || view.APIKeySource != "env" || strings.Contains(body, "env-key-not-shown") {
		t.Fatalf("env GET = %d %+v", code, view)
	}
	t.Setenv("TAVILY_API_KEY", "")

	code, body, view = fx.do(t, http.MethodPut, `{"webSearch":{"clearApiKey":true}}`)
	if code != http.StatusOK || view.APIKeyConfigured || view.Available || !strings.Contains(view.Reason, "no API key for tavily") {
		t.Fatalf("clear = %d %s", code, body)
	}
	if fx.saved.WebSearch.APIKey != "" {
		t.Fatal("key not cleared")
	}

	code, body, view = fx.do(t, http.MethodPut, `{"webSearch":{"provider":"searxng","baseUrl":"http://127.0.0.1:8888"}}`)
	if code != http.StatusOK || !view.Available || view.BaseURL != "http://127.0.0.1:8888" {
		t.Fatalf("searxng = %d %s", code, body)
	}
}

func TestSettingsWebSearchRejectsInvalid(t *testing.T) {
	clearWebSearchEnvBackend(t)
	fx := newWebSearchSettingsServer(t, &config.Config{WebSearch: config.WebSearchConfig{Provider: "brave", APIKey: fixtureWebSearchKey}})
	for body, want := range map[string]string{
		`{"webSearch":{"provider":"nope"}}`:                   "webSearch.provider must be one of",
		`{"webSearch":{"maxResults":11}}`:                     "webSearch.maxResults must be between 0 and 10",
		`{"webSearch":{"apiKey":"!cat /etc/passwd"}}`:         "API keys cannot execute commands",
		`{"webSearch":{"apiKey":"other","clearApiKey":true}}`: "cannot replace and clear",
		`{"webSearch":{"baseUrl":"ftp://x.example"}}`:         "webSearch.baseUrl",
		`{"webSearch":{"provider":"brave","secret":"x"}}`:     "invalid settings request",
	} {
		code, out, _ := fx.do(t, http.MethodPut, body)
		if code != http.StatusBadRequest || !strings.Contains(out, want) {
			t.Errorf("%s: %d %s, want %q", body, code, out, want)
		}
		if strings.Contains(out, fixtureWebSearchKey) {
			t.Errorf("%s: response leaks the key", body)
		}
	}
	if fx.saved != nil {
		t.Fatal("an invalid update was saved")
	}
}

func TestSettingsProviderNativeWebSearchSupported(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.Provider{
		"chat":      {API: "openai-completions", BaseURL: "https://chat.example/v1"},
		"responses": {API: ai.APIResponses, BaseURL: "https://responses.example/v1"},
		"messages":  {API: ai.APIMessages, BaseURL: "https://messages.example"},
		"relay":     {API: ai.APIGemini, BaseURL: "https://gemini-relay.example"},
	}}
	fx := newWebSearchSettingsServer(t, cfg)
	out := httptest.NewRecorder()
	fx.server.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/v1/settings", nil))
	var wire struct {
		Providers []struct {
			ID                       string `json:"id"`
			NativeWebSearchSupported *bool  `json:"nativeWebSearchSupported"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"chat": false, "responses": true, "messages": true, "relay": false}
	for _, p := range wire.Providers {
		if p.NativeWebSearchSupported == nil || *p.NativeWebSearchSupported != want[p.ID] {
			t.Fatalf("%s nativeWebSearchSupported = %v", p.ID, p.NativeWebSearchSupported)
		}
	}
	if len(wire.Providers) != len(want) {
		t.Fatalf("providers = %d", len(wire.Providers))
	}
	// The read-only field is accepted on PUT.
	code, body, _ := fx.do(t, http.MethodPut, `{"providers":[{"id":"chat","nativeWebSearchSupported":true}]}`)
	if code != http.StatusOK {
		t.Fatalf("PUT read-only field = %d %s", code, body)
	}
}

func fallbackAgent() *agent.Agent {
	a := agent.New(nil, "model", 100, "system")
	a.Tools = []tools.Tool{{Def: ai.NewTool("Read", "read", `{"type":"object"}`)}}
	a.WebSearchFallback = func() (tools.Tool, bool) {
		return tools.Tool{Def: ai.NewTool(tools.WebSearchToolName, "search", `{"type":"object"}`), Run: func(context.Context, json.RawMessage) (string, error) { return "", nil }}, true
	}
	return a
}

func toolNames(ts []tools.Tool) []string {
	var names []string
	for _, t := range ts {
		names = append(names, t.Def.Function.Name)
	}
	return names
}

func TestApplyRunOptionsWebSearchFallback(t *testing.T) {
	cases := []struct {
		name    string
		options protocol.RunOptions
		want    []string
	}{
		{"agent search on", protocol.RunOptions{Mode: "agent", Search: "enabled"}, []string{"Read", tools.WebSearchToolName}},
		{"agent search off", protocol.RunOptions{Mode: "agent", Search: "disabled"}, []string{"Read"}},
		{"chat search on", protocol.RunOptions{Mode: "chat", Search: "enabled"}, []string{tools.WebSearchToolName}},
		{"chat search off", protocol.RunOptions{Mode: "chat", Search: "disabled"}, nil},
		{"denied", protocol.RunOptions{Mode: "agent", Search: "enabled", Tools: &protocol.ToolSelection{Policies: map[string]string{tools.WebSearchToolName: "deny"}}}, []string{"Read"}},
		{"allowed", protocol.RunOptions{Mode: "chat", Search: "enabled", Tools: &protocol.ToolSelection{Policies: map[string]string{tools.WebSearchToolName: "allow"}}}, []string{tools.WebSearchToolName}},
		{"plan", protocol.RunOptions{Mode: "agent", Search: "enabled", PlanModeEnabled: true}, []string{"Read", tools.WebSearchToolName, "ExitPlanMode"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := fallbackAgent()
			restore := applyRunOptions(a, tc.options)
			got := toolNames(a.AllTools())
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("tools = %v, want %v", got, want)
			}
			restore()
			if a.RunToolsSet || a.NativeWebSearch {
				t.Fatal("run options not restored")
			}
			if slices.Contains(toolNames(a.AllTools()), tools.WebSearchToolName) {
				t.Fatal("web_search outlived the run")
			}
		})
	}
}

func TestNormalizeRunOptionsAcceptsWebSearchPolicy(t *testing.T) {
	root := t.TempDir()
	available := []tools.Tool{{Def: ai.NewTool("Read", "read", `{"type":"object"}`)}}
	for _, policy := range []string{"allow", "ask", "deny"} {
		got, err := normalizeRunOptions(&protocol.RunOptions{Search: "enabled", Tools: &protocol.ToolSelection{Policies: map[string]string{tools.WebSearchToolName: policy}}}, root, available)
		if err != nil || got.Tools.Policies[tools.WebSearchToolName] != policy {
			t.Fatalf("%s: %+v %v", policy, got.Tools, err)
		}
	}
}

func TestRunToolGateWebSearch(t *testing.T) {
	asks := 0
	ask := func(tools.GateRequest) (tools.GateDecision, string) {
		asks++
		return tools.GateAllowOnce, "asked"
	}
	req := tools.GateRequest{Tool: tools.WebSearchToolName, Command: "news"}
	// Plan mode asks instead of refusing, as for the provider's own search.
	if d, _ := runToolGate(protocol.RunOptions{ApprovalPolicy: "ask", PlanModeEnabled: true}, req, permissions.Decision{}, ask); d != tools.GateAllowOnce || asks != 1 {
		t.Fatalf("plan mode: decision=%v asks=%d", d, asks)
	}
	if d, _ := runToolGate(protocol.RunOptions{ApprovalPolicy: "ask", Tools: &protocol.ToolSelection{Policies: map[string]string{tools.WebSearchToolName: "allow"}}}, req, permissions.Decision{}, ask); d != tools.GateAllowOnce || asks != 1 {
		t.Fatalf("allow policy: decision=%v asks=%d", d, asks)
	}
	if d, _ := runToolGate(protocol.RunOptions{ApprovalPolicy: "ask", Tools: &protocol.ToolSelection{Policies: map[string]string{tools.WebSearchToolName: "deny"}}}, req, permissions.Decision{}, ask); d != tools.GateReject {
		t.Fatalf("deny policy: decision=%v", d)
	}
	if d, _ := runToolGate(protocol.RunOptions{ApprovalPolicy: "ask"}, req, permissions.Decision{}, ask); d != tools.GateAllowOnce || asks != 2 {
		t.Fatalf("default: decision=%v asks=%d", d, asks)
	}
}

func TestAttachWebSearchFollowsLiveSettings(t *testing.T) {
	clearWebSearchEnvBackend(t)
	fx := newWebSearchSettingsServer(t, &config.Config{})
	a := agent.New(nil, "model", 100, "system")
	fx.server.attachWebSearch(a)
	a.NativeWebSearch = true
	if _, ok := a.WebSearchFallbackTool(); ok {
		t.Fatal("offered before configuration")
	}
	if code, body, _ := fx.do(t, http.MethodPut, `{"webSearch":{"provider":"jina","apiKey":"`+fixtureWebSearchKey+`"}}`); code != http.StatusOK {
		t.Fatalf("PUT = %d %s", code, body)
	}
	if _, ok := a.WebSearchFallbackTool(); !ok {
		t.Fatal("saved settings did not apply to the session agent")
	}
}
