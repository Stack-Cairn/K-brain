package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

const fixtureSearchKey = "fixture-search-key-0123456789"

// clearWebSearchEnv keeps the developer's environment out of the tests.
func clearWebSearchEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{config.EnvWebSearchProvider, config.EnvWebSearchAPIKey, config.EnvWebSearchBaseURL,
		"TAVILY_API_KEY", "BRAVE_SEARCH_API_KEY", "BOCHA_API_KEY", "JINA_API_KEY"} {
		t.Setenv(name, "")
	}
}

type seenRequest struct {
	Method string
	Path   string
	Query  map[string][]string
	Header http.Header
	Body   map[string]any
}

// searchServer answers every request with status and body and records what it got.
func searchServer(t *testing.T, status int, body string) (*httptest.Server, func() []seenRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []seenRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s := seenRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone()}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &s.Body)
		}
		mu.Lock()
		seen = append(seen, s)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []seenRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenRequest(nil), seen...)
	}
}

func runWebSearchTool(t *testing.T, ctx context.Context, cfg config.WebSearchConfig, args string) (string, error) {
	t.Helper()
	tool := WebSearchTool(func() config.WebSearchConfig { return cfg })
	return tool.Run(ctx, json.RawMessage(args))
}

func onlyRequest(t *testing.T, seen func() []seenRequest) seenRequest {
	t.Helper()
	got := seen()
	if len(got) != 1 {
		t.Fatalf("server saw %d requests", len(got))
	}
	return got[0]
}

