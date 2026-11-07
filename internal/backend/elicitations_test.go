package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/mcp/mcptest"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// The fake MCP server is this test binary itself (TestMain serves
// mcptest.ServeElicit when mcptest.ElicitServerEnv is set), over stdio as a
// configured server would run.

const elicitSchema = `{"type":"object","properties":{"username":{"type":"string","title":"User name","minLength":2},"password":{"type":"string","title":"Password"},"plan":{"type":"string","oneOf":[{"const":"free","title":"Free"},{"const":"pro","title":"Pro"}],"default":"free"},"seats":{"type":"integer","minimum":1,"maximum":50},"notify":{"type":"boolean","default":true},"topics":{"type":"array","items":{"type":"string","enum":["news","billing"]},"maxItems":2}},"required":["username","password","seats"]}`

const elicitSecret = "s3cret-value-must-not-be-journaled"

// elicitClient calls the fake server's ask tool once, then repeats the tool
// result as its answer.
type elicitClient struct {
	scriptedClient
	args string
}

func (c *elicitClient) Clone() ai.Client { return c }
func (c *elicitClient) Stream(_ context.Context, request ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	for _, message := range request.Messages {
		if message.Role == "tool" {
			text("RESULT " + message.Content)
			return ai.Message{Role: "assistant", Content: "RESULT " + message.Content, StopReason: ai.StopReasonStop}, ai.Usage{}, nil
		}
	}
	call := ai.ToolCall{ID: "elicit-call", Type: "function"}
	call.Function.Name, call.Function.Arguments = "mcp__forms__ask", c.args
	return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}, StopReason: ai.StopReasonToolUse}, ai.Usage{}, nil
}

type elicitEnv struct {
	server   *httptest.Server
	store    *session.Store
	eventDir string
	root     string
}

func elicitFixture(t *testing.T, timeout time.Duration, message, schema string) elicitEnv {
	t.Helper()
	root := t.TempDir()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	live, err := mcp.NewLiveManager(context.Background(), filepath.Join(root, "mcp.json"), mcp.LiveSettings{Servers: []mcp.LiveServer{{
		ID: "forms", Enabled: true, Transport: "stdio", Command: exe, Env: map[string]string{mcptest.ElicitServerEnv: "1"},
		Policy: map[string]string{"ask": "allow"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(live.ToolsFor(context.Background(), root, nil)) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("MCP not ready: %+v", live.Statuses())
		}
		time.Sleep(10 * time.Millisecond)
	}
	args, _ := json.Marshal(map[string]any{"message": message, "schema": schema})
	eventDir := filepath.Join(root, "events")
	service, err := New(Options{Store: store, EventDir: eventDir, MemoryRoot: filepath.Join(root, "memory"), DefaultCWD: root, MCP: live, QuestionTimeout: timeout,
		Models: []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}},
		Factory: func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
			return agent.New(&elicitClient{args: string(args)}, m.Model, 4096, "system"), nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service)
	t.Cleanup(func() { server.Close(); service.Close(); live.Close(); store.Close() })
	return elicitEnv{server: server, store: store, eventDir: eventDir, root: root}
}

func startElicitRun(t *testing.T, env elicitEnv, optIn bool) (protocol.Session, protocol.RunAccepted, *bufio.Scanner) {
	t.Helper()
	sess := createTestSession(t, env.server.URL)
	var run protocol.RunAccepted
	in := protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "elicit-run", Prompt: "sign me in"}
	if optIn {
		in.Options = &protocol.RunOptions{MCPElicitation: true}
	}
	resp := postJSON(t, http.DefaultClient, env.server.URL+"/v1/sessions/"+sess.ID+"/runs", in, &run)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status %d", resp.StatusCode)
	}
	events, err := http.Get(env.server.URL + "/v1/sessions/" + sess.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { events.Body.Close() })
	return sess, run, bufio.NewScanner(events.Body)
}

