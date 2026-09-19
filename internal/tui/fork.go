package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

type namePrompt struct {
	label string
	draft string
	mask  bool
	onOK  func(string)
}

func (m *model) openNamePrompt(label, value string, onOK func(string)) {
	m.namePrompt = &namePrompt{label: label, draft: m.input.Value(), onOK: onOK}
	m.input.SetValue(value)
	m.input.CursorEnd()
	m.growInput()
}

func (m *model) closeNamePrompt() {
	m.input.SetValue(m.namePrompt.draft)
	m.input.CursorEnd()
	m.namePrompt = nil
	m.growInput()
}

func (p *namePrompt) maskedValue(v string) string {
	if !p.mask {
		return v
	}
	return strings.Repeat("•", len([]rune(v)))
}

func (m *model) forkCommand(arg string) {
	if m.store == nil {
		m.append(errStyle.Render("no session store"))
		return
	}
	if arg != "" {
		m.fork(len(m.agent.Messages), arg)
		return
	}

	suggest := "session (fork #1)"
	if m.sessionID != "" {
		if meta, _, err := m.store.Load(m.sessionID); err == nil {
			if t, err := m.store.ForkTitle(meta.Title); err == nil {
				suggest = t
			}
		}
	}
	m.openForkPrompt(len(m.agent.Messages), false, suggest)
}

func (m *model) forksCommand() {
	if m.store == nil || m.sessionID == "" {
		m.append(dimStyle.Render("(no session tree yet)"))
		return
	}
	meta, _, err := m.store.Load(m.sessionID)
	if err != nil {
		m.append(errStyle.Render("fork tree failed: " + err.Error()))
		return
	}
	children, err := m.store.ForksOf(m.sessionID)
	if err != nil {
		m.append(errStyle.Render("fork tree failed: " + err.Error()))
		return
	}
	var b strings.Builder
	b.WriteString("session tree\n")
	b.WriteString("└─ " + meta.ID + "  " + meta.Title)
	if meta.ForkedFrom != "" {
		b.WriteString("  (from " + meta.ForkedFrom + ")")
	}
	for _, child := range children {
		b.WriteString("\n   ├─ " + child.ID + "  " + child.Title)
	}
	if len(children) == 0 {
		b.WriteString("\n   (no forks)")
	}
	m.append(dimStyle.Render(b.String()))
}

func (m *model) openForkPrompt(cut int, picker bool, suggest ...string) {
	name := ""
	if len(suggest) > 0 {
		name = suggest[0]
	}
	m.openNamePrompt("⑂ fork name:", name, func(title string) {
		m.fork(cut, title)
	})
	if picker {
		m.append(dimStyle.Render("⑂ forking from the selected message — name the copy (enter) or esc"))
	}
}

func (m *model) fork(cut int, title string) {
	if m.busy {
		m.busyFork(title)
		return
	}
	title = strings.TrimSpace(title)
	if title == "" {
		m.append(errStyle.Render("fork needs a name"))
		return
	}
	if len(m.agent.Messages)+len(m.future) <= 1 {
		m.append(dimStyle.Render("(nothing to fork yet)"))
		return
	}

	if len(m.future) > 0 {
		if cut+1 <= len(m.agent.Messages) {
			m.future = nil
		} else {
			if _, ok := m.applyRewind(cut + 1); !ok {
				return
			}
		}
	}
	if !m.persist() {
		return
	}
	if m.sessionID == "" {
		return
	}
	cut = min(max(cut, 0), len(m.agent.Messages)-1)
	oldID := m.sessionID
	oldTitle := oldID
	if meta, _, err := m.store.Load(oldID); err == nil && meta.Title != "" {
		oldTitle = meta.Title
	}
	boundary, err := m.history.Boundary(cut + 1)
	if err != nil {
		m.append(errStyle.Render("fork failed: " + err.Error()))
		return
	}
	newID, err := m.store.Fork(oldID, boundary-1, title)
	if err != nil {
		m.append(errStyle.Render("fork failed: " + err.Error()))
		return
	}
	m.sessionID = newID
	m.agent.Tasks().SetSessionID(newID)
	m.agent.SetSessionID(newID)
	if err := m.loadHistory(newID); err != nil {
		m.append(errStyle.Render("fork reload failed: " + err.Error()))
		return
	}
	m.sessTitle, m.titled = title, true
	m.future = nil
	m.rebuildTranscript()
	m.append(dimStyle.Render(fmt.Sprintf("⑂ forked %q → %q (%s) — the original is under /resume", oldTitle, title, newID)))
}

