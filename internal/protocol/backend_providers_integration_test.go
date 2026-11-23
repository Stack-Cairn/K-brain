package protocol_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/backend"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

const chainImage = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jWZkAAAAASUVORK5CYII="

// Only the upstream HTTP boundary is scripted; sessions, tools and adapters are production implementations.
func TestBackendProviderChainRestartsAndRecoversInterruptedTools(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("real-file-result"), 0600); err != nil {
		t.Fatal(err)
	}
	apis := []string{ai.APIChatCompletions, ai.APIResponses, ai.APIGemini, ai.APIMessages, ai.APIChatCompletions}
	type step struct {
		api, mode, id, text string
		prior               []string
	}
	var mu sync.Mutex
	var pending []step
	var captured [][]byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, err.Error(), 400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if len(pending) == 0 {
			t.Errorf("unexpected upstream request: %s", body)
			http.Error(w, "unexpected request", 400)
			return
		}
		want := pending[0]
		pending = pending[1:]
		captured = append(captured, body)
		checkProviderRequest(t, r, body, want.api)
		for _, text := range want.prior {
			if !strings.Contains(string(body), text) {
				t.Errorf("%s lost completed history %q: %s", want.api, text, body)
			}
		}
		for _, secret := range []string{"private-thought", "encrypted-private", "signed-private", "interrupted-call", "incomplete-answer"} {
			if want.api == ai.APIChatCompletions && secret == "private-thought" {
				continue
			}
			if want.api == ai.APIResponses && secret == "encrypted-private" {
				continue
			}
			if want.api == ai.APIMessages && (secret == "private-thought" || secret == "signed-private") {
				continue // Signed replay stays with the originating adapter.
			}
			if strings.Contains(string(body), secret) {
				t.Errorf("%s replayed provider-private or failed content %q: %s", want.api, secret, body)
			}
		}
		if want.mode == "answer" && !strings.Contains(string(body), "real-file-result") {
			t.Errorf("%s did not receive actual file tool result: %s", want.api, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeProviderFrames(w, want.api, want.mode, want.id, want.text)
	}))
	defer upstream.Close()

	dir, eventDir := t.TempDir(), t.TempDir()
	var store *session.Store
	var service *backend.Server
	var server *httptest.Server
	start := func() {
		var err error
		store, err = session.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		service, err = backend.New(backend.Options{Store: store, EventDir: eventDir, DefaultCWD: cwd,
			Factory: func(_ context.Context, workdir string, model protocol.ModelRef) (*agent.Agent, error) {
				client, err := ai.NewClient(ai.ClientOptions{API: model.Provider, BaseURL: upstream.URL, APIKey: "chain-key", MaxRetries: 1})
				if err != nil {
					return nil, err
				}
				ag := agent.New(client, model.Model, 4096, "Chain integration system")
				ag.ModelName, ag.Provider, ag.WorkingDir = model.Model, model.Provider, workdir
				ag.Vision, ag.MaxTurns = true, 4
				return ag, nil
			}})
		if err != nil {
			t.Fatal(err)
		}
		server = httptest.NewServer(service)
	}
	stop := func() {
		server.Close()
		if err := service.Close(); err != nil {
			t.Error(err)
		}
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}
	start()
	defer func() { stop() }()
	var view protocol.Session
	chainJSON(t, http.MethodPost, server.URL+"/v1/sessions", protocol.CreateSessionRequest{CWD: cwd, Model: protocol.ModelRef{Provider: apis[0], Model: "chain-model"}}, http.StatusCreated, &view)
	var prior []string
	var lastSeq int64
	var allEvents []protocol.Event
	for i, api := range apis {
		selected := protocol.ModelRef{Provider: api, Model: "chain-model"}
		before := append([]protocol.Message(nil), view.Messages...)
		chainJSON(t, http.MethodPatch, server.URL+"/v1/sessions/"+view.ID, protocol.UpdateSessionRequest{Model: &selected}, http.StatusOK, &view)
		if len(before) != len(view.Messages) || !reflect.DeepEqual(append([]protocol.Message(nil), before...), append([]protocol.Message(nil), view.Messages...)) {
			t.Fatal("model switch mutated canonical history")
		}
		id, answer := fmt.Sprintf("call-%d", i), fmt.Sprintf("completed-answer-%d", i)
		mu.Lock()
		pending = append(pending, step{api: api, mode: "tool", id: id, prior: prior}, step{api: api, mode: "answer", text: answer, prior: prior})
		mu.Unlock()
		content := []protocol.ContentBlock{{Type: protocol.ContentText, Text: "before-image"}, {Type: protocol.ContentImage, ImageURL: chainImage}, {Type: protocol.ContentText, Text: "after-image"}}
		var accepted protocol.RunAccepted
		chainJSON(t, http.MethodPost, server.URL+"/v1/sessions/"+view.ID+"/runs", protocol.PromptRequest{ConversationID: view.ID, ClientRequestID: fmt.Sprintf("success-%d", i), Content: content}, http.StatusAccepted, &accepted)
		events := chainEvents(t, server.URL, view.ID, lastSeq)
		assertChainEvents(t, events, accepted.RunID, protocol.EventRunCompleted, true)
		allEvents = append(allEvents, events...)
		lastSeq = events[len(events)-1].Seq
		chainJSON(t, http.MethodGet, server.URL+"/v1/sessions/"+view.ID, nil, http.StatusOK, &view)
		assertChainHistory(t, view, i+1)
		prior = append(append([]string(nil), prior...), answer)

		if i == len(apis)-1 {
			break
		}
		mu.Lock()
		pending = append(pending, step{api: api, mode: "interrupt", id: "interrupted-call", text: "incomplete-answer", prior: prior})
		mu.Unlock()
		chainJSON(t, http.MethodPost, server.URL+"/v1/sessions/"+view.ID+"/runs", protocol.PromptRequest{ConversationID: view.ID, ClientRequestID: fmt.Sprintf("failure-%d", i), Prompt: "interrupt then switch"}, http.StatusAccepted, &accepted)
		events = chainEvents(t, server.URL, view.ID, lastSeq)
		assertChainEvents(t, events, accepted.RunID, protocol.EventRunFailed, false)
		allEvents = append(allEvents, events...)
		lastSeq = events[len(events)-1].Seq
		chainJSON(t, http.MethodGet, server.URL+"/v1/sessions/"+view.ID, nil, http.StatusOK, &view)
		assertChainHistory(t, view, i+1)
		beforeRestart := view
		stop()
		start()
		chainJSON(t, http.MethodGet, server.URL+"/v1/sessions/"+view.ID, nil, http.StatusOK, &view)
		if !reflect.DeepEqual(beforeRestart.Messages, view.Messages) || view.Model != selected || view.LastSeq != lastSeq {
			t.Fatalf("restart changed canonical session: before=%+v after=%+v", beforeRestart, view)
		}
		replay := chainEvents(t, server.URL, view.ID, 0)
		if len(replay) == 0 || len(replay) > len(allEvents) {
			t.Fatalf("restart lost the canonical event journal: replay=%d expected-at-least=%d", len(replay), len(allEvents))
		}
		for j := range replay {
			if replay[j].Seq != allEvents[j].Seq || replay[j].Type != allEvents[j].Type || replay[j].RunID != allEvents[j].RunID {
				t.Fatalf("restart changed event %d: replay=%+v expected=%+v", j, replay[j], allEvents[j])
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pending) != 0 || len(captured) != 14 {
		t.Fatalf("upstream requests=%d pending=%d, want exactly 14 requests", len(captured), len(pending))
	}
}

func chainJSON(t *testing.T, method, url string, input any, status int, output any) {
	t.Helper()
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = strings.NewReader(string(data))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("%s %s status=%d want=%d: %s", method, url, resp.StatusCode, status, data)
	}
	if output != nil {
		if err := json.Unmarshal(data, output); err != nil {
			t.Fatal(err)
		}
	}
}

