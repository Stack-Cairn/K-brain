package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/goalloop"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
)

// goalVerification is the hidden request checking a GOAL_MET claim. Like a goal
// formulation it holds the session busy so Esc can stop it.
type goalVerification struct {
	ctx       context.Context
	cancel    context.CancelFunc
	agent     *agent.Agent
	sessionID string
	goal      string
}

type goalVerifyMsg struct {
	request *goalVerification
	verdict goalloop.Verdict
	err     error
}

// claimGoal settles a reply that leads with GOAL_MET: it completes the goal at once
// when verification is off (or there is no client to ask), or after too many
// rejections; otherwise it starts the hidden verification request.
func (m *model) claimGoal() (tea.Model, tea.Cmd) {
	if !goalloop.VerifyEnabled(m.cfg) || m.agent == nil || m.agent.Client == nil {
		return m.completeGoal("")
	}
	if limit := goalloop.VerifyMaxRejections(m.cfg); m.goalVerifyRejects >= limit {
		m.append(errStyle.Render(fmt.Sprintf("◎ goal verifier already rejected %d claim(s) — accepting this one without another check", m.goalVerifyRejects)))
		return m.completeGoal("")
	}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = sandbox.WithPolicy(ctx, m.sandboxPolicy)
	op := &goalVerification{ctx: ctx, cancel: cancel, agent: m.agent, sessionID: m.sessionID, goal: m.goal}
	m.goalVerify = op
	m.cancel = cancel
	m.busy = true
	m.interrupt1 = false
	m.append(dimStyle.Render("◎ verifying the goal claim…"))
	work := func() tea.Msg {
		verdict, err := op.agent.VerifyGoal(op.ctx, op.goal)
		if op.ctx.Err() != nil {
			err = op.ctx.Err()
		}
		return goalVerifyMsg{request: op, verdict: verdict, err: err}
	}
	return m, tea.Batch(m.spin.Tick, work)
}

func (m *model) cancelGoalVerify() {
	if m.goalVerify == nil {
		return
	}
	m.goalVerify.cancel()
	m.goalVerify = nil
	m.cancel = nil
	m.busy = false
	m.interrupt1 = false
}

func (m *model) completeGoal(note string) (tea.Model, tea.Cmd) {
	m.append(dimStyle.Render("◎ goal met after " + strconv.Itoa(m.goalRounds) + " round(s)" + note))
	m.setGoal("")
	return m.drainQueueAfterGoalCheck()
}

func (m *model) finishGoalVerify(msg goalVerifyMsg) (tea.Model, tea.Cmd) {
	op := msg.request
	if op == nil || op != m.goalVerify {
		return m, nil
	}
	if op.ctx.Err() != nil {
		msg.err = op.ctx.Err()
	}
	m.cancelGoalVerify()
	if m.agent != op.agent || m.sessionID != op.sessionID || m.goal != op.goal {
		return m, nil
	}
	switch {
	case errors.Is(msg.err, context.Canceled):
		m.append(dimStyle.Render("(interrupted — goal check stopped; /goal resume to continue, /goal clear to drop)"))
		m.tp.goalState = "paused"
		return m.drainQueueAfterGoalCheck()
	case msg.err != nil:
		// Never let a broken verifier hold a finished goal: accept the claim as before.
		m.append(dimStyle.Render("◎ goal verifier unavailable (" + oneLine(msg.err.Error()) + ") — accepting the claim"))
		return m.completeGoal("")
	case msg.verdict.Met:
		note := ""
		if msg.verdict.Reason != "" {
			note = " — verified: " + msg.verdict.Reason
		}
		return m.completeGoal(note)
	}
	m.goalVerifyRejects++
	m.append(errStyle.Render(fmt.Sprintf("◎ goal verifier: not met (%d/%d) — %s", m.goalVerifyRejects, goalloop.VerifyMaxRejections(m.cfg), msg.verdict.Reason)))
	if len(m.queue) > 0 {
		// The user's queued message runs first, as after any round; the goal check
		// follows that turn.
		return m.drainQueueAfterGoalCheck()
	}
	if m.goalRounds >= m.goalMaxRounds() {
		m.append(errStyle.Render(fmt.Sprintf("◎ goal paused after %d rounds — /goal resume to continue, /goal clear to drop", m.goalRounds)))
		m.tp.goalState = "paused"
		return m, nil
	}
	m.goalRounds++
	return m.submitGoal(goalloop.RejectedPrompt(m.goal, msg.verdict.Reason))
}

// drainQueueAfterGoalCheck starts what the user queued while the goal check ran.
func (m *model) drainQueueAfterGoalCheck() (tea.Model, tea.Cmd) {
	for len(m.queue) > 0 {
		next := m.queue[0]
		if strings.HasPrefix(next, "!") {
			m.queue = m.queue[1:]
			m.queueSel = -1
			m.runShellQueued(next)
			continue
		}
		return m.drainQueueHead()
	}
	return m, nil
}
