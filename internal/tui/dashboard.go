package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The dashboard (/dashboard, or "Dashboard" in the Ctrl+P palette) is a
// full-screen list of sessions: this workspace's first, then recent ones
// from other workspaces, and the current session's subagents. It draws
// through the shared surface frame (picker_view.go).
//
// What a row's status rests on:
//   - the current session: this process's own state (a turn running, a
//     dialog waiting on the user);
//   - other sessions: the store's run lease, which another kn or the backend
//     holds while it runs a turn there. A session open elsewhere but between
//     turns cannot be told apart from one nobody has open, so it shows as
//     idle with its last saved activity;
//   - subagents: their task status, the only place an error is known.

const (
	// dashLocalLimit and dashOtherLimit bound what one refresh reads: the
	// newest sessions of this workspace and of all workspaces.
	dashLocalLimit = 100
	dashOtherLimit = 60
	// dashTaskLimit caps the subagent section.
	dashTaskLimit = 8
	// dashRefreshEvery is how often an open dashboard re-reads the store.
	dashRefreshEvery = 2 * time.Second
)

// dashStatus is what a row says about a session or a subagent.
type dashStatus int

const (
	dashIdle dashStatus = iota
	dashRunning
	dashWaiting
	dashError
	dashDone
)

func (s dashStatus) word() string {
	switch s {
	case dashRunning:
		return "running"
	case dashWaiting:
		return "waiting"
	case dashError:
		return "error"
	case dashDone:
		return "done"
	}
	return "idle"
}

func (s dashStatus) glyph() string {
	switch s {
	case dashRunning:
		return "●"
	case dashWaiting:
		return "◆"
	case dashError:
		return "✗"
	case dashDone:
		return "✓"
	}
	return "○"
}

func (s dashStatus) style() lipgloss.Style {
	switch s {
	case dashRunning:
		return runningStyle
	case dashWaiting:
		return warnStyle
	case dashError:
		return errStyle
	case dashDone:
		return okStyle
	}
	return textMutedStyle
}

// dashEntry is one stored session as the last refresh read it.
type dashEntry struct {
	meta session.Meta
	msgs int
	run  *session.ExternalRun
}

// dashRow is one list entry: a session or a subagent of this session.
type dashRow struct {
	section string
	entry   *dashEntry
	task    *agent.BackgroundTask
}

func (r dashRow) key() string {
	if r.task != nil {
		return "task:" + r.task.ID
	}
	return r.entry.meta.ID
}

type dashboard struct {
	entries []dashEntry
	tasks   []agent.BackgroundTask
	total   int
	rows    []dashRow
	sel     int

	filter string
	// typing is true while "/" has the filter line taking keys.
	typing bool
	// grouped lists sessions under one heading per workspace.
	grouped bool

	// confirm is the id awaiting a delete confirmation.
	confirm string
	note    string
	// seq numbers refresh ticks, so a reopened dashboard ignores the old
	// one's.
	seq int

	previews map[string][2]string
	todos    map[string]dashTodos
}

// dashTodos is a session's stored plan, read when its row is selected.
type dashTodos struct {
	at    time.Time
	items []agent.Todo
}

type dashTickMsg struct{ seq int }

func (m *model) dashOpen() bool { return m.app != nil && m.app.dash != nil }

// openDashboard shows the dashboard and starts its refresh ticks.
func (m *model) openDashboard() tea.Cmd {
	if m.store == nil {
		m.append(errStyle.Render(m.tr("session store unavailable")))
		return nil
	}
	app := m.ensureApp()
	app.modal = nil
	prev := 0
	if app.dash != nil {
		prev = app.dash.seq
	}
	app.dash = &dashboard{seq: prev + 1, previews: map[string][2]string{}, todos: map[string]dashTodos{}}
	m.menu = nil
	m.dashReload()
	return m.dashScheduleTick()
}

func (m *model) dashScheduleTick() tea.Cmd {
	seq := m.app.dash.seq
	return tea.Tick(dashRefreshEvery, func(time.Time) tea.Msg { return dashTickMsg{seq: seq} })
}