func chainEvents(t *testing.T, base, id string, seq int64) []protocol.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/sessions/"+id+"/events?after_seq="+strconv.FormatInt(seq, 10), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status=%d", resp.StatusCode)
	}
	var events []protocol.Event
	scan := bufio.NewScanner(resp.Body)
	for scan.Scan() {
		if !strings.HasPrefix(scan.Text(), "data: ") {
			continue
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(scan.Text(), "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if err := event.Validate(); err != nil {
			t.Fatal(err)
		}
		if event.Seq != seq+1 {
			t.Fatalf("event sequence=%d, want=%d", event.Seq, seq+1)
		}
		seq = event.Seq
		events = append(events, event)
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func assertChainEvents(t *testing.T, events []protocol.Event, run, terminal string, tools bool) {
	t.Helper()
	var calls, results, thinking, terminals int
	for _, event := range events {
		if event.RunID != run {
			t.Fatalf("unexpected run event: %+v", event)
		}
		switch event.Type {
		case protocol.EventToolCall:
			calls++
		case protocol.EventToolResult:
			results++
		case protocol.EventThinkingDelta:
			thinking++
		case protocol.EventRunCompleted, protocol.EventRunFailed, protocol.EventRunCancelled:
			terminals++
			if event.Type != terminal {
				t.Fatalf("terminal=%s, want=%s: %s", event.Type, terminal, event.Payload)
			}
		}
	}
	if terminals != 1 || len(events) == 0 || events[len(events)-1].Type != terminal || (tools && (calls != 1 || results != 1 || thinking == 0)) || (!tools && (calls != 0 || results != 0)) {
		t.Fatalf("canonical events terminal=%d calls=%d results=%d thinking=%d: %+v", terminals, calls, results, thinking, events)
	}
}

func assertChainHistory(t *testing.T, view protocol.Session, rounds int) {
	t.Helper()
	calls, results := map[string]int{}, map[string]int{}
	var images int
	for _, message := range view.Messages {
		if err := message.Validate(); err != nil {
			t.Fatal(err)
		}
		for _, call := range message.ToolCalls {
			calls[call.ID]++
			if call.Name != "read" || string(call.Arguments) != `{"path":"notes.txt"}` {
				t.Fatalf("call=%+v", call)
			}
		}
		if message.Role == protocol.RoleTool {
			results[message.ToolCallID]++
			if message.Name != "read" || len(message.Content) != 1 || !strings.Contains(message.Content[0].Text, "real-file-result") {
				t.Fatalf("tool result=%+v", message)
			}
		}
		for _, block := range message.Content {
			// Streamed reasoning is kept for display as a thinking block (it is
			// never sent back, see the request checks); it must not leak into text.
			if strings.Contains(block.Text, "incomplete-answer") || block.Type != protocol.ContentThinking && strings.Contains(block.Text, "private-thought") {
				t.Fatalf("failed/private message persisted: %+v", message)
			}
			if block.Type == protocol.ContentImage {
				images++
				if block.ImageURL != chainImage {
					t.Fatalf("changed image=%+v", block)
				}
			}
		}
	}
	if len(calls) != rounds || len(results) != rounds || images != rounds {
		t.Fatalf("history calls=%v results=%v images=%d rounds=%d", calls, results, images, rounds)
	}
	for id, count := range calls {
		if count != 1 || results[id] != 1 {
			t.Fatalf("unpaired/duplicated tool %s calls=%d results=%d", id, count, results[id])
		}
	}
}

func checkProviderRequest(t *testing.T, r *http.Request, body []byte, api string) {
	t.Helper()
	path := map[string]string{ai.APIChatCompletions: "/chat/completions", ai.APIResponses: "/responses", ai.APIMessages: "/v1/messages", ai.APIGemini: "/v1beta/models/chain-model:streamGenerateContent"}[api]
	if r.Method != http.MethodPost || r.URL.Path != path {
		t.Errorf("%s endpoint=%s %s", api, r.Method, r.URL)
	}
	switch api {
	case ai.APIMessages:
		if r.Header.Get("X-Api-Key") != "chain-key" || r.Header.Get("Anthropic-Version") != "2023-06-01" {
			t.Error("Anthropic auth headers missing")
		}
	case ai.APIGemini:
		if r.URL.Query().Get("key") != "chain-key" {
			t.Error("Gemini key missing")
		}
	default:
		if r.Header.Get("Authorization") != "Bearer chain-key" {
			t.Error("Bearer key missing")
		}
	}
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		t.Error(err)
		return
	}
	if request["tools"] == nil {
		t.Errorf("%s has no production tool definitions", api)
	}
	if api != ai.APIGemini && request["model"] != "chain-model" {
		t.Errorf("%s wrong model=%v", api, request["model"])
	}
}

func writeProviderFrames(w http.ResponseWriter, api, mode, id, text string) {
	send := func(value any) { data, _ := json.Marshal(value); fmt.Fprintf(w, "data: %s\n\n", data) }
	call := mode != "answer"
	interrupt := mode == "interrupt"
	switch api {
	case ai.APIChatCompletions:
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": "private-thought"}}}})
		if call {
			send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": "read", "arguments": `{"path":"notes.txt"}`}}}}}}})
		} else {
			send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": text}}}})
		}
		send(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 3}})
		if !interrupt {
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
	case ai.APIResponses:
		send(map[string]any{"type": "response.created", "response": map[string]any{"usage": map[string]any{"input_tokens": 20, "output_tokens": 3}}})
		send(map[string]any{"type": "response.reasoning_summary_text.delta", "delta": "private-thought"})
		send(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "reasoning", "id": "private-reasoning-id", "encrypted_content": "encrypted-private"}})
		var output []any
		if call {
			item := map[string]any{"type": "function_call", "id": "item-" + id, "call_id": id, "name": "read", "arguments": `{"path":"notes.txt"}`}
			send(map[string]any{"type": "response.output_item.added", "item": item})
			output = append(output, item)
		} else {
			output = append(output, map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": text}}})
		}
		if !interrupt {
			send(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": output, "usage": map[string]any{"input_tokens": 20, "output_tokens": 3}}})
		}
	case ai.APIMessages:
		send(map[string]any{"type": "message_start", "message": map[string]any{"usage": map[string]any{"input_tokens": 20, "output_tokens": 3}}})
		send(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "thinking", "thinking": "private-thought", "signature": "signed-private"}})
		send(map[string]any{"type": "content_block_stop", "index": 0})
		block := map[string]any{"type": "text", "text": text}
		if call {
			block = map[string]any{"type": "tool_use", "id": id, "name": "read", "input": map[string]any{"path": "notes.txt"}}
		}
		send(map[string]any{"type": "content_block_start", "index": 1, "content_block": block})
		if !interrupt {
			send(map[string]any{"type": "content_block_stop", "index": 1})
			reason := "end_turn"
			if call {
				reason = "tool_use"
			}
			send(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason}, "usage": map[string]any{"output_tokens": 3}})
			send(map[string]any{"type": "message_stop"})
		}
	case ai.APIGemini:
		parts := []any{map[string]any{"thought": true, "text": "private-thought"}}
		if call {
			parts = append(parts, map[string]any{"functionCall": map[string]any{"id": id, "name": "read", "args": map[string]any{"path": "notes.txt"}}})
		} else {
			parts = append(parts, map[string]any{"text": text})
		}
		candidate := map[string]any{"content": map[string]any{"role": "model", "parts": parts}}
		if !interrupt {
			candidate["finishReason"] = "STOP"
		}
		send(map[string]any{"candidates": []any{candidate}, "usageMetadata": map[string]any{"promptTokenCount": 20, "candidatesTokenCount": 3}})
	}
}
