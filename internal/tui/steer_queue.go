package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/skills"
)

// Messages typed while a turn runs wait in the queue, where they can still be
// edited, moved or removed, until the round of tool calls running ends. Then
// the turn takes them: the model reads them right after the tool results,
// in the same turn, instead of after the turn ends. Slash commands stay
// queued for the end of the turn, and a turn that answers without calling
// another tool leaves the queue to be sent as the next turn, as before.

// steerPointWait bounds how long a turn waits for the UI to hand over its
// queue at a tool boundary. The UI answers within a frame; a UI held up (an
// external editor open) only delays the hand-over to the next boundary.
const steerPointWait = time.Second

// steerPointMsg asks the UI to hand the queue to the running turn; done
// closes once it has.
type steerPointMsg struct{ done chan struct{} }

// steerEcho is a queued message handed to the turn: shown is what the user
// typed, sent what the turn got (mentions, skills and images expanded).
type steerEcho struct{ shown, sent string }

// steerPoint is the turn's AtToolBoundary: when anything is queued it asks the
// UI for it and waits until the UI has steered it in, the turn is cancelled,
// or steerPointWait passes. It never waits when the queue is empty.
func steerPoint(ctx context.Context, queued *atomic.Bool, send func(tea.Msg)) func() {
	return func() {
		if !queued.Load() {
			return
		}
		done := make(chan struct{})
		send(steerPointMsg{done: done})
		timer := time.NewTimer(steerPointWait)
		defer timer.Stop()
		select {
		case <-done:
		case <-ctx.Done():
		case <-timer.C:
		}
	}
}

func (m *model) steerQueuedFlag() *atomic.Bool {
	if m.steerFlag == nil {
		m.steerFlag = new(atomic.Bool)
	}
	return m.steerFlag
}

// steerableQueued reports a queued message the running turn could take.
func (m *model) steerableQueued() bool {
	for _, q := range m.queue {
		if steerable(q) {
			return true
		}
	}
	return false
}

func steerable(text string) bool { return !strings.HasPrefix(text, "/") }

// steerQueued hands every queued message but slash commands to the running
// turn, in queue order.
func (m *model) steerQueued() {
	if !m.busy || m.agent == nil || !m.agent.TurnRunning() {
		return
	}
	kept := make([]string, 0, len(m.queue))
	for _, q := range m.queue {
		if !steerable(q) {
			kept = append(kept, q)
			continue
		}
		sent, parts := m.steerPrepared(q)
		m.steering = append(m.steering, steerEcho{shown: q, sent: sent})
		if len(parts) > 0 {
			m.agent.SteerImages(sent, parts)
		} else {
			m.agent.Steer(sent)
		}
	}
	if len(kept) != len(m.queue) {
		m.queue = kept
		m.queueSel = -1
	}
}

// steerPrepared expands a message for the running turn the way a new turn's
// prompt is expanded (image chips, skills, @mentions), leaving the system
// prompt alone while the turn uses it.
func (m *model) steerPrepared(text string) (string, []ai.ContentPart) {
	text = m.expandImageChips(text)
	expanded := expandMentions(expandSkills(text, skills.Scan(skills.DefaultDirs()...)))
	if !m.supportsVision() {
		return expanded, nil
	}
	parts, note := imageParts(text)
	return expanded + note, parts
}

// takeSteering finds the queued message the turn sent as sent, forgets it and
// returns what the user typed.
func (m *model) takeSteering(sent string) (string, bool) {
	for i, s := range m.steering {
		if s.sent == sent {
			m.steering = append(m.steering[:i:i], m.steering[i+1:]...)
			return s.shown, true
		}
	}
	return "", false
}
