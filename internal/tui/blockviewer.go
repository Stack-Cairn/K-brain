package tui

// The block viewer: one transcript block full screen, in its open form (a
// tool's whole output, a thinking run's whole text), scrolled on its own and
// searchable. v opens it on the focused block; Esc returns to the
// transcript with focus where it was.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// blockViewer is the open viewer.
type blockViewer struct {
	uid uint64
	// The block's rows as of width and the text and live tail they were
	// drawn from: a block still streaming redraws as it grows.
	width      int
	text, live string
	rows       []string
	off        int
	// The search: typing a query into draft, or query set with the match
	// rows found for it and the one last jumped to.
	typing bool
	draft  string
	query  string
	hits   []int
	cur    int
}

// openBlockViewer opens block bi full screen.
func (m *model) openBlockViewer(bi int) {
	m.txs().viewer = &blockViewer{uid: m.blocks[bi].uid, width: -1, cur: -1}
	m.sel = nil
}

// viewerBlock is the block the viewer shows, nil once it has gone.
func (m *model) viewerBlock() *block {
	v := m.txs().viewer
	if v == nil {
		return nil
	}
	if bi := m.blockByUID(v.uid); bi >= 0 {
		return &m.blocks[bi]
	}
	return nil
}

// viewerBodyRows is how many content rows fit between the title and the
// footer.
func (m *model) viewerBodyRows() int { return max(m.height-2, 1) }

// syncViewer redraws the viewer's rows when the block or the width changed.
func (m *model) syncViewer() *blockViewer {
	v := m.txs().viewer
	b := m.viewerBlock()
	if v == nil || b == nil {
		m.txs().viewer = nil
		return nil
	}
	w := max(m.width, minRenderWidth)
	if v.width == w && v.text == b.text && v.live == b.live && v.rows != nil {
		return v
	}
	open := *b
	open.expanded, open.folded = true, false
	open.md, open.rows, open.plain, open.stale = nil, nil, nil, true
	if open.thought != nil {
		t := *open.thought
		t.live = false
		open.thought = &t
	}
	v.rows = open.linesAt(w, false)
	v.width, v.text, v.live = w, b.text, b.live
	v.off = min(v.off, v.maxOff(m.viewerBodyRows()))
	if v.query != "" {
		v.hits = viewerHits(v.rows, v.query)
	}
	return v
}

func (v *blockViewer) maxOff(h int) int { return max(len(v.rows)-h, 0) }

func (v *blockViewer) scroll(d, h int) { v.off = max(0, min(v.off+d, v.maxOff(h))) }

// viewerHits is the rows q matches.
func viewerHits(rows []string, q string) []int {
	match := matcher(q)
	var hits []int
	for i, r := range rows {
		if match(ansi.Strip(r)) {
			hits = append(hits, i)
		}
	}
	return hits
}

// blockViewerKey handles a key while the viewer is open.
func (m *model) blockViewerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.syncViewer()
	if v == nil {
		return m, nil
	}
	h := m.viewerBodyRows()
	if v.typing {
		switch msg.Type {
		case tea.KeyEsc:
			v.typing = false
		case tea.KeyEnter:
			v.typing = false
			v.query = v.draft
			v.hits, v.cur = nil, -1
			if v.query != "" {
				v.hits = viewerHits(v.rows, v.query)
				v.findStep(1, h)
			}
		case tea.KeyBackspace:
			if r := []rune(v.draft); len(r) > 0 {
				v.draft = string(r[:len(r)-1])
			}
		case tea.KeySpace:
			v.draft += " "
		case tea.KeyRunes:
			v.draft += string(msg.Runes)
		}
		return m, nil
	}
	k := msg.String()
	switch {
	case msg.Type == tea.KeyEsc || k == "q":
		m.txs().viewer = nil
		if bi := m.focusIndex(); bi >= 0 {
			m.revealBlock(bi)
		}
	case msg.Type == tea.KeyDown || k == "j":
		v.scroll(1, h)
	case msg.Type == tea.KeyUp || k == "k":
		v.scroll(-1, h)
	case msg.Type == tea.KeyPgDown || msg.Type == tea.KeySpace:
		v.scroll(h, h)
	case msg.Type == tea.KeyPgUp:
		v.scroll(-h, h)
	case msg.Type == tea.KeyHome || k == "g":
		v.off = 0
	case msg.Type == tea.KeyEnd || k == "G":
		v.off = v.maxOff(h)
	case k == "/":
		v.typing, v.draft = true, ""
	case k == "n":
		v.findStep(1, h)
	case k == "N":
		v.findStep(-1, h)
	}
	return m, nil
}

