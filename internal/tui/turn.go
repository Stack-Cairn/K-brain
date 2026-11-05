package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/prompts"
	"github.com/Stack-Cairn/K-brain/internal/routing"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/skills"
	tea "github.com/charmbracelet/bubbletea"
)

func (m *model) busyStats() string {
	if m.turnStart.IsZero() {
		return ""
	}
	d := max(m.nowFn().Sub(m.turnStart), 0)
	elapsed := d.Round(time.Second)
	// Spend and context live in the info row; the busy row only says how long.
	return fmt.Sprintf(" %d:%02d", int(elapsed.Minutes()), int(elapsed.Seconds())%60)
}

func (m *model) prepareTurn(text string) (string, []ai.ContentPart) {
	if m.promptManaged && m.agent != nil {
		m.sysPrompt = prompts.Build(m.agent.WorkingDir, time.Now())
	}
	sk := skills.Scan(skills.DefaultDirs()...)
	sys := m.sysPrompt + skills.PromptBlock(sk)
	if m.pluginMgr != nil {
		sys += m.pluginMgr.PromptBlock()
	}
	if m.mcpMgr != nil {
		sys += m.mcpMgr.InstructionsBlock()
	}
	m.agent.Messages[0].Content = sys
	m.agent.RefreshMemory()

	text = m.expandImageChips(text)
	expanded := expandMentions(expandSkills(text, sk))
	if !m.supportsVision() {

		return expanded, nil
	}
	parts, withNote := imageParts(text)
	return expanded + withNote, parts
}

func (m *model) supportsVision() bool {
	return modelSupportsVision(m.cfg, m.modelName, m.agent.Model, m.catalogs, m.provName)
}

func modelSupportsVision(cfg *config.Config, modelName, modelID string, catalogs map[string]config.Catalog, provName string) bool {
	return routing.SupportsVision(cfg, modelName, modelID, catalogs, provName)
}

func (m *model) drainQueueHead() (tea.Model, tea.Cmd) {
	next := m.queue[0]
	m.queue = m.queue[1:]
	m.queueSel = -1
	m.hist = append(m.hist, next)
	m.histIdx = len(m.hist)
	return m.submit(next)
}

func (m *model) submit(text string) (tea.Model, tea.Cmd) {
	return m.submitTurn(text, true)
}

func (m *model) submitGoal(text string) (tea.Model, tea.Cmd) {
	return m.submitTurn(text, false)
}

func (m *model) submitTurn(text string, authored bool) (tea.Model, tea.Cmd) {
	return m.submitTurnAs(text, authored, false)
}

// submitNotice runs a turn for a runtime notice (a background subagent, wait or
// workflow reporting back): it shows as a status row and reaches the model as
// a background message, not as something the user wrote.
func (m *model) submitNotice(text string) (tea.Model, tea.Cmd) {
	return m.submitTurnAs(text, false, true)
}