// nextElicitEvent returns the next event of one of kinds, or fails at the end of
// the run.
func nextElicitEvent(t *testing.T, stream *bufio.Scanner, kinds ...string) protocol.Event {
	t.Helper()
	for stream.Scan() {
		line, ok := strings.CutPrefix(stream.Text(), "data: ")
		if !ok {
			continue
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		for _, kind := range kinds {
			if event.Type == kind {
				return event
			}
		}
		if event.Type == protocol.EventRunCompleted || event.Type == protocol.EventRunFailed || event.Type == protocol.EventRunCancelled {
			t.Fatalf("run ended (%s) before %v", event.Type, kinds)
		}
	}
	t.Fatalf("stream ended before %v", kinds)
	return protocol.Event{}
}

func postElicitation(t *testing.T, env elicitEnv, sessionID, elicitationID string, body any) (int, string) {
	t.Helper()
	resp := postJSON(t, http.DefaultClient, env.server.URL+"/v1/sessions/"+sessionID+"/elicitations/"+elicitationID, body, nil)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

// serverSaw decodes the fake server's report of the client's replies from a
// tool result.
func serverSaw(t *testing.T, output string) map[string]any {
	t.Helper()
	var replies []map[string]any
	if err := json.Unmarshal([]byte(output), &replies); err != nil || len(replies) != 1 {
		t.Fatalf("tool output %q: %v", output, err)
	}
	return replies[0]
}

func TestElicitationAcceptOverV1(t *testing.T) {
	env := elicitFixture(t, 30*time.Second, "Sign in to Acme Tracker", elicitSchema)
	sess, run, stream := startElicitRun(t, env, true)
	var req protocol.ElicitationRequest
	if err := json.Unmarshal(nextElicitEvent(t, stream, protocol.EventElicitationRequested).Payload, &req); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range req.Fields {
		names = append(names, f.Name)
	}
	if req.Server != "forms" || req.Tool != "ask" || req.Message != "Sign in to Acme Tracker" || req.RunID != run.RunID || req.ToolCallID != "elicit-call" ||
		!strings.HasPrefix(req.ElicitationID, "elicit-") || req.DeadlineAt <= time.Now().UnixMilli() || !json.Valid(req.RequestedSchema) {
		t.Fatalf("request = %+v", req)
	}
	if !reflect.DeepEqual(names, []string{"username", "password", "plan", "seats", "notify", "topics"}) {
		t.Fatalf("fields %v are not in the server's order", names)
	}
	if !req.Fields[1].Sensitive || req.Fields[2].Default != "free" || len(req.Fields[2].Options) != 2 || req.Fields[2].Options[1] != (protocol.ElicitOption{Value: "pro", Label: "Pro"}) {
		t.Fatalf("fields = %+v", req.Fields)
	}

	answer := protocol.ElicitationAnswerRequest{ConversationID: sess.ID, RunID: run.RunID, ElicitationID: req.ElicitationID, Action: "accept",
		Content: map[string]any{"username": "ada", "password": elicitSecret, "plan": "pro", "seats": 3, "topics": []string{"billing", "news"}}}

	// Invalid answers are refused and leave the form pending.
	bad := answer
	bad.Content = map[string]any{"username": "a", "password": "x", "seats": 0, "plan": "gold"}
	code, body := postElicitation(t, env, sess.ID, req.ElicitationID, bad)
	var refusal struct {
		Error       string            `json:"error"`
		FieldErrors map[string]string `json:"field_errors"`
	}
	json.Unmarshal([]byte(body), &refusal)
	if code != http.StatusBadRequest || refusal.FieldErrors["username"] != "must be at least 2 characters" || refusal.FieldErrors["seats"] != "must be at least 1" ||
		refusal.FieldErrors["plan"] != "must be one of the listed choices" || !strings.HasPrefix(refusal.Error, "invalid content: ") {
		t.Fatalf("invalid answer: %d %s", code, body)
	}
	bad.Content = map[string]any{"username": "ada", "password": "x", "seats": 1, "extra": true}
	if code, body := postElicitation(t, env, sess.ID, req.ElicitationID, bad); code != http.StatusBadRequest || !strings.Contains(body, `\"extra\" is not a field`) {
		t.Fatalf("unknown field: %d %s", code, body)
	}
	bad = answer
	bad.Action = "decline"
	if code, body := postElicitation(t, env, sess.ID, req.ElicitationID, bad); code != http.StatusBadRequest || !strings.Contains(body, "only sent with action accept") {
		t.Fatalf("decline with content: %d %s", code, body)
	}
	bad.Action, bad.Content = "maybe", nil
	if code, _ := postElicitation(t, env, sess.ID, req.ElicitationID, bad); code != http.StatusBadRequest {
		t.Fatalf("bad action: %d", code)
	}
	bad = answer
	bad.ConversationID = "other"
	if code, _ := postElicitation(t, env, sess.ID, req.ElicitationID, bad); code != http.StatusConflict {
		t.Fatalf("identity mismatch: %d", code)
	}
	bad = answer
	bad.RunID = "run-other"
	if code, _ := postElicitation(t, env, sess.ID, req.ElicitationID, bad); code != http.StatusNotFound {
		t.Fatalf("wrong run: %d", code)
	}
	unknown := map[string]any{"conversation_id": sess.ID, "run_id": run.RunID, "elicitation_id": req.ElicitationID, "action": "accept", "unknown": 1}
	if code, _ := postElicitation(t, env, sess.ID, req.ElicitationID, unknown); code != http.StatusBadRequest {
		t.Fatalf("unknown request field: %d", code)
	}

	if code, body := postElicitation(t, env, sess.ID, req.ElicitationID, answer); code != http.StatusOK || body != "{\"ok\":true}\n" {
		t.Fatalf("answer: %d %s", code, body)
	}
	if code, body := postElicitation(t, env, sess.ID, req.ElicitationID, answer); code != http.StatusOK || !strings.Contains(body, `"duplicate":true`) {
		t.Fatalf("repeated answer: %d %s", code, body)
	}
	other := answer
	other.Action, other.Content = "decline", nil
	if code, _ := postElicitation(t, env, sess.ID, req.ElicitationID, other); code != http.StatusConflict {
		t.Fatalf("conflicting answer: %d", code)
	}

	var res protocol.ElicitationResolution
	resolvedEvent := nextElicitEvent(t, stream, protocol.EventElicitationResolved)
	json.Unmarshal(resolvedEvent.Payload, &res)
	wantKept := map[string]any{"username": "ada", "plan": "pro", "seats": float64(3), "topics": []any{"news", "billing"}}
	if res.Action != "accept" || res.Reason != "user" || res.Server != "forms" || res.ElicitationID != req.ElicitationID || res.ToolCallID != "elicit-call" ||
		!reflect.DeepEqual(res.Content, wantKept) || !reflect.DeepEqual(res.Redacted, []string{"password"}) {
		t.Fatalf("resolution = %+v", res)
	}
	var result protocol.ToolResultEvent
	json.Unmarshal(nextElicitEvent(t, stream, protocol.EventToolResult).Payload, &result)
	saw := serverSaw(t, result.ToolResult.Output)
	sent, _ := json.Marshal(saw["result"])
	// The server receives the whole answer, the default it was not sent
	// (notify) filled in by the SDK.
	if string(sent) != `{"action":"accept","content":{"notify":true,"password":"`+elicitSecret+`","plan":"pro","seats":3,"topics":["news","billing"],"username":"ada"}}` {
		t.Fatalf("server received %s", sent)
	}
	if got := result.ToolResult.Elicitations; len(got) != 1 || got[0].ElicitationID != req.ElicitationID || got[0].Action != "accept" || !reflect.DeepEqual(got[0].Content, wantKept) || len(got[0].Fields) != 6 {
		t.Fatalf("tool.result elicitations = %+v", got)
	}

	// Replay shows the request and the resolution again.
	replay, err := http.Get(env.server.URL + "/v1/sessions/" + sess.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range readSSE(t, replay) {
		if strings.HasPrefix(e.Type, "elicitation.") {
			kinds = append(kinds, e.Type)
		}
		if e.Type == protocol.EventElicitationResolved && string(e.Payload) != string(resolvedEvent.Payload) {
			t.Fatalf("replayed resolution changed: %s", e.Payload)
		}
	}
	if !reflect.DeepEqual(kinds, []string{protocol.EventElicitationRequested, protocol.EventElicitationResolved}) {
		t.Fatalf("replayed %v", kinds)
	}

	// History keeps the form and the answer, without the secret.
	historyOf := func(base string) []protocol.Elicitation {
		t.Helper()
		var page struct {
			Session struct {
				Messages []struct {
					ToolCalls []struct {
						ID           string                 `json:"id"`
						Elicitations []protocol.Elicitation `json:"elicitations"`
					} `json:"tool_calls"`
				} `json:"messages"`
			} `json:"session"`
		}
		if code := doJSON(t, http.MethodGet, base+"/v1/sessions/"+sess.ID+"/history?max_messages=100", nil, &page); code != http.StatusOK {
			t.Fatalf("history status %d", code)
		}
		for _, m := range page.Session.Messages {
			for _, call := range m.ToolCalls {
				if call.ID == "elicit-call" {
					return call.Elicitations
				}
			}
		}
		t.Fatalf("history has no elicit-call")
		return nil
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if h := historyOf(env.server.URL); len(h) == 1 {
			if h[0].Server != "forms" || h[0].Message != "Sign in to Acme Tracker" || !reflect.DeepEqual(h[0].Content, wantKept) || !reflect.DeepEqual(h[0].Redacted, []string{"password"}) {
				t.Fatalf("history = %+v", h[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("history never recorded the elicitation")
		}
	}

	// Only the model's own reply (the server's echo) may carry the secret;
	// the elicitation events and history never do.
	for _, e := range readEvents(t, env, sess.ID) {
		if strings.HasPrefix(e.Type, "elicitation.") && strings.Contains(string(e.Payload), elicitSecret) {
			t.Fatalf("%s journaled the secret: %s", e.Type, e.Payload)
		}
	}
}

func readEvents(t *testing.T, env elicitEnv, sessionID string) []protocol.Event {
	t.Helper()
	resp, err := http.Get(env.server.URL + "/v1/sessions/" + sessionID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	return readSSE(t, resp)
}

func TestElicitationDeclineAndCancelOverV1(t *testing.T) {
	for _, action := range []string{"decline", "cancel"} {
		t.Run(action, func(t *testing.T) {
			env := elicitFixture(t, 30*time.Second, "Share your plan?", `{"type":"object","properties":{"plan":{"type":"string","enum":["a","b"]}}}`)
			sess, run, stream := startElicitRun(t, env, true)
			var req protocol.ElicitationRequest
			json.Unmarshal(nextElicitEvent(t, stream, protocol.EventElicitationRequested).Payload, &req)
			answer := protocol.ElicitationAnswerRequest{ConversationID: sess.ID, RunID: run.RunID, ElicitationID: req.ElicitationID, Action: action}
			if code, body := postElicitation(t, env, sess.ID, req.ElicitationID, answer); code != http.StatusOK {
				t.Fatalf("answer: %d %s", code, body)
			}
			var res protocol.ElicitationResolution
			json.Unmarshal(nextElicitEvent(t, stream, protocol.EventElicitationResolved).Payload, &res)
			if res.Action != action || res.Reason != "user" || res.Content != nil {
				t.Fatalf("resolution = %+v", res)
			}
			var result protocol.ToolResultEvent
			json.Unmarshal(nextElicitEvent(t, stream, protocol.EventToolResult).Payload, &result)
			if sent, _ := json.Marshal(serverSaw(t, result.ToolResult.Output)["result"]); string(sent) != `{"action":"`+action+`"}` {
				t.Fatalf("server received %s", sent)
			}
		})
	}
}

func TestElicitationTimeoutAndRunCancel(t *testing.T) {
	for _, mode := range []string{"timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			timeout := 30 * time.Second
			if mode == "timeout" {
				timeout = 300 * time.Millisecond
			}
			env := elicitFixture(t, timeout, "Confirm?", `{"type":"object","properties":{}}`)
			sess, run, stream := startElicitRun(t, env, true)
			var req protocol.ElicitationRequest
			json.Unmarshal(nextElicitEvent(t, stream, protocol.EventElicitationRequested).Payload, &req)
			if mode == "cancel" {
				r := postJSON(t, http.DefaultClient, env.server.URL+"/v1/sessions/"+sess.ID+"/runs/"+run.RunID+"/cancel", map[string]any{}, nil)
				r.Body.Close()
			}
			var res protocol.ElicitationResolution
			json.Unmarshal(nextElicitEvent(t, stream, protocol.EventElicitationResolved).Payload, &res)
			wantReason := map[string]string{"timeout": "timeout", "cancel": "cancelled"}[mode]
			if res.Action != "cancel" || res.Reason != wantReason {
				t.Fatalf("resolution = %+v", res)
			}
			if mode == "timeout" {
				var result protocol.ToolResultEvent
				json.Unmarshal(nextElicitEvent(t, stream, protocol.EventToolResult).Payload, &result)
				if sent, _ := json.Marshal(serverSaw(t, result.ToolResult.Output)["result"]); string(sent) != `{"action":"cancel"}` {
					t.Fatalf("server received %s", sent)
				}
			}
			// A late answer finds the form settled.
			answer := protocol.ElicitationAnswerRequest{ConversationID: sess.ID, RunID: run.RunID, ElicitationID: req.ElicitationID, Action: "accept"}
			if code, body := postElicitation(t, env, sess.ID, req.ElicitationID, answer); code != http.StatusConflict {
				t.Fatalf("late answer: %d %s", code, body)
			}
		})
	}
}

func TestElicitationWithoutOptInDeclines(t *testing.T) {
	env := elicitFixture(t, 30*time.Second, "Sign in", elicitSchema)
	sess, _, stream := startElicitRun(t, env, false)
	var result protocol.ToolResultEvent
	json.Unmarshal(nextElicitEvent(t, stream, protocol.EventToolResult).Payload, &result)
	if sent, _ := json.Marshal(serverSaw(t, result.ToolResult.Output)["result"]); string(sent) != `{"action":"decline"}` {
		t.Fatalf("server received %s", sent)
	}
	if got := result.ToolResult.Elicitations; len(got) != 1 || got[0].Reason != "no_client" || got[0].Action != "decline" || got[0].ElicitationID != "" || got[0].Server != "forms" {
		t.Fatalf("tool.result elicitations = %+v", got)
	}
	for _, e := range readEvents(t, env, sess.ID) {
		if strings.HasPrefix(e.Type, "elicitation.") {
			t.Fatalf("a run without mcp_elicitation published %s", e.Type)
		}
	}
}

func TestElicitationInvalidSchemaRefused(t *testing.T) {
	env := elicitFixture(t, 30*time.Second, "Nested", `{"type":"object","properties":{"when":{"type":"string","format":"date","pattern":"^2"},"n":{"type":"integer","minimum":0.2,"maximum":0.8}}}`)
	sess, _, stream := startElicitRun(t, env, true)
	var result protocol.ToolResultEvent
	json.Unmarshal(nextElicitEvent(t, stream, protocol.EventToolResult).Payload, &result)
	saw := serverSaw(t, result.ToolResult.Output)
	errObj, _ := saw["error"].(map[string]any)
	if errObj["code"] != float64(-32602) || !strings.Contains(errObj["message"].(string), `invalid requestedSchema: property "n": no integer lies between`) {
		t.Fatalf("server received %v", saw)
	}
	if len(result.ToolResult.Elicitations) != 0 {
		t.Fatalf("refused request recorded: %+v", result.ToolResult.Elicitations)
	}
	for _, e := range readEvents(t, env, sess.ID) {
		if strings.HasPrefix(e.Type, "elicitation.") {
			t.Fatalf("a refused schema published %s", e.Type)
		}
	}
}

func TestElicitationUnattendedDeclines(t *testing.T) {
	env := elicitFixture(t, 30*time.Second, "Sign in", elicitSchema)
	createCronPromptForTest(t, env.server.URL, env.root, "nightly")
	if code := doCronJSON(t, env.server.URL+"/v1/cron/nightly/run-now", http.MethodPost, nil, nil); code != http.StatusAccepted {
		t.Fatal(code)
	}
	runs := waitCronRunsFor(t, env.server.URL, "nightly", 15*time.Second)
	var found bool
	for _, e := range readEvents(t, env, runs.SessionID) {
		if strings.HasPrefix(e.Type, "elicitation.") {
			t.Fatalf("an unattended run published %s", e.Type)
		}
		if e.Type == protocol.EventToolResult {
			var result protocol.ToolResultEvent
			json.Unmarshal(e.Payload, &result)
			if got := result.ToolResult.Elicitations; len(got) == 1 && got[0].Reason == "unattended" && got[0].Action == "decline" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("no unattended decline in the tool result")
	}
}

func waitCronRunsFor(t *testing.T, base, id string, limit time.Duration) CronRunRecord {
	t.Helper()
	deadline := time.Now().Add(limit)
	for {
		var response struct {
			Runs []CronRunRecord `json:"runs"`
		}
		if doCronJSON(t, base+"/v1/cron/"+id+"/runs", http.MethodGet, nil, &response) == http.StatusOK && len(response.Runs) == 1 && response.Runs[0].State == "done" {
			return response.Runs[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("cron run did not finish: %+v", response.Runs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHealthAdvertisesMCPElicitation(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	var health struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/health", nil, &health); code != http.StatusOK || health.Capabilities["mcp_elicitation"] != float64(1) {
		t.Fatalf("health %d: %v", code, health.Capabilities)
	}
}
