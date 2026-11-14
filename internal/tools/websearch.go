package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/netproxy"
)

// WebSearchToolName is the function tool that searches the web through the
// configured third-party service when the model's API has no native search.
const WebSearchToolName = "web_search"

// webSearchTimeout bounds one search; a variable so tests can shorten it.
var webSearchTimeout = 15 * time.Second

const (
	webSearchMaxBody     = 2 << 20
	webSearchMaxOutput   = 12_000
	webSearchSnippetMax  = 500
	webSearchTitleMax    = 200
	webSearchErrExcerpt  = 300
	webSearchMaxDomains  = 10
	webSearchMaxQueryLen = 400
)

var webSearchRecencies = []string{"day", "week", "month", "year"}

// webSearchHTTP builds the client searches use; tests replace it.
var webSearchHTTP = func() *http.Client {
	// The proxied transport honours the app proxy, or HTTPS_PROXY/NO_PROXY when
	// run standalone; loopback hosts (a local SearXNG) are never proxied.
	return &http.Client{Transport: netproxy.Transport(true), Timeout: webSearchTimeout}
}

// WebSearchFallback returns a function that offers the web_search tool while
// settings names a usable search service. It reads settings on every call, so
// a changed configuration applies to the next request without a restart.
func WebSearchFallback(settings func() config.WebSearchConfig) func() (Tool, bool) {
	return func() (Tool, bool) {
		if settings == nil || !settings().Effective().Available() {
			return Tool{}, false
		}
		return WebSearchTool(settings), true
	}
}

// WebSearchTool is the web_search tool backed by the service settings names.
func WebSearchTool(settings func() config.WebSearchConfig) Tool {
	return Tool{
		Def: ai.NewTool(WebSearchToolName,
			"Search the web through the configured search service and return a numbered list of results: title, URL, publication date when known, and a short snippet. Use it for current events, recent releases, documentation you do not know, or to find pages to read; then cite the URLs you rely on, and read a page in full with a fetch tool when one is available and the snippet is not enough. Keep queries short and specific. recency limits results to the past day, week, month or year where the service supports it; domains limits results to those sites. The search times out after 15 s.",
			fmt.Sprintf(`{"type":"object","properties":{"query":{"type":"string","description":"What to search for"},"max_results":{"type":"number","minimum":1,"maximum":%d,"description":"Number of results (default from the configuration, at most %d)"},"recency":{"type":"string","enum":["day","week","month","year"],"description":"Only results published within the past day, week, month or year"},"domains":{"type":"array","items":{"type":"string"},"maxItems":%d,"description":"Only results from these sites, e.g. [\"go.dev\"]"}},"required":["query"],"additionalProperties":false}`, config.WebSearchMaxResults, config.WebSearchMaxResults, webSearchMaxDomains)),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			if settings == nil {
				return "", errors.New("web_search: no web search service is configured")
			}
			return runWebSearch(ctx, settings(), args)
		},
	}
}

type webSearchQuery struct {
	Query   string
	Count   int
	Recency string
	Domains []string
}

type webSearchHit struct {
	Title, URL, Snippet, Published string
}

// webSearchBackend is one search service: how to ask it and how to read its answer.
type webSearchBackend struct {
	label       string
	defaultBase string
	// recency reports whether the service filters by publication time.
	recency bool
	request func(ctx context.Context, base, key string, q webSearchQuery) (*http.Request, error)
	parse   func(body []byte) ([]webSearchHit, error)
}

var webSearchBackends = map[string]webSearchBackend{
	config.WebSearchTavily:  {label: "Tavily", defaultBase: "https://api.tavily.com", recency: true, request: tavilyRequest, parse: tavilyParse},
	config.WebSearchBrave:   {label: "Brave Search", defaultBase: "https://api.search.brave.com", recency: true, request: braveRequest, parse: braveParse},
	config.WebSearchSearXNG: {label: "SearXNG", recency: true, request: searxngRequest, parse: searxngParse},
	config.WebSearchBocha:   {label: "Bocha", defaultBase: "https://api.bochaai.com", recency: true, request: bochaRequest, parse: bochaParse},
	config.WebSearchJina:    {label: "Jina", defaultBase: "https://s.jina.ai", request: jinaRequest, parse: jinaParse},
}

