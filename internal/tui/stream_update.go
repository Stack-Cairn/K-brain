package tui

// The stream and tool cases of Update. Each message the running turn sends
// has one handler here; Update only dispatches to them. Text and thinking come
// first, then the turn lifecycle, then the tool rows.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	tea "github.com/charmbracelet/bubbletea"
)

// onText commits streamed reply text up to its last newline; the rest
// stays in the live area until more arrives.
func (m *model) onText(msg textMsg) (tea.Model, tea.Cmd) {
	m.flushThink()
	m.current += string(msg)

	if i := strings.LastIndexByte(m.current, '\n'); i >= 0 {
		done := m.current[:i]
		m.current = m.current[i+1:]
		m.appendAssistant(done)
	}
	return m, nil
}

// onThink does the same for reasoning text while thinking is shown.
func (m *model) onThink(msg thinkMsg) (tea.Model, tea.Cmd) {
	if m.showThinking {
		m.flushCurrent()
		m.streamThink(string(msg))
	}
	return m, nil
}

// onSteered shows a message the running turn picked up mid-stream.
func (m *model) onSteered(msg steeredMsg) (tea.Model, tea.Cmd) {
	m.flushThink()
	m.flushCurrent()
	if _, ok := parseNotice(string(msg)); ok {
		m.appendNotice(string(msg))
		return m, nil
	}
	m.append(youStyle.Render(glyphUser) + linkifyFilePaths(string(msg), realFileExists) + dimStyle.Render("  (steered)"))
	return m, nil
}

func (m *model) onCompactStart(msg compactStartMsg) (tea.Model, tea.Cmd) {
	m.flushThink()
	m.flushCurrent()
	m.append(dimStyle.Render(fmt.Sprintf("◎ compacting %d msgs (est. %s) with %s…",
		msg.took, fmtTok(msg.est), m.compactModelLabel())))
	return m, nil
}

func (m *model) onContextStart(msg contextStartMsg) (tea.Model, tea.Cmd) {
	m.flushThink()
	m.flushCurrent()
	m.append(dimStyle.Render(fmt.Sprintf("◎ starting new context window after %d msgs (est. %s)…", msg.took, fmtTok(msg.est))))
	return m, nil
}

func (m *model) onCompact(msg compactMsg) (tea.Model, tea.Cmd) {
	m.flushThink()
	m.flushCurrent()
	switch {
	case msg.err != nil:
		m.append(errStyle.Render("compact failed: " + msg.err.Error()))
	case msg.summary == "" && !msg.fresh:

	default:
		if m.store == nil {
			snapshots := map[int]string{}
			for index, ref := range m.snapshots {
				if index >= msg.cutoff {
					snapshots[2+index-msg.cutoff] = ref
				}
			}
			m.snapshots = snapshots
		}
		if m.store != nil && msg.before != nil {
			m.historyEvents = append(m.historyEvents, msg)
			if err := m.saveHistory(msg.after); err != nil {
				m.append(errStyle.Render("compaction save failed: " + err.Error()))
			} else {
				msg.preserved = true
			}
		}
		m.future = nil
		m.msgBlock = nil
		m.append(m.compactResultLine(msg))
	}
	return m, nil
}

// onTurnDone settles a finished turn: errors, persistence, the queue and
// the next goal round.
func (m *model) onTurnDone(msg turnDoneMsg) (tea.Model, tea.Cmd) {
	m.flushThink()
	m.flushCurrent()
	m.clearQueuedTools()
	m.settleRunningTools()
	m.busy = false
	if m.memoryPending != nil {
		m.applyMemorySwitch(*m.memoryPending)
	}
	m.cancel = nil
	m.interrupt1 = false

	m.turnStart = time.Time{}

	canceled := errors.Is(msg.err, context.Canceled)
	if msg.err != nil && !canceled {
		m.append(errStyle.Render("error: " + msg.err.Error()))
	} else if canceled {
		m.append(dimStyle.Render("(interrupted — any running tool calls will be recorded as interrupted; k-brain can retry them next turn)"))
	}
	m.persist()
	m.maybeTitle()
	m.recordTurnSnapshot(msg)

	if m.pendingForkID != "" {
		m.switchToForked(m.pendingForkID)
		m.pendingForkID = ""
		return m, nil
	}

	for len(m.queue) > 0 && (msg.err == nil || canceled) {
		next := m.queue[0]
		if strings.HasPrefix(next, "!") {
			m.queue = m.queue[1:]
			m.queueSel = -1
			m.runShellQueued(next)
			continue
		}
		return m.drainQueueHead()
	}

	if m.goal != "" && msg.err == nil {
		if msg.stopReason != ai.StopReasonLength && goalMet(msg.final) {
			m.append(dimStyle.Render("◎ goal met after " + strconv.Itoa(m.goalRounds) + " round(s)"))
			m.setGoal("")
			return m, nil
		}
		if msg.stopReason != ai.StopReasonLength && goalNeedsInput(msg.final) {
			// Another round cannot get the answer; the user's reply is the next turn,
			// and the goal check runs again after it.
			m.append(dimStyle.Render("◎ goal waiting for your reply — answer to continue, /goal clear to drop"))
			return m, nil
		}
		if m.goalRounds >= m.goalMaxRounds() {
			m.append(errStyle.Render(fmt.Sprintf("◎ goal paused after %d rounds — /goal resume to continue, /goal clear to drop", m.goalRounds)))
			return m, nil
		}
		m.goalRounds++
		return m.submitGoal(goalContinuePrompt(m.goal))
	}
	return m, nil
}

