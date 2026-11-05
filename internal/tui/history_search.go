package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/session"
)

// historySearch is the Ctrl+R overlay: the composer holds the query and a
// panel under it lists the prompts that match, best first. It searches the
// prompts written in this working directory, or in every directory after Tab.
type historySearch struct {
	all     bool
	cwd     string
	entries []histEntry
	loading bool
	matches []histMatch
	sel     int
	// draft is the composer as it was before the search, put back on Esc.
	draft composerSnap
}

// histEntry is one prompt from the history. An empty cwd means the directory
// is not known, which only the "all directories" scope shows.
type histEntry struct {
	text string
	cwd  string
	at   time.Time
}

type histMatch struct {
	idx   int
	pos   map[int]bool
	score int
}

const (
	historyRows       = 8
	historyLoadLimit  = 5000
	historyMatchLimit = 500
)

// historyCwd is the directory history search scopes to.
func (m *model) historyCwd() string {
	dir := ""
	if m.agent != nil {
		dir = m.agent.WorkingDir
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	return filepath.Clean(dir)
}

// openHistorySearch opens the overlay over the current draft. The session
// store is read in the background; until it answers the overlay searches what
// this run already holds.
func (m *model) openHistorySearch() tea.Cmd {
	c := m.composer()
	hs := &historySearch{cwd: m.historyCwd(), draft: m.composerSnap(), loading: m.store != nil}
	known := ""
	if m.store == nil {
		known = hs.cwd
	}
	hs.entries = sessionHistEntries(m.hist, nil, known)
	c.hs = hs
	m.menu = nil
	m.input.Reset()
	m.growInput()
	m.refilterHistory()
	if m.store == nil {
		return nil
	}
	st := m.store
	return func() tea.Msg {
		entries, err := st.UserHistoryEntries(historyLoadLimit)
		return viewProbe{fn: func(m *model) { m.historyLoaded(entries, err) }}
	}
}

// sessionHistEntries merges the store's entries with the prompts this run
// holds in hist (oldest first). Prompts of this run the store does not have
// yet are the newest and belong to cwd; the rest keep the store's directory.
func sessionHistEntries(hist []string, stored []session.UserHistoryEntry, cwd string) []histEntry {
	inStore := map[string]bool{}
	for _, e := range stored {
		inStore[e.Text] = true
	}
	var out []histEntry
	seen := map[string]bool{}
	for i := len(hist) - 1; i >= 0; i-- {
		t := strings.TrimSpace(hist[i])
		if t == "" || inStore[t] || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, histEntry{text: t, cwd: cwd})
	}
	for _, e := range stored {
		out = append(out, histEntry{text: e.Text, cwd: filepath.Clean(e.CWD), at: e.At})
	}
	return out
}

func (m *model) historyLoaded(entries []session.UserHistoryEntry, err error) {
	if m.cmp == nil || m.cmp.hs == nil {
		return
	}
	hs := m.cmp.hs
	hs.loading = false
	if err != nil {
		return
	}
	hs.entries = sessionHistEntries(m.hist, entries, hs.cwd)
	m.refilterHistory()
}

// refilterHistory matches the query against the entries in scope. In the
// all-directories scope a prompt written in several directories is listed
// once.
func (m *model) refilterHistory() {
	hs := m.cmp.hs
	query := strings.Join(strings.Fields(m.input.Value()), " ")
	hs.matches = hs.matches[:0]
	seen := map[string]bool{}
	for i, e := range hs.entries {
		if !hs.all && e.cwd != hs.cwd {
			continue
		}
		if seen[e.text] {
			continue
		}
		score, pos, ok := fuzzyMatch(e.text, query)
		if !ok {
			continue
		}
		seen[e.text] = true
		hs.matches = append(hs.matches, histMatch{idx: i, pos: pos, score: score})
		if query == "" && len(hs.matches) >= historyMatchLimit {
			break
		}
	}
	if query != "" {
		sort.SliceStable(hs.matches, func(a, b int) bool { return hs.matches[a].score > hs.matches[b].score })
		if len(hs.matches) > historyMatchLimit {
			hs.matches = hs.matches[:historyMatchLimit]
		}
	}
	hs.sel = 0
}

