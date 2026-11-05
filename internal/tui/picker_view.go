package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The app surfaces (keyboard cheatsheet, settings, extensions, memory) are
// full-screen modals built from one list model: optional tabs, a filter line,
// a sectioned list with one selected row, an optional preview pane for the
// selected row, and a footer of key hints. The session picker draws through
// the same frame so every picker looks and moves alike.

// appSurfaceState is the app-surfaces area's state on the model.
type appSurfaceState struct {
	// modal is the open surface, or nil.
	modal *surfaceModal
}

// surfaceOpen reports whether an app-surface modal covers the screen.
func (m *model) surfaceOpen() bool { return m.app != nil && m.app.modal != nil }

// surfaceRow is one selectable row of a surface.
type surfaceRow struct {
	// section groups rows under a muted heading; rows of one section are
	// contiguous.
	section string
	// key is the left column: a key chord, a setting or a server name.
	key  string
	desc string
	// value is the right-aligned state, e.g. "on" or "ready · 4 tools".
	value string
	// muted draws the row dim: disabled servers, done memories.
	muted bool
	// search is extra text the filter matches but the row does not show.
	search string
	// preview is the selected row's detail pane.
	preview func(m *model) []string
	// enter runs on Enter; step on ←/→ (delta -1 or +1). Either may be nil.
	enter func(m *model) tea.Cmd
	step  func(m *model, delta int)
	// alt runs on Ctrl+R: reconnect a server, reload plugins.
	alt func(m *model)
}

// surfaceTab is one tab of a surface; a surface with one tab draws no tab row.
type surfaceTab struct {
	name  string
	rows  func(m *model) []surfaceRow
	empty string
	// hints is the footer for this tab.
	hints string
}

// surfaceModal is an open surface.
type surfaceModal struct {
	kind   string
	title  string
	tabs   []surfaceTab
	tab    int
	filter string
	sel    int
	rows   []surfaceRow
	// note is the result of the last action, shown above the footer.
	note string
}

func (m *model) ensureApp() *appSurfaceState {
	if m.app == nil {
		m.app = &appSurfaceState{}
	}
	return m.app
}

// openSurface shows s with its first matching tab selected.
func (m *model) openSurface(s *surfaceModal) {
	m.ensureApp().modal = s
	m.menu = nil
	s.refresh(m)
}

func (m *model) closeSurface() {
	if m.app != nil {
		m.app.modal = nil
	}
}

// refresh rebuilds the visible rows from the current tab and filter, keeping
// the selection on the same row when it is still listed.
func (s *surfaceModal) refresh(m *model) {
	prev := ""
	if s.sel >= 0 && s.sel < len(s.rows) {
		prev = s.rows[s.sel].section + "\x00" + s.rows[s.sel].key
	}
	var all []surfaceRow
	if s.tab < len(s.tabs) && s.tabs[s.tab].rows != nil {
		all = s.tabs[s.tab].rows(m)
	}
	terms := strings.Fields(strings.ToLower(s.filter))
	s.rows = s.rows[:0]
	for _, r := range all {
		if surfaceMatch(terms, r) {
			s.rows = append(s.rows, r)
		}
	}
	s.sel = min(max(s.sel, 0), max(len(s.rows)-1, 0))
	for i, r := range s.rows {
		if r.section+"\x00"+r.key == prev {
			s.sel = i
			break
		}
	}
}