// dashTick refreshes an open dashboard and schedules the next tick.
func (m *model) dashTick(t dashTickMsg) tea.Cmd {
	if !m.dashOpen() || m.app.dash.seq != t.seq {
		return nil
	}
	if m.app.dash.confirm == "" {
		m.dashReload()
	}
	return m.dashScheduleTick()
}

// dashReload re-reads the store and the subagent list, keeping the
// selection on the same row.
func (m *model) dashReload() {
	d := m.app.dash
	here := cwd()
	local, err := m.store.ListPage(context.Background(), session.PageOptions{Limit: dashLocalLimit, CWD: &here})
	if err != nil {
		d.note = err.Error()
		return
	}
	all, err := m.store.ListPage(context.Background(), session.PageOptions{Limit: dashOtherLimit})
	if err != nil {
		d.note = err.Error()
		return
	}
	seen := map[string]bool{}
	d.entries = d.entries[:0]
	for _, p := range []session.Page{local, all} {
		for i, meta := range p.Sessions {
			if seen[meta.ID] || meta.TaskID != "" {
				continue
			}
			seen[meta.ID] = true
			e := dashEntry{meta: meta}
			if i < len(p.MessageCounts) {
				e.msgs = p.MessageCounts[i]
			}
			if i < len(p.ExternalRuns) {
				e.run = p.ExternalRuns[i]
			}
			d.entries = append(d.entries, e)
		}
	}
	sort.SliceStable(d.entries, func(i, j int) bool {
		return d.entries[i].meta.UpdatedAt.After(d.entries[j].meta.UpdatedAt)
	})
	d.total = max(all.Total, len(d.entries))
	d.tasks = m.dockTasks()
	if len(d.tasks) > dashTaskLimit {
		d.tasks = d.tasks[:dashTaskLimit]
	}
	clear(d.previews)
	m.dashRebuild()
}

// dashRebuild recomputes the visible rows from the loaded sessions, the
// filter and the grouping.
func (m *model) dashRebuild() {
	d := m.app.dash
	prev := ""
	if d.sel >= 0 && d.sel < len(d.rows) {
		prev = d.rows[d.sel].key()
	}
	terms := strings.Fields(strings.ToLower(d.filter))
	d.rows = d.rows[:0]
	for i := range d.tasks {
		t := &d.tasks[i]
		if dashMatches(terms, t.ID, t.Description, string(t.Status)) {
			d.rows = append(d.rows, dashRow{section: m.tr("This session's subagents"), task: t})
		}
	}
	here := cwd()
	var sessions []dashRow
	for i := range d.entries {
		e := &d.entries[i]
		meta := e.meta
		if !dashMatches(terms, meta.Title, meta.ID, meta.CWD, meta.Model, meta.Provider, meta.Goal, strings.Join(meta.Tags, " ")) {
			continue
		}
		var section string
		switch {
		case d.grouped:
			section = dashWorkspace(meta.CWD)
		case meta.CWD == here:
			section = m.tr("This workspace")
		default:
			section = m.tr("Other workspaces")
		}
		sessions = append(sessions, dashRow{section: section, entry: e})
	}
	// Sections in order of first appearance, this workspace first; the
	// entries are newest first, so each group is too.
	first := map[string]int{}
	for i, r := range sessions {
		if _, ok := first[r.section]; !ok {
			first[r.section] = i
			if r.entry.meta.CWD == here {
				first[r.section] = -1
			}
		}
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		return first[sessions[i].section] < first[sessions[j].section]
	})
	d.rows = append(d.rows, sessions...)
	d.sel = min(max(d.sel, 0), max(len(d.rows)-1, 0))
	for i, r := range d.rows {
		if r.key() == prev {
			d.sel = i
			break
		}
	}
}

