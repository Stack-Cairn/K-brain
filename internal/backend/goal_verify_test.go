package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// verifyProvider is an OpenAI-compatible fixture: streamed requests are goal rounds
// and get the scripted replies; plain completions are verifier requests and get the
// scripted verdicts. A verdict "!400" answers with an HTTP error.
type verifyProvider struct {
	mu       sync.Mutex
	replies  []string
	verdicts []string
	turns    []string
	checks   []string
}

func (p *verifyProvider) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Stream   bool `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &req)
	last := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			raw, _ := json.Marshal(req.Messages[i].Content)
			last = string(raw)
			break
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if req.Stream {
		p.turns = append(p.turns, last)
		reply := "still working"
		if len(p.replies) > 0 {
			reply, p.replies = p.replies[0], p.replies[1:]
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": reply}, "finish_reason": "stop"}}})
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+string(chunk)+"\n\ndata: [DONE]\n\n")
		return
	}
	p.checks = append(p.checks, last)
	verdict := `{"met":true,"reason":"checked"}`
	if len(p.verdicts) > 0 {
		verdict, p.verdicts = p.verdicts[0], p.verdicts[1:]
	}
	if verdict == "!400" {
		http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
		return
	}
	out, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": verdict}, "finish_reason": "stop"}}})
	_, _ = w.Write(out)
}

func (p *verifyProvider) snapshot() (turns, checks []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.turns...), append([]string(nil), p.checks...)
}

func newVerifyGoalServer(t *testing.T, p *verifyProvider, cfg *config.Config) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(srv.Close)
	client := ai.New(srv.URL, "k")
	client.MaxRetries = 0
	server, _, base := newGoalTestServer(t, client)
	if cfg != nil {
		// Runs check the selected model against the settings; describe the fixture.
		cfg.Models = map[string]config.Model{"fixture-model": {Providers: []string{"fixture"}}}
		cfg.Providers = map[string]config.Provider{"fixture": {BaseURL: srv.URL + "/v1"}}
		server.settings = NewSettingsStore(cfg, func(*config.Config) error { return nil })
	}
	return base
}

func TestGoalVerifierRejectionRunsAnotherRoundWithTheReason(t *testing.T) {
	p := &verifyProvider{
		replies:  []string{"GOAL_MET — done", "GOAL_MET — now really done"},
		verdicts: []string{`{"met":false,"reason":"tests were never run"}`, "```json\n{\"met\":true,\"reason\":\"tests pass in the log\"}\n```"},
	}
	base := newVerifyGoalServer(t, p, nil)
	sess := createTestSession(t, base)
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "ship the feature", Run: &protocol.PromptRequest{ClientRequestID: "g-1"}})
	final := waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })
	if v := final.Verification; v == nil || v.Verdict != "met" || v.Reason != "tests pass in the log" || v.Rejections != 1 {
		t.Fatalf("final verification: %+v", final.Verification)
	}
	turns, checks := p.snapshot()
	if len(turns) != 2 || len(checks) != 2 {
		t.Fatalf("turns %d checks %d", len(turns), len(checks))
	}
	if !strings.Contains(turns[1], "[goal verifier]") || !strings.Contains(turns[1], "tests were never run") || !strings.Contains(turns[1], "[goal check]") {
		t.Fatalf("second round must carry the verifier's reason: %s", turns[1])
	}
	if !strings.Contains(checks[0], "ship the feature") || !strings.Contains(checks[0], "GOAL_MET — done") {
		t.Fatalf("verifier request must carry the goal and the claim: %s", checks[0])
	}
	var sawRejection bool
	for _, event := range goalEvents(t, base, sess.ID) {
		if v := event.Verification; v != nil && v.Verdict == "not_met" && event.Status == "continuing" && event.Round == 1 {
			sawRejection = true
		}
	}
	if !sawRejection {
		t.Fatal("the rejection must be published as a continuing goal event")
	}
}

func TestGoalVerifierFailureAcceptsTheClaim(t *testing.T) {
	for name, verdict := range map[string]string{"http error": "!400", "unparseable": "looks fine to me"} {
		t.Run(name, func(t *testing.T) {
			p := &verifyProvider{replies: []string{"GOAL_MET — done"}, verdicts: []string{verdict}}
			base := newVerifyGoalServer(t, p, nil)
			sess := createTestSession(t, base)
			postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "ship it", Run: &protocol.PromptRequest{ClientRequestID: "g-1"}})
			final := waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })
			if v := final.Verification; v == nil || v.Verdict != "error" || v.Reason == "" {
				t.Fatalf("verification: %+v", final.Verification)
			}
			if turns, _ := p.snapshot(); len(turns) != 1 {
				t.Fatalf("no extra round expected, got %d", len(turns))
			}
		})
	}
}

func TestGoalVerifierDisabledMakesNoCall(t *testing.T) {
	off := false
	p := &verifyProvider{replies: []string{"GOAL_MET — done"}}
	base := newVerifyGoalServer(t, p, &config.Config{GoalVerify: &off})
	sess := createTestSession(t, base)
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "ship it", Run: &protocol.PromptRequest{ClientRequestID: "g-1"}})
	final := waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })
	if final.Verification != nil {
		t.Fatalf("no verification expected: %+v", final.Verification)
	}
	if _, checks := p.snapshot(); len(checks) != 0 {
		t.Fatalf("verifier must not be called when off, got %d calls", len(checks))
	}
}

func TestGoalVerifierRejectionCapAcceptsTheClaim(t *testing.T) {
	p := &verifyProvider{
		replies:  []string{"GOAL_MET — a", "GOAL_MET — b", "GOAL_MET — c"},
		verdicts: []string{`{"met":false,"reason":"no"}`, `{"met":false,"reason":"still no"}`},
	}
	base := newVerifyGoalServer(t, p, &config.Config{GoalVerifyMaxRejections: 2})
	sess := createTestSession(t, base)
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "ship it", Run: &protocol.PromptRequest{ClientRequestID: "g-1"}})
	final := waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })
	if v := final.Verification; v == nil || v.Verdict != "cap" || v.Rejections != 2 {
		t.Fatalf("verification: %+v", final.Verification)
	}
	if turns, checks := p.snapshot(); len(turns) != 3 || len(checks) != 2 {
		t.Fatalf("turns %d checks %d", len(turns), len(checks))
	}
}