// surfaceMatch reports whether every filter term occurs in the row.
func surfaceMatch(terms []string, r surfaceRow) bool {
	if len(terms) == 0 {
		return true
	}
	hay := strings.ToLower(r.section + " " + r.key + " " + r.desc + " " + r.value + " " + r.search)
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

func (s *surfaceModal) selected() *surfaceRow {
	if s.sel < 0 || s.sel >= len(s.rows) {
		return nil
	}
	return &s.rows[s.sel]
}

func (s *surfaceModal) switchTab(m *model, delta int) {
	if len(s.tabs) < 2 {
		return
	}
	s.tab = (s.tab + delta + len(s.tabs)) % len(s.tabs)
	s.sel, s.note = 0, ""
	s.refresh(m)
}

// surfaceKey handles a key while a surface is open.
func (m *model) surfaceKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.app.modal
	page := max(m.surfaceListRows()-1, 1)
	switch msg.Type {
	case tea.KeyCtrlC:
		m.closeSurface()
	case tea.KeyEsc:
		if s.filter != "" {
			s.filter = ""
			s.refresh(m)
			break
		}
		m.closeSurface()
	case tea.KeyUp, tea.KeyCtrlP:
		s.sel = max(s.sel-1, 0)
	case tea.KeyDown, tea.KeyCtrlN:
		s.sel = min(s.sel+1, max(len(s.rows)-1, 0))
	case tea.KeyPgUp:
		s.sel = max(s.sel-page, 0)
	case tea.KeyPgDown:
		s.sel = min(s.sel+page, max(len(s.rows)-1, 0))
	case tea.KeyHome:
		s.sel = 0
	case tea.KeyEnd:
		s.sel = max(len(s.rows)-1, 0)
	case tea.KeyTab:
		s.switchTab(m, 1)
	case tea.KeyShiftTab:
		s.switchTab(m, -1)
	case tea.KeyLeft, tea.KeyRight:
		delta := 1
		if msg.Type == tea.KeyLeft {
			delta = -1
		}
		if r := s.selected(); r != nil && r.step != nil {
			r.step(m, delta)
			if m.surfaceOpen() {
				m.app.modal.refresh(m)
			}
		} else {
			s.switchTab(m, delta)
		}
	case tea.KeyEnter:
		r := s.selected()
		if r == nil {
			break
		}
		var cmd tea.Cmd
		switch {
		case r.enter != nil:
			cmd = r.enter(m)
		case r.step != nil:
			r.step(m, 1)
		}
		if m.surfaceOpen() {
			m.app.modal.refresh(m)
		}
		return m, cmd
	case tea.KeyCtrlR:
		if r := s.selected(); r != nil && r.alt != nil {
			r.alt(m)
			if m.surfaceOpen() {
				m.app.modal.refresh(m)
			}
		}
	case tea.KeyBackspace:
		if s.filter != "" {
			_, size := lastRune(s.filter)
			s.filter = s.filter[:len(s.filter)-size]
			s.refresh(m)
		}
	case tea.KeyRunes, tea.KeySpace:
		s.filter += string(msg.Runes)
		if msg.Type == tea.KeySpace && len(msg.Runes) == 0 {
			s.filter += " "
		}
		s.sel = 0
		s.refresh(m)
	}
	return m, nil
}

// lastRune is the final rune of s and its byte size.
func lastRune(s string) (rune, int) {
	rs := []rune(s)
	if len(rs) == 0 {
		return 0, 0
	}
	r := rs[len(rs)-1]
	return r, len(string(r))
}

// surfaceMouse turns the wheel into selection moves; clicks are ignored so
// they never reach the hidden transcript.
func (m *model) surfaceMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	s := m.app.modal
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		s.sel = max(s.sel-1, 0)
	case tea.MouseButtonWheelDown:
		s.sel = min(s.sel+1, max(len(s.rows)-1, 0))
	}
	return m, nil
}

// surfaceMsg routes the area's asynchronous messages. It reports false for
// anything else so Update carries on.
func (m *model) surfaceMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case pickerSearchTick:
		return m, m.pickerSearchFire(msg), true
	case pickerSearchMsg:
		m.pickerSearchDone(msg)
		return m, nil, true
	case tea.MouseMsg:
		if m.surfaceOpen() {
			r, cmd := m.surfaceMouse(msg)
			return r, cmd, true
		}
		if m.picker != nil {
			m.pickerMouse(msg)
			return m, nil, true
		}
	}
	return m, nil, false
}

// surfaceSize is the screen the modal draws on; before the first size
// message it assumes 80×24.
func (m *model) surfaceSize() (int, int) {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	return w, h
}

// surfaceListRows is how many list lines the open surface has room for.
func (m *model) surfaceListRows() int {
	if !m.surfaceOpen() {
		return 1
	}
	_, h := m.surfaceSize()
	s := m.app.modal
	f := m.surfaceFrameFor(s)
	return f.listBudget(h)
}

// surfaceView draws the open surface.
func (m *model) surfaceView() string {
	w, h := m.surfaceSize()
	return m.surfaceFrameFor(m.app.modal).render(w, h)
}