func dashMatches(terms []string, fields ...string) bool {
	if len(terms) == 0 {
		return true
	}
	hay := strings.ToLower(strings.Join(fields, " "))
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// dashWorkspace is a session directory for display: home as ~.
func dashWorkspace(dir string) string {
	if dir == "" {
		return "?"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if dir == home {
			return "~"
		}
		if strings.HasPrefix(dir, home+string(filepath.Separator)) {
			return "~" + filepath.ToSlash(dir[len(home):])
		}
	}
	return filepath.ToSlash(dir)
}

// dashWorkspaceShort is the last two elements of a workspace, for a column.
func dashWorkspaceShort(dir string) string {
	w := dashWorkspace(dir)
	parts := strings.Split(strings.TrimRight(w, "/"), "/")
	if len(parts) > 2 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return w
}

// sessionStatus is a stored session's state and a line explaining it.
func (m *model) sessionStatus(e *dashEntry) (dashStatus, string) {
	if e.meta.ID == m.sessionID && m.sessionID != "" {
		switch {
		case m.permDialog != nil:
			return dashWaiting, m.tr("this session · waiting for a permission answer")
		case m.askDialog != nil || m.elicitOpen():
			return dashWaiting, m.tr("this session · waiting for your answer")
		case m.planOpen():
			return dashWaiting, m.tr("this session · waiting for plan approval")
		case m.busy:
			return dashRunning, m.tr("this session · running a turn")
		}
		return dashIdle, m.tr("this session · idle")
	}
	if e.run != nil {
		s := fmt.Sprintf(m.tr("running a turn in %s"), dashClient(e.run.Client))
		if e.run.PID > 0 {
			s += fmt.Sprintf(" (pid %d)", e.run.PID)
		}
		if !e.run.StartedAt.IsZero() {
			s += fmt.Sprintf(m.tr(" · started %s"), m.dashAgo(e.run.StartedAt))
		}
		return dashRunning, s
	}
	return dashIdle, fmt.Sprintf(m.tr("no turn running · last activity %s (stored state)"), m.dashAgo(e.meta.UpdatedAt))
}

func dashClient(c string) string {
	if c == "" {
		return "another process"
	}
	return c
}

func taskStatus(m *model, t *agent.BackgroundTask) dashStatus {
	switch {
	case m.subRunning(*t):
		return dashRunning
	case t.Status == agent.TaskError:
		return dashError
	case t.Status == agent.TaskDone:
		return dashDone
	}
	return dashIdle
}

// dtr translates a short word of the dashboard. Its translations are
// keyed under "dashboard: " so a word as plain as "status" changes here
// and nowhere else.
func (m *model) dtr(s string) string {
	if t := m.tr("dashboard: " + s); t != "dashboard: "+s {
		return t
	}
	return s
}

// dashAgo is ago measured against the model's clock.
func (m *model) dashAgo(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := m.nowFn().Sub(t)
	switch {
	case d < time.Minute:
		return m.dtr("just now")
	case d < time.Hour:
		return fmt.Sprintf(m.dtr("%dm ago"), int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf(m.dtr("%dh ago"), int(d.Hours()))
	default:
		return fmt.Sprintf(m.dtr("%dd ago"), int(d.Hours()/24))
	}
}

// dashUsage is a session's stored token total and, when every model it
// used has a price, its cost.
func (m *model) dashUsage(meta session.Meta) (int, float64, bool) {
	tokens := meta.UsageIn + meta.UsageOut
	if tokens == 0 {
		return 0, 0, false
	}
	if m.cfg != nil && m.cfg.SubscriptionBilled(meta.Provider) {
		return tokens, 0, false
	}
	total := 0.0
	add := func(label string, u ai.Usage) bool {
		model, prov, _ := strings.Cut(label, " @ ")
		if prov == "" {
			prov = meta.Provider
		}
		c, ok := m.usageCost(model, prov, u)
		total += c
		return ok
	}
	if len(meta.ModelUsage) == 0 {
		if !add(meta.Model, ai.Usage{PromptTokens: meta.UsageIn, CompletionTokens: meta.UsageOut}) {
			return tokens, 0, false
		}
	}
	for label, u := range meta.ModelUsage {
		if !add(label, u) {
			return tokens, 0, false
		}
	}
	for label, u := range meta.SubUsage {
		if !add(label, u) {
			return tokens, 0, false
		}
	}
	return tokens, total, true
}

func (m *model) dashTitle(meta session.Meta) string {
	if meta.Title != "" {
		return strings.Join(strings.Fields(meta.Title), " ")
	}
	if meta.ID == m.sessionID {
		return m.tr("(this session, untitled)")
	}
	return m.tr("(untitled)")
}

// dashKey handles a key while the dashboard is open.
func (m *model) dashKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := m.app.dash
	if d.confirm != "" {
		id := d.confirm
		d.confirm = ""
		if msg.Type == tea.KeyRunes && strings.EqualFold(string(msg.Runes), "y") {
			m.dashDelete(id)
		} else {
			d.note = m.tr("delete cancelled")
		}
		return m, nil
	}
	if d.typing {
		switch msg.Type {
		case tea.KeyEsc:
			d.typing, d.filter = false, ""
			m.dashRebuild()
		case tea.KeyEnter:
			d.typing = false
		case tea.KeyBackspace:
			if d.filter != "" {
				_, size := lastRune(d.filter)
				d.filter = d.filter[:len(d.filter)-size]
				m.dashRebuild()
			} else {
				d.typing = false
			}
		case tea.KeySpace:
			d.filter += " "
			m.dashRebuild()
		case tea.KeyRunes:
			d.filter += string(msg.Runes)
			d.sel = 0
			m.dashRebuild()
		case tea.KeyUp, tea.KeyDown, tea.KeyCtrlC:
			d.typing = false
			return m.dashKey(msg)
		}
		return m, nil
	}
	page := max(m.dashFrame().listBudget(m.surfaceHeight())/2, 1)
	switch msg.Type {
	case tea.KeyCtrlC:
		m.closeSurface()
	case tea.KeyEsc:
		if d.filter != "" {
			d.filter = ""
			m.dashRebuild()
			break
		}
		m.closeSurface()
	case tea.KeyUp, tea.KeyCtrlP:
		m.dashMove(-1)
	case tea.KeyDown, tea.KeyCtrlN:
		m.dashMove(1)
	case tea.KeyPgUp:
		m.dashMove(-page)
	case tea.KeyPgDown:
		m.dashMove(page)
	case tea.KeyHome:
		m.dashMove(-len(d.rows))
	case tea.KeyEnd:
		m.dashMove(len(d.rows))
	case tea.KeyTab, tea.KeyShiftTab:
		d.grouped = !d.grouped
		m.dashRebuild()
	case tea.KeyCtrlD:
		m.dashAskDelete()
	case tea.KeyEnter:
		return m.dashEnter()
	case tea.KeyRunes:
		// Keys typed quickly can arrive together: "/gate" starts a filter.
		if rest, ok := strings.CutPrefix(string(msg.Runes), "/"); ok && !msg.Paste {
			d.typing = true
			if rest != "" {
				d.filter += rest
				d.sel = 0
				m.dashRebuild()
			}
			break
		}
		switch string(msg.Runes) {
		case "j":
			m.dashMove(1)
		case "k":
			m.dashMove(-1)
		case "g":
			m.dashMove(-len(d.rows))
		case "G":
			m.dashMove(len(d.rows))
		case "r":
			m.dashReload()
			d.note = m.dtr("refreshed")
		case "d":
			m.dashAskDelete()
		case "n":
			m.closeSurface()
			return m.command("/new")
		case "q":
			m.closeSurface()
		}
	}
	return m, nil
}

func (m *model) surfaceHeight() int {
	_, h := m.surfaceSize()
	return h
}

func (m *model) dashMove(delta int) {
	d := m.app.dash
	d.sel = min(max(d.sel+delta, 0), max(len(d.rows)-1, 0))
	d.note = ""
}

func (m *model) dashSelected() *dashRow {
	d := m.app.dash
	if d.sel < 0 || d.sel >= len(d.rows) {
		return nil
	}
	return &d.rows[d.sel]
}

// dashEnter opens the selected row: resumes a session, or opens a
// subagent's live view.
func (m *model) dashEnter() (tea.Model, tea.Cmd) {
	d := m.app.dash
	r := m.dashSelected()
	if r == nil {
		return m, nil
	}
	if r.task != nil {
		id := r.task.ID
		m.closeSurface()
		m.openTask(id)
		return m, nil
	}
	id := r.entry.meta.ID
	if id == m.sessionID {
		m.closeSurface()
		return m, nil
	}
	if m.busy {
		d.note = m.tr("busy — open another session after this turn")
		return m, nil
	}
	m.closeSurface()
	if err := m.resume(id); err != nil {
		m.append(errStyle.Render(err.Error()))
	}
	return m, nil
}

func (m *model) dashAskDelete() {
	d := m.app.dash
	r := m.dashSelected()
	switch {
	case r == nil:
	case r.task != nil:
		d.note = m.tr("subagents are not deleted here")
	case r.entry.meta.ID == m.sessionID:
		d.note = m.tr("the current session cannot be deleted here")
	case r.entry.run != nil:
		d.note = m.tr("that session is running a turn elsewhere")
	default:
		d.confirm = r.entry.meta.ID
	}
}

// dashDelete removes a session after the user confirmed it, through the
// same store call as the /resume picker.
func (m *model) dashDelete(id string) {
	d := m.app.dash
	if err := m.store.Delete(id); err != nil {
		d.note = "delete: " + err.Error()
		return
	}
	m.dashReload()
	d.note = fmt.Sprintf(m.tr("deleted session %s"), id)
}

// dashMouse moves the selection with the wheel.
func (m *model) dashMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.dashMove(-1)
	case tea.MouseButtonWheelDown:
		m.dashMove(1)
	}
	return m, nil
}

