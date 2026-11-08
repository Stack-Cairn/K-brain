package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// backgroundToolClient calls Bash once with a command that runs until gate
// exists, then records what each later request carries.
type backgroundToolClient struct {
	gate     string
	mu       sync.Mutex
	calls    int
	toolText string
	notice   string
	answered chan struct{}
}

func (c *backgroundToolClient) Models(context.Context) ([]ai.ModelInfo, error) { return nil, nil }
func (c *backgroundToolClient) Complete(context.Context, ai.Request) (string, ai.Usage, error) {
	return "", ai.Usage{}, nil
}
func (c *backgroundToolClient) Clone() ai.Client   { return c }
func (c *backgroundToolClient) SetCacheKey(string) {}
func (c *backgroundToolClient) Endpoint() string   { return "http://background-tool-fixture.invalid" }
func (c *backgroundToolClient) Stream(_ context.Context, request ai.Request, onText, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 1 {
		command := `echo first; while [ ! -f '` + c.gate + `' ]; do sleep 0.05; done; echo second`
		if runtime.GOOS == "windows" {
			command = `Write-Output first; while (-not (Test-Path '` + c.gate + `')) { Start-Sleep -Milliseconds 50 }; Write-Output second`
		}
		args, _ := json.Marshal(map[string]any{"command": command})
		call := ai.ToolCall{ID: "bg-call", Type: "function"}
		call.Function.Name = "Bash"
		call.Function.Arguments = string(args)
		return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}}, ai.Usage{}, nil
	}
	for _, message := range request.Messages {
		if message.Role == "tool" && c.toolText == "" {
			c.toolText = message.Content
		}
		if strings.Contains(message.Content, "[process bash-") {
			c.notice = message.Content
		}
	}
	if c.notice != "" && c.answered != nil {
		close(c.answered)
		c.answered = nil
	}
	if onText != nil {
		onText("ok")
	}
	return ai.Message{Role: "assistant", Content: "ok", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

func newBackgroundToolServer(t *testing.T, client *backgroundToolClient) (*Server, string) {
	t.Helper()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	factory := func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
		ag := agent.New(client, selected.Model, 1024, "system")
		ag.ModelName, ag.Provider = selected.Model, selected.Provider
		return ag, nil
	}
	backendServer, err := New(Options{Store: store, Factory: factory, EventDir: t.TempDir(), DefaultCWD: t.TempDir(), Models: []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(backendServer)
	t.Cleanup(func() {
		server.Close()
		_ = backendServer.Close()
	})
	return backendServer, server.URL
}

func postBackground(t *testing.T, base, sessionID, runID, callID string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(base+"/v1/sessions/"+sessionID+"/runs/"+runID+"/tools/"+callID+"/background", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// backgroundRunningCall starts a run whose Bash call blocks, then moves it to
// the background through the endpoint once it is running.
func backgroundRunningCall(t *testing.T, base string) (sessionID string, accepted protocol.RunAccepted, processID string) {
	t.Helper()
	sess := createTestSession(t, base)
	response := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "background-tool", Prompt: "run it", Options: &protocol.RunOptions{ApprovalPolicy: "auto"}}, &accepted)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("run status=%d", response.StatusCode)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		code, out := postBackground(t, base, sess.ID, accepted.RunID, "bg-call")
		if code == http.StatusOK {
			if out["ok"] != true || out["tool_call_id"] != "bg-call" {
				t.Fatalf("background response = %v", out)
			}
			processID, _ = out["process_id"].(string)
			if !strings.HasPrefix(processID, "bash-") {
				t.Fatalf("process_id = %q", processID)
			}
			return sess.ID, accepted, processID
		}
		// 409 until the call's command is running.
		if code != http.StatusConflict || time.Now().After(deadline) {
			t.Fatalf("background status %d: %v", code, out)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBackgroundToolEndpoint(t *testing.T) {
	client := &backgroundToolClient{gate: filepath.Join(t.TempDir(), "gate"), answered: make(chan struct{})}
	answered := client.answered
	backendServer, base := newBackgroundToolServer(t, client)
	if code, _ := postBackground(t, base, "missing", "run", "call"); code != http.StatusNotFound {
		t.Fatalf("unknown session status %d", code)
	}
	sessionID, accepted, processID := backgroundRunningCall(t, base)

	response, err := http.Get(base + "/v1/sessions/" + sessionID + "/events?after_seq=" + strconv.FormatInt(accepted.AcceptedSeq-1, 10))
	if err != nil {
		t.Fatal(err)
	}
	if events := readSSE(t, response); !hasEventType(events, protocol.EventRunCompleted) {
		t.Fatalf("events=%+v", events)
	}
	client.mu.Lock()
	toolText := client.toolText
	client.mu.Unlock()
	for _, want := range []string{"moved this command to the background", "process_id=" + processID, "first", "ManagedProcess"} {
		if !strings.Contains(toolText, want) {
			t.Fatalf("tool result missing %q:\n%s", want, toolText)
		}
	}
	// The run is over: a repeat, or one for another run, is a conflict.
	if code, _ := postBackground(t, base, sessionID, accepted.RunID, "bg-call"); code != http.StatusConflict {
		t.Fatalf("repeat status %d", code)
	}

	rt, err := backendServer.loadRuntimeByID(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	processes := rt.processManager
	backendServer.releaseRuntime(rt)
	if !processes.HasRunningProcesses() {
		t.Fatal("the backgrounded command is not running in the session's process manager")
	}

	// Its exit wakes the session with a notice carrying its later output.
	if err := os.WriteFile(client.gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-answered:
	case <-time.After(20 * time.Second):
		t.Fatal("no exit notice reached the model")
	}
	client.mu.Lock()
	notice := client.notice
	client.mu.Unlock()
	if !strings.Contains(notice, processID) || !strings.Contains(notice, "second") {
		t.Fatalf("exit notice:\n%s", notice)
	}
}

func TestCloseSessionStopsBackgroundedCommand(t *testing.T) {
	client := &backgroundToolClient{gate: filepath.Join(t.TempDir(), "gate")}
	backendServer, base := newBackgroundToolServer(t, client)
	sessionID, _, _ := backgroundRunningCall(t, base)
	rt, err := backendServer.loadRuntimeByID(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	processes := rt.processManager
	backendServer.releaseRuntime(rt)
	if code := doJSON(t, http.MethodPost, base+"/v1/sessions/"+sessionID+"/close", nil, nil); code != http.StatusOK {
		t.Fatalf("close status %d", code)
	}
	if processes.HasRunningProcesses() {
		t.Fatal("closing the session left the backgrounded command running")
	}
}
