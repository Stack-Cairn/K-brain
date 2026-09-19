package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestOutputLimitPersistsAndResumesThroughACP(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var requests, runs atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if n > 1 && (!strings.Contains(string(body), "partial") || strings.Contains(string(body), `"raw_stop_reason"`)) {
					t.Errorf("incorrect resumed context: %s", body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(s string) { fmt.Fprintf(w, "data: %s\n\n", s) }
				switch protocol {
				case "responses":
					if n == 1 {
						emit(`{"type":"response.output_item.added","item":{"id":"item","type":"function_call","call_id":"call","name":"write_test","arguments":"{}"}}`)
						emit(`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"text":"partial"}]}],"usage":{"input_tokens":17,"output_tokens":3}}}`)
					} else {
						emit(`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"text":"done"}]}],"usage":{"input_tokens":17,"output_tokens":3}}}`)
					}
				case "anthropic":
					emit(`{"type":"message_start","message":{"usage":{"input_tokens":17,"output_tokens":3}}}`)
					if n == 1 {
						emit(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"partial"}}`)
						emit(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call","name":"write_test","input":{}}}`)
						emit(`{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`)
					} else {
						emit(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"done"}}`)
						emit(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)
					}
					emit(`{"type":"message_stop"}`)
				default:
					if n == 1 {
						emit(`{"choices":[{"delta":{"content":"partial","tool_calls":[{"index":0,"id":"call","function":{"name":"write_test","arguments":"{}"}}]},"finish_reason":"length"}],"usage":{"prompt_tokens":17,"completion_tokens":3}}`)
					} else {
						emit(`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":17,"completion_tokens":3}}`)
					}
					emit(`[DONE]`)
				}
			}))
			defer srv.Close()
			st := testStore(t)
			f := newFixture(t, nil, st, func(ctx context.Context, cwd string, servers map[string]mcp.ServerConfig) (*agent.Agent, *mcp.Manager, error) {
				ag, manager, err := factoryFor(srv, []tools.Tool{{Def: ai.NewTool("write_test", "test", `{"type":"object"}`), Run: func(context.Context, json.RawMessage) (string, error) { runs.Add(1); return "unexpected", nil }}})(ctx, cwd, servers)
				if protocol == "responses" {
					ag.Client = ai.NewResponses(srv.URL, "key")
				}
				if protocol == "anthropic" {
					ag.Client = ai.NewAnthropic(srv.URL, "key")
				}
				return ag, manager, err
			})
			f.initialize(t)
			dir := t.TempDir()
			id := f.newSession(t, dir)
			response, err := f.prompt(t, id, "first")
			if err != nil || response.StopReason != acp.StopReasonMaxTokens || runs.Load() != 0 {
				t.Fatalf("first prompt: %+v, %v; tool runs=%d", response, err, runs.Load())
			}
			_, msgs, err := st.Load(string(id))
			if err != nil {
				t.Fatal(err)
			}
			last := msgs[len(msgs)-1]
			if last.StopReason != ai.StopReasonLength || !strings.HasPrefix(last.Content, "partial\n") || last.Usage == nil || last.Usage.PromptTokens != 17 || len(last.ToolCalls) != 0 {
				t.Fatalf("saved partial: %+v", last)
			}
			if _, err := f.conn.CloseSession(t.Context(), acp.CloseSessionRequest{SessionId: id}); err != nil {
				t.Fatal(err)
			}
			if err := restoreForTest(t.Context(), f.bridge, id, dir, true); err != nil {
				t.Fatal(err)
			}
			if f.bridge.getSession(id).ag.LastStopReason() != ai.StopReasonLength {
				t.Fatal("stop reason lost on load")
			}
			response, err = f.prompt(t, id, "continue")
			if err != nil || response.StopReason != acp.StopReasonEndTurn || requests.Load() != 2 || runs.Load() != 0 {
				t.Fatalf("continued prompt: %+v, %v; requests=%d tools=%d", response, err, requests.Load(), runs.Load())
			}
			u := f.bridge.getSession(id).ag.UsageSummary().Total
			if u.PromptTokens != 34 || u.CompletionTokens != 6 {
				t.Fatalf("usage after resume: %+v", u)
			}
		})
	}
}
