package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
)

const goalFormulationTimeout = 2 * time.Minute

type goalFormulation struct {
	ctx       context.Context
	cancel    context.CancelFunc
	agent     *agent.Agent
	sessionID string
}

type goalFromContextMsg struct {
	request *goalFormulation
	goal    string
	err     error
}

func (m *model) startGoalFromContext(fields []string) (tea.Model, tea.Cmd) {
	if m.busy {
		m.append(dimStyle.Render("(busy — /goal-from-context after this turn)"))
		return m, nil
	}
	window := agent.GoalFromContextDefaultWindow
	if len(fields) > 2 {
		m.append(errStyle.Render("usage: /goal-from-context [n]"))
		return m, nil
	}
	if len(fields) == 2 {
		n, err := strconv.Atoi(fields[1])
		if err != nil || n < 2 {
			m.append(errStyle.Render("usage: /goal-from-context [n] — n ≥ 2 messages of context (default " + strconv.Itoa(agent.GoalFromContextDefaultWindow) + ")"))
			return m, nil
		}
		window = n
	}
	tail, err := agent.GoalFromContextMessages(m.agent.Messages, window)
	if err != nil {
		m.append(errStyle.Render(err.Error()))
		return m, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	ctx = sandbox.WithPolicy(ctx, m.sandboxPolicy)
	op := &goalFormulation{ctx: ctx, cancel: cancel, agent: m.agent, sessionID: m.sessionID}
	m.goalRequest = op
	m.cancel = cancel
	m.busy = true
	m.interrupt1 = false
	m.append(dimStyle.Render(fmt.Sprintf("◎ formulating goal from the last %d messages…", len(tail))))
	client := m.agent.Client
	req := ai.Request{
		Model: m.agent.Model, MaxTokens: 8192,
		Messages: []ai.Message{{Role: "user", Content: agent.BuildGoalFromContextPrompt(tail)}},
	}
	work := func() tea.Msg {
		ctx, cancel := context.WithTimeout(op.ctx, goalFormulationTimeout)
		defer cancel()
		if err := ctx.Err(); err != nil {
			return goalFromContextMsg{request: op, err: err}
		}
		goal, usage, err := client.Complete(ctx, req)
		op.agent.AddUsage(usage)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return goalFromContextMsg{request: op, goal: goal, err: err}
	}
	return m, tea.Batch(m.spin.Tick, work)
}

func (m *model) cancelGoalFromContext() {
	if m.goalRequest == nil {
		return
	}
	m.goalRequest.cancel()
	m.goalRequest = nil
	m.cancel = nil
	m.busy = false
	m.interrupt1 = false
}

func (m *model) finishGoalFromContext(msg goalFromContextMsg) (tea.Model, tea.Cmd) {
	op := msg.request
	if op == nil || op != m.goalRequest {
		return m, nil
	}
	if op.ctx.Err() != nil {
		msg.err = op.ctx.Err()
	}
	m.cancelGoalFromContext()
	if m.agent != op.agent || m.sessionID != op.sessionID {
		return m, nil
	}
	m.flushThink()
	m.flushCurrent()
	switch {
	case errors.Is(msg.err, context.Canceled):
		m.append(dimStyle.Render("(interrupted)"))
	case errors.Is(msg.err, context.DeadlineExceeded):
		m.append(errStyle.Render("goal-from-context failed: request timed out; the previous goal is unchanged"))
	case msg.err != nil:
		m.append(errStyle.Render("goal-from-context failed: " + msg.err.Error()))
	case strings.TrimSpace(msg.goal) == "":
		m.append(errStyle.Render("goal-from-context: model returned an empty goal"))
	default:
		goal := strings.TrimSpace(msg.goal)
		m.setGoal(goal)
		m.append(dimStyle.Render("◎ goal set: " + goal))
		return m.submit(goal)
	}
	return m, nil
}
