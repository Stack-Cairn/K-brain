package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// pickerPageSize is how many sessions the picker loads at a time; scrolling
// past the oldest loaded one fetches the next page.
const pickerPageSize = 50

// pickerSearchDelay debounces the full-text search while the filter is typed.
const pickerSearchDelay = 250 * time.Millisecond

// picker is the /resume session browser. metas is the visible list, newest
// first; all is every loaded session before the filter.
type picker struct {
	metas    []session.Meta
	idx      int
	previews map[string][2]string

	all   []session.Meta
	next  *session.PageCursor
	total int

	filter string
	// hits is the full-text result for hitsFor, merged into metas; content
	// marks the sessions that only matched there.
	hits    []session.Meta
	hitsFor string
	content map[string]bool
	seq     int
	pending bool

	// titles caches the fallback title of untitled sessions, snippets the
	// matching line of content-only hits.
	titles   map[string]string
	snippets map[string]string

	// confirm is the id awaiting a delete confirmation.
	confirm string
	note    string
}

type pickerSearchTick struct{ seq int }

type pickerSearchMsg struct {
	seq   int
	query string
	metas []session.Meta
	err   error
}

func (m *model) pickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.picker
	if p.confirm != "" {
		id := p.confirm
		p.confirm = ""
		if msg.Type == tea.KeyRunes && strings.EqualFold(string(msg.Runes), "y") {
			m.pickerDelete(id)
		} else {
			p.note = m.tr("delete cancelled")
		}
		return m, nil
	}
	page := max(m.pickerListBudget()/2, 1)
	switch msg.Type {
	case tea.KeyCtrlC:
		m.picker = nil
	case tea.KeyEsc:
		if p.filter != "" {
			return m, m.pickerSetFilter("")
		}
		m.picker = nil
	case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
		m.pickerMove(-1)
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		m.pickerMove(1)
	case tea.KeyPgUp:
		m.pickerMove(-page)
	case tea.KeyPgDown:
		m.pickerMove(page)
	case tea.KeyHome:
		m.pickerMove(-len(p.metas))
	case tea.KeyEnd:
		m.pickerMove(len(p.metas))
	case tea.KeyCtrlD:
		if len(p.metas) == 0 {
			break
		}
		id := p.metas[p.idx].ID
		if id == m.sessionID {
			p.note = m.tr("the current session cannot be deleted here")
			break
		}
		p.confirm = id
	case tea.KeyEnter:
		if len(p.metas) == 0 {
			break
		}
		id := p.metas[p.idx].ID
		m.picker = nil
		if err := m.resume(id); err != nil {
			m.append(errStyle.Render(err.Error()))
		}
	case tea.KeyBackspace:
		if p.filter != "" {
			_, size := lastRune(p.filter)
			return m, m.pickerSetFilter(p.filter[:len(p.filter)-size])
		}
	case tea.KeySpace:
		return m, m.pickerSetFilter(p.filter + " ")
	case tea.KeyRunes:
		return m, m.pickerSetFilter(p.filter + string(msg.Runes))
	}
	return m, nil
}

// pickerMouse moves the selection with the wheel.
func (m *model) pickerMouse(msg tea.MouseMsg) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.pickerMove(-1)
	case tea.MouseButtonWheelDown:
		m.pickerMove(1)
	}
}

// pickerMove moves the selection by delta, loading older pages when it runs
// past the oldest loaded session of an unfiltered list.
func (m *model) pickerMove(delta int) {
	p := m.picker
	want := p.idx + delta
	for want > len(p.metas)-1 && p.filter == "" && p.next != nil {
		if !m.pickerLoadPage() {
			break
		}
	}
	p.idx = min(max(want, 0), max(len(p.metas)-1, 0))
	p.note = ""
	if len(p.metas) > 0 {
		p.loadPreview(m.store)
	}
}

func (p *picker) loadPreview(store *session.Store) {
	id := p.metas[p.idx].ID
	if _, ok := p.previews[id]; !ok {
		u, a := store.LastExchange(id)
		p.previews[id] = [2]string{u, a}
	}
}

