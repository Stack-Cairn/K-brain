package tui

import (
	"fmt"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

func (m *model) ensureHistory(msgs []ai.Message) error {
	if m.history != nil && m.historyID == m.sessionID {
		return nil
	}
	if len(msgs) == 0 {
		return fmt.Errorf("session has no system prompt")
	}
	if m.sessionID == "" {
		id, err := m.store.Create(cwd(), m.modelName, m.provName)
		if err != nil {
			return err
		}
		m.sessionID = id
		if !m.busy {
			m.bindSessionIdentity()
		}
	}
	h, _, err := m.store.History(m.sessionID, msgs[:1])
	if err != nil {
		return err
	}
	m.history, m.historyID = h, m.sessionID
	return nil
}

func (m *model) bindSessionIdentity() {
	if m.agent.SessionIDValue() == m.sessionID {
		return
	}
	bashrun.SetMarkers(m.sessionID, m.agent.Model)
	m.agent.Tasks().SetSessionID(m.sessionID)
	m.agent.SetSessionID(m.sessionID)
}

func (m *model) prepareHistory() {
	if m.store == nil {
		return
	}
	if err := m.ensureHistory(m.agent.MessagesSnapshot()); err != nil {
		m.append(errStyle.Render("session preparation failed: " + err.Error()))
		return
	}
	m.bindSessionIdentity()
}

func (m *model) saveHistory(msgs []ai.Message) error {
	if m.historyErr != nil {
		return m.historyErr
	}
	for len(m.historyEvents) > 0 {
		event := m.historyEvents[0]
		if err := m.ensureHistory(event.before); err != nil {
			return err
		}
		if err := m.history.Observe(event.before); err != nil {
			m.historyErr = err
			return err
		}
		if event.turnAt != nil && m.turnSnapshotSeq == nil {
			if seq, ok := m.history.Sequence(*event.turnAt); ok {
				m.turnSnapshotSeq = &seq
			}
		}
		if err := m.history.Compact(event.summary, event.cutoff, event.info.Model, event.info.Usage); err != nil {
			m.historyErr = err
			return err
		}
		m.historyEvents = m.historyEvents[1:]
	}
	if err := m.ensureHistory(msgs); err != nil {
		return err
	}
	if err := m.store.SaveHistoryWithUsage(m.sessionID, m.history, msgs, m.modelName, m.provName, m.agent.UsageSummary()); err != nil {
		return err
	}
	m.saved = len(msgs)
	return nil
}

func (m *model) loadHistory(id string) error {
	h, msgs, err := m.store.History(id, m.agent.Messages[:1])
	if err != nil {
		return err
	}
	m.history, m.historyID = h, id
	m.historyEvents, m.historyErr = nil, nil
	m.turnSnapshotSeq = nil
	m.agent.Messages = msgs
	m.snapshots = m.store.Snapshots(id)
	m.saved = len(msgs)
	return nil
}

func (m *model) resetHistory() {
	m.history, m.historyID = nil, ""
	m.historyEvents, m.historyErr = nil, nil
	m.turnSnapshotSeq = nil
	m.snapshots = nil
}

func (m *model) snapshotSequence(index int) (int, bool) {
	if m.turnSnapshotSeq != nil {
		return *m.turnSnapshotSeq, true
	}
	if m.store != nil {
		if m.history == nil {
			return 0, false
		}
		return m.history.Sequence(index)
	}
	return index, index > 0
}

func (m *model) recordTurnSnapshot(msg turnDoneMsg) {
	if msg.snap == "" {
		return
	}
	if msg.clean {
		dropSnapshot(msg.snap)
		return
	}
	seq, ok := m.snapshotSequence(msg.at)
	if !ok {
		return
	}
	if m.snapshots == nil {
		m.snapshots = map[int]string{}
	}
	m.snapshots[seq] = msg.snap
	if m.store != nil && m.sessionID != "" {
		if err := m.store.SetSnapshot(m.sessionID, seq, msg.snap); err != nil {
			m.append(errStyle.Render("snapshot save failed: " + err.Error()))
		}
	}
}
