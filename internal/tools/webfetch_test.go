package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// allowFetches gates every request through and records what was asked.
func allowFetches(asked *[]GateRequest) context.Context {
	return WithGate(context.Background(), func(req GateRequest) (GateDecision, string) {
		if asked != nil {
			*asked = append(*asked, req)
		}
		return GateAllowOnce, ""
	})
}

func fetchArgs(url string, extra ...string) json.RawMessage {
	fields := append([]string{fmt.Sprintf(`"url":%q`, url)}, extra...)
	return json.RawMessage("{" + strings.Join(fields, ",") + "}")
}

func runFetch(t *testing.T, ctx context.Context, args json.RawMessage) string {
	t.Helper()
	return Execute(ctx, []Tool{webFetchTool()}, "web_fetch", args)
}

// fetchHeader returns the value of one key=value header line of a fetch result.
func fetchHeader(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
		if line == "" {
			break
		}
	}
	return ""
}

func fetchBody(out string) string {
	_, body, _ := strings.Cut(out, "\n\n")
	return body
}

func serve(t *testing.T, contentType string, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		} else {
			w.Header()["Content-Type"] = nil
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWebFetchConvertsHTMLToText(t *testing.T) {
	page := `<!doctype html><html><head><title>The  &amp; Title</title>
<style>body { color: red }</style><script>var hidden = "script text";</script>
<meta charset="utf-8"></head>
<body>
<nav hidden><a href="/skip">hidden nav</a></nav>
<h1>Main   heading</h1>
<p>First   paragraph with <b>bold</b>
 and a <a href="/docs/page?x=1">relative link</a> and <a href="#top">an anchor</a>.</p>
<p>Line one<br>Line two &lt;tag&gt; caf&eacute; it&#8217;s&nbsp;here</p>
<noscript>enable javascript</noscript>
<svg><text>svg text</text></svg>
<ul><li>apple</li><li>banana <a href="https://example.com/b">https://example.com/b</a></li></ul>
<ol><li>one</li><li>two</li></ol>
<h2>Second</h2>
<table><tr><th>k</th><th>v</th></tr><tr><td>a</td><td>1</td></tr></table>
<pre>  indented
    code</pre>
<div style="display: none">invisible</div>
<p>Last</p>
</body></html>`
	srv := serve(t, "text/html; charset=utf-8", []byte(page))
	out := runFetch(t, allowFetches(nil), fetchArgs(srv.URL+"/start#frag"))
	if strings.HasPrefix(out, "Error") {
		t.Fatal(out)
	}
	want := strings.Join([]string{
		"# Main heading",
		"",
		"First paragraph with bold and a relative link (" + srv.URL + "/docs/page?x=1) and an anchor.",
		"",
		"Line one",
		"Line two <tag> café it’s here",
		"",
		"- apple",
		"- banana https://example.com/b",
		"",
		"1. one",
		"2. two",
		"",
		"## Second",
		"",
		"k | v",
		"a | 1",
		"",
		"  indented",
		"    code",
		"",
		"Last",
	}, "\n")
	if got := fetchBody(out); got != want {
		t.Errorf("text:\n%s\n--- want:\n%s", got, want)
	}
	for key, want := range map[string]string{
		"url":          srv.URL + "/start",
		"status":       "200",
		"content_type": "text/html; charset=utf-8",
		"title":        "The & Title",
		"offset":       "0",
		"truncated":    "false",
	} {
		if got := fetchHeader(out, key); got != want {
			t.Errorf("%s=%q, want %q", key, got, want)
		}
	}
	if got := fetchHeader(out, "total_chars"); got != fmt.Sprint(len([]rune(want))) {
		t.Errorf("total_chars=%s, want %d", got, len([]rune(want)))
	}
	for _, gone := range []string{"script text", "color: red", "enable javascript", "svg text", "hidden nav", "invisible"} {
		if strings.Contains(out, gone) {
			t.Errorf("output kept %q", gone)
		}
	}
}

// Broken markup is repaired the way browsers do: a page that never closes its head,
// has none, or leaves a skipped element open still shows its body.
func TestWebFetchHTMLRepairsBrokenMarkup(t *testing.T) {
	for name, page := range map[string]string{
		"unclosed head": `<html><head><title>T</title><meta charset=utf-8><div>body text</div>`,
		"no head":       `<title>T</title><p>body text`,
		"unclosed nav":  `<title>T</title><div><nav><a href="/">menu</a><nav>more</div><p>body text`,
	} {
		got, title := htmlToText(page, nil)
		if got != "body text" || title != "T" {
			t.Errorf("%s: text %q title %q", name, got, title)
		}
	}
}

func TestWebFetchPassesTextThrough(t *testing.T) {
	for _, c := range []struct{ contentType, body string }{
		{"text/plain; charset=utf-8", "plain\r\n  text  with   spaces\n"},
		{"application/json", `{"a": [1, 2], "b": "<p>not html</p>"}`},
		{"text/markdown", "# Title\n\n- item\n"},
		{"", "sniffed plain text\n"},
	} {
		srv := serve(t, c.contentType, []byte(c.body))
		out := runFetch(t, allowFetches(nil), fetchArgs(srv.URL))
		want := strings.ReplaceAll(c.body, "\r\n", "\n")
		if got := fetchBody(out); got != want {
			t.Errorf("%q: body %q, want %q\n%s", c.contentType, got, want, out)
		}
		if fetchHeader(out, "title") != "" {
			t.Errorf("%q: non-HTML content has no title", c.contentType)
		}
	}
}

func TestWebFetchDecodesDeclaredCharset(t *testing.T) {
	gbk := []byte("<html><head><meta charset=\"gbk\"></head><body><p>\xd6\xd0\xce\xc4</p></body></html>")
	srv := serve(t, "text/html", gbk)
	if got := fetchBody(runFetch(t, allowFetches(nil), fetchArgs(srv.URL))); got != "中文" {
		t.Errorf("GBK page = %q, want 中文", got)
	}
}

func TestWebFetchPagesWithOffsetAndMaxChars(t *testing.T) {
	text := strings.Repeat("é", 10) + strings.Repeat("x", 15) // 25 chars, multibyte first
	srv := serve(t, "text/plain; charset=utf-8", []byte(text))
	ctx := allowFetches(nil)

	first := runFetch(t, ctx, fetchArgs(srv.URL, `"max_chars":10`))
	if fetchBody(first) != strings.Repeat("é", 10) || fetchHeader(first, "truncated") != "true" || fetchHeader(first, "next_offset") != "10" || fetchHeader(first, "total_chars") != "25" {
		t.Fatalf("first page:\n%s", first)
	}
	second := runFetch(t, ctx, fetchArgs(srv.URL, `"max_chars":10`, `"offset":10`))
	if fetchBody(second) != strings.Repeat("x", 10) || fetchHeader(second, "offset") != "10" || fetchHeader(second, "next_offset") != "20" {
		t.Fatalf("second page:\n%s", second)
	}
	last := runFetch(t, ctx, fetchArgs(srv.URL, `"max_chars":"10"`, `"offset":20`))
	if fetchBody(last) != "xxxxx" || fetchHeader(last, "truncated") != "false" || strings.Contains(last, "next_offset") {
		t.Fatalf("last page:\n%s", last)
	}
	if past := runFetch(t, ctx, fetchArgs(srv.URL, `"offset":26`)); !strings.HasPrefix(past, "Error: offset 26 is past the end of the text (25 chars)") {
		t.Fatalf("offset past the end: %s", past)
	}
}

func TestWebFetchCapsMaxChars(t *testing.T) {
	srv := serve(t, "text/plain", []byte(strings.Repeat("a", webFetchMaxChars+500)))
	ctx := allowFetches(nil)
	huge := runFetch(t, ctx, fetchArgs(srv.URL, `"max_chars":10000000`))
	if n := len(fetchBody(huge)); n != webFetchMaxChars || fetchHeader(huge, "truncated") != "true" {
		t.Fatalf("max_chars cap: got %d chars, truncated=%s", n, fetchHeader(huge, "truncated"))
	}
	def := runFetch(t, ctx, fetchArgs(srv.URL))
	if n := len(fetchBody(def)); n != webFetchDefaultChars {
		t.Fatalf("default max_chars: got %d chars", n)
	}
}

func TestWebFetchCapsTheBody(t *testing.T) {
	var sent atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		chunk := []byte(strings.Repeat("b", 64<<10))
		for range (webFetchMaxBody * 2) / len(chunk) {
			n, err := w.Write(chunk)
			sent.Add(int64(n))
			if err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	out := runFetch(t, allowFetches(nil), fetchArgs(srv.URL, `"offset":5242000`))
	if fetchHeader(out, "total_chars") != fmt.Sprint(webFetchMaxBody) || !strings.HasPrefix(fetchHeader(out, "body_truncated"), "true") {
		t.Fatalf("body cap:\n%.400s", out)
	}
	if n := len(fetchBody(out)); n != webFetchMaxBody-5242000 {
		t.Fatalf("tail after the cap = %d chars", n)
	}
}

func TestWebFetchFollowsSameHostRedirects(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old":
			http.Redirect(w, r, "/middle", http.StatusMovedPermanently)
		case "/middle":
			http.Redirect(w, r, srv.URL+"/new", http.StatusFound)
		case "/new":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "arrived")
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	}))
	defer srv.Close()
	var asked []GateRequest
	ctx := allowFetches(&asked)
	out := runFetch(t, ctx, fetchArgs(srv.URL+"/old"))
	if fetchBody(out) != "arrived" || fetchHeader(out, "url") != srv.URL+"/new" || fetchHeader(out, "status") != "200" {
		t.Fatalf("redirect:\n%s", out)
	}
	if len(asked) != 1 {
		t.Errorf("a same-host redirect should not ask again, asked %d times", len(asked))
	}
	if loop := runFetch(t, ctx, fetchArgs(srv.URL+"/loop")); !strings.Contains(loop, "stopped after 5 redirects") {
		t.Errorf("redirect loop: %s", loop)
	}
}