func (m *model) openPicker() {
	if m.store == nil {
		m.append(errStyle.Render("session store unavailable"))
		return
	}
	m.picker = &picker{
		previews: map[string][2]string{},
		titles:   map[string]string{},
		snippets: map[string]string{},
		content:  map[string]bool{},
	}
	if !m.pickerLoadPage() {
		m.picker = nil
		return
	}
	if len(m.picker.all) == 0 {
		m.picker = nil
		m.append(dimStyle.Render("(no previous sessions)"))
		return
	}
	m.picker.loadPreview(m.store)
}

// pickerLoadPage appends the next page of sessions. It reports false when
// the store failed, after saying so.
func (m *model) pickerLoadPage() bool {
	p := m.picker
	page, err := m.store.ListPage(context.Background(), session.PageOptions{Limit: pickerPageSize, After: p.next})
	if err != nil {
		if len(p.all) == 0 {
			m.append(errStyle.Render(err.Error()))
		} else {
			p.note = err.Error()
		}
		p.next = nil
		return false
	}
	p.all = append(p.all, page.Sessions...)
	p.next, p.total = page.Next, max(page.Total, len(p.all))
	m.pickerRebuild()
	return true
}

// pickerSetFilter changes the filter, re-filters the loaded sessions at once
// and schedules the full-text search.
func (m *model) pickerSetFilter(f string) tea.Cmd {
	p := m.picker
	p.filter, p.idx, p.note = f, 0, ""
	p.seq++
	m.pickerRebuild()
	if len(p.metas) > 0 {
		p.loadPreview(m.store)
	}
	q := strings.TrimSpace(f)
	if q == "" {
		p.pending = false
		return nil
	}
	p.pending = true
	seq := p.seq
	return tea.Tick(pickerSearchDelay, func(time.Time) tea.Msg { return pickerSearchTick{seq: seq} })
}

// pickerSearchFire starts the search once typing has paused.
func (m *model) pickerSearchFire(t pickerSearchTick) tea.Cmd {
	p := m.picker
	if p == nil || t.seq != p.seq || m.store == nil {
		return nil
	}
	store, query, seq := m.store, strings.TrimSpace(p.filter), p.seq
	return func() tea.Msg {
		metas, err := store.Search(query, false)
		return pickerSearchMsg{seq: seq, query: query, metas: metas, err: err}
	}
}

// pickerSearchDone merges a finished search into the list, unless the filter
// has moved on since.
func (m *model) pickerSearchDone(r pickerSearchMsg) {
	p := m.picker
	if p == nil || r.seq != p.seq {
		return
	}
	p.pending = false
	if r.err != nil {
		p.note = "search: " + r.err.Error()
		return
	}
	sel := ""
	if p.idx < len(p.metas) {
		sel = p.metas[p.idx].ID
	}
	p.hits, p.hitsFor = r.metas, r.query
	m.pickerRebuild()
	for i, meta := range p.metas {
		if meta.ID == sel {
			p.idx = i
		}
	}
	if len(p.metas) > 0 {
		p.loadPreview(m.store)
	}
}

// pickerRebuild recomputes metas from the loaded sessions, the filter and
// the full-text hits.
func (m *model) pickerRebuild() {
	p := m.picker
	terms := strings.Fields(strings.ToLower(p.filter))
	seen := map[string]bool{}
	p.metas = p.metas[:0]
	clear(p.content)
	for _, meta := range p.all {
		if len(terms) == 0 || m.pickerMatch(meta, terms) {
			p.metas = append(p.metas, meta)
			seen[meta.ID] = true
		}
	}
	if len(terms) > 0 && p.hitsFor == strings.TrimSpace(p.filter) {
		for _, meta := range p.hits {
			if seen[meta.ID] || meta.TaskID != "" {
				continue
			}
			seen[meta.ID] = true
			p.content[meta.ID] = true
			p.metas = append(p.metas, meta)
		}
		sort.SliceStable(p.metas, func(i, j int) bool { return p.metas[i].UpdatedAt.After(p.metas[j].UpdatedAt) })
	}
	p.idx = min(max(p.idx, 0), max(len(p.metas)-1, 0))
}