func runWebSearch(ctx context.Context, settings config.WebSearchConfig, args json.RawMessage) (string, error) {
	var a struct {
		Query      string   `json:"query"`
		MaxResults int      `json:"max_results"`
		Recency    string   `json:"recency"`
		Domains    []string `json:"domains"`
	}
	if err := decodeArgs(WebSearchToolName, args, &a, "query"); err != nil {
		return "", err
	}
	q := webSearchQuery{Query: strings.Join(strings.Fields(a.Query), " ")}
	if q.Query == "" {
		return "", errors.New("web_search: query is empty")
	}
	if r := []rune(q.Query); len(r) > webSearchMaxQueryLen {
		return "", fmt.Errorf("web_search: query is longer than %d characters; shorten it", webSearchMaxQueryLen)
	}
	q.Recency = strings.ToLower(strings.TrimSpace(a.Recency))
	if q.Recency != "" && !slices.Contains(webSearchRecencies, q.Recency) {
		return "", fmt.Errorf("web_search: recency must be day, week, month or year")
	}
	domains, err := webSearchDomains(a.Domains)
	if err != nil {
		return "", err
	}
	q.Domains = domains

	eff := settings.Effective()
	if !eff.Available() {
		return "", errors.New("web_search: " + eff.Problem)
	}
	backend, ok := webSearchBackends[eff.Provider]
	if !ok {
		return "", fmt.Errorf("web_search: unsupported provider %q", eff.Provider)
	}
	q.Count = eff.MaxResults
	if a.MaxResults > 0 {
		q.Count = a.MaxResults
	}
	q.Count = min(max(q.Count, 1), config.WebSearchMaxResults)

	if deny := checkGateRequest(ctx, GateRequest{Context: ctx, Tool: WebSearchToolName, Command: q.Query, Description: "Search the web with " + backend.label}); deny != "" {
		return "", errors.New(deny)
	}
	key, err := eff.ResolveKey(ctx)
	if err != nil {
		return "", errors.New("web_search: " + err.Error())
	}
	base := eff.BaseURL
	if base == "" {
		base = backend.defaultBase
	}
	base = strings.TrimRight(base, "/")

	ctx, cancel := context.WithTimeout(ctx, webSearchTimeout)
	defer cancel()
	hits, err := doWebSearch(ctx, backend, base, key, q)
	if errors.Is(err, context.Canceled) {
		// Keep a cancellation recognizable to the caller; it carries no service text.
		return "", err
	}
	if err != nil {
		return "", errors.New(redactWebSearchKey(err.Error(), key))
	}
	out := formatWebSearch(eff.Provider, q, hits, backend.recency)
	return redactWebSearchKey(out, key), nil
}

func doWebSearch(ctx context.Context, backend webSearchBackend, base, key string, q webSearchQuery) ([]webSearchHit, error) {
	req, err := backend.request(ctx, base, key, q)
	if err != nil {
		return nil, fmt.Errorf("web_search: could not build the %s request", backend.label)
	}
	req.Header.Set("User-Agent", "k-brain/"+Version)
	resp, err := webSearchHTTP().Do(req)
	if err != nil {
		return nil, webSearchTransportError(ctx, backend.label, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, webSearchMaxBody+1))
	if err != nil {
		return nil, webSearchTransportError(ctx, backend.label, err)
	}
	if len(body) > webSearchMaxBody {
		return nil, fmt.Errorf("web_search: the %s response is larger than %d MB", backend.label, webSearchMaxBody>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, webSearchStatusError(backend, resp, body)
	}
	hits, err := backend.parse(body)
	var serviceErr webSearchServiceError
	if errors.As(err, &serviceErr) {
		return nil, fmt.Errorf("web_search: %s reported error %s: %s", backend.label, serviceErr.code, serviceErr.msg)
	}
	if err != nil {
		return nil, fmt.Errorf("web_search: could not read the %s response: %v", backend.label, err)
	}
	return hits, nil
}

// webSearchTransportError maps a failed exchange to a message without the
// request URL, which may carry credentials in its user info.
func webSearchTransportError(ctx context.Context, label string, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("web_search: %s did not answer within %s; try again or narrow the query", label, webSearchTimeout)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Errorf("web_search: %s did not answer within %s; try again or narrow the query", label, webSearchTimeout)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("web_search: could not reach %s: %v", label, err)
}

func webSearchStatusError(backend webSearchBackend, resp *http.Response, body []byte) error {
	label, code := backend.label, resp.StatusCode
	detail := webSearchErrorDetail(body)
	suffix := ""
	if detail != "" {
		suffix = ": " + detail
	}
	switch {
	case code == http.StatusUnauthorized || (code == http.StatusForbidden && backend.label != "SearXNG"):
		return fmt.Errorf("web_search: %s rejected the API key (HTTP %d); check webSearch.apiKey%s", label, code, suffix)
	case code == http.StatusForbidden:
		return fmt.Errorf("web_search: %s refused the request (HTTP 403); enable the json format under search.formats in its settings.yml, or check access to the instance%s", label, suffix)
	case code == http.StatusPaymentRequired:
		return fmt.Errorf("web_search: %s reports the plan's quota or balance is used up (HTTP 402)%s", label, suffix)
	case code == http.StatusTooManyRequests:
		wait := ""
		if s := strings.TrimSpace(resp.Header.Get("Retry-After")); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n >= 0 {
				wait = fmt.Sprintf(" (retry after %d s)", n)
			}
		}
		return fmt.Errorf("web_search: %s rate limit reached (HTTP 429)%s; wait before searching again%s", label, wait, suffix)
	case code >= 500:
		return fmt.Errorf("web_search: %s is unavailable (HTTP %d); try again later%s", label, code, suffix)
	}
	return fmt.Errorf("web_search: %s returned HTTP %d%s", label, code, suffix)
}