// dashColumns are the optional columns a w-cell row has room for.
type dashColumns struct {
	status, workspace, model, usage bool
}

func dashColumnsFor(w int) dashColumns {
	return dashColumns{status: w >= 64, workspace: w >= 90, model: w >= 110, usage: w >= 120}
}

const (
	dashStatusW    = 8
	dashWorkspaceW = 20
	dashModelW     = 16
	dashUsageW     = 16
	dashAgeW       = 8
)

// pad fits s into exactly w cells, cut or padded on the right.
func dashPad(s string, w int) string {
	s = ansi.Truncate(s, w, ellipsis())
	return s + strings.Repeat(" ", max(w-lipgloss.Width(s), 0))
}

// padLeft fits s into exactly w cells, padded on the left.
func dashPadLeft(s string, w int) string {
	s = ansi.Truncate(s, w, ellipsis())
	return strings.Repeat(" ", max(w-lipgloss.Width(s), 0)) + s
}

// dashRowLine draws one row: caret, status glyph, title, then the columns
// the width allows and the age.
func (m *model) dashRowLine(r dashRow, w int, selected bool) string {
	cols := dashColumnsFor(w)
	var st dashStatus
	var title, ws, model, usage, age string
	current := false
	if r.task != nil {
		t := r.task
		st = taskStatus(m, t)
		title = strings.Join(strings.Fields(t.Description), " ")
		if title == "" {
			title = t.ID
		}
		ws = t.ID
		age = m.dashAgo(t.StartedAt)
	} else {
		e := r.entry
		st, _ = m.sessionStatus(e)
		title = m.dashTitle(e.meta)
		current = e.meta.ID == m.sessionID
		ws = dashWorkspaceShort(e.meta.CWD)
		model = e.meta.Model
		if tok, cost, ok := m.dashUsage(e.meta); tok > 0 {
			usage = fmtTok(tok)
			if ok {
				usage += " · " + fmtCost(cost)
			}
		}
		age = m.dashAgo(e.meta.UpdatedAt)
	}
	lead := "  "
	if selected {
		lead = glyphs.User + " "
	}
	glyph := chromeGlyphs(st.glyph())
	right := ""
	if cols.status {
		right += " " + dashPad(m.dtr(st.word()), dashStatusW)
	}
	if cols.workspace {
		right += " " + dashPad(ws, dashWorkspaceW)
	}
	if cols.model {
		right += " " + dashPad(model, dashModelW)
	}
	if cols.usage {
		right += " " + dashPadLeft(usage, dashUsageW)
	}
	right += " " + dashPadLeft(age, dashAgeW)
	right = chromeGlyphs(right)
	mark := ""
	if current {
		mark = m.tr("(current)") + " "
	}
	titleW := max(w-1-lipgloss.Width(lead)-2-lipgloss.Width(right)-lipgloss.Width(mark), 4)
	title = dashPad(chromeGlyphs(title), titleW)
	if selected {
		line := lead + glyph + " " + mark + title + right
		line = ansi.Truncate(line, max(w-1, 1), ellipsis())
		return optionSelStyle.Render(line + strings.Repeat(" ", max(w-1-lipgloss.Width(line), 0)))
	}
	ts := title
	if strings.HasPrefix(title, "(") {
		ts = textMutedStyle.Render(title)
	}
	line := lead + st.style().Render(glyph) + " " + accentFgStyle.Render(mark) + ts + textHintStyle.Render(right)
	return ansi.Truncate(line, w, ellipsis())
}

