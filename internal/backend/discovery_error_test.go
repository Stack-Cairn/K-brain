package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

const discoveryTestKey = "sk-discovery-fixture-0123456789"

// discoverAt runs POST /v1/settings/providers/p/models for a provider stored
// with the given base URL and returns the HTTP status and decoded body.
func discoverAt(t *testing.T, baseURL string) (int, map[string]any) {
	t.Helper()
	settings := NewSettingsStore(&config.Config{
		Providers: map[string]config.Provider{
			"p": {API: ai.APIChatCompletions, BaseURL: baseURL, APIKey: discoveryTestKey},
		},
		Models: map[string]config.Model{},
	}, nil)
	sessions, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.Close() })
	server, err := New(Options{
		Store: sessions,
		Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
			return agent.New(&settingsProviderTestClient{}, "m", 100, ""), nil
		},
		Settings: settings,
		EventDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(server)
	t.Cleanup(backend.Close)
	response, err := backend.Client().Post(backend.URL+"/v1/settings/providers/p/models", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("body %s: %v", raw, err)
	}
	return response.StatusCode, body
}

// A failed discovery says why: the provider's HTTP status, a stable errorCode
// and the provider's own short message, while keeping the 502 and the generic
// error text older clients show.
func TestProviderModelDiscoveryReportsUpstreamFailure(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		code    string
		message string
	}{
		{"unauthorized", 401, `{"error":{"message":"Incorrect API key provided: ` + discoveryTestKey + `.","type":"invalid_request_error"}}`, "invalid_api_key", "Incorrect API key provided: [redacted]."},
		{"forbidden", 403, `{"error":"key is not allowed to list models"}`, "invalid_api_key", "key is not allowed to list models"},
		{"not found", 404, `{"message":"no such route"}`, "not_found", "no such route"},
		{"rate limited", 429, `{"error":{"message":"Too many\n  requests"}}`, "rate_limited", "Too many requests"},
		{"server error", 500, `<html><body>Internal Server Error</body></html>`, "upstream_error", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			status, body := discoverAt(t, upstream.URL+"/v1")
			if status != http.StatusBadGateway {
				t.Fatalf("status = %d, body %v", status, body)
			}
			if body["error"] != "model discovery failed" || body["version"] != protocol.Version {
				t.Fatalf("generic fields changed: %v", body)
			}
			if body["errorCode"] != tc.code || body["upstreamStatus"] != float64(tc.status) {
				t.Fatalf("errorCode/upstreamStatus = %v/%v, want %s/%d", body["errorCode"], body["upstreamStatus"], tc.code, tc.status)
			}
			message, present := body["upstreamMessage"]
			if tc.message == "" && present || tc.message != "" && message != tc.message {
				t.Fatalf("upstreamMessage = %#v, want %q", message, tc.message)
			}
		})
	}
}

// A provider that cannot be reached at all reports unreachable and no status.
func TestProviderModelDiscoveryReportsUnreachableProvider(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	address := closed.URL
	closed.Close()
	status, body := discoverAt(t, address+"/v1")
	if status != http.StatusBadGateway || body["error"] != "model discovery failed" {
		t.Fatalf("status = %d, body %v", status, body)
	}
	if body["errorCode"] != "unreachable" {
		t.Fatalf("errorCode = %v", body["errorCode"])
	}
	if _, ok := body["upstreamStatus"]; ok {
		t.Fatalf("unreachable provider has upstreamStatus: %v", body)
	}
}

// A malformed success body is neither a key nor a network problem.
func TestProviderModelDiscoveryReportsMalformedCatalog(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":`)
	}))
	defer upstream.Close()
	status, body := discoverAt(t, upstream.URL+"/v1")
	if status != http.StatusBadGateway || body["errorCode"] != "upstream_error" {
		t.Fatalf("status = %d, body %v", status, body)
	}
}

func TestUpstreamMessageDropsSecretsAndLongText(t *testing.T) {
	p := config.Provider{APIKey: "plain-provider-key", CustomHeaders: []config.CustomHeader{{Key: "X-Token", Value: "header-secret-value"}}}
	got := ai.ProviderMessage(`{"error":{"message":"bad plain-provider-key and header-secret-value, token_abcdefgh1234 rejected"}}`, literalSecrets(p)...)
	if got != "bad [redacted] and [redacted], [redacted] rejected" {
		t.Fatalf("message = %q", got)
	}
	long := ai.ProviderMessage(`{"message":"` + strings.Repeat("word ", 100) + `"}`)
	if n := len([]rune(long)); n != 200 || !strings.HasSuffix(long, "…") {
		t.Fatalf("long message has %d runes: %q", n, long)
	}
}
