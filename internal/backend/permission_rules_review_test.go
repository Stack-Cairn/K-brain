package backend

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
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// reviewRunRules is runRules with full run options.
func reviewRunRules(t *testing.T, base, sessionID string, options protocol.RunOptions, answer func(protocol.PermissionRequest) protocol.PermissionDecision) ([]protocol.Event, []protocol.PermissionRequest) {
	t.Helper()
	var accepted protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sessionID+"/runs", protocol.PromptRequest{ConversationID: sessionID, ClientRequestID: fmt.Sprintf("review-rules-%d", rulesRequestSeq.Add(1)), Prompt: "go", Options: &options}, &accepted)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("accept status=%d", resp.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/events?after_seq=%d", base, sessionID, accepted.AcceptedSeq-1), nil)
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	var events []protocol.Event
	var prompts []protocol.PermissionRequest
	scanner := bufio.NewScanner(stream.Body)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.RunID != accepted.RunID {
			continue
		}
		events = append(events, event)
		switch event.Type {
		case protocol.EventPermissionRequest:
			var prompt protocol.PermissionRequest
			_ = json.Unmarshal(event.Payload, &prompt)
			prompts = append(prompts, prompt)
			decision := protocol.PermissionDecision{Decision: "reject"}
			if answer != nil {
				decision = answer(prompt)
			}
			decision.PermissionID = prompt.PermissionID
			r := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sessionID+"/permissions/"+prompt.PermissionID, protocol.PermissionDecisionRequest{ConversationID: sessionID, RunID: accepted.RunID, Decision: decision}, nil)
			_, _ = io.ReadAll(r.Body)
			r.Body.Close()
		case protocol.EventRunCompleted, protocol.EventRunFailed, protocol.EventRunCancelled:
			return events, prompts
		}
	}
	t.Fatalf("run did not finish: %+v", events)
	return nil, nil
}

// P2: rememberSuggestion writes the rules before the handler claims the waiter, so
// a concurrent reject (second client) wins the prompt while the allow_always that
// lost with 404 still persisted its rule.
func TestReviewConcurrentRejectLeavesNoRememberedRule(t *testing.T) {
	const tries = 20
	leaked := 0
	for i := 0; i < tries; i++ {
		client := &rulesClient{batches: [][]string{{"hostname"}}}
		base, _ := newRulesServer(t, client)
		sess := createTestSession(t, base)
		var mu sync.Mutex
		var allowStatus, rejectStatus int
		_, prompts := reviewRunRules(t, base, sess.ID, protocol.RunOptions{Mode: "agent", PermissionMode: "normal"}, func(p protocol.PermissionRequest) protocol.PermissionDecision {
			var wg sync.WaitGroup
			post := func(decision protocol.PermissionDecision, status *int) {
				defer wg.Done()
				decision.PermissionID = p.PermissionID
				r := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/permissions/"+p.PermissionID, protocol.PermissionDecisionRequest{ConversationID: sess.ID, Decision: decision}, nil)
				r.Body.Close()
				mu.Lock()
				*status = r.StatusCode
				mu.Unlock()
			}
			wg.Add(2)
			go post(protocol.PermissionDecision{Decision: "allow_always", Scope: "project"}, &allowStatus)
			go post(protocol.PermissionDecision{Decision: "reject"}, &rejectStatus)
			wg.Wait()
			// Already answered; the helper's own answer gets 404.
			return protocol.PermissionDecision{Decision: "reject"}
		})
		if len(prompts) != 1 {
			t.Fatalf("prompts=%d", len(prompts))
		}
		listed := listRules(t, base, "workspace="+sess.CWD)
		if rejectStatus == http.StatusOK && allowStatus != http.StatusOK && len(listed.Rules) != 0 {
			leaked++
		}
	}
	if leaked != 0 {
		t.Fatalf("%d/%d: the prompt was rejected and allow_always got 404, yet its project rule was stored", leaked, tries)
	}
}