// fuzzyMatch scores text against query, word by word and ignoring case.
// Every word must occur, as a run of characters or at least in order; a run
// scores higher than a scatter, and a run that starts a word higher still.
// pos is the set of matched rune positions, for highlighting.
func fuzzyMatch(text, query string) (int, map[int]bool, bool) {
	if query == "" {
		return 0, nil, true
	}
	lower := []rune(strings.ToLower(text))
	pos := map[int]bool{}
	score := 0
	for word := range strings.FieldsSeq(strings.ToLower(query)) {
		q := []rune(word)
		if at := runeIndex(lower, q); at >= 0 {
			s := 1000 - min(at, 200)
			if at == 0 || !unicode.IsLetter(lower[at-1]) && !unicode.IsDigit(lower[at-1]) {
				s += 300
			}
			score += s
			for k := range q {
				pos[at+k] = true
			}
			continue
		}
		j, gaps, last := 0, 0, -1
		var hit []int
		for i := 0; i < len(lower) && j < len(q); i++ {
			if lower[i] == q[j] {
				if last >= 0 && i != last+1 {
					gaps++
				}
				hit = append(hit, i)
				last = i
				j++
			}
		}
		if j < len(q) {
			return 0, nil, false
		}
		score += max(500-gaps*25-hit[0], 1)
		for _, h := range hit {
			pos[h] = true
		}
	}
	return score, pos, true
}

func runeIndex(s, sub []rune) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for k := range sub {
			if s[i+k] != sub[k] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// historySearchKey drives the overlay. Typing edits the query in the
// composer; the list keys move the selection; Enter takes the selected prompt
// into the composer and Esc puts the draft back.
func (m *model) historySearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	hs := m.cmp.hs
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC, tea.KeyCtrlG:
		m.closeHistorySearch(false)
		return m, nil
	case tea.KeyEnter:
		m.closeHistorySearch(len(hs.matches) > 0)
		return m, nil
	case tea.KeyTab:
		hs.all = !hs.all
		m.refilterHistory()
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyCtrlR:
		if len(hs.matches) > 0 {
			hs.sel = min(hs.sel+1, len(hs.matches)-1)
		}
		return m, nil
	case tea.KeyUp, tea.KeyCtrlP:
		hs.sel = max(hs.sel-1, 0)
		return m, nil
	case tea.KeyPgDown:
		if len(hs.matches) > 0 {
			hs.sel = min(hs.sel+historyRows, len(hs.matches)-1)
		}
		return m, nil
	case tea.KeyPgUp:
		hs.sel = max(hs.sel-historyRows, 0)
		return m, nil
	case tea.KeyCtrlJ:
		return m, nil
	}
	before := m.input.Value()
	var cmd tea.Cmd
	if msg.Paste {
		m.input.InsertString(strings.Join(strings.Fields(string(msg.Runes)), " "))
	} else {
		m.input, cmd = m.input.Update(msg)
	}
	if m.input.Value() != before {
		m.refilterHistory()
	}
	return m, cmd
}

// closeHistorySearch closes the overlay. Accepting puts the selected prompt
// in the composer, and the draft it replaced stays one Ctrl+Z away; otherwise
// the draft comes back as it was.
func (m *model) closeHistorySearch(accept bool) {
	c := m.composer()
	hs := c.hs
	c.hs = nil
	if accept && hs.sel < len(hs.matches) {
		text := hs.entries[hs.matches[hs.sel].idx].text
		if strings.TrimSpace(hs.draft.val) != "" && hs.draft.val != text {
			c.pushUndo(hs.draft)
			c.redo = nil
		}
		m.setComposer(text, len([]rune(text)))
		m.histIdx = len(m.hist)
	} else {
		m.restoreSnap(hs.draft)
	}
	c.resync(m)
	m.refreshMenu()
}

// historyPanelRows is the overlay's height below the composer, its separator
// included.
func (m *model) historyPanelRows() int {
	if m.cmp == nil || m.cmp.hs == nil {
		return 0
	}
	return lipgloss.Height(m.historyPanelView()) + 1
}