// A redirect to another host is reported, not followed: the gate approved one host.
func TestWebFetchReportsCrossHostRedirects(t *testing.T) {
	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { otherHits.Add(1) }))
	defer other.Close()
	// localhost and 127.0.0.1 are different hosts to the gate.
	target := strings.Replace(other.URL, "127.0.0.1", "localhost", 1) + "/elsewhere?q=1"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer srv.Close()
	out := runFetch(t, allowFetches(nil), fetchArgs(srv.URL+"/go"))
	if !strings.HasPrefix(out, "Error: ") || !strings.Contains(out, "redirect="+target) || !strings.Contains(out, "status=302") || !strings.Contains(out, "Call web_fetch with that URL") {
		t.Fatalf("cross-host redirect:\n%s", out)
	}
	if otherHits.Load() != 0 {
		t.Fatal("the redirect target was fetched")
	}
}

func TestWebFetchReportsHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "<html><body><h1>Not here</h1><p>"+strings.Repeat("long ", 400)+"</p></body></html>")
	}))
	defer srv.Close()
	out := runFetch(t, allowFetches(nil), fetchArgs(srv.URL+"/missing"))
	if !strings.HasPrefix(out, "Error: HTTP 404 Not Found from "+srv.URL+"/missing") || !strings.Contains(out, "\nstatus=404\n") || !strings.Contains(out, "# Not here") {
		t.Fatalf("404:\n%s", out)
	}
	if n := len([]rune(out)); n > 1000 {
		t.Errorf("the error excerpt should be short, got %d chars", n)
	}
}