func TestWebSearchTavily(t *testing.T) {
	clearWebSearchEnv(t)
	srv, seen := searchServer(t, 200, `{"results":[
		{"title":"Go 1.30 <b>released</b>","url":"https://go.dev/blog/go1.30","content":"The Go team &amp; friends.","published_date":"2026-08-01"},
		{"title":"dup","url":"https://go.dev/blog/go1.30","content":"same URL"},
		{"title":"bad","url":"javascript:alert(1)","content":"dropped"},
		{"title":"","url":"https://example.org/x","content":"no title"}]}`)
	out, err := runWebSearchTool(t, context.Background(),
		config.WebSearchConfig{Provider: "tavily", APIKey: fixtureSearchKey, BaseURL: srv.URL + "/"},
		`{"query":"  go   release ","max_results":3,"recency":"week","domains":["https://Go.dev/blog","*.example.org"]}`)
	if err != nil {
		t.Fatal(err)
	}
	r := onlyRequest(t, seen)
	if r.Method != http.MethodPost || r.Path != "/search" {
		t.Fatalf("request %s %s", r.Method, r.Path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+fixtureSearchKey {
		t.Fatalf("Authorization = %q", got)
	}
	if r.Body["query"] != "go release" || r.Body["max_results"] != float64(3) || r.Body["time_range"] != "week" || r.Body["include_answer"] != false {
		t.Fatalf("body = %v", r.Body)
	}
	if d := fmt.Sprint(r.Body["include_domains"]); d != "[go.dev example.org]" {
		t.Fatalf("include_domains = %s", d)
	}
	for _, want := range []string{
		"provider=tavily\n", "query=go release\n", "recency=week\n", "domains=go.dev,example.org\n", "results=2\n",
		"1. Go 1.30 released\n   https://go.dev/blog/go1.30\n   published: 2026-08-01\n   The Go team & friends.",
		"2. example.org\n   https://example.org/x",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "javascript:") || strings.Contains(out, fixtureSearchKey) {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestWebSearchBrave(t *testing.T) {
	clearWebSearchEnv(t)
	t.Setenv("BRAVE_SEARCH_API_KEY", fixtureSearchKey)
	srv, seen := searchServer(t, 200, `{"web":{"results":[{"title":"A","url":"https://a.example/1","description":"<strong>alpha</strong>","age":"2 days ago"}]}}`)
	out, err := runWebSearchTool(t, context.Background(),
		config.WebSearchConfig{Provider: "brave", BaseURL: srv.URL, MaxResults: 4},
		`{"query":"alpha","recency":"month","domains":["a.example","b.example"]}`)
	if err != nil {
		t.Fatal(err)
	}
	r := onlyRequest(t, seen)
	if r.Method != http.MethodGet || r.Path != "/res/v1/web/search" {
		t.Fatalf("request %s %s", r.Method, r.Path)
	}
	if r.Header.Get("X-Subscription-Token") != fixtureSearchKey || r.Header.Get("Authorization") != "" {
		t.Fatalf("headers = %v", r.Header)
	}
	if q := r.Query; q["q"][0] != "alpha (site:a.example OR site:b.example)" || q["count"][0] != "4" || q["freshness"][0] != "pm" {
		t.Fatalf("query = %v", q)
	}
	if !strings.Contains(out, "1. A\n   https://a.example/1\n   published: 2 days ago\n   alpha") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestWebSearchSearXNG(t *testing.T) {
	clearWebSearchEnv(t)
	srv, seen := searchServer(t, 200, `{"results":[{"title":"S","url":"http://s.example/","content":"searx","publishedDate":"2026-01-02T00:00:00"}]}`)
	out, err := runWebSearchTool(t, context.Background(),
		config.WebSearchConfig{Provider: "searxng", BaseURL: srv.URL},
		`{"query":"searx","recency":"year","domains":["s.example"]}`)
	if err != nil {
		t.Fatal(err)
	}
	r := onlyRequest(t, seen)
	if r.Method != http.MethodGet || r.Path != "/search" || r.Header.Get("Authorization") != "" {
		t.Fatalf("request %s %s %v", r.Method, r.Path, r.Header)
	}
	if q := r.Query; q["q"][0] != "searx site:s.example" || q["format"][0] != "json" || q["time_range"][0] != "year" || q["pageno"][0] != "1" {
		t.Fatalf("query = %v", q)
	}
	if !strings.Contains(out, "provider=searxng") || !strings.Contains(out, "1. S\n   http://s.example/") {
		t.Fatalf("output:\n%s", out)
	}

	// An optional key goes out as a bearer token.
	srv2, seen2 := searchServer(t, 200, `{"results":[]}`)
	out, err = runWebSearchTool(t, context.Background(),
		config.WebSearchConfig{Provider: "searxng", BaseURL: srv2.URL, APIKey: fixtureSearchKey}, `{"query":"none"}`)
	if err != nil {
		t.Fatal(err)
	}
	if onlyRequest(t, seen2).Header.Get("Authorization") != "Bearer "+fixtureSearchKey {
		t.Fatal("searxng key not sent")
	}
	if !strings.Contains(out, "results=0") || !strings.Contains(out, "No results.") {
		t.Fatalf("empty output:\n%s", out)
	}
}

func TestWebSearchBocha(t *testing.T) {
	clearWebSearchEnv(t)
	srv, seen := searchServer(t, 200, `{"code":200,"msg":null,"data":{"webPages":{"value":[
		{"name":"博查","url":"https://bocha.example/a","snippet":"short","summary":"long summary","datePublished":"2026-05-01T08:00:00+08:00"}]}}}`)
	out, err := runWebSearchTool(t, context.Background(),
		config.WebSearchConfig{Provider: "bocha", APIKey: fixtureSearchKey, BaseURL: srv.URL},
		`{"query":"天气","recency":"day","domains":["a.example","b.example"]}`)
	if err != nil {
		t.Fatal(err)
	}
	r := onlyRequest(t, seen)
	if r.Method != http.MethodPost || r.Path != "/v1/web-search" || r.Header.Get("Authorization") != "Bearer "+fixtureSearchKey {
		t.Fatalf("request %s %s", r.Method, r.Path)
	}
	if r.Body["query"] != "天气" || r.Body["freshness"] != "oneDay" || r.Body["include"] != "a.example|b.example" || r.Body["count"] != float64(5) || r.Body["summary"] != true {
		t.Fatalf("body = %v", r.Body)
	}
	if !strings.Contains(out, "1. 博查\n   https://bocha.example/a\n   published: 2026-05-01T08:00:00+08:00\n   long summary") {
		t.Fatalf("output:\n%s", out)
	}

	// Without recency the service is told not to limit by date.
	srv2, seen2 := searchServer(t, 200, `{"code":200,"data":{"webPages":{"value":[]}}}`)
	if _, err := runWebSearchTool(t, context.Background(), config.WebSearchConfig{Provider: "bocha", APIKey: fixtureSearchKey, BaseURL: srv2.URL}, `{"query":"x"}`); err != nil {
		t.Fatal(err)
	}
	if b := onlyRequest(t, seen2).Body; b["freshness"] != "noLimit" || b["include"] != nil {
		t.Fatalf("body = %v", b)
	}
}

func TestWebSearchBochaServiceError(t *testing.T) {
	clearWebSearchEnv(t)
	srv, _ := searchServer(t, 200, `{"code":403,"msg":"余额不足 for `+fixtureSearchKey+`"}`)
	_, err := runWebSearchTool(t, context.Background(), config.WebSearchConfig{Provider: "bocha", APIKey: fixtureSearchKey, BaseURL: srv.URL}, `{"query":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "Bocha reported error 403: 余额不足") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), fixtureSearchKey) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("key not redacted: %v", err)
	}
}

func TestWebSearchJina(t *testing.T) {
	clearWebSearchEnv(t)
	srv, seen := searchServer(t, 200, `{"code":200,"data":[{"title":"J","url":"https://j.example/","description":"","content":"from content","date":""}]}`)
	out, err := runWebSearchTool(t, context.Background(),
		config.WebSearchConfig{Provider: "jina", APIKey: fixtureSearchKey, BaseURL: srv.URL},
		`{"query":"jina","recency":"day","domains":["j.example"]}`)
	if err != nil {
		t.Fatal(err)
	}
	r := onlyRequest(t, seen)
	if r.Method != http.MethodGet || r.Path != "/" || r.Query["q"][0] != "jina site:j.example" {
		t.Fatalf("request %s %s %v", r.Method, r.Path, r.Query)
	}
	if r.Header.Get("Authorization") != "Bearer "+fixtureSearchKey || r.Header.Get("X-Respond-With") != "no-content" || r.Header.Get("Accept") != "application/json" {
		t.Fatalf("headers = %v", r.Header)
	}
	if !strings.Contains(out, "recency=day (not supported by jina") || !strings.Contains(out, "1. J\n   https://j.example/\n   from content") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestWebSearchGeneralKeyEnvWins(t *testing.T) {
	clearWebSearchEnv(t)
	t.Setenv(config.EnvWebSearchAPIKey, "general-env-key")
	t.Setenv("TAVILY_API_KEY", "provider-env-key")
	srv, seen := searchServer(t, 200, `{"results":[]}`)
	if _, err := runWebSearchTool(t, context.Background(), config.WebSearchConfig{Provider: "tavily", APIKey: "config-key", BaseURL: srv.URL}, `{"query":"x"}`); err != nil {
		t.Fatal(err)
	}
	if got := onlyRequest(t, seen).Header.Get("Authorization"); got != "Bearer general-env-key" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestWebSearchHTTPErrors(t *testing.T) {
	cases := []struct {
		provider string
		status   int
		body     string
		want     []string
	}{
		{"tavily", 401, `{"detail":{"error":"Unauthorized: missing or invalid API key ` + fixtureSearchKey + `"}}`, []string{"Tavily rejected the API key (HTTP 401); check webSearch.apiKey", "Unauthorized", "[redacted]"}},
		{"brave", 403, `{"error":{"detail":"subscription token invalid"}}`, []string{"Brave Search rejected the API key (HTTP 403)", "subscription token invalid"}},
		{"searxng", 403, `<html>Forbidden</html>`, []string{"SearXNG refused the request (HTTP 403); enable the json format", "Forbidden"}},
		{"tavily", 432, `{"detail":{"error":"plan limit"}}`, []string{"Tavily returned HTTP 432: plan limit"}},
		{"jina", 402, `{"readableMessage":"InsufficientBalanceError"}`, []string{"Jina reports the plan's quota or balance is used up (HTTP 402)", "InsufficientBalanceError"}},
		{"bocha", 429, `{"message":"too many"}`, []string{"Bocha rate limit reached (HTTP 429) (retry after 7 s); wait before searching again: too many"}},
		{"brave", 503, ``, []string{"Brave Search is unavailable (HTTP 503); try again later"}},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s-%d", tc.provider, tc.status), func(t *testing.T) {
			clearWebSearchEnv(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status == 429 {
					w.Header().Set("Retry-After", "7")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			_, err := runWebSearchTool(t, context.Background(), config.WebSearchConfig{Provider: tc.provider, APIKey: fixtureSearchKey, BaseURL: srv.URL}, `{"query":"x"}`)
			if err == nil {
				t.Fatal("no error")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %v, want %q", err, want)
				}
			}
			if strings.Contains(err.Error(), fixtureSearchKey) || strings.Contains(err.Error(), srv.URL) {
				t.Fatalf("error leaks key or URL: %v", err)
			}
		})
	}
}

func TestWebSearchBadJSON(t *testing.T) {
	clearWebSearchEnv(t)
	srv, _ := searchServer(t, 200, `not json`)
	_, err := runWebSearchTool(t, context.Background(), config.WebSearchConfig{Provider: "tavily", APIKey: fixtureSearchKey, BaseURL: srv.URL}, `{"query":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "could not read the Tavily response") {
		t.Fatalf("err = %v", err)
	}
}

func TestWebSearchTimeout(t *testing.T) {
	clearWebSearchEnv(t)
	old := webSearchTimeout
	webSearchTimeout = 100 * time.Millisecond
	t.Cleanup(func() { webSearchTimeout = old })
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	_, err := runWebSearchTool(t, context.Background(), config.WebSearchConfig{Provider: "brave", APIKey: fixtureSearchKey, BaseURL: srv.URL}, `{"query":"slow"}`)
	if err == nil || !strings.Contains(err.Error(), "Brave Search did not answer within 100ms") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout not applied")
	}
}

func TestWebSearchCanceled(t *testing.T) {
	clearWebSearchEnv(t)
	release := make(chan struct{})
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	_, err := runWebSearchTool(t, ctx, config.WebSearchConfig{Provider: "tavily", APIKey: fixtureSearchKey, BaseURL: srv.URL}, `{"query":"x"}`)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestWebSearchUnreachable(t *testing.T) {
	clearWebSearchEnv(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	base := strings.Replace(srv.URL, "http://", "http://user:"+fixtureSearchKey+"@", 1)
	srv.Close()
	_, err := runWebSearchTool(t, context.Background(), config.WebSearchConfig{Provider: "searxng", BaseURL: base}, `{"query":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "could not reach SearXNG") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), fixtureSearchKey) {
		t.Fatalf("error leaks URL credentials: %v", err)
	}
}

func TestWebSearchRedactsEchoedKey(t *testing.T) {
	clearWebSearchEnv(t)
	srv, _ := searchServer(t, 200, `{"results":[{"title":"echo `+fixtureSearchKey+`","url":"https://e.example/","content":"key=`+fixtureSearchKey+`"}]}`)
	out, err := runWebSearchTool(t, context.Background(), config.WebSearchConfig{Provider: "tavily", APIKey: fixtureSearchKey, BaseURL: srv.URL}, `{"query":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, fixtureSearchKey) || !strings.Contains(out, "[redacted]") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestWebSearchGate(t *testing.T) {
	clearWebSearchEnv(t)
	srv, seen := searchServer(t, 200, `{"results":[]}`)
	var asked GateRequest
	ctx := WithGate(context.Background(), func(r GateRequest) (GateDecision, string) {
		asked = r
		return GateReject, ""
	})
	_, err := runWebSearchTool(t, ctx, config.WebSearchConfig{Provider: "tavily", APIKey: fixtureSearchKey, BaseURL: srv.URL}, `{"query":"  latest   news "}`)
	if err == nil || !strings.HasPrefix(err.Error(), "Permission denied") {
		t.Fatalf("err = %v", err)
	}
	if asked.Tool != WebSearchToolName || asked.Command != "latest news" || asked.Description != "Search the web with Tavily" {
		t.Fatalf("gate request = %+v", asked)
	}
	if len(seen()) != 0 {
		t.Fatal("search ran after the gate refused it")
	}
	if strings.Contains(fmt.Sprintf("%+v", asked), fixtureSearchKey) {
		t.Fatal("gate request carries the key")
	}
}

func TestWebSearchArgumentErrors(t *testing.T) {
	clearWebSearchEnv(t)
	cfg := config.WebSearchConfig{Provider: "tavily", APIKey: fixtureSearchKey, BaseURL: "http://127.0.0.1:1"}
	cases := map[string]string{
		`{}`:                              "query",
		`{"query":"   "}`:                 "query is empty",
		`{"query":"x","recency":"hour"}`:  "recency must be day, week, month or year",
		`{"query":"x","domains":["a b"]}`: "is not a domain name",
		`{"query":"` + strings.Repeat("q", 401) + `"}`:                                                           "longer than 400",
		`{"query":"x","domains":["a.io","b.io","c.io","d.io","e.io","f.io","g.io","h.io","i.io","j.io","k.io"]}`: "at most 10 domains",
	}
	for args, want := range cases {
		_, err := runWebSearchTool(t, context.Background(), cfg, args)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", args, err, want)
		}
	}
	// An unusable configuration says why.
	_, err := runWebSearchTool(t, context.Background(), config.WebSearchConfig{Provider: "brave"}, `{"query":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "no API key for brave") {
		t.Fatalf("err = %v", err)
	}
}

func TestWebSearchMaxResultsCapped(t *testing.T) {
	clearWebSearchEnv(t)
	var results []string
	for i := range 15 {
		results = append(results, fmt.Sprintf(`{"title":"r%d","url":"https://r.example/%d","content":"c"}`, i, i))
	}
	srv, seen := searchServer(t, 200, `{"results":[`+strings.Join(results, ",")+`]}`)
	out, err := runWebSearchTool(t, context.Background(), config.WebSearchConfig{Provider: "tavily", APIKey: fixtureSearchKey, BaseURL: srv.URL}, `{"query":"x","max_results":50}`)
	if err != nil {
		t.Fatal(err)
	}
	if onlyRequest(t, seen).Body["max_results"] != float64(config.WebSearchMaxResults) {
		t.Fatal("max_results not capped in the request")
	}
	if !strings.Contains(out, "results=10\n") || strings.Contains(out, "r.example/10") {
		t.Fatalf("output not capped:\n%s", out)
	}
}

func TestWebSearchOutputBudget(t *testing.T) {
	long := strings.Repeat("word ", 400)
	var hits []webSearchHit
	for i := range 10 {
		hits = append(hits, webSearchHit{Title: strings.Repeat("t", 300), URL: fmt.Sprintf("https://b.example/%d/%s", i, strings.Repeat("p", 1500)), Snippet: long})
	}
	out := formatWebSearch("tavily", webSearchQuery{Query: "q", Count: 10}, hits, true)
	if len(out) > webSearchMaxOutput {
		t.Fatalf("output is %d bytes", len(out))
	}
	if !strings.Contains(out, "truncated=true") {
		t.Fatalf("no truncation note:\n%s", out[:200])
	}
	if !strings.Contains(out, "…") {
		t.Fatal("snippets not clipped")
	}
}

func TestWebSearchFallbackAvailability(t *testing.T) {
	clearWebSearchEnv(t)
	cfg := config.WebSearchConfig{}
	fallback := WebSearchFallback(func() config.WebSearchConfig { return cfg })
	if _, ok := fallback(); ok {
		t.Fatal("offered without a provider")
	}
	cfg = config.WebSearchConfig{Provider: "jina", APIKey: fixtureSearchKey}
	tool, ok := fallback()
	if !ok || tool.Def.Function.Name != WebSearchToolName {
		t.Fatal("not offered once configured")
	}
	if strings.Contains(string(tool.Def.Function.Parameters), fixtureSearchKey) || strings.Contains(tool.Def.Function.Description, fixtureSearchKey) {
		t.Fatal("definition carries the key")
	}
	if _, ok := WebSearchFallback(nil)(); ok {
		t.Fatal("nil settings offered the tool")
	}
}
