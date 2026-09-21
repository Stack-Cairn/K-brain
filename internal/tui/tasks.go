package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type taskEventMsg struct {
	view *taskView
	id   string
	kind int
	s    string
	s2   string
}

func sendTaskMsg(p *tea.Program, msg taskEventMsg) {
	if p == nil {
		return
	}
	if msg.view != nil {
		if msg.view.stream.push(msg) {
			go p.Send(taskStreamReadyMsg{view: msg.view})
		}
		return
	}
	go p.Send(msg)
}

func renderTaskEvent(buf *strings.Builder, kind int, s, s2 string) {
	switch kind {
	case 0:
		buf.WriteString(s)
	case 1:
		fmt.Fprintf(buf, "\n%s %s %s\n", toolStyle.Render("⚒"), s, dimStyle.Render(s2))
	case 2:
		preview := strings.Split(strings.TrimRight(s2, "\n"), "\n")
		if len(preview) > 4 {
			preview = append(preview[:4], fmt.Sprintf("… +%d lines", len(preview)-4))
		}
		fmt.Fprintf(buf, "%s\n", dimStyle.Render("  "+strings.Join(preview, "\n  ")))
	case 3:

		fmt.Fprintf(buf, "\n%s %s\n", youStyle.Render("you:"), s)
	case 4:
		if s != "" {
			fmt.Fprintf(buf, "\n%s\n", errStyle.Render(s))
		} else {
			buf.WriteString("\n")
		}
	}
}

type taskView struct {
	id    string
	vp    viewport.Model
	buf   strings.Builder
	live  bool
	input textinput.Model
	busy  bool

	followCancel context.CancelFunc
	unsubscribe  func()
	stream       taskStream
}

const tasksDockHeight = 6

