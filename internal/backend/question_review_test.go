package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// questionReviewFixture is questionFixture with an AI reviewer whose replies cannot be
// parsed: a reviewed call would get an unknown verdict and a manual prompt.
func questionReviewFixture(t *testing.T, approval config.AIApprovalConfig) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "broken"}}}})
	}))
	t.Cleanup(provider.Close)
	backend, server, _, _ := questionFixture(t, 5*time.Second)
	approval.Provider, approval.Model = "fixture", "small"
	backend.settings = NewSettingsStore(&config.Config{AIApproval: approval, Providers: map[string]config.Provider{"fixture": {API: "openai-completions", BaseURL: provider.URL, APIKey: "fixture-key"}}, Models: map[string]config.Model{"small": {Providers: []string{"fixture"}}, "fixture-model": {Providers: []string{"fixture"}}}}, nil)
	return server, calls
}

func startQuestionRunIn(t *testing.T, base string, sess protocol.Session, options protocol.RunOptions) protocol.RunAccepted {
	t.Helper()
	var accepted protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "ask", Prompt: "ask me", Options: &options}, &accepted)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status=%d", resp.StatusCode)
	}
	return accepted
}

// sessionEvents streams a session's events until the stream ends or the deadline.
func sessionEvents(t *testing.T, base, sessionID string) *bufio.Scanner {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/sessions/"+sessionID+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	return scanner
}

func nextEvent(t *testing.T, scanner *bufio.Scanner) (protocol.Event, bool) {
	t.Helper()
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		return event, true
	}
	return protocol.Event{}, false
}

// AskUserQuestion only asks the user: in auto_review, or with a global AI reviewer, it
// must not wait for a review (up to approval.Timeout) or fall back to a manual prompt
// when the review cannot be parsed. question.requested follows tool.call directly.
func TestAskUserQuestionSkipsAIReview(t *testing.T) {
	for _, tc := range []struct {
		name, permissionMode, scope string
		approval                    config.AIApprovalConfig
	}{
		{"auto-review", "auto_review", "", config.AIApprovalConfig{Mode: "off"}},
		{"auto-review-per-run", "auto_review", "per_run", config.AIApprovalConfig{Mode: "off"}},
		{"global-auto-normal", "normal", "", config.AIApprovalConfig{Mode: "auto"}},
		{"global-auto-full-access", "full_access", "", config.AIApprovalConfig{Mode: "auto"}},
		{"global-risk-only", "normal", "", config.AIApprovalConfig{Mode: "risk_only"}},
		{"plan", "plan", "", config.AIApprovalConfig{Mode: "auto"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, reviews := questionReviewFixture(t, tc.approval)
			sess := createTestSession(t, server.URL)
			run := startQuestionRunIn(t, server.URL, sess, protocol.RunOptions{Mode: "agent", ApprovalPolicy: "ask", PermissionMode: tc.permissionMode, ReviewScope: tc.scope})
			events := sessionEvents(t, server.URL, sess.ID)
			var question protocol.QuestionRequest
			sawCall := false
			for question.QuestionID == "" {
				event, ok := nextEvent(t, events)
				if !ok {
					t.Fatal("stream ended before question.requested")
				}
				switch {
				case event.Type == protocol.EventToolCall:
					sawCall = true
					continue
				case !sawCall:
					continue
				case event.Type == protocol.EventQuestionRequested:
					_ = json.Unmarshal(event.Payload, &question)
				default:
					t.Fatalf("%s came between tool.call and question.requested: %s", event.Type, event.Payload)
				}
			}
			answer := protocol.QuestionAnswerRequest{ConversationID: sess.ID, RunID: run.RunID, QuestionID: question.QuestionID, Answers: questionDefaults(question.Questions)}
			resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/questions/"+question.QuestionID, answer, nil)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("answer status=%d", resp.StatusCode)
			}
			completed := false
			for !completed {
				event, ok := nextEvent(t, events)
				if !ok {
					t.Fatal("stream ended before run.completed")
				}
				switch event.Type {
				case protocol.EventPermissionRequest, "approval.reviewed", protocol.EventRunFailed:
					t.Fatalf("unexpected %s: %s", event.Type, event.Payload)
				case protocol.EventRunCompleted:
					completed = true
				}
			}
			if n := reviews.Load(); n != 0 {
				t.Fatalf("reviewer contacted %d times", n)
			}
		})
	}
}

// Skipping the review does not skip the permission rules: a deny rule for
// AskUserQuestion refuses the call in every mode, with or without a reviewer, before
// any question is shown.
func TestAskUserQuestionDenyRuleWins(t *testing.T) {
	for _, tc := range []struct {
		name, permissionMode string
		approval             config.AIApprovalConfig
	}{
		{"auto-review", "auto_review", config.AIApprovalConfig{Mode: "off"}},
		{"global-auto", "normal", config.AIApprovalConfig{Mode: "auto"}},
		{"normal-no-reviewer", "normal", config.AIApprovalConfig{Mode: "off"}},
		{"always", "always", config.AIApprovalConfig{Mode: "off"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, reviews := questionReviewFixture(t, tc.approval)
			sess := createTestSession(t, server.URL)
			resp := postJSON(t, http.DefaultClient, server.URL+"/v1/permissions/rules", protocol.PermissionRuleCreate{Rule: "AskUserQuestion", Effect: "deny", Scope: "session", SessionID: sess.ID}, nil)
			resp.Body.Close()
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("rule status=%d", resp.StatusCode)
			}
			startQuestionRunIn(t, server.URL, sess, protocol.RunOptions{Mode: "agent", ApprovalPolicy: "ask", PermissionMode: tc.permissionMode})
			events := sessionEvents(t, server.URL, sess.ID)
			denied := false
			for {
				event, ok := nextEvent(t, events)
				if !ok {
					t.Fatal("stream ended before the run did")
				}
				if event.Type == protocol.EventQuestionRequested || event.Type == protocol.EventPermissionRequest {
					t.Fatalf("deny rule did not win: %s %s", event.Type, event.Payload)
				}
				if event.Type == protocol.EventToolResult && strings.Contains(string(event.Payload), "denied by permission rule AskUserQuestion") {
					denied = true
				}
				if event.Type == protocol.EventRunCompleted || event.Type == protocol.EventRunFailed {
					break
				}
			}
			if !denied {
				t.Fatal("tool result does not report the deny rule")
			}
			if n := reviews.Load(); n != 0 {
				t.Fatalf("reviewer contacted %d times", n)
			}
		})
	}
}