// pickerMatch reports whether every term occurs in the session's title
// (or fallback title), id, directory, model or tags.
func (m *model) pickerMatch(meta session.Meta, terms []string) bool {
	hay := strings.ToLower(strings.Join([]string{meta.Title, m.pickerTitle(meta), meta.ID, meta.CWD, meta.Model, meta.Provider, strings.Join(meta.Tags, " ")}, " "))
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// pickerTitle is the session's title, or for an untitled session the first
// line of its first user message.
func (m *model) pickerTitle(meta session.Meta) string {
	if meta.Title != "" {
		return meta.Title
	}
	p := m.picker
	if t, ok := p.titles[meta.ID]; ok {
		return t
	}
	t := ""
	for _, msg := range m.store.RawMessages(meta.ID) {
		if msg.Role != "user" {
			continue
		}
		if line := firstTextLine(msg.TextContent()); line != "" {
			t = truncLine(line, 80)
			break
		}
	}
	p.titles[meta.ID] = t
	return t
}

func firstTextLine(s string) string {
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// pickerSnippet is the first message line of a content-only hit that holds
// the query.
func (m *model) pickerSnippet(id string) string {
	p := m.picker
	if s, ok := p.snippets[id]; ok {
		return s
	}
	q := strings.ToLower(strings.TrimSpace(p.hitsFor))
	snip := ""
	for _, msg := range m.store.RawMessages(id) {
		for l := range strings.SplitSeq(msg.TextContent(), "\n") {
			low := strings.ToLower(l)
			at := strings.Index(low, q)
			if q == "" || at < 0 {
				continue
			}
			start := max(at-30, 0)
			snip = strings.TrimSpace(l[start:])
			if start > 0 {
				snip = "…" + snip
			}
			snip = msg.Role + ": " + truncLine(snip, 120)
			break
		}
		if snip != "" {
			break
		}
	}
	p.snippets[id] = snip
	return snip
}

// pickerDelete removes a session after the user confirmed it.
func (m *model) pickerDelete(id string) {
	p := m.picker
	if err := m.store.Delete(id); err != nil {
		p.note = "delete: " + err.Error()
		return
	}
	drop := func(list []session.Meta) []session.Meta {
		out := list[:0]
		for _, meta := range list {
			if meta.ID != id {
				out = append(out, meta)
			}
		}
		return out
	}
	p.all, p.hits = drop(p.all), drop(p.hits)
	p.total = max(p.total-1, len(p.all))
	m.pickerRebuild()
	p.note = fmt.Sprintf(m.tr("deleted session %s"), id)
	if len(p.all) == 0 && p.next == nil {
		m.picker = nil
		m.append(dimStyle.Render(fmt.Sprintf(m.tr("deleted session %s"), id)))
		return
	}
	if len(p.metas) > 0 {
		p.loadPreview(m.store)
	}
}

const previewLines = 5

func (m *model) pickerFrame() surfaceFrame {
	p := m.picker
	w, _ := m.surfaceSize()
	f := surfaceFrame{
		title:       m.tr("Resume session"),
		filter:      p.filter,
		placeholder: m.tr("type to filter · searches titles and message text"),
		footer:      m.tr("↑/↓ select · Enter resume · Ctrl+D delete · Esc cancel"),
		note:        p.note,
	}
	total := max(p.total, len(p.all))
	if p.filter != "" {
		f.right = fmt.Sprintf("%d/%d", min(p.idx+1, len(p.metas)), len(p.metas))
	} else {
		f.right = fmt.Sprintf("%d/%d", min(p.idx+1, len(p.metas)), total)
	}
	if p.pending && f.note == "" {
		f.note = m.tr("searching message text…")
	}
	if p.confirm != "" {
		title := p.confirm
		for _, meta := range p.metas {
			if meta.ID == p.confirm {
				title = m.pickerTitle(meta)
			}
		}
		f.confirm = fmt.Sprintf(m.tr("delete session \"%s\"? y to confirm, any other key cancels"), truncLine(title, 40))
	}
	for i, meta := range p.metas {
		if i == p.idx {
			f.selLine = len(f.list)
		}
		f.list = append(f.list, m.pickerRow(meta, w, i == p.idx))
	}
	if len(p.metas) == 0 {
		empty := "no sessions match"
		if p.pending {
			empty = "searching message text…"
		}
		f.list = append(f.list, "  "+textMutedStyle.Render(chromeGlyphs(m.tr(empty))))
	}
	if p.filter == "" && p.next != nil {
		f.list = append(f.list, "  "+textHintStyle.Render(chromeGlyphs(m.tr("↓ older sessions load as you scroll"))))
	}
	if len(p.metas) > 0 {
		f.preview = m.pickerPreview(p.metas[p.idx], w)
	}
	return f
}

// pickerRow is one session: title on the left, age and model on the right.
func (m *model) pickerRow(meta session.Meta, w int, selected bool) string {
	title := m.pickerTitle(meta)
	untitled := title == ""
	if untitled {
		title = m.tr("(untitled)")
	}
	title = chromeGlyphs(strings.Join(strings.Fields(title), " "))
	right := ago(meta.UpdatedAt)
	if meta.Model != "" && w >= 60 {
		right += " · " + meta.Model
	}
	if m.picker.content[meta.ID] {
		right = m.tr("text match") + " · " + right
	}
	if meta.ID == m.sessionID {
		right = m.tr("(current)") + " · " + right
	}
	right = chromeGlyphs(right)
	lead := "  "
	if selected {
		lead = glyphs.User + " "
	}
	rightW := lipgloss.Width(right)
	titleW := max(w-lipgloss.Width(lead)-rightW-3, 8)
	title = ansi.Truncate(title, titleW, ellipsis())
	gap := max(w-lipgloss.Width(lead)-lipgloss.Width(title)-rightW-1, 1)
	if selected {
		line := ansi.Truncate(lead+title+strings.Repeat(" ", gap)+right, max(w-1, 1), ellipsis())
		return optionSelStyle.Render(line + strings.Repeat(" ", max(w-1-lipgloss.Width(line), 0)))
	}
	ts := title
	if untitled {
		ts = textMutedStyle.Render(title)
	}
	return ansi.Truncate(lead+ts+strings.Repeat(" ", gap)+textHintStyle.Render(right), w, ellipsis())
}

// pickerPreview is the selected session's detail: where and what it ran,
// the matching text of a search hit, and its last exchange.
func (m *model) pickerPreview(meta session.Meta, w int) []string {
	p := m.picker
	info := meta.ID
	if meta.Model != "" {
		info += " · " + meta.Model + "@" + meta.Provider
	}
	if meta.CWD != "" {
		info += " · " + meta.CWD
	}
	if len(meta.Tags) > 0 {
		info += " · #" + strings.Join(meta.Tags, " #")
	}
	out := []string{textMutedStyle.Render(chromeGlyphs(info))}
	if p.content[meta.ID] {
		if s := m.pickerSnippet(meta.ID); s != "" {
			out = append(out, accentFgStyle.Render(chromeGlyphs(truncLine(s, max(w-4, 8)))))
		}
	}
	prev := p.previews[meta.ID]
	out = append(out, previewBlock(youStyle.Render(glyphUser), prev[0], w)...)
	out = append(out, previewBlock(botStyle.Render(glyphAssistant), prev[1], w)...)
	return out
}

// pickerListBudget is how many list rows the picker has room for.
func (m *model) pickerListBudget() int {
	_, h := m.surfaceSize()
	return m.pickerFrame().listBudget(h)
}

func (m *model) pickerView() string {
	w, h := m.surfaceSize()
	return m.pickerFrame().render(w, h)
}

func previewBlock(prefix, text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	w := max(width-6, 8)
	var lines []string
	for i, l := range strings.Split(text, "\n") {
		wrapped := strings.Split(ansi.Wrap(l, w, ""), "\n")
		for j, wl := range wrapped {
			if i == 0 && j == 0 {
				lines = append(lines, prefix+wl)
			} else {
				lines = append(lines, "  "+wl)
			}
		}
	}
	if len(lines) > previewLines {
		lines = append(lines[:previewLines],
			dimStyle.Render(chromeGlyphs(fmt.Sprintf("  … +%d lines (full text after resume)", len(lines)-previewLines))))
	}
	return lines
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
