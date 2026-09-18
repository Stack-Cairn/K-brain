package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type taskEventMsg struct {
	id   string
	kind int
	s    string
	s2   string
}

func sendTaskMsg(p *tea.Program, msg taskEventMsg) {
	if p == nil {
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
			preview = append(preview[:4], fmt.Sprintf("… +%d lines", len(s2)-4))
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
		lines[i] = "   " + dimStyle.Render("│ ") + truncLine(l, max(m.width-10, 8))
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
		hint := " ⚙ subagents — ↑/↓ select (↑ past top: back to input) · space expand · enter open"
		if m.taskExpanded {
			hint = " ⚙ subagents — ↑/↓ select · space collapse · enter open"
		}
		rows = append(rows, dimStyle.Render(hint))
	}

	budget := tasksDockHeight - len(rows)
	if len(tasks) > budget {
		budget--
	}

	extra := 0
	if m.tasksFocus && m.taskExpanded && m.taskSel < len(tasks) {
		extra = len(m.dockTaskExpand(tasks[m.taskSel].ID))
	}
	lo := 0
	if m.tasksFocus && m.taskSel >= budget-extra {
		lo = m.taskSel - (budget - extra) + 1
	}
	hi := min(lo+budget-extra, len(tasks))
	hi = max(hi, min(lo+1, len(tasks)))

	m.dockOffsets = m.dockOffsets[:0]
	m.dockLo = lo
	offset := 0
	for i := lo; i < hi; i++ {
		t := tasks[i]
		icon := toolStyle.Render("⏳")
		switch t.Status {
		case agent.TaskDone:
			icon = "✓"
		case agent.TaskError, agent.TaskCancelled:
			icon = errStyle.Render("✗")
		}
		line := fmt.Sprintf("%s %s  %s", icon, t.ID, truncLine(t.Description, max(m.width-24, 8)))
		var meta string
		if t.Status == agent.TaskRunning {
			meta = fmt.Sprintf("  %ds", int(time.Since(t.StartedAt).Seconds()))
		} else {
			meta = "  " + string(t.Status)
		}
		selected := m.tasksFocus && i == m.taskSel
		switch {
		case selected:
			line = botStyle.Render(" → "+line) + toolStyle.Render(meta)
		case t.Status == agent.TaskRunning:
			line = "   " + toolStyle.Render(line) + dimStyle.Render(meta)
		default:
			line = "   " + line + dimStyle.Render(meta)
		}
		m.dockOffsets = append(m.dockOffsets, offset)
		rows = append(rows, line)
		offset++
		if selected && m.taskExpanded {
			for _, el := range m.dockTaskExpand(t.ID) {
				rows = append(rows, el)
				offset++
			}
		}
	}
	m.dockTaskRows = offset
	if more := len(tasks) - hi; more > 0 {
		rows = append(rows, dimStyle.Render(fmt.Sprintf("   … +%d more (ctrl+t to browse)", more)))
	}
	return strings.Join(rows, "\n")
}

func (m *model) openTask(id string) {
	t, ok := m.agent.Tasks().Get(id)
	if !ok {
		return
	}
	tv := &taskView{id: id}
	tv.input = textinput.New()
	tv.input.Prompt = youStyle.Render("› ")
	tv.input.Placeholder = "message this subagent (enter to send)"
	tv.input.Focus()
	fmt.Fprintf(&tv.buf, "%s %s  %s\n\n%s %s\n",
		toolStyle.Render("⚙"), t.ID, t.Description,
		youStyle.Render("prompt:"), t.Prompt)
	p := m.prog
	events, truncated, live := m.agent.Tasks().SubscribeWithJournal(id, agent.Events{
		OnText: func(s string) {
			sendTaskMsg(p, taskEventMsg{id: id, kind: 0, s: s})
		},
		OnToolStart: func(_, n, a string) {
			sendTaskMsg(p, taskEventMsg{id: id, kind: 1, s: n, s2: a})
		},
		OnToolEnd: func(_, n, r string) {
			sendTaskMsg(p, taskEventMsg{id: id, kind: 2, s: n, s2: r})
		},
		OnSteer: func(s string) {
			sendTaskMsg(p, taskEventMsg{id: id, kind: 3, s: s})
		},
	})
	tv.live = live
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
				preview = append(preview[:4], fmt.Sprintf("… +%d lines", len(msg.Content)-4))
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
	tv.input.SetValue("")
	switch {
	case t.Status == agent.TaskRunning:
		if err := m.agent.SteerTask(tv.id, text); err != nil {
			fmt.Fprintf(&tv.buf, "\n%s\n", errStyle.Render(err.Error()))
			break
		}
		fmt.Fprintf(&tv.buf, "\n%s %s\n", youStyle.Render("you:"), text)
	case tv.busy:
		fmt.Fprintf(&tv.buf, "\n%s\n", dimStyle.Render("(still replying — wait for the current reply)"))
	default:
		fmt.Fprintf(&tv.buf, "\n%s %s\n\n", youStyle.Render("you:"), text)
		ctx, cancel := context.WithCancel(context.Background())
		tv.busy, tv.followCancel = true, cancel
		p, id := m.prog, tv.id
		ev := agent.Events{
			OnText: func(s string) {
				sendTaskMsg(p, taskEventMsg{id: id, kind: 0, s: s})
			},
			OnToolStart: func(_, n, a string) {
				sendTaskMsg(p, taskEventMsg{id: id, kind: 1, s: n, s2: a})
			},
			OnToolEnd: func(_, n, r string) {
				sendTaskMsg(p, taskEventMsg{id: id, kind: 2, s: n, s2: r})
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
			sendTaskMsg(p, taskEventMsg{id: id, kind: 4, s: e})
		}()
	}
	m.refreshTaskVP()
}

func (m *model) refreshTaskVP() {
	tv := m.taskVP
	if tv == nil {
		return
	}

	tv.vp.Width, tv.vp.Height = m.width, max(m.height-3, 1)
	tv.input.Width = max(m.width-4, 8)
	atBottom := tv.vp.AtBottom()
	tv.vp.SetContent(tv.buf.String())
	if atBottom {
		tv.vp.GotoBottom()
	}
}

func (m *model) taskViewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	tv := m.taskVP
	switch msg.Type {
	case tea.KeyEsc:
		m.taskVP = nil
		m.tasksFocus = true
		return m, nil
	case tea.KeyCtrlT:
		m.taskVP = nil
		m.tasksFocus = true
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

func (m *model) taskViewView() string {
	tv := m.taskVP
	t, ok := m.agent.Tasks().Get(tv.id)
	status := "running"
	if ok {
		status = string(t.Status)
	}
	if ok && t.Restored {
		status += ", restored"
	}
	if tv.busy {
		status += ", replying"
	}
	head := toolStyle.Render(fmt.Sprintf(" ⚙ %s — %s", tv.id, truncLine(t.Description, max(m.width-30, 8)))) +
		dimStyle.Render("  ("+status+")")
	in := " " + tv.input.View()
	hint := " esc back · ↑/↓ scroll · enter send (steers while running, chats after) · ctrl+x cancel"
	if ok && t.Restored {
		in = dimStyle.Render(" (restored from a previous session — read-only)")
		hint = " esc back · ↑/↓ scroll"
	}
	foot := dimStyle.Render(hint)
	return head + "\n" + sanitizeView(tv.vp.View()) + "\n" + in + "\n" + foot
}