func TestWebFetchRefusesBinaryContent(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	for _, c := range []struct {
		contentType string
		body        []byte
	}{
		{"image/png", png},
		{"application/pdf", []byte("%PDF-1.7\n")},
		{"application/octet-stream", []byte{0, 1, 2, 3, 0xff}},
		{"", png},
	} {
		srv := serve(t, c.contentType, c.body)
		out := runFetch(t, allowFetches(nil), fetchArgs(srv.URL+"/file"))
		if !strings.HasPrefix(out, "Error: ") || !strings.Contains(out, "binary content") || !strings.Contains(out, "web_fetch only reads text") {
			t.Errorf("%q should be refused:\n%s", c.contentType, out)
		}
	}
	// A text file served as octet-stream is still text.
	srv := serve(t, "application/octet-stream", []byte("readme text\n"))
	if out := runFetch(t, allowFetches(nil), fetchArgs(srv.URL)); fetchBody(out) != "readme text\n" {
		t.Errorf("text served as octet-stream:\n%s", out)
	}
}

func TestWebFetchRefusesOtherSchemes(t *testing.T) {
	var asked []GateRequest
	ctx := allowFetches(&asked)
	for raw, want := range map[string]string{
		"file:///etc/passwd":            "only fetches http and https",
		"ftp://example.com/x":           "only fetches http and https",
		"javascript:alert(1)":           "only fetches http and https",
		"example.com/page":              "only fetches http and https",
		"http:///nohost":                "has no host",
		"https://user:pw@example.com/x": "does not send credentials",
	} {
		if out := runFetch(t, ctx, fetchArgs(raw)); !strings.HasPrefix(out, "Error: ") || !strings.Contains(out, want) {
			t.Errorf("%s: %s", raw, out)
		}
	}
	if out := runFetch(t, ctx, json.RawMessage(`{}`)); !strings.Contains(out, `missing required argument "url"`) {
		t.Errorf("missing url: %s", out)
	}
	if len(asked) != 0 {
		t.Errorf("refused URLs reached the gate: %+v", asked)
	}
}

// A rejected gate stops the fetch before any request reaches the server, and the gate
// sees the URL with a host rule, the same request AI review builds from the args.
func TestWebFetchRejectedGateSendsNothing(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	var seen GateRequest
	ctx := WithGate(context.Background(), func(req GateRequest) (GateDecision, string) {
		seen = req
		return GateReject, "no network today"
	})
	args := fetchArgs(srv.URL + "/Path?q=1#frag")
	out := runFetch(t, ctx, args)
	if out != "Error: Permission denied: no network today" {
		t.Fatalf("rejected fetch: %s", out)
	}
	if hits.Load() != 0 {
		t.Fatal("a rejected fetch reached the server")
	}
	want := srv.URL + "/Path?q=1"
	if seen.Tool != "web_fetch" || seen.Command != want || seen.Rule != "domain:127.0.0.1" {
		t.Errorf("gate request = %+v", seen)
	}
	if pre := GateRequestFor(context.Background(), "web_fetch", args); pre.Command != want || pre.Rule != "domain:127.0.0.1" {
		t.Errorf("GateRequestFor = %+v", pre)
	}
}

func TestWebFetchSendsAGenericUserAgent(t *testing.T) {
	var agent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/plain")
	}))
	defer srv.Close()
	old := Version
	Version = "1.2.3"
	defer func() { Version = old }()
	runFetch(t, allowFetches(nil), fetchArgs(srv.URL))
	if agent != "k-brain/1.2.3" {
		t.Errorf("User-Agent = %q", agent)
	}
}
