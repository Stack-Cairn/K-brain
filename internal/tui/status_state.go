package tui

// The status area: what the turn-status row, the footer, the window title and
// desktop notifications know about the running turn. Its state lives behind
// model.st; Update calls statusBefore ahead of its own switch and statusAfter
// once it is done, so the area sees every message without the dispatch code
// knowing what it does with them.

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// Turn phases, as the status row names them.
const (
	phasePreparing = "preparing"
	phaseWaiting   = "waiting_model"
	phaseThinking  = "thinking"
	phaseWriting   = "writing"
	phaseTools     = "tools"
)

type (
	// phaseMsg is the agent's OnPhase stage.
	phaseMsg string
	// retryMsg is one provider retry the client is about to make.
	retryMsg ai.RetryEvent
	// toolOutcomeMsg is a finished tool call's structured outcome.
	toolOutcomeMsg struct {
		id     string
		failed bool
	}
)

// retryState is the provider retry in progress: the client sleeps for delay
// after at, then sends attempt (1-based) of max.
type retryState struct {
	attempt, max int
	delay        time.Duration
	at           time.Time
	reason       string
}

type statusState struct {
	// The running turn. turnStart mirrors model.turnStart: a change means a
	// new turn began, which resets the counters below.
	turnStart  time.Time
	phase      string
	phaseSince time.Time
	outTok     int // completion tokens of the calls that finished this turn
	streamed   int // bytes streamed since the last usage report
	tools      int
	toolFails  int
	costStart  float64
	costKnown  bool
	retry      *retryState

	compacting   bool
	compactSince time.Time
	compactTook  time.Duration // the last compaction's duration, for its result line

	// Edges seen by statusAfter.
	wasBusy        bool
	hadPerm        bool
	hadAsk         bool
	lastTurnFailed bool

	note  notifier
	usage *usageCard
	line  statusLine
	sched scheduleCache
}

// status is the area state, created on first use so models built by hand in
// tests need no setup.
func (m *model) status() *statusState {
	if m.st == nil {
		m.st = &statusState{}
	}
	return m.st
}

// statusEvents routes the agent callbacks the status row reads into the
// program, keeping whatever handlers ev already had.
func statusEvents(ev agent.Events, send func(tea.Msg)) agent.Events {
	prevPhase, prevRetry, prevResult := ev.OnPhase, ev.OnRetry, ev.OnToolResult
	ev.OnPhase = func(stage string) {
		if prevPhase != nil {
			prevPhase(stage)
		}
		send(phaseMsg(stage))
	}
	ev.OnRetry = func(e ai.RetryEvent) {
		if prevRetry != nil {
			prevRetry(e)
		}
		send(retryMsg(e))
	}
	ev.OnToolResult = func(id, name string, r tools.Result) {
		if prevResult != nil {
			prevResult(id, name, r)
		}
		failed := r.Failed || r.TimedOut || r.ExitCode != nil && *r.ExitCode != 0
		send(toolOutcomeMsg{id: id, failed: failed})
	}
	return ev
}

// statusBefore runs ahead of Update's switch. It records what the status
// area needs from the stream and consumes the messages that are its own.
func (m *model) statusBefore(msg tea.Msg) (tea.Cmd, bool) {
	st := m.status()
	now := m.nowFn()
	switch msg := msg.(type) {
	case tea.FocusMsg:
		st.note.setFocus(true)
		return nil, true
	case tea.BlurMsg:
		st.note.setFocus(false)
		return nil, true
	case phaseMsg:
		st.setPhase(string(msg), now)
		return nil, true
	case retryMsg:
		st.retry = &retryState{attempt: msg.Attempt + 1, max: msg.Max, delay: msg.Delay, at: now, reason: retryReason(msg.Err)}
		return nil, true
	case toolOutcomeMsg:
		if msg.failed {
			st.toolFails++
		}
		return nil, true
	case statusLineMsg:
		st.line.receive(msg, now)
		return nil, true
	case textMsg:
		st.retry = nil
		st.streamed += len(msg)
		st.setPhase(phaseWriting, now)
	case thinkMsg:
		st.retry = nil
		st.streamed += len(msg)
		st.setPhase(phaseThinking, now)
	case toolCallMsg:
		st.retry = nil
	case toolStartMsg:
		st.retry = nil
		st.tools++
		st.setPhase(phaseTools, now)
	case usageMsg:
		st.retry = nil
		st.outTok += msg.CompletionTokens
		st.streamed = 0
	case compactStartMsg, contextStartMsg:
		st.beginCompaction(now)
	case compactMsg:
		st.compactTook = 0
		if st.compacting {
			st.compactTook = now.Sub(st.compactSince)
		}
		st.compacting = false
	case turnDoneMsg:
		st.retry = nil
		st.compacting = false
	}
	return nil, false
}

// statusAfter runs once Update has handled a message. It notices edges — a
// turn starting or ending, a card opening — and returns cmd with whatever the
// status area adds to it.
func (m *model) statusAfter(cmd tea.Cmd) tea.Cmd {
	st := m.status()
	now := m.nowFn()
	if m.busy && !m.turnStart.IsZero() && !m.turnStart.Equal(st.turnStart) {
		st.beginTurn(m, now)
	}

	perm, ask := m.permDialog != nil, m.askDialog != nil
	switch {
	case st.wasBusy && !m.busy:
		m.notifyTurnDone()
	case perm && !st.hadPerm:
		m.notifyApproval()
	case ask && !st.hadAsk:
		m.notifyQuestion()
	}
	st.wasBusy, st.hadPerm, st.hadAsk = m.busy, perm, ask
	if !m.busy {
		st.turnStart = time.Time{}
	}

	st.sched.refresh(m, now)
	if title := m.syncTitle(now); title != nil {
		cmd = tea.Batch(cmd, title)
	}
	if run := m.statusLineCmd(now); run != nil {
		cmd = tea.Batch(cmd, run)
	}
	return cmd
}

func (st *statusState) setPhase(phase string, now time.Time) {
	if st.phase == phase {
		return
	}
	st.phase, st.phaseSince = phase, now
}

func (st *statusState) beginCompaction(now time.Time) {
	st.compacting, st.compactSince = true, now
}

// beginTurn resets the per-turn readouts when a new turn starts.
func (st *statusState) beginTurn(m *model, now time.Time) {
	st.turnStart = m.turnStart
	st.phase, st.phaseSince = phasePreparing, now
	st.outTok, st.streamed, st.tools, st.toolFails = 0, 0, 0, 0
	st.retry = nil
	st.lastTurnFailed = false
	st.costStart, st.costKnown = 0, false
	if m.agent != nil {
		st.costStart, st.costKnown = m.sessionCost()
	}
}

// markCompacting is called when a manual /compact or /new-context starts, which
// reaches the TUI without a compaction-start event.
func (m *model) markCompacting() {
	m.status().beginCompaction(m.nowFn())
}

// turnTokens is the live output-token count: what finished calls reported
// plus an estimate of what is streaming now.
func (st *statusState) turnTokens() int {
	return st.outTok + st.streamed/4
}