func (m *model) surfaceFrameFor(s *surfaceModal) surfaceFrame {
	w, _ := m.surfaceSize()
	f := surfaceFrame{
		title:       m.tr(s.title),
		filter:      s.filter,
		placeholder: m.tr("type to filter"),
		note:        s.note,
	}
	if len(s.tabs) > 1 {
		for _, t := range s.tabs {
			f.tabs = append(f.tabs, m.tr(t.name))
		}
		f.tab = s.tab
	}
	tab := surfaceTab{}
	if s.tab < len(s.tabs) {
		tab = s.tabs[s.tab]
	}
	if len(s.rows) > 0 {
		f.right = strings.TrimSpace(strconv.Itoa(s.sel+1) + "/" + strconv.Itoa(len(s.rows)))
	}
	keyW := 0
	for _, r := range s.rows {
		keyW = max(keyW, lipgloss.Width(chromeGlyphs(m.tr(r.key))))
	}
	keyW = min(keyW, max(w/3, 8))
	section := ""
	for i, r := range s.rows {
		if r.section != section {
			section = r.section
			if section != "" {
				if len(f.list) > 0 {
					f.list = append(f.list, "")
					f.items = append(f.items, false)
				}
				f.list = append(f.list, "  "+textMutedStyle.Render(chromeGlyphs(m.tr(section))))
				f.items = append(f.items, false)
			}
		}
		if i == s.sel {
			f.selLine = len(f.list)
		}
		f.list = append(f.list, surfaceRowLine(m, r, keyW, w, i == s.sel))
		f.items = append(f.items, true)
	}
	if len(s.rows) == 0 {
		empty := tab.empty
		if s.filter != "" || empty == "" {
			empty = "no matches"
		}
		f.list = append(f.list, "  "+textMutedStyle.Render(chromeGlyphs(m.tr(empty))))
		f.items = append(f.items, false)
	}
	if r := s.selected(); r != nil && r.preview != nil {
		f.preview = r.preview(m)
	}
	hints := tab.hints
	if hints == "" {
		hints = "↑/↓ select · Enter apply · Esc close"
	}
	hints = m.tr(hints)
	if len(s.tabs) > 1 {
		hints = m.tr("Tab switch tab · ") + hints
	}
	f.footer = hints
	return f
}

// surfaceRowLine is one list row: a caret on the selected row, the key
// column, the description, and the value flush right.
func surfaceRowLine(m *model, r surfaceRow, keyW, w int, selected bool) string {
	key := chromeGlyphs(m.tr(r.key))
	key = ansi.Truncate(key, keyW, ellipsis())
	key += strings.Repeat(" ", max(keyW-lipgloss.Width(key), 0))
	desc := chromeGlyphs(m.tr(r.desc))
	value := chromeGlyphs(r.value)
	lead := "  "
	if selected {
		lead = glyphs.User + " "
	}
	valueW := lipgloss.Width(value)
	descW := max(w-lipgloss.Width(lead)-keyW-2-valueW-2, 0)
	desc = ansi.Truncate(desc, descW, ellipsis())
	gap := max(w-lipgloss.Width(lead)-keyW-2-lipgloss.Width(desc)-valueW-1, 1)
	if value == "" {
		gap = 0
	}
	if selected {
		line := lead + key + "  " + desc + strings.Repeat(" ", gap) + value
		line = ansi.Truncate(line, max(w-1, 1), ellipsis())
		return optionSelStyle.Render(line + strings.Repeat(" ", max(w-1-lipgloss.Width(line), 0)))
	}
	keyStyled := key
	descStyled := textSecondaryStyle.Render(desc)
	valueStyled := textHintStyle.Render(value)
	if r.muted {
		keyStyled, descStyled, valueStyled = textMutedStyle.Render(key), textMutedStyle.Render(desc), textMutedStyle.Render(value)
	}
	line := lead + keyStyled + "  " + descStyled + strings.Repeat(" ", gap) + valueStyled
	return ansi.Truncate(line, w, ellipsis())
}

// ellipsis is the truncation tail for the active glyph set.
func ellipsis() string { return chromeGlyphs("…") }

// surfaceFrame is the shared full-screen picker layout.
type surfaceFrame struct {
	title string
	// right is shown flush right on the title row (a count or position).
	right       string
	tabs        []string
	tab         int
	filter      string
	placeholder string
	// list is pre-rendered list lines; selLine is the selected one, kept in
	// view when the list scrolls.
	list    []string
	selLine int
	// items marks the list lines that are entries, for the "N more"
	// markers; nil means every line is one.
	items []bool
	// preview is the selected row's detail, drawn under the list when there
	// is room.
	preview []string
	note    string
	footer  string
	// confirm replaces the footer with a question, e.g. a delete prompt.
	confirm string
}

// headerRows is the title, the optional tab row, the filter line and a rule.
func (f surfaceFrame) headerRows() int {
	n := 3
	if len(f.tabs) > 1 {
		n++
	}
	return n
}