// onToolCall adds or refreshes the queued row of a tool call the model has
// announced but not started.
func (m *model) onToolCall(msg toolCallMsg) (tea.Model, tea.Cmd) {
	if hiddenToolCall(msg.name) {
		return m, nil
	}
	m.flushThink()
	m.flushCurrent()
	row := dimStyle.Render("⋯ " + msg.name + m.batchSuffix(msg.name, msg.id) + " " + queuedSubject(msg.name, msg.args))

	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockToolQueued && m.blocks[i].toolID == msg.id {
			m.blocks[i].text, m.blocks[i].stale = row, true
			m.refreshVP()
			return m, nil
		}
	}
	m.blocks = append(m.blocks, block{kind: blockToolQueued, text: row, toolID: msg.id, toolName: msg.name, toolArgs: msg.args})

	for i := range m.blocks {
		if b := &m.blocks[i]; b.kind == blockToolQueued && b.toolName == msg.name && b.toolID != msg.id {
			b.text = dimStyle.Render("⋯ " + b.toolName + m.batchSuffix(b.toolName, b.toolID) + " " + queuedSubject(b.toolName, b.toolArgs))

			b.stale = true
		}
	}
	m.refreshVP()
	return m, nil
}

// onToolStart turns a queued row into a running one, in place.
func (m *model) onToolStart(msg toolStartMsg) (tea.Model, tea.Cmd) {
	if hiddenToolCall(msg.name) {
		return m, nil
	}
	m.flushThink()
	m.flushCurrent()

	suffix := m.batchSuffix(msg.name, msg.id)

	// The running row takes its queued row's place, so a batch keeps its order.
	at := -1
	for i := range slices.Backward(m.blocks) {
		if m.blocks[i].kind == blockToolQueued && m.blocks[i].toolID == msg.id {
			m.blocks = slices.Delete(m.blocks, i, i+1)
			at = i
			break
		}
	}
	args := msg.args
	switch msg.name {
	case "browser_exec", "computer_exec":

		if label := browserStepLabel(msg.args); label != "" {
			args = label
		}
	case "subagent":

		args = toolSubject("subagent", msg.args)
	}

	row := toolStyle.Render("⚒ "+toolVerb(msg.name)+suffix+" ") + dimStyle.Render(args)

	run := block{kind: blockToolRun, text: row, toolID: msg.id, toolRunning: true, toolName: msg.name, toolArgs: msg.args}
	if at >= 0 {
		m.blocks = slices.Insert(m.blocks, at, run)
	} else {
		m.blocks = append(m.blocks, run)
	}
	m.refreshVP()
	if msg.name == "subagent" {
		return m, nil
	}
	toolTimer.start(msg.id)
	return m, m.ensureToolTick()
}

// onToolOutput shows the last lines of a running tool's output.
func (m *model) onToolOutput(msg toolOutputMsg) (tea.Model, tea.Cmd) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		b := &m.blocks[i]
		if b.kind == blockToolRun && b.toolRunning && b.toolID == msg.id {
			if tail := lastLines(msg.text, 3); tail != "" {
				b.live = tail
				b.stale = true
				m.refreshVP()
			}
			break
		}
	}
	return m, nil
}

// onToolEnd closes the running row and inserts the result block under it.
func (m *model) onToolEnd(msg toolEndMsg) (tea.Model, tea.Cmd) {
	if hiddenToolCall(msg.name) {
		if !toolResultFailed(msg.result) {
			return m, nil
		}
		// A plan the agent could not record is worth seeing: its call shows
		// with the error, as any failed call does.
		m.flushThink()
		m.flushCurrent()
		m.blocks = append(m.blocks, block{kind: blockToolRun, text: toolHeaderRow(msg.name, "", true), toolID: msg.id, toolName: msg.name, toolFailed: true})
	}
	hdr := -1
	for i := len(m.blocks) - 1; i >= 0; i-- {
		b := &m.blocks[i]
		if b.kind == blockToolRun && b.toolRunning && b.toolID == msg.id {
			b.toolRunning = false
			b.toolFailed = toolResultFailed(msg.result)
			b.live = ""

			b.text = toolHeaderRow(msg.name, b.toolArgs, b.toolFailed)
			b.stale = true
			hdr = i
			break
		}
	}

	result := block{kind: blockTool, text: msg.result, toolName: msg.name}
	if msg.name == "subagent" {
		if id := backgroundSpawnID(msg.result); id != "" {
			// The spawn result is written for the model; the row shows the
			// task's live state instead.
			result = block{kind: blockText, subID: id, subLine: true}
			if t, ok := m.subTask(id); ok {
				result.text = m.subagentRowText(t, true)
			}
		}
	}
	if hdr >= 0 && result.kind == blockTool {
		linkToolResult(&m.blocks[hdr], &result)
	}
	if hdr >= 0 && hdr+1 < len(m.blocks) {
		m.blocks = append(m.blocks[:hdr+1], append([]block{result}, m.blocks[hdr+1:]...)...)

		for i := range m.msgBlock {
			if m.msgBlock[i] > hdr {
				m.msgBlock[i]++
			}
		}
	} else {
		m.blocks = append(m.blocks, result)
	}
	m.keepFollow()
	if msg.name == "subagent" {
		m.refreshSubagentRows()
		m.refreshVP()
		return m, m.ensureSubTick()
	}
	if groupableTool(msg.name) {
		m.groupTrailingTools()
	}
	m.refreshVP()
	return m, nil
}
