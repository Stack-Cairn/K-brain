package tui

// Thinking blocks. While a reasoning run streams in, its block shows the
// run's last few lines, the line still arriving included, with no header of
// its own: the status row's spinner already says "Thinking…". When the reply
// or a tool call starts, the run is over and joins the turn's one thinking
// row, which reads "Thought for Xs" for every run of the turn together and
// opens to the whole text when toggled (a click, or e / Enter while the
// transcript has focus). Runs adding up to less than a second leave no row.

import (
	"fmt"
	"strings"
	"time"
)

// thinkingLiveRows is how many rows of a streaming run stay on screen.
const thinkingLiveRows = 3

// thinkMinShown is how long a turn has to think before its row shows.
const thinkMinShown = time.Second

// thinkMeta is a thinking block's timing. took is zero for a run restored
// from history, which recorded no time; turn is the turn it belongs to
// (model.thinkTurn).
type thinkMeta struct {
	start time.Time
	took  time.Duration
	live  bool
	turn  int
}

// thinkCarry is the turn's thinking too short to show so far: the next run
// of the same turn starts from it.
type thinkCarry struct {
	text string
	took time.Duration
	turn int
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
	if t.live {
		// The status row names the phase; the live block is only its tail.
		if strings.TrimSpace(body) == "" {
			body = "…"
		}
		return thinkingRows(body)
	}
	head := thinkingStyle.Render(chromeGlyphs(thinkingHead(b.text, t)))
	if strings.TrimSpace(body) == "" {
		return head
	}
	return head + "\n" + thinkingRows(body)
}

// thinkingRows indents and styles a thinking body.
func thinkingRows(body string) string {
	// Styled a row at a time: one Render over several rows pads them all
	// to the widest.
	rows := strings.Split(indentPlain(body, 2), "\n")
	for i, r := range rows {
		if r != "" {
			rows[i] = thinkingStyle.Render(r)
		}
	}
	return strings.Join(rows, "\n")
}

// thinkingHead is a settled block's header row: how long the turn thought,
// or, for a run restored from history, how much it wrote.
func thinkingHead(text string, t *thinkMeta) string {
	if t.took > 0 {
		return fmt.Sprintf("%s Thought for %s", glyphs.Thought, fmtThinkDuration(t.took))
	}
	return glyphs.Thought + " Thought · " + linesLabel(text)
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
	m.blocks = append(m.blocks, block{kind: blockThinking, text: s, thought: &thinkMeta{start: m.nowFn(), live: true, turn: m.thinkTurn}})
	m.refreshVP()
}

// turnThought is the current turn's settled thinking row, if it has one.
func (m *model) turnThought() *block {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		b := &m.blocks[i]
		if b.kind == blockUser {
			return nil
		}
		if b.kind == blockThinking && b.thought != nil && !b.thought.live && b.thought.took > 0 && b.thought.turn == m.thinkTurn {
			return b
		}
	}
	return nil
}

// startThinkTurn begins a new turn for thinking: its runs gather in a row of
// their own.
func (m *model) startThinkTurn() {
	m.thinkTurn++
	m.thinkCarry = thinkCarry{}
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
		m.settleThought(b)
		m.refreshVP()
	}
	m.inThink = false
}

// settleThought ends the live run b: it joins the turn's thinking row, or
// becomes that row once the turn has thought for a second; until then it is
// carried, unseen, to the turn's next run.
func (m *model) settleThought(b *block) {
	took := max(m.nowFn().Sub(b.thought.start), time.Nanosecond)
	text := strings.TrimRight(b.text, " \n")
	if text == "" {
		m.dropBlock(b)
		return
	}
	if row := m.turnThought(); row != nil {
		row.text += "\n" + text
		row.thought.took += took
		row.invalidate()
		m.dropBlock(b)
		return
	}
	if c := m.thinkCarry; c.turn == m.thinkTurn && c.text != "" {
		text, took = c.text+"\n"+text, took+c.took
	}
	m.thinkCarry = thinkCarry{}
	if took < thinkMinShown {
		m.thinkCarry = thinkCarry{text: text, took: took, turn: m.thinkTurn}
		m.dropBlock(b)
		return
	}
	b.thought.live = false
	b.thought.took = took
	b.live = ""
	b.text = text
	b.invalidate()
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
