package tui

// Transcript focus keys that act on the focused block's source: [ and ]
// jump between user turns, y copies the block, r shows a message's raw
// markdown in place.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// focusTurn moves focus from block from to the previous (d < 0) or next
// (d > 0) prompt, staying put when there is none.
func (m *model) focusTurn(from, d int) {
	for i := from + d; i >= 0 && i < len(m.blocks); i += d {
		if b := &m.blocks[i]; b.kind == blockUser && focusable(b) {
			m.txs().focus.uid = b.uid
			m.revealBlock(i)
			return
		}
	}
}

// blockSource is what y copies from a block: the code alone when a message
// is a single fenced code block, a message's markdown source otherwise, and
// the plain text of any other block.
func blockSource(b *block) string {
	text := ansi.Strip(b.text)
	if b.kind != blockAssistant && b.kind != blockUser {
		return text
	}
	if segs := splitFences(strings.TrimSpace(text)); len(segs) == 1 && segs[0].code {
		return segs[0].text
	}
	return text
}

// copyFocused copies the focused block's source.
func (m *model) copyFocused(bi int) tea.Cmd {
	return copyTextCmd(blockSource(&m.blocks[bi]))
}

// hasRawView reports whether block b is drawn from markdown: a reply. A
// prompt is drawn as typed already.
func hasRawView(b *block) bool { return b.kind == blockAssistant }

// toggleRaw flips the focused reply between its rendered and its raw
// markdown form.
func (m *model) toggleRaw(bi int) {
	b := &m.blocks[bi]
	if !hasRawView(b) {
		m.flashNotice(m.tr("only a reply has a raw view"))
		return
	}
	b.raw = !b.raw
	b.invalidate()
	m.refreshVP()
	m.revealBlock(bi)
}

// rawReplyRows draws a reply's markdown source as plain, wrapped text behind
// the reply's glyph.
func rawReplyRows(b *block, width int) []string {
	w := max(width-2, 1)
	glyph := botStyle.Render(glyphAssistant)
	var rows []string
	for _, l := range strings.Split(ansi.Strip(b.text), "\n") {
		rows = append(rows, strings.Split(ansi.Hardwrap(l, w, true), "\n")...)
	}
	for i, r := range rows {
		lead := "  "
		if i == 0 {
			lead = glyph
		}
		rows[i] = lead + textSecondaryStyle.Render(r)
	}
	return rows
}
