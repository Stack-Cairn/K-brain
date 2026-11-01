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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/permissions"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// rulesClient answers each user turn with the next batch of Bash calls, then "done"
// once the tool results come back.
type rulesClient struct {
	scriptedClient
	lock    sync.Mutex
	batches [][]string
	next    int
	callID  int
}

func (c *rulesClient) Stream(_ context.Context, req ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if last := req.Messages[len(req.Messages)-1]; last.Role != "tool" && c.next < len(c.batches) {
		var calls []ai.ToolCall
		for _, command := range c.batches[c.next] {
			c.callID++
			call := ai.ToolCall{ID: fmt.Sprintf("rules-call-%d", c.callID), Type: "function"}
			args, _ := json.Marshal(map[string]string{"command": command})
			call.Function.Name, call.Function.Arguments = "Bash", string(args)
			calls = append(calls, call)
		}
		c.next++
		return ai.Message{Role: "assistant", ToolCalls: calls}, ai.Usage{}, nil
	}
	if text != nil {
		text("done")
	}
	return ai.Message{Role: "assistant", Content: "done", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

var rulesRequestSeq atomic.Int64

func newRulesServer(t *testing.T, client ai.Client) (string, string) {
	t.Helper()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rulesDir := t.TempDir()
	eventDir := t.TempDir()
	factory := func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
		ag := agent.New(client, selected.Model, 1024, "system")
		ag.ModelName, ag.Provider = selected.Model, selected.Provider
		return ag, nil
	}
	backendServer, err := New(Options{Store: store, Factory: factory, EventDir: eventDir, Models: []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}}, DefaultCWD: t.TempDir(), PermissionRulesDir: rulesDir})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(backendServer)
	t.Cleanup(func() {
		httpServer.Close()
		_ = backendServer.Close()
	})
	return httpServer.URL, eventDir
}

// runRules starts a run and answers every prompt with answer; it returns the run's
// events and the prompts it saw.
func runRules(t *testing.T, base, sessionID, mode string, answer func(protocol.PermissionRequest) protocol.PermissionDecision) ([]protocol.Event, []protocol.PermissionRequest) {
	t.Helper()
	options := protocol.RunOptions{Mode: "agent", PermissionMode: mode}
	var accepted protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sessionID+"/runs", protocol.PromptRequest{ConversationID: sessionID, ClientRequestID: fmt.Sprintf("rules-%d", rulesRequestSeq.Add(1)), Prompt: "go", Options: &options}, &accepted)
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
			if answer == nil {
				t.Fatalf("unexpected prompt %+v", prompt)
			}
			decision := answer(prompt)
			if decision.Decision == "" {
				continue
			}
			decision.PermissionID = prompt.PermissionID
			r := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sessionID+"/permissions/"+prompt.PermissionID, protocol.PermissionDecisionRequest{ConversationID: sessionID, RunID: accepted.RunID, Decision: decision}, nil)
			body, _ := io.ReadAll(r.Body)
			r.Body.Close()
			if r.StatusCode != http.StatusOK {
				t.Fatalf("answer status=%d: %s", r.StatusCode, body)
			}
		case protocol.EventRunCompleted, protocol.EventRunFailed, protocol.EventRunCancelled:
			return events, prompts
		}
	}
	t.Fatalf("run did not finish: %+v", events)
	return nil, nil
}

func toolResults(t *testing.T, events []protocol.Event) []protocol.ToolResult {
	t.Helper()
	var out []protocol.ToolResult
	for _, event := range events {
		if event.Type == protocol.EventToolResult {
			var result protocol.ToolResultEvent
			_ = json.Unmarshal(event.Payload, &result)
			out = append(out, result.ToolResult)
		}
	}
	return out
}

func autoResolved(events []protocol.Event) []protocol.PermissionAutoResolved {
	var out []protocol.PermissionAutoResolved
	for _, event := range events {
		if event.Type == protocol.EventPermissionAutoResolved {
			var payload protocol.PermissionAutoResolved
			_ = json.Unmarshal(event.Payload, &payload)
			out = append(out, payload)
		}
	}
	return out
}