// dashHeader names the columns above the list.
func (m *model) dashHeader(w int) string {
	cols := dashColumnsFor(w)
	right := ""
	if cols.status {
		right += " " + dashPad(m.dtr("status"), dashStatusW)
	}
	if cols.workspace {
		right += " " + dashPad(m.dtr("workspace"), dashWorkspaceW)
	}
	if cols.model {
		right += " " + dashPad(m.dtr("model"), dashModelW)
	}
	if cols.usage {
		right += " " + dashPadLeft(m.tr("tokens · cost"), dashUsageW)
	}
	right += " " + dashPadLeft(m.dtr("active"), dashAgeW)
	titleW := max(w-1-2-2-lipgloss.Width(right), 4)
	return textMutedStyle.Render(ansi.Truncate("    "+dashPad(m.dtr("session"), titleW)+chromeGlyphs(right), w-1, ellipsis()))
}

func (m *model) dashFrame() surfaceFrame {
	d := m.app.dash
	w, _ := m.surfaceSize()
	f := surfaceFrame{
		title:       m.tr("Dashboard"),
		filter:      d.filter,
		placeholder: m.tr("press / and type a title, workspace or model"),
		footer:      m.tr("Enter open · n new · d delete · / filter · Tab group by workspace · r refresh · Esc close"),
		note:        d.note,
	}
	if d.typing {
		f.placeholder = filterCaret() + " " + m.tr("type to filter · Enter done · Esc clear")
		f.footer = m.tr("type to filter · Enter done · Esc clear")
		if d.filter != "" {
			f.filter = d.filter
		}
	}
	if f.note == "" {
		f.note = m.tr("● running = a turn in progress here, in another kn or in the backend; idle rows show their last saved state")
	}
	sessions := 0
	for _, r := range d.rows {
		if r.entry != nil {
			sessions++
		}
	}
	if d.filter != "" {
		f.right = fmt.Sprintf(m.tr("%d of %d sessions"), sessions, len(d.entries))
	} else {
		f.right = fmt.Sprintf(m.tr("%d sessions"), d.total)
	}
	if d.grouped {
		f.right = m.tr("by workspace") + " · " + f.right
	}
	if d.confirm != "" {
		title := d.confirm
		for _, e := range d.entries {
			if e.meta.ID == d.confirm {
				title = m.dashTitle(e.meta)
			}
		}
		f.confirm = fmt.Sprintf(m.tr("delete session \"%s\"? y to confirm, any other key cancels"), truncLine(title, 40))
	}
	if len(d.rows) > 0 {
		f.list = append(f.list, m.dashHeader(w))
		f.items = append(f.items, false)
	}
	section := ""
	for i, r := range d.rows {
		if r.section != section {
			section = r.section
			if i > 0 {
				f.list = append(f.list, "")
				f.items = append(f.items, false)
			}
			f.list = append(f.list, "  "+textMutedStyle.Render(chromeGlyphs(section)))
			f.items = append(f.items, false)
		}
		if i == d.sel {
			f.selLine = len(f.list)
		}
		f.list = append(f.list, m.dashRowLine(r, w, i == d.sel))
		f.items = append(f.items, true)
	}
	if len(d.rows) == 0 {
		empty := m.tr("no sessions yet · n starts one")
		if d.filter != "" {
			empty = m.tr("no sessions match")
		}
		f.list = append(f.list, "  "+textMutedStyle.Render(chromeGlyphs(empty)))
		f.items = append(f.items, false)
	}
	if r := m.dashSelected(); r != nil {
		f.preview = m.dashPreview(*r, w)
	}
	return f
}

