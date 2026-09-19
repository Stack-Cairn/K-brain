package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

type permAnswer struct {
	optionID string
	err      error
}

type fakeClient struct {
	mu      sync.Mutex
	updates []acp.SessionNotification
	perms   []acp.RequestPermissionRequest
	answer  func(acp.RequestPermissionRequest) permAnswer
}

func (c *fakeClient) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	c.mu.Lock()
	c.updates = append(c.updates, n)
	c.mu.Unlock()
	return nil
}

func (c *fakeClient) RequestPermission(_ context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	c.mu.Lock()
	c.perms = append(c.perms, p)
	ans := c.answer
	c.mu.Unlock()
	if ans == nil {
		ans = func(acp.RequestPermissionRequest) permAnswer { return permAnswer{optionID: optAllowOnce} }
	}
	a := ans(p)
	if a.err != nil {
		return acp.RequestPermissionResponse{}, a.err
	}
	if a.optionID == "" {
		return acp.RequestPermissionResponse{
			Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}},
		}, nil
	}
	return acp.RequestPermissionResponse{
		Outcome: acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: acp.PermissionOptionId(a.optionID)}},
	}, nil
}

func (c *fakeClient) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, acp.NewMethodNotFound("fs/read_text_file")
}

func (c *fakeClient) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, acp.NewMethodNotFound("fs/write_text_file")
}

func (c *fakeClient) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, acp.NewMethodNotFound("terminal/create")
}

func (c *fakeClient) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, nil
}

func (c *fakeClient) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, acp.NewMethodNotFound("terminal/output")
}

func (c *fakeClient) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, nil
}

func (c *fakeClient) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, nil
}

func (c *fakeClient) UnstableCompleteElicitation(context.Context, acp.UnstableCompleteElicitationNotification) error {
	return nil
}

func (c *fakeClient) UnstableCreateElicitation(context.Context, acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	return acp.UnstableCreateElicitationResponse{}, acp.NewMethodNotFound("elicitation/create")
}

func (c *fakeClient) UnstableConnectMcp(context.Context, acp.UnstableConnectMcpRequest) (acp.UnstableConnectMcpResponse, error) {
	return acp.UnstableConnectMcpResponse{}, acp.NewMethodNotFound("mcp/connect")
}

func (c *fakeClient) UnstableDisconnectMcp(context.Context, acp.UnstableDisconnectMcpRequest) (acp.UnstableDisconnectMcpResponse, error) {
	return acp.UnstableDisconnectMcpResponse{}, acp.NewMethodNotFound("mcp/disconnect")
}

func (c *fakeClient) snapshot() []acp.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]acp.SessionNotification(nil), c.updates...)
}

func (c *fakeClient) waitFor(t *testing.T, cond func(acp.SessionNotification) bool, what string) acp.SessionNotification {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, u := range c.snapshot() {
			if cond(u) {
				return u
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; updates so far: %s", what, summarizeUpdates(c.snapshot()))
	return acp.SessionNotification{}
}

func summarizeUpdates(us []acp.SessionNotification) string {
	var b strings.Builder
	for _, u := range us {
		fmt.Fprintf(&b, "%s ", updateKind(u.Update))
	}
	return b.String()
}

func updateKind(u acp.SessionUpdate) string {
	switch {
	case u.AgentMessageChunk != nil:
		return "agent_chunk"
	case u.UserMessageChunk != nil:
		return "user_chunk"
	case u.AgentThoughtChunk != nil:
		return "thought_chunk"
	case u.ToolCall != nil:
		return "tool_call(" + string(u.ToolCall.ToolCallId) + ")"
	case u.ToolCallUpdate != nil:
		s := ""
		if u.ToolCallUpdate.Status != nil {
			s = string(*u.ToolCallUpdate.Status)
		}
		return "tool_call_update(" + string(u.ToolCallUpdate.ToolCallId) + "," + s + ")"
	case u.Plan != nil:
		return "plan"
	case u.UsageUpdate != nil:
		return "usage"
	default:
		return "other"
	}
}

type step struct {
	text     string
	toolName string
	toolArgs string
	block    <-chan struct{}
}

func scriptServer(t *testing.T, steps []step) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		i := call
		call++
		mu.Unlock()
		if i >= len(steps) {
			i = len(steps) - 1
		}
		st := steps[i]
		w.Header().Set("Content-Type", "text/event-stream")
		if st.toolName != "" {
			args, _ := json.Marshal(st.toolArgs)
			fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_%d","type":"function","function":{"name":%q,"arguments":%s}}]}}]}`+"\n\n", i, st.toolName, string(args))
		} else {
			text, _ := json.Marshal(st.text)
			fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":%s},"finish_reason":"stop"}]}`+"\n\n", text)
		}
		if st.block != nil {

			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-st.block:
			case <-r.Context().Done():
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type fixture struct {
	bridge     *Bridge
	client     *fakeClient
	conn       *acp.ClientSideConnection
	disconnect func()
}

func newFixture(t *testing.T, client *fakeClient, store *session.Store, newAgent Factory) *fixture {
	t.Helper()
	if client == nil {
		client = &fakeClient{}
	}
	agentR, clientW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	clientR, agentW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	b := NewBridge("test", newAgent, store, false, nil)
	agentConn := acp.NewAgentSideConnection(b, agentW, agentR)
	b.SetAgentConnection(agentConn)

	conn := acp.NewClientSideConnection(client, clientW, clientR)
	if os.Getenv("ACP_TEST_DEBUG") != "" {
		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
		conn.SetLogger(logger)
		agentConn.SetLogger(logger)
	}
	disconnect := sync.OnceFunc(func() {
		_ = agentW.Close()
		_ = clientW.Close()
		_ = agentR.Close()
		_ = clientR.Close()
	})
	t.Cleanup(func() {
		disconnect()
		b.CloseAll()
	})
	return &fixture{bridge: b, client: client, conn: conn, disconnect: disconnect}
}

func (f *fixture) initialize(t *testing.T) acp.InitializeResponse {
	t.Helper()
	resp, err := f.conn.Initialize(context.Background(), acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return resp
}

func (f *fixture) newSession(t *testing.T, cwd string) acp.SessionId {
	t.Helper()
	resp, err := f.conn.NewSession(context.Background(), acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	return resp.SessionId
}

func (f *fixture) prompt(t *testing.T, id acp.SessionId, text string) (acp.PromptResponse, error) {
	t.Helper()
	return f.conn.Prompt(context.Background(), acp.PromptRequest{
		SessionId: id,
		Prompt:    []acp.ContentBlock{acp.TextBlock(text)},
	})
}

func factoryFor(srv *httptest.Server, ts []tools.Tool) Factory {
	return func(_ context.Context, cwd string, _ map[string]mcp.ServerConfig) (*agent.Agent, *mcp.Manager, error) {
		ag := agent.New(ai.New(srv.URL, "k"), "m", 4096, "sys")
		ag.BrowserDisabled = true
		ag.ComputerDisabled = true
		if len(ts) > 0 {
			ag.Tools = append(ts, ag.Tools...)
		}
		return ag, mcp.NewManager(nil), nil
	}
}
