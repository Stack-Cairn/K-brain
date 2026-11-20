package tui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/goalloop"
)

// goalVerifyFixture serves the verifier's completion with status/verdict and records
// every request body; streamed turns reply "still working".
type goalVerifyFixture struct {
	mu      sync.Mutex
	checks  []string
	streams []string
}

func (f *goalVerifyFixture) counts() (checks, streams []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.checks...), append([]string(nil), f.streams...)
}

func goalVerifyModel(t *testing.T, status int, verdict string) (*model, *goalVerifyFixture) {
	t.Helper()
	f := &goalVerifyFixture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(b, &req)
		f.mu.Lock()
		defer f.mu.Unlock()
		if req.Stream {
			f.streams = append(f.streams, string(b))
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"still working\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		f.checks = append(f.checks, string(b))
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": verdict}}}})
		w.WriteHeader(status)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	m := compactCmdModel()
	client := ai.New(srv.URL, "k")
	client.MaxRetries = 0
	m.agent = agent.New(client, "model1", 100, "sys")
	m.agent.Messages = append(m.agent.Messages,
		ai.Message{Role: "user", Content: "ship the thing"},
		ai.Message{Role: "assistant", Content: "GOAL_MET — shipped"},
	)
	m.goal = "ship the thing"
	t.Cleanup(func() {
		if m.cancel != nil {
			m.cancel()
		}
	})
	return m, f
}

// runGoalVerify feeds the claim and then the verifier's result back into the model.
func runGoalVerify(t *testing.T, m *model) tea.Cmd {
	t.Helper()
	_, cmd := m.Update(turnDoneMsg{final: "GOAL_MET — shipped"})
	if cmd == nil || !m.busy || m.goalVerify == nil {
		t.Fatal("verification was not scheduled")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatal("expected asynchronous verification and spinner commands")
	}
	for _, work := range batch {
		if msg, ok := work().(goalVerifyMsg); ok {
			_, next := m.Update(msg)
			return next
		}
	}
	t.Fatal("missing verification result")
	return nil
}

func transcriptHas(m *model, want string) bool {
	for _, b := range m.blocks {
		if strings.Contains(b.text, want) {
			return true
		}
	}
	return false
}

func TestGoalVerifierRejectionStartsAnotherRoundWithTheReason(t *testing.T) {
	m, f := goalVerifyModel(t, 200, `{"met":false,"reason":"the tests were never run"}`)
	next := runGoalVerify(t, m)
	if next == nil || m.goal != "ship the thing" || m.goalRounds != 1 || m.goalVerifyRejects != 1 {
		t.Fatalf("rejection must continue the goal: goal %q rounds %d rejects %d", m.goal, m.goalRounds, m.goalVerifyRejects)
	}
	if !transcriptHas(m, "goal verifier: not met") || !transcriptHas(m, "the tests were never run") {
		t.Fatal("the verifier's reason must be shown")
	}
	checks, _ := f.counts()
	if len(checks) != 1 || !strings.Contains(checks[0], "ship the thing") || strings.Contains(checks[0], `"tools"`) {
		t.Fatalf("verifier request: %v", checks)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, streams := f.counts()
		if len(streams) > 0 {
			if !strings.Contains(streams[0], "[goal verifier]") || !strings.Contains(streams[0], "the tests were never run") {
				t.Fatalf("next round must carry the reason: %s", streams[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no next round was sent")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGoalVerifierAcceptanceCompletesTheGoal(t *testing.T) {
	m, _ := goalVerifyModel(t, 200, `{"met":true,"reason":"tests pass in the log"}`)
	runGoalVerify(t, m)
	if m.goal != "" || m.busy {
		t.Fatalf("accepted claim must complete the goal: %q busy %v", m.goal, m.busy)
	}
	if !transcriptHas(m, "verified: tests pass in the log") {
		t.Fatal("the verifier's reason must be shown")
	}
}

func TestGoalVerifierFailureAcceptsTheClaim(t *testing.T) {
	m, _ := goalVerifyModel(t, 400, `bad`)
	runGoalVerify(t, m)
	if m.goal != "" {
		t.Fatalf("a failed verifier must accept the claim, goal still %q", m.goal)
	}
	if !transcriptHas(m, "goal verifier unavailable") {
		t.Fatal("the failure must be noted")
	}
}

func TestGoalVerifierDisabledMakesNoCall(t *testing.T) {
	m, f := goalVerifyModel(t, 200, `{"met":false,"reason":"no"}`)
	off := false
	m.cfg.GoalVerify = &off
	_, cmd := m.Update(turnDoneMsg{final: "GOAL_MET — shipped"})
	if m.goal != "" || m.goalVerify != nil || cmd != nil {
		t.Fatal("with the verifier off the claim completes at once")
	}
	if checks, _ := f.counts(); len(checks) != 0 {
		t.Fatalf("no verifier call expected, got %d", len(checks))
	}
}

func TestGoalVerifierCapAcceptsWithoutAnotherCall(t *testing.T) {
	m, f := goalVerifyModel(t, 200, `{"met":false,"reason":"no"}`)
	m.goalVerifyRejects = goalloop.DefaultVerifyMaxRejections
	m.Update(turnDoneMsg{final: "GOAL_MET — shipped"})
	if m.goal != "" || !transcriptHas(m, "accepting this one without another check") {
		t.Fatal("past the cap the claim is accepted with a warning")
	}
	if checks, _ := f.counts(); len(checks) != 0 {
		t.Fatalf("no verifier call expected, got %d", len(checks))
	}
}

func TestGoalVerifyCommandTogglesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("K_BRAIN_HOME", home)
	t.Setenv("LIVEAGENT_HOME", home)
	m, _ := goalVerifyModel(t, 200, `{}`)
	m.command("/goal verify off")
	if goalloop.VerifyEnabled(m.cfg) {
		t.Fatal("/goal verify off must turn the verifier off")
	}
	m.command("/goal verify on")
	if !goalloop.VerifyEnabled(m.cfg) || m.cfg.GoalVerify != nil {
		t.Fatal("/goal verify on must restore the default")
	}
	if m.goal != "ship the thing" {
		t.Fatalf("/goal verify must not set a goal, goal is %q", m.goal)
	}
}