func (m *model) busyFork(title string) {
	title = strings.TrimSpace(title)
	if title == "" {
		m.append(errStyle.Render("fork needs a name"))
		return
	}
	if m.sessionID == "" {
		m.append(dimStyle.Render("(nothing to fork yet — the first turn hasn't been saved; /fork again after this turn)"))
		return
	}
	if m.pendingForkID != "" {
		m.append(dimStyle.Render("(already forked — switching to the copy when this turn ends)"))
		return
	}
	oldTitle := m.sessionID
	meta, msgs, err := m.store.Load(m.sessionID)
	if err != nil {
		m.append(errStyle.Render("fork failed: " + err.Error()))
		return
	}
	if len(msgs) == 0 {
		m.append(dimStyle.Render("(nothing to fork yet — the first turn hasn't been saved; /fork again after this turn)"))
		return
	}
	if meta.Title != "" {
		oldTitle = meta.Title
	}

	newID, err := m.store.Fork(m.sessionID, 1<<30, title)
	if err != nil {
		m.append(errStyle.Render("fork failed: " + err.Error()))
		return
	}
	m.pendingForkID = newID
	m.append(dimStyle.Render(fmt.Sprintf("⑂ forked %q → %q (%s) — open it in another session now: kn --resume %s · k-brain switches to the copy when this turn ends", oldTitle, title, newID, newID)))
}

func (m *model) switchToForked(id string) {
	meta, msgs, err := m.store.Load(id)
	if err != nil {
		m.append(errStyle.Render("fork switch failed: " + err.Error()))
		return
	}
	m.flushThink()
	m.flushCurrent()
	m.queue, m.queueSel = nil, -1
	m.future = nil
	oldID := m.sessionID

	if ag, mn, pn, err := buildAgent(m.cfg, meta.Model, meta.Provider, m.sysPrompt); err == nil {
		m.agent, m.modelName, m.provName = ag, mn, pn
	} else {
		m.agent = agent.New(m.agent.Client, m.agent.Model, m.agent.MaxTokens, m.sysPrompt, agent.WithExperimental(m.agent.Experimental()))
		m.agent.ModelName, m.agent.Provider = m.modelName, m.provName
		m.agent.Vision = m.supportsVision()
		m.agent.ContextLimit = m.contextLimitFor(m.provName, m.agent.Model)
	}
	m.applyCompactModel()
	m.applyTaskModel()
	m.agent.CompactThreshold = compactThresholdFor(m.cfg)
	m.wireTasks()

	if meta.Effort != "" && slices.Contains(m.effortsFor(), meta.Effort) {
		m.agent.Effort = meta.Effort
	}
	m.agent.RestoreUsage(meta.UsageSummary(msgs))
	m.sessionID = meta.ID
	m.sessTitle = meta.Title
	bashrun.SetMarkers(meta.ID, m.agent.Model)
	if err := m.loadHistory(meta.ID); err != nil {
		m.append(errStyle.Render("fork reload failed: " + err.Error()))
		return
	}
	m.agent.Tasks().SetSessionID(meta.ID)
	m.agent.SetSessionID(meta.ID)
	m.agent.LoadTodosJSON(m.store.Todos(meta.ID))
	m.goal = meta.Goal
	m.goalRounds = 0
	m.titled = true
	m.rebuildTranscript()
	m.append(dimStyle.Render(fmt.Sprintf("⑂ switched to the fork %q (%s) — the original %s kept the finished turn and is under /resume", meta.Title, meta.ID, oldID)))
}

func (m *model) renameCommand(arg string) {
	if m.store == nil {
		m.append(errStyle.Render("no session store"))
		return
	}
	if arg != "" {
		m.rename(arg)
		return
	}
	cur := ""
	if m.sessionID != "" {
		if meta, _, err := m.store.Load(m.sessionID); err == nil {
			cur = meta.Title
		}
	}
	m.openNamePrompt("✎ session name:", cur, m.rename)
}

func (m *model) rename(title string) {
	title = strings.TrimSpace(title)
	if title == "" {
		m.append(errStyle.Render("rename needs a title"))
		return
	}
	m.persist()
	if m.sessionID == "" {
		return
	}
	if err := m.store.SetTitle(m.sessionID, title); err != nil {
		m.append(errStyle.Render("rename failed: " + err.Error()))
		return
	}
	m.sessTitle = title
	m.titled = true
	m.append(dimStyle.Render("✎ session renamed: " + title))
}
