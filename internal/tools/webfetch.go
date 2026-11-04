package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html/charset"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/netproxy"
	"github.com/Stack-Cairn/K-brain/internal/permissions"
)

// Version names the build in web_fetch's User-Agent; the CLI sets it at startup.
var Version = "dev"

const (
	webFetchDefaultChars = 20_000
	webFetchMaxChars     = 100_000
	webFetchMaxBody      = 5 << 20
	webFetchTimeout      = 30 * time.Second
	webFetchMaxRedirects = 5
	webFetchExcerpt      = 500
)

// errCrossHostRedirect stops a redirect to another host, which the model fetches
// with a new call so that the permission gate sees that host.
var errCrossHostRedirect = errors.New("cross-host redirect")

func webFetchTool() Tool {
	return Tool{
		Def: ai.NewTool("web_fetch",
			"Fetch an http(s) URL with GET and return its content as readable text: HTML is converted to text (scripts, styles and markup dropped; links kept as `text (url)`), and plain text, JSON, XML and markdown are returned as is. Binary content such as images, PDFs and archives is refused. The output starts with url=, status=, content_type=, total_chars=, offset= and truncated= lines; when truncated=true, call again with the offset from next_offset to read on. Redirects within the same host are followed; a redirect to another host is reported so you can fetch it in a new call. Each fetch asks the user for permission unless a rule allows the host. Responses are read up to 5 MB and the request times out after 30 s.",
			fmt.Sprintf(`{"type":"object","properties":{"url":{"type":"string","description":"Absolute http:// or https:// URL to fetch"},"max_chars":{"type":"number","minimum":1,"description":"Maximum characters of text to return (default %d, at most %d)"},"offset":{"type":"number","minimum":0,"description":"Characters of the converted text to skip, for paging (default 0)"}},"required":["url"],"additionalProperties":false}`, webFetchDefaultChars, webFetchMaxChars)),
		Run: runWebFetch,
	}
}

// parseFetchURL validates a URL web_fetch may request and returns it normalized: a
// lowercase host and no fragment, so the gate shows and matches what is sent.
func parseFetchURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("web_fetch only fetches http and https URLs, not %q", raw)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("URL %q has no host", raw)
	}
	if u.User != nil {
		return nil, fmt.Errorf("web_fetch does not send credentials embedded in a URL; remove the user info from %q", raw)
	}
	u.Host = strings.ToLower(u.Host)
	u.Fragment, u.RawFragment = "", ""
	return u, nil
}

// webFetchGateRequest is the gate request for fetching u: the command is the URL and
// the rule its host, which "always allow" remembers.
func webFetchGateRequest(ctx context.Context, u *url.URL) GateRequest {
	return GateRequest{Context: ctx, Tool: "web_fetch", Command: u.String(), Rule: "domain:" + permissions.URLHost(u.String())}
}