// P2: in risk_only review the user reads the AI risk and decides. A Read the
// reviewer rates high for sensitive data now runs without asking because the
// read-only allowance comes before the ask; before the rules it asked, and the TUI
// turns the read-only allowance off for reviewed calls.
func TestReviewRiskOnlyHighRiskReadStillAsks(t *testing.T) {
	high := `{"risk":"high","sandbox":"low","sensitive_data":"high","model_behavior":"low","injection":"low","reason":"Reads a private key"}`
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": high}}}})
	}))
	defer provider.Close()
	root := t.TempDir()
	target := filepath.Join(root, "id_rsa")
	if err := os.WriteFile(target, []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"path": target})
	client := &runControlsClient{tool: "Read", args: string(args)}
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, httpServer := newToolHistoryServer(t, store, t.TempDir(), client)
	cfg := &config.Config{AIApproval: config.AIApprovalConfig{Mode: "risk_only", Provider: "fixture", Model: "small"}, Providers: map[string]config.Provider{"fixture": {API: "openai-completions", BaseURL: provider.URL, APIKey: "fixture-key"}}, Models: map[string]config.Model{"small": {Providers: []string{"fixture"}}, "fixture-model": {Providers: []string{"fixture"}}}}
	server.settings = NewSettingsStore(cfg, nil)
	sess := createTestSession(t, httpServer.URL)
	options := protocol.RunOptions{Mode: "agent", PermissionMode: "normal", WorkspaceRoots: []protocol.WorkspaceRoot{{Path: root, Access: "write"}}}
	var accepted protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, httpServer.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "risk-only-read", Prompt: "read key", Options: &options}, &accepted)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	events := readToolHistoryRun(t, httpServer.URL, sess.ID, accepted.RunID, "reject")
	if !hasToolHistoryEvent(events, "approval.reviewed") {
		t.Fatal("precondition: the reviewer ran")
	}
	if !hasToolHistoryEvent(events, protocol.EventPermissionRequest) {
		t.Fatalf("risk_only + normal: a high-risk Read ran without asking (auto_resolved=%v)", hasToolHistoryEvent(events, protocol.EventPermissionAutoResolved))
	}
}

// P2 compatibility: LiveAgent before rules shows `rule` as the scope of
// allow_always ("本会话始终允许 {rule}") and sends it without a scope. For
// `hostname; whoami` the prompt says rule=hostname, but every suggested rule,
// Bash(whoami:*) too, is stored, so a later bare `whoami` runs without asking.
func TestReviewLegacyAllowAlwaysStoresOnlyWhatRuleShows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("K_BRAIN_SHELL", "powershell.exe")
	}
	base, _ := newRulesServer(t, &rulesClient{batches: [][]string{{"hostname; whoami"}, {"whoami"}}})
	sess := createTestSession(t, base)
	_, prompts := reviewRunRules(t, base, sess.ID, protocol.RunOptions{Mode: "agent", PermissionMode: "normal"}, func(protocol.PermissionRequest) protocol.PermissionDecision {
		return protocol.PermissionDecision{Decision: "allow_always"}
	})
	if len(prompts) != 1 || prompts[0].Suggestion == nil {
		t.Fatalf("precondition: one prompt with a suggestion: %+v", prompts)
	}
	shown := prompts[0].Rule
	_, second := reviewRunRules(t, base, sess.ID, protocol.RunOptions{Mode: "agent", PermissionMode: "normal"}, nil)
	if len(second) != 1 {
		t.Fatalf("legacy client showed allow_always scope %q, backend stored %v; bare whoami then ran without a prompt", shown, prompts[0].Suggestion.Rules)
	}
}

// P2: an explicit per-tool ask prompts every call (runToolGate puts tool policy ask
// before allow rules), yet settleCoveredPermissions releases a parallel call of the
// same tool that is already waiting once allow_always stores a rule for it.
type reviewParallelClient struct {
	scriptedClient
	lock sync.Mutex
	tool string
	args []string
	sent bool
}