// webSearchErrorDetail is the service's own error text, short and on one line.
func webSearchErrorDetail(body []byte) string {
	var m map[string]any
	text := ""
	if json.Unmarshal(body, &m) == nil {
		for _, k := range []string{"detail", "error", "message", "msg", "readableMessage"} {
			if s := webSearchErrorText(m[k]); s != "" {
				text = s
				break
			}
		}
	} else {
		text = string(body)
	}
	text = strings.Join(strings.Fields(stripWebSearchMarkup(text)), " ")
	return clipRunes(text, webSearchErrExcerpt)
}

func webSearchErrorText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		for _, k := range []string{"error", "message", "detail", "msg"} {
			if s := webSearchErrorText(x[k]); s != "" {
				return s
			}
		}
	}
	return ""
}

// redactWebSearchKey removes the API key from text a service may have echoed.
func redactWebSearchKey(s, key string) string {
	key = strings.TrimSpace(key)
	if len(key) < 4 {
		return s
	}
	return strings.ReplaceAll(s, key, "[redacted]")
}

var webSearchDomainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// webSearchDomains normalizes the domains filter: bare lowercase hosts, no
// duplicates. A URL is reduced to its host.
func webSearchDomains(in []string) ([]string, error) {
	var out []string
	for _, raw := range in {
		d := strings.ToLower(strings.TrimSpace(raw))
		if d == "" {
			continue
		}
		if strings.Contains(d, "://") {
			if u, err := url.Parse(d); err == nil {
				d = u.Hostname()
			}
		}
		d = strings.TrimPrefix(d, "*.")
		d, _, _ = strings.Cut(d, "/")
		d = strings.TrimSuffix(d, ".")
		if !webSearchDomainPattern.MatchString(d) {
			return nil, fmt.Errorf("web_search: %q is not a domain name", raw)
		}
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	if len(out) > webSearchMaxDomains {
		return nil, fmt.Errorf("web_search: at most %d domains", webSearchMaxDomains)
	}
	return out, nil
}

// siteQuery adds site: operators for services without a domain filter.
func siteQuery(q webSearchQuery) string {
	switch len(q.Domains) {
	case 0:
		return q.Query
	case 1:
		return q.Query + " site:" + q.Domains[0]
	}
	sites := make([]string, len(q.Domains))
	for i, d := range q.Domains {
		sites[i] = "site:" + d
	}
	return q.Query + " (" + strings.Join(sites, " OR ") + ")"
}

func jsonRequest(ctx context.Context, endpoint string, payload any) (*http.Request, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func getRequest(ctx context.Context, endpoint string, query url.Values) (*http.Request, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// Tavily: POST /search with a bearer key.
func tavilyRequest(ctx context.Context, base, key string, q webSearchQuery) (*http.Request, error) {
	payload := map[string]any{"query": q.Query, "max_results": q.Count, "search_depth": "basic", "include_answer": false}
	if q.Recency != "" {
		payload["time_range"] = q.Recency
	}
	if len(q.Domains) > 0 {
		payload["include_domains"] = q.Domains
	}
	req, err := jsonRequest(ctx, base+"/search", payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	return req, nil
}

func tavilyParse(body []byte) ([]webSearchHit, error) {
	var r struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Content       string `json:"content"`
			PublishedDate string `json:"published_date"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	hits := make([]webSearchHit, 0, len(r.Results))
	for _, x := range r.Results {
		hits = append(hits, webSearchHit{Title: x.Title, URL: x.URL, Snippet: x.Content, Published: x.PublishedDate})
	}
	return hits, nil
}

// Brave Search: GET /res/v1/web/search with the key in X-Subscription-Token.
func braveRequest(ctx context.Context, base, key string, q webSearchQuery) (*http.Request, error) {
	v := url.Values{"q": {siteQuery(q)}, "count": {strconv.Itoa(q.Count)}}
	if f := map[string]string{"day": "pd", "week": "pw", "month": "pm", "year": "py"}[q.Recency]; f != "" {
		v.Set("freshness", f)
	}
	req, err := getRequest(ctx, base+"/res/v1/web/search", v)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Subscription-Token", key)
	return req, nil
}

func braveParse(body []byte) ([]webSearchHit, error) {
	var r struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				Age         string `json:"age"`
				PageAge     string `json:"page_age"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	hits := make([]webSearchHit, 0, len(r.Web.Results))
	for _, x := range r.Web.Results {
		published := x.PageAge
		if published == "" {
			published = x.Age
		}
		hits = append(hits, webSearchHit{Title: x.Title, URL: x.URL, Snippet: x.Description, Published: published})
	}
	return hits, nil
}

// SearXNG: GET /search?format=json on the configured instance; a key, when set,
// is sent as a bearer token for instances behind an authenticating proxy.
func searxngRequest(ctx context.Context, base, key string, q webSearchQuery) (*http.Request, error) {
	v := url.Values{"q": {siteQuery(q)}, "format": {"json"}, "pageno": {"1"}}
	if q.Recency != "" {
		v.Set("time_range", q.Recency)
	}
	req, err := getRequest(ctx, base+"/search", v)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return req, nil
}

func searxngParse(body []byte) ([]webSearchHit, error) {
	var r struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Content       string `json:"content"`
			PublishedDate string `json:"publishedDate"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	hits := make([]webSearchHit, 0, len(r.Results))
	for _, x := range r.Results {
		hits = append(hits, webSearchHit{Title: x.Title, URL: x.URL, Snippet: x.Content, Published: x.PublishedDate})
	}
	return hits, nil
}

// Bocha: POST /v1/web-search with a bearer key.
func bochaRequest(ctx context.Context, base, key string, q webSearchQuery) (*http.Request, error) {
	payload := map[string]any{"query": q.Query, "count": q.Count, "summary": true}
	if f := map[string]string{"day": "oneDay", "week": "oneWeek", "month": "oneMonth", "year": "oneYear"}[q.Recency]; f != "" {
		payload["freshness"] = f
	} else {
		payload["freshness"] = "noLimit"
	}
	if len(q.Domains) > 0 {
		payload["include"] = strings.Join(q.Domains, "|")
	}
	req, err := jsonRequest(ctx, base+"/v1/web-search", payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	return req, nil
}

func bochaParse(body []byte) ([]webSearchHit, error) {
	var r struct {
		Code json.Number `json:"code"`
		Msg  string      `json:"msg"`
		Data struct {
			WebPages struct {
				Value []struct {
					Name          string `json:"name"`
					URL           string `json:"url"`
					Snippet       string `json:"snippet"`
					Summary       string `json:"summary"`
					DatePublished string `json:"datePublished"`
				} `json:"value"`
			} `json:"webPages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if c := r.Code.String(); c != "" && c != "200" {
		msg := strings.TrimSpace(r.Msg)
		if msg == "" {
			msg = "no message"
		}
		return nil, webSearchServiceError{code: c, msg: clipRunes(msg, webSearchErrExcerpt)}
	}
	hits := make([]webSearchHit, 0, len(r.Data.WebPages.Value))
	for _, x := range r.Data.WebPages.Value {
		snippet := x.Summary
		if strings.TrimSpace(snippet) == "" {
			snippet = x.Snippet
		}
		hits = append(hits, webSearchHit{Title: x.Name, URL: x.URL, Snippet: snippet, Published: x.DatePublished})
	}
	return hits, nil
}

// Jina: GET /?q= with a bearer key, asking for results without page content.
func jinaRequest(ctx context.Context, base, key string, q webSearchQuery) (*http.Request, error) {
	req, err := getRequest(ctx, base+"/", url.Values{"q": {siteQuery(q)}})
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Respond-With", "no-content")
	return req, nil
}

func jinaParse(body []byte) ([]webSearchHit, error) {
	var r struct {
		Data []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Content     string `json:"content"`
			Date        string `json:"date"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	hits := make([]webSearchHit, 0, len(r.Data))
	for _, x := range r.Data {
		snippet := x.Description
		if strings.TrimSpace(snippet) == "" {
			snippet = x.Content
		}
		hits = append(hits, webSearchHit{Title: x.Title, URL: x.URL, Snippet: snippet, Published: x.Date})
	}
	return hits, nil
}

// webSearchServiceError is an error a service reports inside a successful HTTP response.
type webSearchServiceError struct{ code, msg string }

func (e webSearchServiceError) Error() string { return "service error " + e.code + ": " + e.msg }

var webSearchTag = regexp.MustCompile(`<[^>]*>`)

// stripWebSearchMarkup drops highlight tags such as <strong> and decodes entities.
func stripWebSearchMarkup(s string) string {
	return html.UnescapeString(webSearchTag.ReplaceAllString(s, ""))
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n])) + "…"
	}
	return s
}

func cleanWebSearchText(s string, n int) string {
	return clipRunes(strings.Join(strings.Fields(stripWebSearchMarkup(s)), " "), n)
}

// formatWebSearch is the tool result: a short header, then one numbered entry per
// result, within webSearchMaxOutput characters.
func formatWebSearch(provider string, q webSearchQuery, hits []webSearchHit, recency bool) string {
	var entries []string
	seen := map[string]bool{}
	for _, h := range hits {
		link := strings.TrimSpace(h.URL)
		u, err := url.Parse(link)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || seen[link] {
			continue
		}
		seen[link] = true
		if len(entries) >= q.Count {
			break
		}
		title := cleanWebSearchText(h.Title, webSearchTitleMax)
		if title == "" {
			title = u.Host
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d. %s\n   %s\n", len(entries)+1, title, link)
		if p := cleanWebSearchText(h.Published, 40); p != "" {
			fmt.Fprintf(&b, "   published: %s\n", p)
		}
		if s := cleanWebSearchText(h.Snippet, webSearchSnippetMax); s != "" {
			fmt.Fprintf(&b, "   %s\n", s)
		}
		entries = append(entries, b.String())
	}
	var head strings.Builder
	fmt.Fprintf(&head, "provider=%s\nquery=%s\n", provider, q.Query)
	if q.Recency != "" {
		if recency {
			fmt.Fprintf(&head, "recency=%s\n", q.Recency)
		} else {
			fmt.Fprintf(&head, "recency=%s (not supported by %s; results are not filtered by date)\n", q.Recency, provider)
		}
	}
	if len(q.Domains) > 0 {
		fmt.Fprintf(&head, "domains=%s\n", strings.Join(q.Domains, ","))
	}
	budget := webSearchMaxOutput - head.Len() - 64
	kept := 0
	for _, e := range entries {
		if budget-len(e)-1 < 0 && kept > 0 {
			break
		}
		budget -= len(e) + 1
		kept++
	}
	fmt.Fprintf(&head, "results=%d\n", kept)
	if kept < len(entries) {
		fmt.Fprintf(&head, "truncated=true (%d more results omitted to fit the output limit)\n", len(entries)-kept)
	}
	if kept == 0 {
		head.WriteString("\nNo results. Try other keywords, a broader query, or drop the recency and domains filters.")
		return head.String()
	}
	head.WriteString("\n")
	head.WriteString(strings.TrimRight(strings.Join(entries[:kept], "\n"), "\n"))
	return head.String()
}