func listRules(t *testing.T, base, query string) protocol.PermissionRulesResponse {
	t.Helper()
	resp, err := http.Get(base + "/v1/permissions/rules?" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out protocol.PermissionRulesResponse
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("list status=%d: %s", resp.StatusCode, b)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPermissionRulesHTTPReadOnlyCommandRunsWithoutPrompt(t *testing.T) {
	base, _ := newRulesServer(t, &rulesClient{batches: [][]string{{"echo rules-read-only"}}})
	sess := createTestSession(t, base)
	events, prompts := runRules(t, base, sess.ID, "normal", nil)
	if len(prompts) != 0 {
		t.Fatalf("a read-only command must not prompt: %+v", prompts)
	}
	results := toolResults(t, events)
	if len(results) != 1 || results[0].Failed {
		t.Fatalf("results=%+v", results)
	}
	auto := autoResolved(events)
	if len(auto) != 1 || auto[0].Source != "read_only" || auto[0].Tool != "Bash" || auto[0].ToolCallID == "" {
		t.Fatalf("auto_resolved=%+v", auto)
	}
}

func TestPermissionRulesHTTPRememberForProjectCoversNextRun(t *testing.T) {
	base, _ := newRulesServer(t, &rulesClient{batches: [][]string{{"hostname"}, {"hostname"}}})
	sess := createTestSession(t, base)
	_, prompts := runRules(t, base, sess.ID, "normal", func(p protocol.PermissionRequest) protocol.PermissionDecision {
		return protocol.PermissionDecision{Decision: "allow_always", Scope: "project"}
	})
	if len(prompts) != 1 {
		t.Fatalf("prompts=%+v", prompts)
	}
	p := prompts[0]
	if p.Suggestion == nil || len(p.Suggestion.Rules) != 1 || p.Suggestion.Rules[0] != "Bash(hostname:*)" || p.Suggestion.DefaultScope != "project" {
		t.Fatalf("suggestion=%+v", p.Suggestion)
	}
	if p.Shell == "" {
		t.Fatal("the prompt names the shell")
	}
	offered := false
	for _, option := range p.Options {
		offered = offered || option.ID == "allow_always"
	}
	if !offered {
		t.Fatalf("allow_always is offered with a suggestion: %+v", p.Options)
	}
	events, prompts := runRules(t, base, sess.ID, "normal", nil)
	if len(prompts) != 0 {
		t.Fatalf("the remembered rule covers the second run: %+v", prompts)
	}
	if auto := autoResolved(events); len(auto) != 1 || auto[0].Source != "rule" || auto[0].Rule != "Bash(hostname:*)" || auto[0].Scope != "project" {
		t.Fatalf("auto_resolved=%+v", auto)
	}
	listed := listRules(t, base, "workspace="+sess.CWD)
	if len(listed.Rules) != 1 || listed.Rules[0].Scope != "project" || listed.Rules[0].Rule != "Bash(hostname:*)" || listed.Rules[0].SourceSession != sess.ID {
		t.Fatalf("listed=%+v", listed)
	}
	// Another project does not see it.
	other := createTestSession(t, base)
	if l := listRules(t, base, "workspace="+other.CWD); len(l.Rules) != 0 {
		t.Fatalf("project rules leaked: %+v", l)
	}
}

// Clients from before rules send allow_always without a scope and tell the user it
// lasts for the session; it must not be stored any wider.
func TestPermissionRulesHTTPAllowAlwaysWithoutScopeIsSessionOnly(t *testing.T) {
	client := &rulesClient{batches: [][]string{{"hostname"}, {"hostname"}, {"hostname"}}}
	base, _ := newRulesServer(t, client)
	sess := createTestSession(t, base)
	runRules(t, base, sess.ID, "normal", func(protocol.PermissionRequest) protocol.PermissionDecision {
		return protocol.PermissionDecision{Decision: "allow_always"}
	})
	if _, prompts := runRules(t, base, sess.ID, "normal", nil); len(prompts) != 0 {
		t.Fatalf("same session is covered: %+v", prompts)
	}
	other := createTestSession(t, base)
	_, prompts := runRules(t, base, other.ID, "normal", func(protocol.PermissionRequest) protocol.PermissionDecision {
		return protocol.PermissionDecision{Decision: "reject"}
	})
	if len(prompts) != 1 {
		t.Fatalf("another session must ask again: %+v", prompts)
	}
	if l := listRules(t, base, "session_id="+sess.ID); len(l.Rules) != 1 || l.Rules[0].Scope != "session" {
		t.Fatalf("session rules=%+v", l)
	}
}

func TestPermissionRulesHTTPDenyRuleRefusesInAlwaysMode(t *testing.T) {
	base, _ := newRulesServer(t, &rulesClient{batches: [][]string{{"hostname"}}})
	sess := createTestSession(t, base)
	var created protocol.PermissionRule
	resp := postJSON(t, http.DefaultClient, base+"/v1/permissions/rules", protocol.PermissionRuleCreate{Rule: "Bash(hostname:*)", Effect: "deny", Scope: "user"}, &created)
	if resp.StatusCode != http.StatusCreated || created.ID == "" || created.Scope != "user" {
		t.Fatalf("create status=%d rule=%+v", resp.StatusCode, created)
	}
	events, prompts := runRules(t, base, sess.ID, "always", nil)
	if len(prompts) != 0 {
		t.Fatalf("a deny rule refuses without asking: %+v", prompts)
	}
	results := toolResults(t, events)
	if len(results) != 1 || !results[0].Failed || !strings.Contains(results[0].Output, "Bash(hostname:*)") {
		t.Fatalf("results=%+v", results)
	}
	// Deleting the rule through the API lets the next run ask normally.
	req, _ := http.NewRequest(http.MethodDelete, base+"/v1/permissions/rules/"+created.ID+"?scope=user", nil)
	del, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	del.Body.Close()
	if del.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d", del.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodDelete, base+"/v1/permissions/rules/"+created.ID+"?scope=user", nil)
	again, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	again.Body.Close()
	if again.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete status=%d", again.StatusCode)
	}
}

func TestPermissionRulesHTTPRememberCoversLaterCallInSameRun(t *testing.T) {
	base, _ := newRulesServer(t, &rulesClient{batches: [][]string{{"hostname", "hostname"}}})
	sess := createTestSession(t, base)
	answers := 0
	events, prompts := runRules(t, base, sess.ID, "normal", func(protocol.PermissionRequest) protocol.PermissionDecision {
		answers++
		return protocol.PermissionDecision{Decision: "allow_always", Scope: "session"}
	})
	results := toolResults(t, events)
	if len(results) != 2 || results[0].Failed || results[1].Failed {
		t.Fatalf("both calls run: %+v", results)
	}
	if len(prompts) != 1 || answers != 1 {
		t.Fatalf("the second call is covered by the rule just remembered: prompts=%d answers=%d", len(prompts), answers)
	}
}

// Prompts already waiting when a rule is remembered are released only when the rule
// covers them and no review sent them to a person.
func TestSettleCoveredPermissionsReleasesOnlyCoveredWaiters(t *testing.T) {
	s := &Server{eventDir: t.TempDir()}
	rt := &runtimeSession{id: "settle-session", permissions: map[string]*permissionWaiter{}}
	waiter := func(command string, settleable bool) *permissionWaiter {
		return &permissionWaiter{gate: tools.GateRequest{Tool: "Bash", Command: command, Shell: "bash"}, ruleSettleable: settleable, result: make(chan tools.GateDecision, 1), reason: make(chan string, 1)}
	}
	covered, held, other := waiter("hostname", true), waiter("hostname -s", false), waiter("whoami", true)
	rt.permissions["covered"], rt.permissions["held"], rt.permissions["other"] = covered, held, other
	if _, err := permissions.AddRule(s.sessionRulesPath(rt.id), "", permissions.StoredRule{Rule: "Bash(hostname:*)", Effect: permissions.Allow}); err != nil {
		t.Fatal(err)
	}
	s.settleCoveredPermissions(rt)
	select {
	case d := <-covered.result:
		if d != tools.GateAllowOnce {
			t.Fatalf("covered decision=%v", d)
		}
	default:
		t.Fatal("the covered prompt was not released")
	}
	for name, w := range map[string]*permissionWaiter{"held": held, "other": other} {
		select {
		case d := <-w.result:
			t.Fatalf("%s prompt was released: %v", name, d)
		default:
		}
		if rt.permissions[name] != w {
			t.Fatalf("%s prompt left the waiting list", name)
		}
	}
	if _, ok := rt.permissions["covered"]; ok {
		t.Fatal("the released prompt stays listed")
	}
}

func TestPermissionRulesHTTPRejectsUnknownScopeAndKeepsPrompt(t *testing.T) {
	base, _ := newRulesServer(t, &rulesClient{batches: [][]string{{"hostname"}}})
	sess := createTestSession(t, base)
	tries := 0
	runRules(t, base, sess.ID, "normal", func(p protocol.PermissionRequest) protocol.PermissionDecision {
		tries++
		r := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/permissions/"+p.PermissionID, protocol.PermissionDecisionRequest{ConversationID: sess.ID, Decision: protocol.PermissionDecision{PermissionID: p.PermissionID, Decision: "allow_always", Scope: "galaxy"}}, nil)
		r.Body.Close()
		if r.StatusCode != http.StatusBadRequest {
			t.Fatalf("unknown scope status=%d", r.StatusCode)
		}
		return protocol.PermissionDecision{Decision: "allow_once"}
	})
	if tries != 1 {
		t.Fatalf("tries=%d", tries)
	}
	if l := listRules(t, base, "session_id="+sess.ID+"&workspace="+sess.CWD); len(l.Rules) != 0 {
		t.Fatalf("nothing is stored for a refused scope: %+v", l)
	}
}

func TestPermissionRulesHTTPDeleteSessionRemovesItsRules(t *testing.T) {
	base, eventDir := newRulesServer(t, &rulesClient{batches: [][]string{{"hostname"}}})
	sess := createTestSession(t, base)
	runRules(t, base, sess.ID, "normal", func(protocol.PermissionRequest) protocol.PermissionDecision {
		return protocol.PermissionDecision{Decision: "allow_always", Scope: "session"}
	})
	path := eventDir + string(os.PathSeparator) + sess.ID + ".permission-rules.json"
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("session rules file: %v", err)
	}
	req, _ := http.NewRequest(http.MethodDelete, base+"/v1/sessions/"+sess.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("session rules survive deletion: %v", err)
	}
}

func TestPermissionRulesHTTPRejectsBadInput(t *testing.T) {
	base, _ := newRulesServer(t, &rulesClient{})
	for _, body := range []protocol.PermissionRuleCreate{
		{Rule: "Bash(", Effect: "allow", Scope: "user"},
		{Rule: "Bash(ls)", Effect: "maybe", Scope: "user"},
		{Rule: "Bash(ls)", Effect: "allow", Scope: "galaxy"},
		{Rule: "Bash(ls)", Effect: "allow", Scope: "session", SessionID: "../x"},
		{Rule: "Bash(ls)", Effect: "allow", Scope: "project", Workspace: "relative/dir"},
	} {
		resp := postJSON(t, http.DefaultClient, base+"/v1/permissions/rules", body, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%+v: status=%d", body, resp.StatusCode)
		}
	}
	resp, err := http.Get(base + "/v1/permissions/rules?session_id=../x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("traversal session id status=%d", resp.StatusCode)
	}
}

var _ ai.Client = (*rulesClient)(nil)