func (c *reviewParallelClient) Stream(_ context.Context, req ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if !c.sent {
		c.sent = true
		var calls []ai.ToolCall
		for i, args := range c.args {
			call := ai.ToolCall{ID: fmt.Sprintf("parallel-%d", i), Type: "function"}
			call.Function.Name, call.Function.Arguments = c.tool, args
			calls = append(calls, call)
		}
		return ai.Message{Role: "assistant", ToolCalls: calls}, ai.Usage{}, nil
	}
	if text != nil {
		text("done")
	}
	return ai.Message{Role: "assistant", Content: "done", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

func TestReviewSettleKeepsExplicitPerToolAskWaiting(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &reviewParallelClient{tool: "ProcessStop", args: []string{`{"process_id":"review-a"}`, `{"process_id":"review-b"}`}}
	backend, err := New(Options{Store: store, EventDir: t.TempDir(), PermissionRulesDir: t.TempDir(), DefaultCWD: t.TempDir(), Models: []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}}, Factory: func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
		ag := agent.New(client, selected.Model, 1024, "system")
		ag.ModelName, ag.Provider = selected.Model, selected.Provider
		return ag, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	host := httptest.NewServer(backend)
	defer host.Close()
	sess := createTestSession(t, host.URL)
	options := protocol.RunOptions{Mode: "agent", PermissionMode: "normal", Tools: &protocol.ToolSelection{Policies: map[string]string{"ProcessStop": "ask"}}}
	var accepted protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, host.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "explicit-ask-parallel", Prompt: "stop both", Options: &options}, &accepted)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	rt, err := backend.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.releaseRuntime(rt)
	var waiting []*permissionWaiter
	for deadline := time.Now().Add(10 * time.Second); len(waiting) < 2; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("precondition: both explicit-ask calls prompt, saw %d", len(waiting))
		}
		rt.mu.Lock()
		waiting = waiting[:0]
		for _, p := range rt.permissions {
			waiting = append(waiting, p)
		}
		rt.mu.Unlock()
	}
	first, second := waiting[0], waiting[1]
	// A run that asks for every call of a tool is not offered a rule for it.
	if first.request.Suggestion != nil || second.request.Suggestion != nil {
		t.Fatalf("explicit ask must not offer allow_always: %+v", first.request)
	}
	answer := func(p *permissionWaiter, decision protocol.PermissionDecision) int {
		decision.PermissionID = p.request.PermissionID
		r := postJSON(t, http.DefaultClient, host.URL+"/v1/sessions/"+sess.ID+"/permissions/"+p.request.PermissionID, protocol.PermissionDecisionRequest{ConversationID: sess.ID, Decision: decision}, nil)
		r.Body.Close()
		return r.StatusCode
	}
	if code := answer(first, protocol.PermissionDecision{Decision: "allow_always", Scope: "session"}); code != http.StatusOK {
		t.Fatalf("answer=%d", code)
	}
	rt.mu.Lock()
	stillWaiting := rt.permissions[second.request.PermissionID] == second
	rt.mu.Unlock()
	answer(second, protocol.PermissionDecision{Decision: "reject"})
	if !stillWaiting {
		t.Fatalf("explicit ProcessStop=ask: the parallel prompt %s was settled without the user", second.request.PermissionID)
	}
}

// P1: /v1/terminal gates terminal input as TerminalSession "input <id> <data>". The
// suggestion for one input is TerminalSession(input:*), which then allows every later
// input to any terminal, including a removal that Suggest itself never remembers.
func TestReviewTerminalInputRememberIsNotEveryInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("K_BRAIN_SHELL", "powershell.exe")
	}
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &terminalHTTPModelClient{entered: make(chan context.Context, 1), release: make(chan struct{})}
	backend, err := New(Options{Store: store, DefaultCWD: t.TempDir(), EventDir: t.TempDir(), PermissionRulesDir: t.TempDir(), Factory: func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(client, "fixture", 1024, "terminal"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	host := httptest.NewServer(backend)
	defer host.Close()
	defer close(client.release)
	sess := createTestSession(t, host.URL)
	var accepted protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, host.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ClientRequestID: "terminal-remember", Prompt: "hold", Options: &protocol.RunOptions{ApprovalPolicy: "ask", PermissionMode: "normal"}}, &accepted)
	if resp.StatusCode != 202 {
		t.Fatalf("run=%d", resp.StatusCode)
	}
	select {
	case <-client.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not start")
	}
	rt, _ := backend.loadRuntimeByID(sess.ID)
	defer backend.releaseRuntime(rt)
	type outcome struct {
		code int
		out  tools.TerminalResponse
	}
	invoke := func(req tools.TerminalRequest) <-chan outcome {
		done := make(chan outcome, 1)
		req.ConversationID, req.RunID = sess.ID, accepted.RunID
		raw, _ := json.Marshal(req)
		go func() {
			w := httptest.NewRecorder()
			backend.ServeHTTP(w, httptest.NewRequest("POST", "/v1/terminal", strings.NewReader(string(raw))))
			var out tools.TerminalResponse
			_ = json.Unmarshal(w.Body.Bytes(), &out)
			done <- outcome{w.Code, out}
		}()
		return done
	}
	// pending returns the prompt the call is waiting on, or its outcome.
	pending := func(done <-chan outcome) (*permissionWaiter, outcome) {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			select {
			case o := <-done:
				return nil, o
			default:
			}
			rt.mu.Lock()
			for _, p := range rt.permissions {
				rt.mu.Unlock()
				return p, outcome{}
			}
			rt.mu.Unlock()
		}
		t.Fatal("neither a prompt nor a result")
		return nil, outcome{}
	}
	answer := func(p *permissionWaiter, decision protocol.PermissionDecision) {
		decision.PermissionID = p.request.PermissionID
		r := postJSON(t, http.DefaultClient, host.URL+"/v1/sessions/"+sess.ID+"/permissions/"+p.request.PermissionID, protocol.PermissionDecisionRequest{ConversationID: sess.ID, Decision: decision}, nil)
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("answer=%d", r.StatusCode)
		}
	}
	created := invoke(tools.TerminalRequest{Action: "create"})
	p, o := pending(created)
	if p != nil {
		answer(p, protocol.PermissionDecision{Decision: "allow_once"})
		o = <-created
	}
	if o.code != 200 || o.out.Session == nil {
		t.Fatalf("create=%d %+v", o.code, o.out)
	}
	terminalID := o.out.Session.ID
	firstInput := invoke(tools.TerminalRequest{Action: "input", SessionID: terminalID, Data: "Write-Output first\r\n"})
	p, o = pending(firstInput)
	if p == nil {
		t.Fatalf("precondition: terminal input asks, got %d", o.code)
	}
	// Terminal input is not a command line the rules can describe; it is allowed
	// once, and an allow_always answer for it stores nothing.
	if p.request.Suggestion != nil {
		t.Fatalf("terminal input must not offer allow_always: %v", p.request.Suggestion.Rules)
	}
	answer(p, protocol.PermissionDecision{Decision: "allow_always", Scope: "session"})
	if o := <-firstInput; o.code != 200 {
		t.Fatalf("first input=%d", o.code)
	}
	victim := filepath.Join(t.TempDir(), "keep.txt")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	removal := "Remove-Item -LiteralPath '" + victim + "'\r\n"
	if runtime.GOOS != "windows" {
		removal = "rm -f '" + victim + "'\n"
	}
	second := invoke(tools.TerminalRequest{Action: "input", SessionID: terminalID, Data: removal})
	p, o = pending(second)
	if p == nil {
		removed := false
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && !removed; time.Sleep(50 * time.Millisecond) {
			_, statErr := os.Stat(victim)
			removed = os.IsNotExist(statErr)
		}
		t.Fatalf("allow_always for one terminal input sent %q without asking (status %d, file removed=%v)", strings.TrimSpace(removal), o.code, removed)
	}
	answer(p, protocol.PermissionDecision{Decision: "reject"})
}

