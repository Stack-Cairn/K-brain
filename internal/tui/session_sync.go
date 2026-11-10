package tui

import (
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// kn 与 LiveAgent 的 backend 共用会话目录。回合运行期间 kn 持有会话的运行租约，
// 让 backend（以及另一个 kn）不会同时在同一会话里运行；回合开始时和每轮模型请求之间
// 保存检查点，LiveAgent 的会话列表因此在回合进行中就能看到它。

// runLeaseClient 是其他进程看到的租约持有者名称。
const runLeaseClient = "kn"

// turnCheckpointGap 是回合内两次检查点之间的最短间隔。
const turnCheckpointGap = 2 * time.Second

// turnCheckpointMsg 携带回合中某一轮开始时的消息快照，由 UI 循环写入会话。
type turnCheckpointMsg struct {
	sessionID string
	msgs      []ai.Message
}

// checkpointEvents 在每一轮模型请求开始时（第一轮立即，之后至少间隔 turnCheckpointGap）
// 发送一个消息快照。此时消息列表里没有未完成的工具调用。
func checkpointEvents(ev agent.Events, sessionID string, snapshot func() []ai.Message, send func(tea.Msg), now func() time.Time) agent.Events {
	prev := ev.OnLifecycle
	var last time.Time
	ev.OnLifecycle = func(event string) {
		if prev != nil {
			prev(event)
		}
		if event != "turn_start" {
			return
		}
		at := now()
		if !last.IsZero() && at.Sub(last) < turnCheckpointGap {
			return
		}
		last = at
		send(turnCheckpointMsg{sessionID: sessionID, msgs: snapshot()})
	}
	return ev
}

// onTurnCheckpoint 保存回合进行中的消息。失败只记录日志：回合结束时还会完整保存一次。
func (m *model) onTurnCheckpoint(msg turnCheckpointMsg) (tea.Model, tea.Cmd) {
	if !m.busy || m.store == nil || m.runLease == nil || msg.sessionID == "" || msg.sessionID != m.sessionID || len(m.historyEvents) > 0 {
		return m, nil
	}
	if err := m.saveHistory(msg.msgs); err != nil {
		config.LogEvent("session.checkpoint", "FAILED id="+m.sessionID+": "+err.Error())
	}
	return m, nil
}

// beginSessionRun 在回合开始前取得运行租约，并在会话被别处（LiveAgent）改动过时
// 重新加载，让回合接着磁盘上的记录继续，而不是覆盖它。返回 false 表示不能开始。
func (m *model) beginSessionRun() bool {
	if m.store == nil || m.sessionID == "" {
		return true
	}
	if m.runLease != nil && m.runLease.ID() == m.sessionID {
		return true
	}
	m.endSessionRun()
	lease, err := m.store.AcquireRun(m.sessionID, runLeaseClient)
	if err != nil {
		var elsewhere *session.RunElsewhereError
		if errors.As(err, &elsewhere) {
			m.append(errStyle.Render("this session is running in " + runClientLabel(elsewhere.Run.Client) + " — wait for that turn to finish, or /new to start another session"))
		} else {
			m.append(errStyle.Render("session lock failed: " + err.Error()))
		}
		return false
	}
	m.runLease = lease
	if err := m.reloadIfChanged(); err != nil {
		m.endSessionRun()
		m.append(errStyle.Render("session reload failed: " + err.Error()))
		return false
	}
	return true
}

// endSessionRun 释放运行租约。
func (m *model) endSessionRun() {
	if m.runLease != nil {
		_ = m.runLease.Release()
		m.runLease = nil
	}
}

func runClientLabel(client string) string {
	switch client {
	case "backend":
		return "LiveAgent"
	case "":
		return "another process"
	}
	return client
}

// reloadIfChanged 在另一个进程修改了当前会话的消息后重新加载并重绘对话。
func (m *model) reloadIfChanged() error {
	if m.store == nil || m.history == nil || m.sessionID == "" || m.historyID != m.sessionID {
		return nil
	}
	stale, err := m.store.HistoryStale(m.sessionID, m.history)
	if err != nil || !stale {
		return err
	}
	meta, _, err := m.store.Load(m.sessionID)
	if err != nil {
		return err
	}
	if err := m.loadHistory(m.sessionID); err != nil {
		return err
	}
	msgs := m.agent.Messages[1:]
	m.agent.RestoreUsage(meta.UsageSummary(msgs))
	if meta.Title != "" {
		m.sessTitle = meta.Title
	}
	m.resetTranscript()
	m.future = nil
	m.append(dimStyle.Render("session changed in another window — reloaded " + meta.ID))
	m.seedTranscript(msgs, 1)
	return nil
}

// unchangedHereButElsewhere 报告 kn 没有未保存的消息，而会话正在别处运行或已被别处改写。
func (m *model) unchangedHereButElsewhere(msgs []ai.Message) bool {
	if m.store == nil || m.runLease != nil || m.sessionID == "" || len(m.historyEvents) > 0 || len(msgs) != m.saved {
		return false
	}
	if _, busy := m.store.ExternalRun(m.sessionID); busy {
		return true
	}
	stale, err := m.store.HistoryStale(m.sessionID, m.history)
	return err == nil && stale
}
