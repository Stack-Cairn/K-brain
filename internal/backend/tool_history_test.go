package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestServerToolHistoryPermissionAndRestore(t *testing.T) {
	for _, tc := range []struct {
		name       string
		decision   string
		wantReason string
	}{
		{name: "deny", decision: "reject", wantReason: string(ai.StopReasonError)},
		{name: "allow", decision: "allow_once"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			storeDir := store.SessionsDir()
			eventDir := t.TempDir()
			fixture := newToolHistoryAI(t)
			backendServer, httpServer := newToolHistoryServer(t, store, eventDir, fixture)

			sess := createTestSession(t, httpServer.URL)
			accepted := runRequest(t, httpServer.URL, sess.ID, "tool-history-1", "run the command")
			events := readToolHistoryRun(t, httpServer.URL, sess.ID, accepted.RunID, tc.decision)
			if len(events) == 0 || events[len(events)-1].Type != protocol.EventRunCompleted {
				t.Fatalf("terminal events = %+v", events)
			}
			if !hasToolHistoryEvent(events, protocol.EventPermissionRequest) || !hasToolHistoryEvent(events, protocol.EventPermissionResult) {
				t.Fatalf("permission events missing: %+v", events)
			}
			if !hasToolHistoryEvent(events, protocol.EventToolCall) || !hasToolHistoryEvent(events, protocol.EventToolResult) {
				t.Fatalf("tool events missing: %+v", events)
			}
			if !hasToolHistoryEvent(events, protocol.EventTextDelta) {
				t.Fatalf("streaming text event missing: %+v", events)
			}
			toolResult := toolHistoryToolResult(t, events)
			if toolResult.Failed != (tc.wantReason != "") {
				t.Fatalf("tool failure flag = %v, expected failure = %v", toolResult.Failed, tc.wantReason != "")
			}
			resultCount := 0
			for _, event := range events {
				if event.Type == protocol.EventToolResult {
					resultCount++
				}
			}
			if resultCount != 1 {
				t.Fatalf("tool result count = %d", resultCount)
			}
			if tc.wantReason == "" {
				if !strings.Contains(toolResult.Output, "early") || !strings.Contains(toolResult.Output, "late") {
					t.Fatalf("allowed terminal output = %q", toolResult.Output)
				}
			} else if !strings.Contains(toolResult.Output, "Permission denied") {
				t.Fatalf("denied terminal output = %q", toolResult.Output)
			}

			toolMessage := toolHistoryToolMessage(t, getSession(t, httpServer.URL, sess.ID))
			if tc.wantReason != "" {
				if toolMessage.StopReason != tc.wantReason {
					t.Fatalf("denied tool stop reason = %q, want %q", toolMessage.StopReason, tc.wantReason)
				}
				if !strings.Contains(toolMessage.Content[0].Text, "Permission denied") {
					t.Fatalf("denied tool output = %+v", toolMessage.Content)
				}
			} else if toolMessage.StopReason != "" {
				t.Fatalf("successful tool stop reason = %q, want empty", toolMessage.StopReason)
			}

			if err := backendServer.Close(); err != nil {
				t.Fatal(err)
			}
			httpServer.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			store2, err := session.Open(storeDir)
			if err != nil {
				t.Fatal(err)
			}
			defer store2.Close()
			_, httpServer2 := newToolHistoryServer(t, store2, eventDir, fixture)
			restored := toolHistoryToolMessage(t, getSession(t, httpServer2.URL, sess.ID))
			if restored.StopReason != toolMessage.StopReason {
				t.Fatalf("restored tool stop reason = %q, original = %q", restored.StopReason, toolMessage.StopReason)
			}
			if tc.wantReason != "" && restored.StopReason != tc.wantReason {
				t.Fatalf("restored denied tool stop reason = %q, want %q", restored.StopReason, tc.wantReason)
			}
			if tc.wantReason == "" && restored.StopReason != "" {
				t.Fatalf("restored successful tool stop reason = %q, want empty", restored.StopReason)
			}
		})
	}
}

func newToolHistoryAI(t *testing.T) ai.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ai.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode AI request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		last := request.Messages[len(request.Messages)-1]
		if last.Role == "tool" {
			writeToolHistorySSE(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "command handled"}, "finish_reason": "stop"}}})
		} else {
			args, _ := json.Marshal(map[string]string{"command": "printf 'early\\n'; sleep 0.05; printf 'late\\n'"})
			writeToolHistorySSE(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{
				"index": 0,
				"id":    "tool-history-call",
				"type":  "function",
				"function": map[string]any{
					"name":      "bash",
					"arguments": string(args),
				},
			}}}, "finish_reason": "tool_calls"}}})
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(server.Close)
	client := ai.New(server.URL, "fixture-key")
	client.MaxRetries = 0
	return client
}

func writeToolHistorySSE(w io.Writer, value any) {
	data, _ := json.Marshal(value)
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func newToolHistoryServer(t *testing.T, store *session.Store, eventDir string, client ai.Client) (*Server, *httptest.Server) {
	t.Helper()
	factory := func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
		ag := agent.New(client, selected.Model, 1024, "system")
		ag.ModelName = selected.Model
		ag.Provider = selected.Provider
		return ag, nil
	}
	backendServer, err := New(Options{
		Store:      store,
		Factory:    factory,
		EventDir:   eventDir,
		Models:     []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}},
		DefaultCWD: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(backendServer)
	t.Cleanup(func() {
		httpServer.Close()
		_ = backendServer.Close()
	})
	return backendServer, httpServer
}

func readToolHistoryRun(t *testing.T, base, sessionID, runID, decision string) []protocol.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/sessions/"+sessionID+"/events?after_seq=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("events status %d: %s", resp.StatusCode, b)
	}

	var events []protocol.Event
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
		if event.Type == protocol.EventPermissionRequest {
			var permission protocol.PermissionRequest
			if err := json.Unmarshal(event.Payload, &permission); err != nil {
				t.Fatal(err)
			}
			decisionResponse := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sessionID+"/permissions/"+permission.PermissionID, protocol.PermissionDecisionRequest{
				ConversationID: sessionID,
				RunID:          runID,
				Decision:       protocol.PermissionDecision{PermissionID: permission.PermissionID, Decision: decision},
			}, nil)
			if decisionResponse.StatusCode != http.StatusOK {
				t.Fatalf("permission status %d", decisionResponse.StatusCode)
			}
			decisionResponse.Body.Close()
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func hasToolHistoryEvent(events []protocol.Event, typ string) bool {
	for _, event := range events {
		if event.Type == typ {
			return true
		}
	}
	return false
}

func toolHistoryToolResult(t *testing.T, events []protocol.Event) protocol.ToolResult {
	t.Helper()
	for _, event := range events {
		if event.Type != protocol.EventToolResult {
			continue
		}
		var result protocol.ToolResultEvent
		if err := json.Unmarshal(event.Payload, &result); err != nil {
			t.Fatal(err)
		}
		return result.ToolResult
	}
	t.Fatalf("tool result event not found: %+v", events)
	return protocol.ToolResult{}
}

func toolHistoryToolMessage(t *testing.T, sess protocol.Session) protocol.Message {
	t.Helper()
	for _, message := range sess.Messages {
		if message.Role == protocol.RoleTool && message.ToolCallID == "tool-history-call" {
			return message
		}
	}
	t.Fatalf("tool message not found in session: %+v", sess.Messages)
	return protocol.Message{}
}
