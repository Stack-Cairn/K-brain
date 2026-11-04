package ai

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
)

type SearchSource struct {
	URL        string `json:"url"`
	Title      string `json:"title,omitempty"`
	SourceType string `json:"sourceType,omitempty"`
}
type HostedSearch struct {
	Type     string         `json:"type"`
	ID       string         `json:"id"`
	Provider string         `json:"provider,omitempty"`
	Status   string         `json:"status"`
	Queries  []string       `json:"queries"`
	Sources  []SearchSource `json:"sources"`
	Error    string         `json:"error,omitempty"`
}
type searchObserverKey struct{}

func WithSearchObserver(ctx context.Context, observer func(HostedSearch)) context.Context {
	return context.WithValue(ctx, searchObserverKey{}, observer)
}

type searchStream struct {
	ctx      context.Context
	provider string
	blocks   []HostedSearch
	indices  map[int]string
	inputs   map[int]string
	last     string
}

func newSearchStream(ctx context.Context, provider string) *searchStream {
	return &searchStream{ctx: ctx, provider: provider, indices: map[int]string{}, inputs: map[int]string{}}
}
func appendSearchTool(existing any, tool map[string]any) []any {
	var tools []any
	if current, ok := existing.([]any); ok {
		tools = append(tools, current...)
	}
	for _, v := range tools {
		if m, ok := v.(map[string]any); ok && m["type"] == tool["type"] {
			return tools
		}
	}
	return append(tools, tool)
}
func searchRecord(v any) map[string]any { m, _ := v.(map[string]any); return m }
func searchString(v any) string         { s, _ := v.(string); return strings.TrimSpace(s) }
func searchArray(v any) []any           { a, _ := v.([]any); return a }
func searchObject(v any) map[string]any {
	if s, ok := v.(string); ok {
		var m map[string]any
		_ = json.Unmarshal([]byte(s), &m)
		return m
	}
	return searchRecord(v)
}
func searchURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}
func searchSources(v any, kind string) []SearchSource {
	if a, ok := v.([]any); ok {
		var out []SearchSource
		for _, x := range a {
			out = append(out, searchSources(x, kind)...)
		}
		return out
	}
	m := searchRecord(v)
	raw := firstNonEmpty(searchString(m["url"]), searchString(m["uri"]), searchString(m["link"]))
	if !searchURL(raw) {
		return nil
	}
	return []SearchSource{{URL: raw, Title: firstNonEmpty(searchString(m["title"]), searchString(m["name"])), SourceType: kind}}
}
func searchQueries(values ...any) []string {
	var out []string
	var add func(any)
	add = func(v any) {
		if a, ok := v.([]any); ok {
			for _, x := range a {
				add(x)
			}
			return
		}
		s := searchString(v)
		if s == "" {
			return
		}
		for _, x := range out {
			if x == s {
				return
			}
		}
		out = append(out, s)
	}
	for _, v := range values {
		add(v)
	}
	return out
}
func (s *searchStream) update(id, status string, queries []string, sources []SearchSource, problem string) {
	if id == "" {
		id = s.last
	}
	if id == "" {
		id = "hosted-search-" + s.provider
	}
	s.last = id
	var b HostedSearch
	for _, x := range s.blocks {
		if x.ID == id {
			b = x
			break
		}
	}
	b.Type = "hostedSearch"
	b.ID = id
	b.Provider = s.provider
	if b.Status == "" {
		b.Status = status
	}
	if status == "failed" || b.Status == "searching" {
		b.Status = status
	}
	if problem != "" {
		b.Error = problem
	}
	for _, q := range queries {
		found := false
		for _, x := range b.Queries {
			if x == q {
				found = true
			}
		}
		if !found {
			b.Queries = append(b.Queries, q)
		}
	}
	for _, v := range sources {
		found := false
		for i, x := range b.Sources {
			if x.URL == v.URL {
				found = true
				if v.Title != "" {
					b.Sources[i].Title = v.Title
				}
				if v.SourceType == "citation" {
					b.Sources[i].SourceType = "citation"
				}
			}
		}
		if !found {
			b.Sources = append(b.Sources, v)
		}
	}
	for i, x := range s.blocks {
		if x.ID == id {
			s.blocks[i] = b
			if observer, ok := s.ctx.Value(searchObserverKey{}).(func(HostedSearch)); ok && observer != nil {
				observer(b)
			}
			return
		}
	}
	s.blocks = append(s.blocks, b)
	if observer, ok := s.ctx.Value(searchObserverKey{}).(func(HostedSearch)); ok && observer != nil {
		observer(b)
	}
}
func (s *searchStream) finish(failed bool) {
	for _, b := range s.blocks {
		if b.Status == "searching" {
			st := "completed"
			if failed {
				st = "failed"
			}
			s.update(b.ID, st, nil, nil, "")
		}
	}
}
func (s *searchStream) accept(data, eventType string) {
	var raw map[string]any
	if json.Unmarshal([]byte(data), &raw) != nil {
		return
	}
	typ := firstNonEmpty(searchString(raw["type"]), eventType)
	switch s.provider {
	case "gemini":
		for _, v := range searchArray(raw["candidates"]) {
			g := searchRecord(searchRecord(v)["groundingMetadata"])
			q := searchQueries(g["webSearchQueries"])
			var src []SearchSource
			for _, x := range searchArray(g["groundingChunks"]) {
				src = append(src, searchSources(searchRecord(x)["web"], "source")...)
			}
			if len(q) > 0 || len(src) > 0 {
				st := "searching"
				if len(src) > 0 {
					st = "completed"
				}
				s.update("", st, q, src, "")
			}
		}
	case "claude_code":
		idx, _ := raw["index"].(float64)
		i := int(idx)
		b := searchRecord(raw["content_block"])
		if typ == "content_block_start" {
			bt := searchString(b["type"])
			if bt == "server_tool_use" && nativeSearchName(searchString(b["name"])) {
				id := searchString(b["id"])
				s.indices[i] = id
				in := searchRecord(b["input"])
				s.update(id, "searching", searchQueries(in["query"]), nil, "")
			}
			if bt == "web_search_tool_result" || bt == "web_fetch_tool_result" {
				s.update(searchString(b["tool_use_id"]), "completed", nil, searchSources(b["content"], "source"), "")
			}
		}
		if typ == "content_block_delta" {
			d := searchRecord(raw["delta"])
			if id := s.indices[i]; id != "" && searchString(d["type"]) == "input_json_delta" {
				s.inputs[i] += searchString(d["partial_json"])
				in := searchObject(s.inputs[i])
				s.update(id, "searching", searchQueries(in["query"]), nil, "")
			}
			if src := searchSources(d["citation"], "citation"); len(src) > 0 {
				s.update("", "completed", nil, src, "")
			}
		}
	default:
		if item := searchRecord(raw["item"]); item != nil {
			s.responsesItem(item, strings.HasSuffix(typ, ".done"))
		}
		for _, v := range searchArray(searchRecord(raw["response"])["output"]) {
			s.responsesItem(searchRecord(v), true)
		}
		for _, v := range []any{raw["annotation"], raw["annotations"]} {
			if src := searchSources(v, "citation"); len(src) > 0 {
				s.update("", "completed", nil, src, "")
			}
		}
	}
}
func (s *searchStream) responsesItem(item map[string]any, done bool) {
	typ := searchString(item["type"])
	if typ != "web_search_call" && typ != "x_search_call" && typ != "x_search_call_output" && !(typ == "custom_tool_call" && nativeSearchName(searchString(item["name"]))) {
		return
	}
	a := searchObject(item["action"])
	in := searchObject(firstNonEmpty(searchString(item["input"]), searchString(item["arguments"])))
	q := searchQueries(a["query"], a["queries"], item["query"], in["query"])
	var src []SearchSource
	for _, k := range []string{"sources", "results", "output"} {
		src = append(src, searchSources(a[k], "source")...)
		src = append(src, searchSources(item[k], "source")...)
	}
	st := "searching"
	if done || searchString(item["status"]) == "completed" {
		st = "completed"
	}
	if searchString(item["status"]) == "failed" {
		st = "failed"
	}
	s.update(firstNonEmpty(searchString(item["id"]), searchString(item["call_id"])), st, q, src, "")
}
func nativeSearchName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "websearch" || n == "builtin_web_search" || n == "web_search" || n == "web_search_preview" || strings.HasPrefix(n, "web_search_2") || strings.HasPrefix(n, "web_search_call") || n == "x_search" || n == "x_keyword_search" || n == "x_semantic_search" || strings.HasPrefix(n, "x_search_call") || n == "webfetch" || n == "builtin_web_fetch" || n == "web_fetch" || strings.HasPrefix(n, "web_fetch_2") || strings.HasPrefix(n, "web_fetch_call")
}
func searchBridge(call ToolCall, blocks []HostedSearch) string {
	var args map[string]any
	_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
	text := "This endpoint emitted a provider-native search request as a client tool call. No client-side search was executed."
	if strings.Contains(strings.ToLower(call.Function.Name), "fetch") {
		text = "This endpoint did not execute the provider-native web_fetch request, so the page content is unavailable."
	}
	q := firstNonEmpty(searchString(args["query"]), searchString(args["search_query"]), searchString(args["additionalContext"]), searchString(args["url"]), searchString(args["uri"]))
	if q != "" {
		text += "\nRequested query or URL: " + q
	}
	seen := map[string]bool{}
	for _, b := range blocks {
		for _, v := range b.Sources {
			if !seen[v.URL] && len(seen) < 10 {
				seen[v.URL] = true
				text += "\n" + firstNonEmpty(v.Title, v.URL) + " - " + v.URL
			}
		}
	}
	if len(seen) == 0 {
		text += "\nNo provider-hosted search sources were returned."
	}
	return text + "\nDo not retry this search/fetch tool. Answer from the available sources and context, and disclose unavailable information."
}

// hostedSearchCall reports a call of the provider's native search or fetch, which the
// client does not run. A name the request declares as a client tool (the CLI's
// web_fetch) stays the client's.
func (r Request) hostedSearchCall(name string) bool {
	if !r.NativeWebSearch || !nativeSearchName(name) {
		return false
	}
	for _, t := range r.Tools {
		if t.Function.Name == name {
			return false
		}
	}
	return true
}
func recoverNativeSearchCalls(req Request, msg *Message, onText func(string)) {
	var kept []ToolCall
	for _, call := range msg.ToolCalls {
		if !req.hostedSearchCall(call.Function.Name) {
			kept = append(kept, call)
			continue
		}
		text := "\n" + searchBridge(call, msg.HostedSearch)
		msg.Content += text
		if onText != nil {
			onText(text)
		}
	}
	msg.ToolCalls = kept
}
func filterNativeSearchCalls(req Request, calls []ToolCall) []ToolCall {
	out := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		if !req.hostedSearchCall(call.Function.Name) {
			out = append(out, call)
		}
	}
	return out
}