// historyPanelView renders the overlay: a header with the scope and count,
// up to historyRows matches with the matched characters picked out, and the
// keys.
func (m *model) historyPanelView() string {
	hs := m.cmp.hs
	width := max(m.width, 1)
	scope := m.tr("this directory")
	other := m.tr("Tab all directories")
	if hs.all {
		scope, other = m.tr("all directories"), m.tr("Tab this directory")
	}
	title := m.tr("history") + " · " + scope
	if hs.loading {
		title += " · " + m.tr("loading…")
	}
	count := fmt.Sprintf("%d", len(hs.matches))
	if len(hs.matches) > 0 {
		count = fmt.Sprintf("%d/%d", hs.sel+1, len(hs.matches))
	}
	var b strings.Builder
	b.WriteString(panelHeader(chromeGlyphs(title), count, width) + "\n")

	if len(hs.matches) == 0 {
		msg := m.tr("no matching prompts")
		if !hs.all {
			msg += " · " + other
		}
		b.WriteString(dimStyle.Render(ansi.Truncate("  "+chromeGlyphs(msg), width, ellipsisGlyph())) + "\n")
	}
	start := max(hs.sel-historyRows+1, 0)
	end := min(start+historyRows, len(hs.matches))
	for i := start; i < end; i++ {
		mt := hs.matches[i]
		e := hs.entries[mt.idx]
		meta := relAge(e.at, m.nowFn())
		if hs.all && e.cwd != "" && e.cwd != hs.cwd {
			meta = strings.TrimSpace(filepath.Base(e.cwd) + " " + meta)
		}
		b.WriteString(m.historyRow(e.text, mt.pos, meta, i == hs.sel, width) + "\n")
	}
	b.WriteString(dimStyle.Render(fitHints("  "+chromeGlyphs(m.tr("Enter use")), []string{m.tr("Esc cancel"), other, m.tr("↑/↓ select")}, width)))
	return b.String()
}

// historyRow is one match: the prompt on one line with the matched
// characters in the accent, and the age (and directory) on the right.
func (m *model) historyRow(text string, pos map[int]bool, meta string, selected bool, width int) string {
	const lead = 2
	metaW := 0
	if meta != "" && width >= 30 {
		metaW = lipgloss.Width(meta) + 2
	}
	room := max(width-lead-metaW, 1)
	runes := []rune(text)
	var body strings.Builder
	used := 0
	plain := func(r rune) string {
		if r == '\n' || r == '\t' || r == '\r' {
			return " "
		}
		return string(r)
	}
	total := 0
	for _, r := range runes {
		total += lipgloss.Width(plain(r))
	}
	limit := room
	if total > room {
		limit = max(room-lipgloss.Width(ellipsisGlyph()), 0)
	}
	for i, r := range runes {
		s := plain(r)
		w := lipgloss.Width(s)
		if used+w > limit {
			body.WriteString(dimStyle.Render(ellipsisGlyph()))
			used += lipgloss.Width(ellipsisGlyph())
			break
		}
		if pos[i] {
			s = accentFgStyle.Bold(true).Render(s)
		}
		body.WriteString(s)
		used += w
	}
	line := body.String()
	if metaW > 0 {
		line += strings.Repeat(" ", max(width-lead-used-metaW, 0)+2) + dimStyle.Render(meta)
	}
	if selected {
		return ansi.Truncate(selectedRowStyle.Width(width).Render(menuMark()+line), width, "")
	}
	return ansi.Truncate("  "+line, width, "")
}

// panelHeader is a panel's title row with a right-aligned note.
func panelHeader(title, note string, width int) string {
	left := statusTitleStyle.Render(ansi.Truncate("  "+title, max(width-lipgloss.Width(note)-3, 1), ellipsisGlyph()))
	gap := width - lipgloss.Width(left) - lipgloss.Width(note) - 1
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + dimStyle.Render(note)
}

// relAge is a short age such as "5m", "3h" or "2d"; empty for an unknown time.
func relAge(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return fmt.Sprintf("%dy", int(d.Hours()/(24*365)))
}