// dashPreview is the selected row's detail.
func (m *model) dashPreview(r dashRow, w int) []string {
	d := m.app.dash
	muted := func(s string) string { return textMutedStyle.Render(chromeGlyphs(s)) }
	if r.task != nil {
		t := r.task
		st := taskStatus(m, t)
		out := []string{
			muted(t.ID + " · " + m.tr("subagent of this session")),
			st.style().Render(chromeGlyphs(st.glyph()+" "+m.dtr(st.word()))) + muted(" · "+fmt.Sprintf(m.tr("started %s"), m.dashAgo(t.StartedAt))),
		}
		if rep := strings.TrimSpace(t.Report); rep != "" {
			out = append(out, previewBlock(textHintStyle.Render(chromeGlyphs("⎿ ")), rep, w)...)
		}
		return out
	}
	e := r.entry
	meta := e.meta
	info := meta.ID
	if meta.Model != "" {
		info += " · " + meta.Model + "@" + meta.Provider
	}
	info += " · " + dashWorkspace(meta.CWD)
	st, why := m.sessionStatus(e)
	out := []string{
		muted(info),
		st.style().Render(chromeGlyphs(st.glyph()+" "+m.dtr(st.word()))) + muted(" · "+why),
	}
	var facts []string
	if tok, cost, ok := m.dashUsage(meta); tok > 0 {
		s := fmt.Sprintf(m.tr("%s tokens"), fmtTok(tok))
		if ok {
			s += " · " + fmtCost(cost)
		}
		facts = append(facts, s)
	}
	if e.msgs > 0 {
		if e.msgs == 1 {
			facts = append(facts, m.tr("1 message"))
		} else {
			facts = append(facts, fmt.Sprintf(m.tr("%d messages"), e.msgs))
		}
	}
	if len(meta.Tags) > 0 {
		facts = append(facts, "#"+strings.Join(meta.Tags, " #"))
	}
	if len(facts) > 0 {
		out = append(out, muted(strings.Join(facts, " · ")))
	}
	if g := strings.TrimSpace(meta.Goal); g != "" {
		out = append(out, accentFgStyle.Render(chromeGlyphs("◎ "+m.dtr("goal")+": "))+truncLine(chromeGlyphs(g), max(w-12, 8)))
	}
	if todo := m.dashTodoLine(meta, w); todo != "" {
		out = append(out, todo)
	}
	prev, ok := d.previews[meta.ID]
	if !ok {
		u, a := m.store.LastExchange(meta.ID)
		prev = [2]string{u, a}
		d.previews[meta.ID] = prev
	}
	out = append(out, previewBlock(youStyle.Render(glyphUser), prev[0], w)...)
	out = append(out, previewBlock(botStyle.Render(glyphAssistant), prev[1], w)...)
	return out
}