// P2: the redacted trajectory export blanks permission.requested command/rule and
// tool arguments, but the new permission.auto_resolved carries the command (and
// permission.requested a suggestion) unredacted.
func TestReviewRedactedTrajectoryHidesAutoResolvedCommand(t *testing.T) {
	const secret = "SECRET-REDACT-7f3"
	base, _ := newRulesServer(t, &rulesClient{batches: [][]string{{"echo " + secret}}})
	sess := createTestSession(t, base)
	events, _ := runRules(t, base, sess.ID, "normal", nil)
	if len(autoResolved(events)) != 1 {
		t.Fatalf("precondition: one auto_resolved: %+v", events)
	}
	for _, event := range redactTrajectoryEvents(events) {
		if strings.HasPrefix(event.Type, "permission.") && strings.Contains(string(event.Payload), secret) {
			t.Errorf("redacted export leaks the command in %s: %s", event.Type, event.Payload)
		}
	}
}

// P2: without Options.PermissionRulesDir only session rules can be stored, yet the
// suggestion preselects default_scope "project"; a client answering with the
// backend's own default gets 400.
func TestReviewDefaultScopeIsStorableWithoutRulesDir(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	client := &rulesClient{batches: [][]string{{"hostname"}}}
	backendServer, err := New(Options{Store: store, EventDir: t.TempDir(), DefaultCWD: t.TempDir(), Models: []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}}, Factory: func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
		ag := agent.New(client, selected.Model, 1024, "system")
		ag.ModelName, ag.Provider = selected.Model, selected.Provider
		return ag, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(backendServer)
	t.Cleanup(func() { host.Close(); _ = backendServer.Close() })
	sess := createTestSession(t, host.URL)
	status := 0
	_, prompts := reviewRunRules(t, host.URL, sess.ID, protocol.RunOptions{Mode: "agent", PermissionMode: "normal"}, func(p protocol.PermissionRequest) protocol.PermissionDecision {
		if p.Suggestion == nil {
			t.Fatal("precondition: suggestion offered")
		}
		r := postJSON(t, http.DefaultClient, host.URL+"/v1/sessions/"+sess.ID+"/permissions/"+p.PermissionID, protocol.PermissionDecisionRequest{ConversationID: sess.ID, Decision: protocol.PermissionDecision{PermissionID: p.PermissionID, Decision: "allow_always", Scope: p.Suggestion.DefaultScope}}, nil)
		r.Body.Close()
		status = r.StatusCode
		return protocol.PermissionDecision{Decision: "reject"}
	})
	if len(prompts) != 1 {
		t.Fatalf("prompts=%d", len(prompts))
	}
	if status != http.StatusOK {
		t.Fatalf("default_scope=%q offered by this backend is refused: status %d", prompts[0].Suggestion.DefaultScope, status)
	}
}