// previewRows is how many preview lines fit on a screen h rows tall.
func (f surfaceFrame) previewRows(h int) int {
	if len(f.preview) == 0 {
		return 0
	}
	room := h - f.headerRows() - f.footerRows() - 4
	if room < 6 {
		return 0
	}
	return min(len(f.preview), max(room/3, 3), 12)
}

func (f surfaceFrame) footerRows() int {
	n := 2
	if f.note != "" {
		n++
	}
	return n
}

// listBudget is how many list lines fit on a screen h rows tall.
func (f surfaceFrame) listBudget(h int) int {
	used := f.headerRows() + f.footerRows()
	if p := f.previewRows(h); p > 0 {
		used += p + 1
	}
	return max(h-used, 1)
}

// render draws the frame at exactly h rows of at most w cells.
func (f surfaceFrame) render(w, h int) string {
	rule := chromeRuleStyle.Render(strings.Repeat(glyphs.Border.Top, max(w, 1)))
	fit := func(s string) string { return ansi.Truncate(s, w, ellipsis()) }
	var out []string

	title := accentFgStyle.Render(glyphs.User) + " " + cardTitleStyle.Render(chromeGlyphs(f.title))
	right := textHintStyle.Render(chromeGlyphs(f.right))
	gap := w - lipgloss.Width(title) - lipgloss.Width(right) - 1
	if f.right == "" || gap < 2 {
		out = append(out, fit(title))
	} else {
		out = append(out, title+strings.Repeat(" ", gap)+right)
	}

	if len(f.tabs) > 1 {
		var parts []string
		for i, t := range f.tabs {
			t = chromeGlyphs(t)
			if i == f.tab {
				parts = append(parts, accentStyle.Render("["+t+"]"))
			} else {
				parts = append(parts, textMutedStyle.Render(" "+t+" "))
			}
		}
		out = append(out, fit("  "+strings.Join(parts, " ")))
	}

	filter := "  " + accentFgStyle.Render("/") + " "
	if f.filter == "" {
		filter += textHintStyle.Render(chromeGlyphs(f.placeholder))
	} else {
		filter += f.filter + textMutedStyle.Render(filterCaret())
	}
	out = append(out, fit(filter), rule)

	budget := f.listBudget(h)
	lo, hi := viewportWindow(len(f.list), f.selLine, budget)
	body := append([]string(nil), f.list[lo:hi]...)
	// The markers count the hidden entries, not the hidden lines: section
	// headings and the blank lines between sections are not entries.
	hidden := func(from, to int) int {
		n := 0
		for i := from; i < to; i++ {
			if f.items == nil || f.items[i] {
				n++
			}
		}
		return n
	}
	if lo > 0 && len(body) > 0 {
		body[0] = textHintStyle.Render(chromeGlyphs("  ↑ " + strconv.Itoa(hidden(0, lo+1)) + " more"))
	}
	if hi < len(f.list) && len(body) > 1 {
		body[len(body)-1] = textHintStyle.Render(chromeGlyphs("  ↓ " + strconv.Itoa(hidden(hi-1, len(f.list))) + " more"))
	}
	for _, l := range body {
		out = append(out, fit(l))
	}
	for range budget - len(body) {
		out = append(out, "")
	}

	if p := f.previewRows(h); p > 0 {
		out = append(out, rule)
		for _, l := range f.preview[:p] {
			out = append(out, fit("  "+l))
		}
	}

	if f.note != "" {
		out = append(out, fit("  "+textSecondaryStyle.Render(chromeGlyphs(f.note))))
	}
	out = append(out, rule)
	if f.confirm != "" {
		out = append(out, fit("  "+warnStyle.Bold(true).Render(chromeGlyphs(f.confirm))))
	} else {
		out = append(out, fit("  "+textHintStyle.Render(chromeGlyphs(f.footer))))
	}
	if len(out) > h {
		out = out[len(out)-h:]
	}
	// Notes and previews carry text from many places; one last pass keeps
	// ASCII mode whole, re-cut in case a glyph grew into several characters.
	if asciiMode {
		for i, l := range out {
			out[i] = fit(chromeGlyphs(l))
		}
	}
	return strings.Join(out, "\n")
}

// wrapPreview wraps text for a preview pane w cells wide.
func wrapPreview(text string, w int) []string {
	var out []string
	for l := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		out = append(out, strings.Split(ansi.Wrap(l, max(w-4, 8), ""), "\n")...)
	}
	return out
}

// filterCaret marks the end of a filter being typed.
func filterCaret() string {
	if asciiMode {
		return "_"
	}
	return "▏"
}