func (m *model) submitTurnAs(text string, authored, notice bool) (tea.Model, tea.Cmd) {
	m.cancelGoalFromContext()
	m.prepareHistory()
	m.turnSnapshotSeq = nil
	m.busy = true
	m.turnStart = m.nowFn()
	prepared, parts := m.prepareTurn(text)
	if notice {
		prepared, parts = text, nil
	}
	userMsgIdx := len(m.agent.Messages)

	preSnap := snapshotWorkspace()

	rewoundFrom := ""
	if authored && len(m.future) > 0 {
		for _, fm := range m.future {
			if fm.Role == "user" && fm.Authored {
				rewoundFrom = oneLine(fm.Content)
				break
			}
		}
	}
	m.discardFuture()

	if authored && m.agent != nil {
		var keep []string
		if m.taskVP != nil {
			keep = append(keep, m.taskVP.id)
		}
		m.agent.Tasks().ClearSettled(keep...)
		m.fgReg().clearSettled(keep...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = sandbox.WithPolicy(ctx, m.sandboxPolicy)
	ctx = m.withAIApproval(ctx, text)
	fg := m.fgReg()
	ctx = agent.WithRequestObserver(ctx, fg)
	if notice {
		ctx = agent.WithBackgroundNotice(ctx)
	}
	m.cancel = cancel
	p := m.prog

	stream := newTurnStream(func(msg tea.Msg) {
		if p != nil {
			p.Send(msg)
		}
	})
	send := stream.emit

	go func() {
		var compactTook, compactKept int
		var compactBefore []ai.Message
		turnAt := userMsgIdx
		events := agent.Events{
			OnText:  stream.text,
			OnThink: stream.think,
			OnToolStart: func(id, n, a string) {
				if n == "subagent" && !subagentBackgroundArg(a) {
					fg.expect(id, toolSubject("subagent", a))
				}
				send(toolStartMsg{id, n, a})
			},
			OnToolEnd: func(id, n, r string) {
				if n == "subagent" {
					fg.forget(id)
				}
				send(toolEndMsg{id, n, r})
			},

			OnToolCall: func(id, n, a string) { send(toolCallMsg{id, n, a}) },

			OnToolOutput: func(id, soFar string) { send(toolOutputMsg{id, soFar}) },
			OnSteer: func(s string) {
				send(steeredMsg(s))
			},
			OnCompactStart: func(took, est int) {
				if rewoundFrom != "" {
					m.agent.SetMessageRewoundFrom(turnAt, rewoundFrom)
				}
				compactBefore = m.agent.MessagesSnapshot()
				send(compactStartMsg{took: took, est: est})
			},
			OnNewContextStart: func(took, est int) {
				compactBefore = m.agent.MessagesSnapshot()
				send(contextStartMsg{took: took, est: est})
			},

			OnCompact: func(took, kept int) { compactTook, compactKept = took, kept },
			OnCompacted: func(sum string, cutoff int, info agent.CompactInfo) {
				at := turnAt
				send(compactMsg{took: compactTook, kept: compactKept, summary: sum, cutoff: cutoff, info: info,
					before: compactBefore, after: m.agent.MessagesSnapshot(), turnAt: &at})
				if turnAt >= cutoff {
					turnAt = 2 + turnAt - cutoff
				} else {
					turnAt = -1
				}
			},
			OnNewContext: func(cutoff int) {
				send(compactMsg{took: len(compactBefore), kept: len(m.agent.MessagesSnapshot()), cutoff: cutoff,
					before: compactBefore, after: m.agent.MessagesSnapshot(), fresh: true})
				turnAt = -1
			},
			OnUsage: func(u ai.Usage) { send(usageMsg(u)) },

			OnRetry: func(ev ai.RetryEvent) {
				send(noticeMsg(fmt.Sprintf("⚠ request failed (%s) — retrying in %s (attempt %d/%d)",
					ev.Err, ev.Delay.Round(time.Millisecond), ev.Attempt+1, ev.Max)))
			},
		}
		m.toolViewEvents(&events, send)
		var final string
		var err error
		switch {
		case len(parts) > 0:
			final, err = m.agent.TurnWithImages(ctx, prepared, parts, events)
		case authored:
			final, err = m.agent.TurnAuthored(ctx, prepared, events)
		default:
			final, err = m.agent.Turn(ctx, prepared, events)
		}
		if rewoundFrom != "" {
			m.agent.SetMessageRewoundFrom(turnAt, rewoundFrom)
		}
		stream.finish(turnDoneMsg{final: final, stopReason: m.agent.LastStopReason(), err: err, at: turnAt, snap: preSnap, clean: workspaceClean()})
	}()
	if notice {
		m.appendNotice(text)
	} else {
		m.appendRaw(blockUser, linkifyFilePaths(text, realFileExists))
	}
	if authored {

		for len(m.msgBlock) <= userMsgIdx {
			m.msgBlock = append(m.msgBlock, -1)
		}
		m.msgBlock[userMsgIdx] = len(m.blocks) - 1
	}
	return m, m.spin.Tick
}
