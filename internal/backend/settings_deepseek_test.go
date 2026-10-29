package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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

func TestDeepSeekModelDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name, base, catalog string
		draft, full         bool
		override            bool
		status              int
		message             string
	}{
		{name: "saved root", catalog: "/models"},
		{name: "saved v1", base: "/v1", catalog: "/v1/models"},
		{name: "new provider", catalog: "/models", draft: true},
		{name: "full completions", base: "/v1/chat/completions", catalog: "/v1/models", full: true},
		{name: "unversioned full completions", base: "/chat/completions", catalog: "/v1/models", full: true},
		{name: "prefixed full completions", base: "/tenant/chat/completions", catalog: "/tenant/v1/models", full: true},
		{name: "explicit catalog", base: "/chat/completions", catalog: "/catalog", full: true, override: true},
		{name: "authentication", catalog: "/models", status: 401, message: "provider rejected the API key (HTTP 401)"},
		{name: "forbidden", catalog: "/models", status: 403, message: "provider denied access to models (HTTP 403)"},
		{name: "rate limit", catalog: "/models", status: 429, message: "provider rate limit reached (HTTP 429); retry later"},
		{name: "unavailable", catalog: "/models", status: 503, message: "provider returned HTTP 503"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != tc.catalog || r.Header.Get("Authorization") != "Bearer fixture-key" {
					t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				if tc.status != 0 {
					http.Error(w, "do-not-expose-upstream-secrets", tc.status)
					return
				}
				_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"deepseek-chat","object":"model","owned_by":"deepseek"},{"id":"deepseek-reasoner","object":"model","owned_by":"deepseek"}]}`)
			}))
			defer upstream.Close()
			p := config.Provider{Type: "deepseek", BaseURL: upstream.URL + tc.base, APIKey: "fixture-key", IsFullURL: tc.full}
			if tc.override {
				p.ModelsURL = upstream.URL + tc.catalog
			}
			providers := map[string]config.Provider{}
			if !tc.draft {
				providers["deepseek"] = p
			}
			settings := NewSettingsStore(&config.Config{Providers: providers}, nil)
			sessions, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer sessions.Close()
			server, err := New(Options{Store: sessions, Settings: settings, EventDir: t.TempDir(), Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
				return agent.New(&settingsProviderTestClient{}, "m", 100, ""), nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			draft := map[string]any{"type": "deepseek", "requestFormat": "openai-responses", "baseUrl": p.BaseURL, "apiKey": "", "isFullUrl": tc.full}
			if tc.override {
				draft["modelsUrl"] = p.ModelsURL
			}
			if tc.draft {
				draft["apiKey"] = "fixture-key"
			}
			body, _ := json.Marshal(draft)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/settings/providers/deepseek/models", strings.NewReader(string(body))))
			if requests != 1 {
				t.Fatalf("upstream requests = %d", requests)
			}
			if tc.status != 0 {
				if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), tc.message) || strings.Contains(w.Body.String(), "do-not-expose") {
					t.Fatalf("error response = %d %s", w.Code, w.Body.String())
				}
				return
			}
			var result struct{ Models []settingsModel }
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK || len(result.Models) != 2 || result.Models[0].ID != "deepseek-chat" || result.Models[1].ID != "deepseek-reasoner" {
				t.Fatalf("discovery = %d %s (%v)", w.Code, w.Body.String(), err)
			}
		})
	}
}

func TestModelDiscoveryErrorSanitizesFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{name: "wrapped auth", err: fmt.Errorf("private: %w", &ai.HTTPError{StatusCode: 401, Body: "secret"}), want: "model discovery failed: provider rejected the API key (HTTP 401)"},
		{name: "missing route", err: &ai.HTTPError{StatusCode: 404, Body: "secret"}, want: "model discovery failed: model endpoint unavailable (HTTP 404); check Base URL and Models URL"},
		{name: "timeout", err: context.DeadlineExceeded, want: "model discovery failed: request timed out; check the connection and proxy settings"},
		{name: "network", err: &net.DNSError{Err: "secret", Name: "private.example"}, want: "model discovery failed: could not connect to provider; check Base URL and proxy settings"},
		{name: "unknown", err: errors.New("secret"), want: "model discovery failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelDiscoveryError(tc.err); got != tc.want {
				t.Fatalf("error = %q, want %q", got, tc.want)
			}
		})
	}
}