func runWebFetch(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		URL      string `json:"url"`
		MaxChars int    `json:"max_chars"`
		Offset   int    `json:"offset"`
	}
	if err := decodeArgs("web_fetch", args, &a, "url"); err != nil {
		return "", err
	}
	target, err := parseFetchURL(a.URL)
	if err != nil {
		return "", err
	}
	if deny := checkGateRequest(ctx, webFetchGateRequest(ctx, target)); deny != "" {
		return "", errors.New(deny)
	}
	maxChars := a.MaxChars
	if maxChars <= 0 {
		maxChars = webFetchDefaultChars
	}
	maxChars = min(maxChars, webFetchMaxChars)
	offset := max(a.Offset, 0)

	ctx, cancel := context.WithTimeout(ctx, webFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "k-brain/"+Version)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,text/markdown,application/json,application/xml;q=0.9,*/*;q=0.5")
	var redirect *url.URL
	client := &http.Client{
		// The proxied transport honours the app proxy, or HTTPS_PROXY/NO_PROXY when
		// run standalone; loopback hosts are never proxied.
		Transport: netproxy.Transport(true),
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) > webFetchMaxRedirects {
				return fmt.Errorf("stopped after %d redirects", webFetchMaxRedirects)
			}
			if !sameFetchHost(next.URL, via[0].URL) {
				redirect = next.URL
				return errCrossHostRedirect
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if errors.Is(err, errCrossHostRedirect) && redirect != nil {
		// Do returns the redirect response with its body already closed.
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		return "", fmt.Errorf("%s redirects to another host: %s\nstatus=%d\nredirect=%s\nCall web_fetch with that URL to follow it.", target, redirect.Host, status, redirect)
	}
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("fetching %s timed out after %s", target, webFetchTimeout)
		}
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBody+1))
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("reading %s timed out after %s", resp.Request.URL, webFetchTimeout)
		}
		return "", fmt.Errorf("reading %s: %w", resp.Request.URL, err)
	}
	capped := len(body) > webFetchMaxBody
	if capped {
		body = trimPartialRune(body[:webFetchMaxBody])
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	kind, isText := fetchKind(contentType, body)
	head := fmt.Sprintf("url=%s\nstatus=%d\ncontent_type=%s\n", resp.Request.URL, resp.StatusCode, contentType)
	failed := resp.StatusCode < 200 || resp.StatusCode > 299
	if !isText && !failed {
		shown := contentType
		if shown == "" {
			shown = "no content type"
		}
		return "", fmt.Errorf("%s returned binary content (%s, %s); web_fetch only reads text\n%s", resp.Request.URL, shown, bytesHuman(len(body)), strings.TrimSuffix(head, "\n"))
	}
	var text, title string
	if isText {
		text = decodeFetchBody(body, contentType)
		if kind == "html" {
			text, title = htmlToText(text, resp.Request.URL)
		} else {
			text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
		}
	}
	if failed {
		excerpt := strings.TrimSpace(text)
		if r := []rune(excerpt); len(r) > webFetchExcerpt {
			excerpt = string(r[:webFetchExcerpt]) + "…"
		}
		return "", fmt.Errorf("HTTP %s from %s\n%s\n%s", resp.Status, resp.Request.URL, strings.TrimSuffix(head, "\n"), excerpt)
	}

	runes := []rune(text)
	if offset > len(runes) {
		return "", fmt.Errorf("offset %d is past the end of the text (%d chars)", offset, len(runes))
	}
	end := min(offset+maxChars, len(runes))
	var b strings.Builder
	b.WriteString(head)
	if title != "" {
		fmt.Fprintf(&b, "title=%s\n", title)
	}
	fmt.Fprintf(&b, "total_chars=%d\noffset=%d\ntruncated=%t\n", len(runes), offset, end < len(runes))
	if end < len(runes) {
		fmt.Fprintf(&b, "next_offset=%d\n", end)
	}
	if capped {
		fmt.Fprintf(&b, "body_truncated=true (only the first %s of the response were read)\n", bytesHuman(webFetchMaxBody))
	}
	b.WriteString("\n")
	b.WriteString(string(runes[offset:end]))
	return b.String(), nil
}

// sameFetchHost reports a redirect that stays on the host the call was approved for;
// a www. prefix does not count as another host.
func sameFetchHost(a, b *url.URL) bool {
	host := func(u *url.URL) string {
		return strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(u.Hostname()), "."), "www.")
	}
	return host(a) == host(b)
}

// trimPartialRune drops an incomplete UTF-8 sequence that a byte cap left at the end,
// which would otherwise make the text look like another encoding.
func trimPartialRune(b []byte) []byte {
	for i := 1; i <= utf8.UTFMax && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			break
		}
	}
	return b
}

// fetchKind classifies a response as "html" or "text", or reports false for binary
// content. A missing or generic content type is sniffed from the body.
func fetchKind(contentType string, body []byte) (string, bool) {
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		media = ""
	}
	media = strings.ToLower(media)
	if media == "" || media == "application/octet-stream" || media == "binary/octet-stream" {
		sample := body[:min(len(body), 512)]
		sniffed, _, _ := mime.ParseMediaType(http.DetectContentType(sample))
		if sniffed == "text/html" || sniffed == "text/xml" {
			return textKind(sniffed), true
		}
		if strings.HasPrefix(sniffed, "text/") && !isBinary(sample) {
			return "text", true
		}
		return "", false
	}
	switch {
	case media == "text/html" || media == "application/xhtml+xml":
		return "html", true
	case strings.HasPrefix(media, "text/"),
		strings.HasSuffix(media, "+json"), strings.HasSuffix(media, "+xml"),
		media == "application/json", media == "application/xml", media == "application/javascript",
		media == "application/x-javascript", media == "application/ecmascript", media == "application/x-ndjson",
		media == "application/yaml", media == "application/x-yaml", media == "application/toml",
		media == "application/x-sh", media == "application/sql", media == "application/graphql":
		return "text", true
	}
	return "", false
}

func textKind(media string) string {
	if media == "text/html" {
		return "html"
	}
	return "text"
}

// decodeFetchBody converts body to UTF-8 using the declared charset, a byte-order
// mark or an HTML meta tag; undeclared bytes that are not UTF-8 are read as Windows-1252.
func decodeFetchBody(body []byte, contentType string) string {
	enc, name, _ := charset.DetermineEncoding(body, contentType)
	if name == "utf-8" || enc == nil {
		return strings.ToValidUTF8(string(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))), "�")
	}
	out, err := enc.NewDecoder().Bytes(body)
	if err != nil {
		return strings.ToValidUTF8(string(body), "�")
	}
	return string(out)
}
