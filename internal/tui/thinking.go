package tui

// Thinking blocks. While a reasoning run streams in, its block shows a
// "Thinking…" header over the run's last few lines, the line still arriving
// included. When the reply or a tool call starts, the run is over: the block
// folds to "Thought for Xs" and opens to the whole text when toggled (a
// click, or e / Enter while the transcript has focus).

import (
	"fmt"
	"strings"
	"time"
)

// thinkingLiveRows is how many rows of a streaming run stay on screen.
const thinkingLiveRows = 3

// thinkMeta is a thinking block's timing. took is zero for a run restored
// from history, which recorded no time.
type thinkMeta struct {
	start time.Time
	took  time.Duration
	live  bool
}

func init() {
	registerBlockKind(blockThinking, blockKindSpec{render: renderThinking, toggle: toggleExpanded})
}

// renderThinking draws a thinking block: a header, then the live tail, the
// whole text when expanded, or nothing more when folded.
func renderThinking(b *block, width int) string {
	t := b.thought
	if t == nil {
		t = &thinkMeta{}
	}
	head := thinkingStyle.Render(chromeGlyphs(thinkingHead(b.text, t)))
	var body string
	switch {
	case t.live:
		src := lastSourceLines(b.text, thinkingLiveRows)
		if b.live != "" {
			src = strings.TrimPrefix(src+"\n"+b.live, "\n")
		}
		rows := strings.Split(wrap(src, max(width-2, 1)), "\n")
		body = strings.Join(rows[max(len(rows)-thinkingLiveRows, 0):], "\n")
	case b.expanded:
		body = wrap(b.text, max(width-2, 1))
	}
	if strings.TrimSpace(body) == "" {
		return head
	}
	// Styled a row at a time: one Render over several rows pads them all
	// to the widest.
	rows := strings.Split(indentPlain(body, 2), "\n")
	for i, r := range rows {
		if r != "" {
			rows[i] = thinkingStyle.Render(r)
		}
	}
	return head + "\n" + strings.Join(rows, "\n")
}

// thinkingHead is the header row's text.
func thinkingHead(text string, t *thinkMeta) string {
	switch {
	case t.live:
		return "◌ Thinking…"
	case t.took > 0:
		return fmt.Sprintf("◌ Thought for %s · %s", fmtThinkDuration(t.took), linesLabel(text))
	}
	return "◌ Thought · " + linesLabel(text)
}

func linesLabel(text string) string {
	if n := strings.Count(strings.TrimRight(text, "\n"), "\n") + 1; n != 1 {
		return fmt.Sprintf("%d lines", n)
	}
	return "1 line"
}

// fmtThinkDuration is a run's length in whole seconds, or under a second.
func fmtThinkDuration(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	}
	return fmtToolDuration(d)
}

// lastSourceLines is the last n lines of s, found from the end so a long run
// costs no more than a short one.
func lastSourceLines(s string, n int) string {
	i := len(s)
	for k := 0; k < n; k++ {
		j := strings.LastIndexByte(s[:i], '\n')
		if j < 0 {
			return s
		}
		i = j
	}
	return s[i+1:]
}

// thinkingBlock is a reasoning run restored from history: folded, untimed.
func thinkingBlock(text string) block {
	return block{kind: blockThinking, text: text, thought: &thinkMeta{}}
}

// liveThinking is the thinking block still streaming, if there is one.
func (m *model) liveThinking() *block {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if b := &m.blocks[i]; b.kind == blockThinking && b.thought != nil && b.thought.live {
			return b
		}
	}
	return nil
}

// streamThink takes a chunk of reasoning: whole lines go into the run's
// block, the line still arriving shows as its live tail.
func (m *model) streamThink(s string) {
	m.curThink += s
	if i := strings.LastIndexByte(m.curThink, '\n'); i >= 0 {
		done := m.curThink[:i]
		m.curThink = m.curThink[i+1:]
		m.appendThink(done)
	}
	if b := m.liveThinking(); b != nil && b.live != m.curThink {
		b.live = m.curThink
		b.invalidate()
		m.keepFollow()
		m.refreshVP()
	} else if b == nil && m.curThink != "" {
		m.appendThink("")
		m.liveThinking().live = m.curThink
		m.refreshVP()
	}
}

// appendThink grows one thinking block per reasoning run.
func (m *model) appendThink(s string) {
	m.keepFollow()
	if b := m.liveThinking(); m.inThink && b != nil {
		if b.text == "" {
			b.text = s
		} else {
			b.text += "\n" + s
		}
		b.invalidate()
		m.refreshVP()
		return
	}
	m.inThink = true
	m.blocks = append(m.blocks, block{kind: blockThinking, text: s, thought: &thinkMeta{start: m.nowFn(), live: true}})
	m.refreshVP()
}

// flushThink ends the reasoning run: its last line joins the block, which
// folds to its summary.
func (m *model) flushThink() {
	cur := strings.TrimRight(m.curThink, " \n")
	m.curThink = ""
	if cur != "" {
		if b := m.liveThinking(); b != nil {
			b.live = ""
		}
		m.appendThink(cur)
	}
	if b := m.liveThinking(); b != nil {
		b.thought.live = false
		b.thought.took = m.nowFn().Sub(b.thought.start)
		b.live = ""
		b.text = strings.TrimRight(b.text, " \n")
		if b.text == "" {
			m.dropBlock(b)
		} else {
			b.invalidate()
		}
		m.refreshVP()
	}
	m.inThink = false
}

// dropBlock removes b from the transcript.
func (m *model) dropBlock(b *block) {
	for i := range m.blocks {
		if &m.blocks[i] == b {
			m.blocks = append(m.blocks[:i], m.blocks[i+1:]...)
			for j := range m.msgBlock {
				if m.msgBlock[j] > i {
					m.msgBlock[j]--
				}
			}
			return
		}
	}
}