// findStep moves to the next match row down (d > 0) or up, wrapping, and
// scrolls it a third of the way down the window.
func (v *blockViewer) findStep(d, h int) {
	if len(v.hits) == 0 {
		v.cur = -1
		return
	}
	switch {
	case v.cur < 0 && d > 0:
		// The first search lands on the first match at or below the top.
		v.cur = 0
		for i, r := range v.hits {
			if r >= v.off {
				v.cur = i
				break
			}
		}
	case v.cur < 0:
		v.cur = len(v.hits) - 1
	default:
		v.cur = (v.cur + d + len(v.hits)) % len(v.hits)
	}
	if r := v.hits[v.cur]; r < v.off || r >= v.off+h {
		v.off = r - h/3
		v.scroll(0, h)
	}
}

// transcriptMouse takes the mouse while the viewer is open: the wheel
// scrolls it and nothing else reaches the transcript underneath.
func (m *model) transcriptMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	if !m.viewerShown() {
		return m, nil, false
	}
	if v := m.syncViewer(); v != nil {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			v.scroll(-3, m.viewerBodyRows())
		case tea.MouseButtonWheelDown:
			v.scroll(3, m.viewerBodyRows())
		}
	}
	return m, nil, true
}

// viewerShown reports whether the viewer has the screen: it is open and no
// dialog that needs an answer (approval, question, interactive command) is.
func (m *model) viewerShown() bool {
	return m.tx != nil && m.tx.viewer != nil && m.permDialog == nil && m.askDialog == nil && m.iactive == nil
}

// blockViewerView is the viewer's frame, and false while it is not shown.
func (m *model) blockViewerView() (string, bool) {
	if !m.viewerShown() {
		return "", false
	}
	v := m.syncViewer()
	if v == nil {
		return "", false
	}
	b := m.viewerBlock()
	h := m.viewerBodyRows()
	w := max(m.width, 1)

	where := m.tr("all")
	switch {
	case len(v.rows) <= h:
	case v.off == 0:
		where = m.tr("top")
	case v.off >= v.maxOff(h):
		where = m.tr("end")
	default:
		where = fmt.Sprintf("%d%%", v.off*100/v.maxOff(h))
	}
	title := accentStyle.Render(focusMark()+" "+m.tr(blockLabel(b))) + dimStyle.Render(chromeGlyphs(" · ")+m.tr(linesLabel(strings.Join(v.rows, "\n"))))
	out := make([]string, 0, h+2)
	out = append(out, barColumns(title, dimStyle.Render(where), w))
	for i := v.off; i < v.off+h; i++ {
		if i >= len(v.rows) {
			out = append(out, "")
			continue
		}
		r := v.rows[i]
		if v.query != "" {
			r = highlightMatches(r, v.query)
		}
		out = append(out, r)
	}
	var foot string
	switch {
	case v.typing:
		foot = accentStyle.Render("/") + " " + v.draft + accentStyle.Render(chromeGlyphs("▏"))
		foot = barColumns(foot, dimStyle.Render(chromeGlyphs(m.tr("Enter search · Esc cancel"))), w)
	case v.query != "":
		state := m.tr("no match")
		if len(v.hits) > 0 {
			state = fmt.Sprintf("%d/%d", max(v.cur+1, 0), len(v.hits))
		}
		foot = barColumns(accentStyle.Render(fmt.Sprintf("%q", v.query))+dimStyle.Render(" "+state),
			dimStyle.Render(chromeGlyphs(m.tr("n/N next/previous · j/k scroll · Esc back"))), w)
	default:
		foot = ansi.Truncate(dimStyle.Render(chromeGlyphs(m.tr("j/k scroll · Space/PgDn page · g/G top/end · / find · Esc back"))), w, "…")
	}
	out = append(out, foot)
	return strings.Join(out, "\n"), true
}
