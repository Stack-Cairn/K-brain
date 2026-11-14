package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// searchClient records the requests it is sent; native says whether it claims
// provider-native web search. It answers with one web_search call first when
// callSearch is set, then with text.
type searchClient struct {
	native     bool
	callSearch bool

	mu       sync.Mutex
	requests []ai.Request
}

func (c *searchClient) Models(context.Context) ([]ai.ModelInfo, error) { return nil, nil }
func (c *searchClient) Complete(context.Context, ai.Request) (string, ai.Usage, error) {
	return "", ai.Usage{}, nil
}
func (c *searchClient) Clone() ai.Client              { return c }
func (c *searchClient) SetCacheKey(string)            {}
func (c *searchClient) Endpoint() string              { return "http://fixture.invalid" }
func (c *searchClient) SupportsNativeWebSearch() bool { return c.native }
func (c *searchClient) Stream(_ context.Context, r ai.Request, _ func(string), _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, r)
	if c.callSearch && len(c.requests) == 1 {
		call := ai.ToolCall{ID: "call-search", Type: "function"}
		call.Function.Name = tools.WebSearchToolName
		call.Function.Arguments = `{"query":"k-brain release"}`
		return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}, StopReason: ai.StopReasonToolUse}, ai.Usage{}, nil
	}
	return ai.Message{Role: "assistant", Content: "done", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

// fakeSearchTool stands in for the configured web_search tool.
func fakeSearchTool(ran *int) tools.Tool {
	return tools.Tool{
		Def: ai.NewTool(tools.WebSearchToolName, "search", `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		Run: func(context.Context, json.RawMessage) (string, error) {
			*ran++
			return "provider=fake\nresults=0", nil
		},
	}
}

func offersTool(ts []tools.Tool, name string) bool {
	return slices.ContainsFunc(ts, func(t tools.Tool) bool { return t.Def.Function.Name == name })
}

// The fallback tool is offered exactly when search is on, the client cannot
// search natively, and a service is configured; the native flag goes to the
// provider only when the fallback does not serve the turn.
func TestWebSearchFallbackRegistrationMatrix(t *testing.T) {
	for _, native := range []bool{true, false} {
		for _, configured := range []bool{true, false} {
			for _, enabled := range []bool{true, false} {
				t.Run(fmt.Sprintf("native=%t/configured=%t/enabled=%t", native, configured, enabled), func(t *testing.T) {
					ran := 0
					a := New(&searchClient{native: native}, "m", 100, "sys")
					a.NativeWebSearch = enabled
					a.WebSearchFallback = func() (tools.Tool, bool) {
						if !configured {
							return tools.Tool{}, false
						}
						return fakeSearchTool(&ran), true
					}
					wantTool := enabled && !native && configured
					if got := offersTool(a.AllTools(), tools.WebSearchToolName); got != wantTool {
						t.Fatalf("web_search offered = %t, want %t", got, wantTool)
					}
					// Native search stays exactly as requested unless the fallback serves it.
					wantNative := enabled && !wantTool
					if got := a.requestNativeSearch(); got != wantNative {
						t.Fatalf("request NativeWebSearch = %t, want %t", got, wantNative)
					}
				})
			}
		}
	}
}

func TestWebSearchFallbackNilKeepsNativeRequest(t *testing.T) {
	a := New(&searchClient{native: false}, "m", 100, "sys")
	a.NativeWebSearch = true
	if offersTool(a.AllTools(), tools.WebSearchToolName) {
		t.Fatal("web_search offered without a fallback")
	}
	if !a.requestNativeSearch() {
		t.Fatal("native request flag dropped without a fallback; the provider must report the unsupported search")
	}
}

func TestWebSearchFallbackNotDuplicated(t *testing.T) {
	ran := 0
	a := New(&searchClient{}, "m", 100, "sys")
	a.NativeWebSearch = true
	a.Tools = append(a.Tools, fakeSearchTool(&ran))
	a.WebSearchFallback = func() (tools.Tool, bool) { return fakeSearchTool(&ran), true }
	n := 0
	for _, tool := range a.AllTools() {
		if tool.Def.Function.Name == tools.WebSearchToolName {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("web_search registered %d times", n)
	}
}

func TestWebSearchFallbackPlanModeKeepsTool(t *testing.T) {
	ran := 0
	a := New(&searchClient{}, "m", 100, "sys")
	a.NativeWebSearch = true
	a.WebSearchFallback = func() (tools.Tool, bool) { return fakeSearchTool(&ran), true }
	a.SetPlanMode(true)
	if !offersTool(a.AllTools(), tools.WebSearchToolName) {
		t.Fatal("plan mode dropped web_search")
	}
}

// A turn sends the tool definition without the native flag and runs the call.
func TestWebSearchFallbackTurn(t *testing.T) {
	ran := 0
	client := &searchClient{callSearch: true}
	a := New(client, "m", 100, "sys")
	a.WorkingDir = t.TempDir()
	a.NativeWebSearch = true
	a.WebSearchFallback = func() (tools.Tool, bool) { return fakeSearchTool(&ran), true }
	if _, err := a.Turn(context.Background(), "what is new?", Events{}); err != nil {
		t.Fatal(err)
	}
	if ran != 1 {
		t.Fatalf("web_search ran %d times", ran)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.requests) < 2 {
		t.Fatalf("requests = %d", len(client.requests))
	}
	for i, r := range client.requests {
		if r.NativeWebSearch {
			t.Fatalf("request %d asked for native search", i)
		}
		if !slices.ContainsFunc(r.Tools, func(d ai.Tool) bool { return d.Function.Name == tools.WebSearchToolName }) {
			t.Fatalf("request %d lacks web_search", i)
		}
	}
}

// A native client keeps native search and is not sent the function tool.
func TestWebSearchNativeTurnUnchanged(t *testing.T) {
	ran := 0
	client := &searchClient{native: true}
	a := New(client, "m", 100, "sys")
	a.WorkingDir = t.TempDir()
	a.NativeWebSearch = true
	a.WebSearchFallback = func() (tools.Tool, bool) { return fakeSearchTool(&ran), true }
	if _, err := a.Turn(context.Background(), "hi", Events{}); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	r := client.requests[0]
	if !r.NativeWebSearch {
		t.Fatal("native search not requested")
	}
	if slices.ContainsFunc(r.Tools, func(d ai.Tool) bool { return d.Function.Name == tools.WebSearchToolName }) {
		t.Fatal("web_search sent alongside native search")
	}
}
