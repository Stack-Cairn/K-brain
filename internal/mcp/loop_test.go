package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestAgentLoopWithMCPTool(t *testing.T) {
	m := newTestManager(t, map[string]ServerConfig{
		"docs":  testCfg("docs"),
		"ghost": {Command: []string{"nope"}, URL: "http://127.0.0.1:1/never", StartupTimeout: 2, ToolTimeout: 2},
	})

	m.Start(context.Background())
	waitReady(t, m)

	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		call++
		switch call {
		case 1:
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"mcp__docs__greet","arguments":"{\"name\":\"agent-loop\"}"}}]}}]}`+"\n\n")
		default:
			last := req.Messages[len(req.Messages)-1]
			if last.Role != "tool" || last.ToolCallID != "t1" || last.Content != "hi agent-loop" {
				t.Errorf("MCP tool result not fed back: %+v", last)
			}
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ag := agent.New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.Tools = append(tools.All(), m.Tools()...)

	final, err := ag.Turn(context.Background(), "greet me", agent.Events{})
	if err != nil {
		t.Fatal(err)
	}
	if final != "done" {
		t.Errorf("final = %q", final)
	}

	var sawGhost bool
	for _, s := range m.Statuses() {
		if s.Name == "ghost" {
			sawGhost = true
			if s.Status != StatusFailed {
				t.Errorf("ghost status = %v", s.Status)
			}
		}
	}
	if !sawGhost {
		t.Error("ghost server missing from statuses")
	}
}

func TestAgentLoopDeadServerToolCallsReturnErrors(t *testing.T) {
	m := NewManager(map[string]ServerConfig{"dead": {Command: []string{"definitely-not-a-real-binary-xyz"}, StartupTimeout: 2, ToolTimeout: 2}})
	m.Start(context.Background())
	t.Cleanup(m.Close)
	waitReady(t, m)

	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		call++
		if call == 1 {

			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"mcp__dead__anything","arguments":"{}"}}]}}]}`+"\n\n")
		} else {
			last := req.Messages[len(req.Messages)-1]
			if last.Role != "tool" || !strings.HasPrefix(last.Content, "Error:") {
				t.Errorf("expected an error tool result, got %+v", last)
			}
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"recovered"},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ag := agent.New(ai.New(srv.URL, "k"), "m", 100, "sys")

	stale := tools.Tool{
		Def: ai.NewTool("mcp__dead__anything", "stale def", `{"type":"object"}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			s := m.servers["dead"]
			return s.call(ctx, "anything", args)
		},
	}
	ag.Tools = append(tools.All(), stale)

	final, err := ag.Turn(context.Background(), "go", agent.Events{})
	if err != nil {
		t.Fatal(err)
	}
	if final != "recovered" {
		t.Errorf("final = %q", final)
	}
}
