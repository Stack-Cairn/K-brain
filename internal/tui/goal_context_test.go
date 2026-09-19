package tui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type goalCompleteClient struct {
	ai.Client
	complete func(context.Context, ai.Request) (string, ai.Usage, error)
}

func (c goalCompleteClient) Complete(ctx context.Context, req ai.Request) (string, ai.Usage, error) {
	return c.complete(ctx, req)
}

func goalContextFixture(t *testing.T) *model {
	t.Helper()
	m := compactCmdModel()
	m.agent.Messages = []ai.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "fix the test"},
		{Role: "assistant", Content: "I will investigate"},
	}
	m.goal, m.goalRounds = "previous goal", 7
	t.Cleanup(m.cancelGoalFromContext)
	return m
}

func goalWork(t *testing.T, m *model) tea.Cmd {
	t.Helper()
	_, cmd := m.command("/goal-from-context")
	if cmd == nil || !m.busy || m.goalRequest == nil {
		t.Fatal("missing pending formulation")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatal("expected spinner and formulation commands")
	}
	return batch[1]
}

func TestGoalFormulationDeferredSnapshotAndDeadline(t *testing.T) {
	m := goalContextFixture(t)
	m.agent.Model = "model1"
	calls := 0
	m.agent.Client = goalCompleteClient{complete: func(ctx context.Context, req ai.Request) (string, ai.Usage, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > goalFormulationTimeout || time.Until(deadline) <= 0 {
			t.Fatal("formulation must have a bounded deadline")
		}
		if req.Model != "model1" || !strings.Contains(req.Messages[0].Content, "fix the test") || strings.Contains(req.Messages[0].Content, "changed history") {
			t.Fatalf("request was not snapshotted: %+v", req)
		}
		return "", ai.Usage{PromptTokens: 11}, context.DeadlineExceeded
	}}
	work := goalWork(t, m)
	if calls != 0 {
		t.Fatal("command handler made a blocking API call")
	}
	m.agent.Model = "model2"
	m.agent.Messages[1].Content = "changed history"
	m.Update(work())
	if calls != 1 || m.busy || m.cancel != nil || m.goalRequest != nil {
		t.Fatal("timeout did not settle the formulation")
	}
	if m.goal != "previous goal" || m.goalRounds != 7 || !strings.Contains(lastBlock(m), "timed out") {
		t.Fatal("timeout changed the existing goal or did not report the error")
	}
	if m.agent.Usage().PromptTokens != 11 {
		t.Fatal("formulation usage was lost")
	}
}

func TestGoalFormulationCancelledBeforeExecution(t *testing.T) {
	m := goalContextFixture(t)
	m.agent.Client = goalCompleteClient{complete: func(context.Context, ai.Request) (string, ai.Usage, error) {
		t.Fatal("cancelled formulation reached the API")
		return "", ai.Usage{}, nil
	}}
	work := goalWork(t, m)
	m.key(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(work())
	if m.busy || m.goal != "previous goal" || !strings.Contains(lastBlock(m), "interrupted") {
		t.Fatal("cancelled request did not settle cleanly")
	}
}

func TestGoalFormulationLateSuccessAfterCancel(t *testing.T) {
	m := goalContextFixture(t)
	m.agent.Client = goalCompleteClient{complete: func(context.Context, ai.Request) (string, ai.Usage, error) {
		return "late goal", ai.Usage{}, nil
	}}
	work := goalWork(t, m)
	msg := work()
	m.cancel()
	m.Update(msg)
	if m.busy || m.goal != "previous goal" || m.goalRounds != 7 {
		t.Fatal("a cancelled success started or replaced the goal")
	}
}

func TestGoalFormulationStaleResults(t *testing.T) {
	for _, kind := range []string{"manual goal", "new request", "new session", "new agent"} {
		t.Run(kind, func(t *testing.T) {
			m := goalContextFixture(t)
			op := pendingGoalFormulation(m)
			var next *goalFormulation
			switch kind {
			case "manual goal":
				m.setGoal("manual replacement")
				if op.ctx.Err() == nil {
					t.Fatal("manual goal did not cancel formulation")
				}
			case "new request":
				m.cancelGoalFromContext()
				next = pendingGoalFormulation(m)
			case "new session":
				m.sessionID = "another-session"
			case "new agent":
				m.agent = compactCmdModel().agent
			}
			_, cmd := m.Update(goalFromContextMsg{request: op, goal: "obsolete result"})
			if cmd != nil || m.goal == "obsolete result" {
				t.Fatal("stale result started a goal")
			}
			if next != nil && (!m.busy || m.goalRequest != next || next.ctx.Err() != nil) {
				t.Fatal("stale result cleared the newer operation")
			}
		})
	}
}

func TestGoalFormulationEmptyResultKeepsGoal(t *testing.T) {
	m := goalContextFixture(t)
	op := pendingGoalFormulation(m)
	m.Update(goalFromContextMsg{request: op, goal: " \n "})
	if m.busy || m.goal != "previous goal" || m.goalRounds != 7 || !strings.Contains(lastBlock(m), "empty goal") {
		t.Fatal("empty formulation changed the existing goal")
	}
}

type goalProgramHarness struct {
	*model
	initial tea.Cmd
	resized chan struct{}
}

func (h *goalProgramHarness) Init() tea.Cmd { return h.initial }

func (h *goalProgramHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := h.model.Update(msg)
	switch msg.(type) {
	case tea.WindowSizeMsg:
		select {
		case h.resized <- struct{}{}:
		default:
		}
	case goalFromContextMsg:
		return h, tea.Quit
	}
	return h, cmd
}

func TestGoalFormulationRetryDoesNotBlockProgram(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	m := goalContextFixture(t)
	client := ai.New(srv.URL, "key")
	retrying := make(chan struct{}, 1)
	client.OnRetry = func(ai.RetryEvent) {
		select {
		case retrying <- struct{}{}:
		default:
		}
	}
	m.agent.Client = client
	_, initial := m.command("/goal-from-context")
	h := &goalProgramHarness{model: m, initial: initial, resized: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	p := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	m.prog = p
	defer p.Kill()
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	select {
	case <-retrying:
	case <-ctx.Done():
		t.Fatal("formulation never entered retry backoff")
	}
	p.Send(tea.WindowSizeMsg{Width: 90, Height: 30})
	select {
	case <-h.resized:
	case <-ctx.Done():
		t.Fatal("retry blocked UI resize")
	}
	p.Send(tea.KeyMsg{Type: tea.KeyEsc})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Escape did not interrupt retry backoff")
	}
	if m.busy || m.cancel != nil || m.goalRequest != nil || m.goal != "previous goal" || m.goalRounds != 7 {
		t.Fatal("retry cancellation did not preserve the goal and release UI state")
	}
}