// dashTodoLine is a session's stored plan in one line: how much is done
// and the step in progress (or the next one). The plan is read only for
// the selected row, and again only when the session has changed.
func (m *model) dashTodoLine(meta session.Meta, w int) string {
	d := m.app.dash
	var items []agent.Todo
	if meta.ID == m.sessionID && m.agent != nil {
		items = m.agent.Todos
	} else {
		c, ok := d.todos[meta.ID]
		if !ok || !c.at.Equal(meta.UpdatedAt) {
			c = dashTodos{at: meta.UpdatedAt}
			if s := m.store.Todos(meta.ID); s != "" {
				_ = json.Unmarshal([]byte(s), &c.items)
			}
			d.todos[meta.ID] = c
		}
		items = c.items
	}
	if len(items) == 0 {
		return ""
	}
	done, next := 0, ""
	for _, it := range items {
		switch it.Status {
		case "completed", "cancelled":
			done++
		case "in_progress":
			next = it.Content
		}
	}
	if next == "" {
		for _, it := range items {
			if it.Status == "pending" {
				next = it.Content
				break
			}
		}
	}
	s := fmt.Sprintf(m.tr("todos %d/%d done"), done, len(items))
	if next != "" {
		s += " · " + m.dtr("next") + ": " + strings.Join(strings.Fields(next), " ")
	}
	return textSecondaryStyle.Render(truncLine(chromeGlyphs("☰ "+s), max(w-4, 8)))
}

// dashView draws the dashboard.
func (m *model) dashView() string {
	w, h := m.surfaceSize()
	return m.dashFrame().render(w, h)
}