func (m *model) dockTasks() []agent.BackgroundTask {
	if m.agent == nil {
		return nil
	}
	var out []agent.BackgroundTask
	for _, t := range m.agent.Tasks().List() {
		if t.Restored {
			continue
		}
		out = append(out, t)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (m *model) clampTaskSel() {
	if n := len(m.dockTasks()); m.taskSel >= n {
		m.taskSel = max(n-1, 0)
	}
}

const dockExpandRows = 4

func (m *model) dockTaskExpand(id string) []string {
	if m.agent == nil {
		return nil
	}
	t, ok := m.agent.Tasks().Get(id)
	if !ok {
		return nil
	}
	events, truncated, _ := m.agent.Tasks().SubscribeWithJournal(id, agent.Events{})
	var buf strings.Builder
	switch {
	case len(events) > 0:
		if truncated {
			buf.WriteString(dimStyle.Render("  [earlier output dropped]") + "\n")
		}
		for _, e := range events {
			renderTaskEvent(&buf, e.Kind, e.S, e.S2)
		}
	case t.Report != "":
		buf.WriteString(t.Report)
	case t.Prompt != "":

		buf.WriteString(t.Prompt)
	default:
		return nil
	}
	lines := strings.Split(strings.TrimRight(ansi.Strip(buf.String()), "\n"), "\n")
	if len(lines) > dockExpandRows {
		lines = lines[len(lines)-dockExpandRows:]
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate("   "+dimStyle.Render("│ ")+dimStyle.Render(taskLine(l)), max(m.width, 1), "…")
	}
	return lines
}

func (m *model) tasksDock() string {
	tasks := m.dockTasks()
	if len(tasks) == 0 {
		m.dockOffsets = nil
		return ""
	}
	m.clampTaskSel()

	rows := make([]string, 0, len(tasks)+2)
	if m.tasksFocus {
		label := fmt.Sprintf("%s %d/%d", m.tr("subagents"), m.taskSel+1, len(tasks))
		rows = append(rows, taskColumns(accentStyle.Render(label), dimStyle.Render(m.taskSummary(tasks)), m.width))
	}

	budget := tasksDockHeight - len(rows)
	var expanded []string
	if m.tasksFocus && m.taskExpanded && m.taskSel < len(tasks) {
		expanded = m.dockTaskExpand(tasks[m.taskSel].ID)
	}
	if len(tasks)+len(expanded) > budget {
		budget--
	}
	extra := min(len(expanded), max(budget-1, 0))
	expanded = expanded[len(expanded)-extra:]
	slots := max(budget-extra, 1)
	lo := 0
	if m.tasksFocus && m.taskSel >= slots {
		lo = m.taskSel - slots + 1
	}
	hi := min(lo+slots, len(tasks))

	m.dockOffsets = m.dockOffsets[:0]
	m.dockLo = lo
	offset := 0
	for i := lo; i < hi; i++ {
		t := tasks[i]
		selected := m.tasksFocus && i == m.taskSel
		m.dockOffsets = append(m.dockOffsets, offset)
		rows = append(rows, m.taskDockRow(t, selected))
		offset++
		if selected && m.taskExpanded {
			for _, el := range expanded {
				rows = append(rows, el)
				offset++
			}
		}
	}
	m.dockTaskRows = offset
	if lo > 0 || hi < len(tasks) {
		more := fmt.Sprintf(m.tr("   ↑ %d more · ↓ %d more · ctrl+t browse"), lo, len(tasks)-hi)
		rows = append(rows, dimStyle.Render(ansi.Truncate(more, max(m.width, 1), "…")))
	}
	return strings.Join(rows, "\n")
}

func (m *model) openTask(id string) {
	t, ok := m.agent.Tasks().Get(id)
	if !ok {
		return
	}
	m.closeTaskView()
	tv := &taskView{id: id}
	tv.input = textinput.New()
	tv.input.Prompt = "❯ "
	tv.input.PromptStyle = accentStyle
	tv.input.PlaceholderStyle = dimStyle
	tv.input.Placeholder = m.tr("message this subagent (enter to send)")
	tv.input.Focus()
	fmt.Fprintf(&tv.buf, "%s %s  %s\n\n%s %s\n",
		toolStyle.Render("⚙"), t.ID, t.Description,
		youStyle.Render("prompt:"), t.Prompt)
	p := m.prog
	events, truncated, live, unsubscribe := m.agent.Tasks().WatchTask(id, agent.Events{
		OnText: func(s string) {
			sendTaskMsg(p, taskEventMsg{view: tv, id: id, kind: 0, s: s})
		},
		OnToolStart: func(_, n, a string) {
			sendTaskMsg(p, taskEventMsg{view: tv, id: id, kind: 1, s: n, s2: a})
		},
		OnToolEnd: func(_, n, r string) {
			sendTaskMsg(p, taskEventMsg{view: tv, id: id, kind: 2, s: n, s2: r})
		},
		OnSteer: func(s string) {
			sendTaskMsg(p, taskEventMsg{view: tv, id: id, kind: 3, s: s})
		},
	})
	tv.live = live
	tv.busy = t.FollowingUp
	tv.unsubscribe = unsubscribe
	if truncated {
		fmt.Fprintf(&tv.buf, "\n%s\n", dimStyle.Render("  [earlier output dropped]"))
	}
	for _, e := range events {
		renderTaskEvent(&tv.buf, e.Kind, e.S, e.S2)
	}
	switch {
	case live:
		if len(events) > 0 {
			tv.buf.WriteString("\n")
		}
		fmt.Fprintf(&tv.buf, "\n%s\n", dimStyle.Render("  running…"))
	case len(events) > 0:

		fmt.Fprintf(&tv.buf, "\n%s %s\n", toolStyle.Render(string(t.Status)+":"), t.Report)
	case t.Restored && m.store != nil:

		if msgs, err := m.store.SubagentTranscript(m.sessionID, t.ID); err == nil && len(msgs) > 0 {
			renderTranscript(&tv.buf, msgs)
		} else {
			fmt.Fprintf(&tv.buf, "\n%s %s\n", toolStyle.Render(string(t.Status)+":"), t.Report)
		}
	default:
		fmt.Fprintf(&tv.buf, "\n%s %s\n", toolStyle.Render(string(t.Status)+":"), t.Report)
	}
	m.taskVP = tv
	m.refreshTaskVP()
}

func renderTranscript(buf *strings.Builder, msgs []ai.Message) {
	for _, msg := range msgs {
		switch msg.Role {
		case "system":
			continue
		case "user":
			fmt.Fprintf(buf, "\n%s %s\n", youStyle.Render("you:"), msg.Content)
		case "assistant":
			if msg.Content != "" {
				fmt.Fprintf(buf, "\n%s\n", msg.Content)
			}
			for _, tc := range msg.ToolCalls {
				fmt.Fprintf(buf, "\n%s %s %s\n", toolStyle.Render("⚒"), tc.Function.Name, dimStyle.Render(tc.Function.Arguments))
			}
		case "tool":
			preview := strings.Split(strings.TrimRight(msg.Content, "\n"), "\n")
			if len(preview) > 4 {
				preview = append(preview[:4], fmt.Sprintf("… +%d lines", len(preview)-4))
			}
			fmt.Fprintf(buf, "%s\n", dimStyle.Render("  "+strings.Join(preview, "\n  ")))
		}
	}
}

func (m *model) taskChatable(tv *taskView) bool {
	t, ok := m.agent.Tasks().Get(tv.id)
	return ok && !t.Restored
}

func (m *model) taskSend(tv *taskView, text string) {
	t, ok := m.agent.Tasks().Get(tv.id)
	if !ok {
		return
	}
	if tv.busy || t.FollowingUp {
		fmt.Fprintf(&tv.buf, "\n%s\n", dimStyle.Render(m.tr("still replying — draft kept; wait for the current response")))
		m.refreshTaskVP()
		return
	}
	tv.input.SetValue("")
	switch {
	case t.Status == agent.TaskRunning:
		if err := m.agent.SteerTask(tv.id, text); err != nil {
			fmt.Fprintf(&tv.buf, "\n%s\n", errStyle.Render(err.Error()))
			break
		}
		fmt.Fprintf(&tv.buf, "\n%s %s\n", youStyle.Render("you:"), text)
	default:
		fmt.Fprintf(&tv.buf, "\n%s %s\n\n", youStyle.Render("you:"), text)
		ctx, cancel := context.WithCancel(context.Background())
		tv.busy, tv.followCancel = true, cancel
		p, id := m.prog, tv.id
		ev := agent.Events{
			OnText: func(s string) {
				sendTaskMsg(p, taskEventMsg{view: tv, id: id, kind: 0, s: s})
			},
			OnToolStart: func(_, n, a string) {
				sendTaskMsg(p, taskEventMsg{view: tv, id: id, kind: 1, s: n, s2: a})
			},
			OnToolEnd: func(_, n, r string) {
				sendTaskMsg(p, taskEventMsg{view: tv, id: id, kind: 2, s: n, s2: r})
			},
		}
		ag := m.agent
		go func() {
			defer cancel()
			_, err := ag.FollowupTask(ctx, id, text, ev)
			e := ""
			if err != nil {
				e = err.Error()
			}
			sendTaskMsg(p, taskEventMsg{view: tv, id: id, kind: 4, s: e})
		}()
	}
	m.refreshTaskVP()
}

func (m *model) refreshTaskVP() {
	tv := m.taskVP
	if tv == nil {
		return
	}

	atBottom := tv.vp.AtBottom()
	tv.vp.Width, tv.vp.Height = max(m.width, 1), max(m.height-m.taskViewChromeRows(), 1)
	tv.input.Width = max(m.width-6, 1)
	tv.vp.SetContent(wrapWideLines(tv.buf.String(), max(m.width, 1)))
	if atBottom {
		tv.vp.GotoBottom()
	}
}

func (m *model) closeTaskView() {
	if m.taskVP != nil {
		m.taskVP.stream.close()
		if m.taskVP.unsubscribe != nil {
			m.taskVP.unsubscribe()
		}
	}
	m.taskVP = nil
}

func (m *model) taskViewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	tv := m.taskVP
	switch msg.Type {
	case tea.KeyEsc:
		m.closeTaskView()
		m.tasksFocus = true
		return m, nil
	case tea.KeyCtrlT:
		m.closeTaskView()
		m.tasksFocus = true
		return m, nil
	case tea.KeyCtrlHome:
		tv.vp.GotoTop()
		return m, nil
	case tea.KeyCtrlEnd:
		tv.vp.GotoBottom()
		return m, nil
	case tea.KeyCtrlX:
		if tv.busy && tv.followCancel != nil {
			tv.followCancel()
		} else {
			m.agent.Tasks().Cancel(tv.id)
		}
		return m, nil
	case tea.KeyEnter:
		if text := strings.TrimSpace(tv.input.Value()); text != "" && m.taskChatable(tv) {
			m.taskSend(tv, text)
		}
		return m, nil
	case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown:
		var cmd tea.Cmd
		tv.vp, cmd = tv.vp.Update(msg)
		return m, cmd
	}
	if !m.taskChatable(tv) {
		var cmd tea.Cmd
		tv.vp, cmd = tv.vp.Update(msg)
		return m, cmd
	}
	var cmd tea.Cmd
	tv.input, cmd = tv.input.Update(msg)
	return m, cmd
}
