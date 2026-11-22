package tui

// Inline mode: kn in the terminal's normal screen instead of the alternate
// one, selected by the inline config key, the /settings row or `kn --inline`.
//
// The transcript is split in two. A block that can no longer change is
// printed once above the program (tea.Println) and from then on belongs to
// the terminal: it scrolls into the scrollback, the terminal's own search and
// selection work on it, and it stays after kn exits. Everything still moving
// is the live region, the frame bubbletea redraws at the bottom: the reply
// streaming in, running and queued tool calls, thinking still arriving, the
// status rows, dialogs and the composer.
//
// A block stays live while it is held (inlineHeld): it is still streaming or
// running, or it sits in the trailing run of tool calls of a busy turn where
// grouping can still fold it or its structured result has not arrived.
// Blocks are printed strictly in order, so a held block keeps every block
// after it live too; in practice holds end within a turn. Two kinds change
// after they are printed and get a short addendum instead of an edit: a block
// expanded with Ctrl+E is printed again expanded, and a subagent's row is
// printed once more, as it ended, when the subagent finishes.
//
// A print reaches the terminal only when bubbletea's renderer handles the
// message, so a printed block stays in the live region until the
// acknowledgement queued behind its print arrives (inlineBatch). Nothing is
// ever reprinted on resize; Ctrl+L redraws only the live region. Clearing or
// rewriting the conversation (/clear, /resume, rewind, fork, import) prints a
// rule and then the conversation as it now stands, since the scrollback is
// the only place inline mode keeps it.
//
// Views that need the whole screen (the palette and pickers, settings and
// other surfaces, the task view, transcript focus and the block viewer) switch
// to the alternate screen while they are open and back after; prints wait
// until they close, since bubbletea drops them in the alternate screen.

import (
	"io"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// InlineOverride, when set (kn --inline / --inline=false), wins over the
// inline config key for this run.
var InlineOverride *bool

// inlineWanted reports whether this run uses inline mode.
func inlineWanted(cfg *config.Config) bool {
	if InlineOverride != nil {
		return *InlineOverride
	}
	return cfg != nil && cfg.Inline != nil && *cfg.Inline
}

type inlineState struct {
	// printed is every block handed to the terminal since the last reset,
	// by uid. Printed blocks form a prefix of m.blocks; done counts it.
	printed map[uint64]inlineRec
	done    int
	// batches are prints not yet acknowledged, oldest first.
	batches []inlineBatch
	// results are the calls whose structured result arrived this turn.
	results map[string]bool
	// subs maps a subagent still running to the uid of its last printed row.
	subs map[string]uint64

	printedAny bool
	// rule is set by a reset: a rule goes out before the next print.
	rule bool
	// glued is set while the live region's first block hangs directly
	// under the last printed one, without a blank row between them.
	glued bool

	// altOn is set while a full-screen view has the alternate screen;
	// entering is set from asking for it until bubbletea has switched.
	altOn, entering bool

	// frameRows and lastFrame are the last inline frame drawn.
	frameRows int
	lastFrame string
}

type inlineRec struct {
	expanded bool
	text     string
}

type inlineBatch struct {
	from  int // index of its first block, or -1 when it prints none
	first uint64
	lines []string
}

// inlinePrintedMsg acknowledges the oldest print batch; inlineAltMsg that
// bubbletea entered the alternate screen.
type (
	inlinePrintedMsg struct{}
	inlineAltMsg     struct{}
)

func newInlineState() *inlineState {
	return &inlineState{printed: map[uint64]inlineRec{}, results: map[string]bool{}, subs: map[string]uint64{}}
}

// inlineLive reports whether the frame is the inline live region (inline
// mode, no full-screen view on the alternate screen).
func (m *model) inlineLive() bool { return m.inl != nil && !m.inl.altOn }

// wantFullScreen reports whether a view that needs the whole screen is open.
func (m *model) wantFullScreen() bool {
	return m.palette != nil || m.picker != nil || m.mpicker != nil || m.surfaceOpen() || m.taskVP != nil ||
		m.viewerShown() || (m.tx != nil && m.tx.focus != nil)
}

// inlineReset is resetTranscript's part: the old transcript stays in the
// scrollback, and a rule separates it from what is printed next.
func (s *inlineState) reset() {
	clear(s.printed)
	clear(s.results)
	clear(s.subs)
	s.done = 0
	s.rule = s.printedAny
}

// inlineMsg takes inline mode's own messages before Update proper, and
// notes the ones it watches. It reports whether msg was its own.
func (m *model) inlineMsg(msg tea.Msg) bool {
	s := m.inl
	if s == nil {
		return false
	}
	switch msg := msg.(type) {
	case toolResultMsg:
		s.results[msg.id] = true
	case inlinePrintedMsg:
		if len(s.batches) > 0 {
			s.batches = s.batches[1:]
		}
		return true
	case inlineAltMsg:
		s.entering, s.altOn = false, true
		return true
	}
	return false
}

// inlineAfter runs after every Update: it switches the alternate screen for
// full-screen views and prints what has become final.
func (m *model) inlineAfter(msg tea.Msg, cmd tea.Cmd) tea.Cmd {
	s := m.inl
	if s == nil || m.width == 0 {
		return cmd
	}
	if !m.busy && len(s.results) > 0 {
		clear(s.results)
	}
	want := m.wantFullScreen()
	switch {
	case s.altOn && !want:
		// Back from a full-screen view: what it may have expanded is not
		// news, and what became final meanwhile prints now.
		s.altOn = false
		for i := range m.blocks {
			if rec, ok := s.printed[m.blocks[i].uid]; ok {
				rec.expanded = m.blocks[i].expanded
				s.printed[m.blocks[i].uid] = rec
			}
		}
		return tea.Batch(cmd, tea.Sequence(append([]tea.Cmd{tea.ExitAltScreen}, m.inlinePrint(false)...)...))
	case s.altOn || s.entering:
		return cmd
	case want:
		if len(s.batches) > 0 {
			// Let the prints in flight land in the normal screen first.
			return cmd
		}
		s.entering = true
		return tea.Batch(cmd, tea.Sequence(tea.EnterAltScreen, func() tea.Msg { return inlineAltMsg{} }))
	}
	_, key := msg.(tea.KeyMsg)
	if p := m.inlinePrint(key); p != nil {
		return tea.Batch(cmd, tea.Sequence(p...))
	}
	return cmd
}

// inlinePrint collects what is due for printing as a print and its
// acknowledgement, or nil when nothing is. revisit also looks for printed
// blocks expanded since.
func (m *model) inlinePrint(revisit bool) []tea.Cmd {
	s := m.inl
	w := m.inlineWidth()
	var lines []string
	if s.rule {
		s.rule = false
		lines = append(lines, "", dimStyle.Render(strings.Repeat("─", w)))
	}
	if revisit {
		lines = append(lines, m.inlineExpanded(w)...)
	}
	lines = append(lines, m.inlineSubsDone(w)...)

	m.inlineEnsureUIDs()
	m.inlineAdvance()
	from, first := -1, uint64(0)
	tail := m.inlineTailStart()
	for i := s.done; i < len(m.blocks); i++ {
		b := &m.blocks[i]
		if b.kind == blockBanner {
			continue
		}
		if _, ok := s.printed[b.uid]; ok {
			continue
		}
		if m.inlineHeld(i, tail) {
			break
		}
		if from < 0 {
			from, first = i, b.uid
		}
		lines = append(lines, m.inlineBlockLines(i, w)...)
		s.printed[b.uid] = inlineRec{expanded: b.expanded, text: b.text}
		if b.subID != "" {
			if t, ok := m.subTask(b.subID); ok && m.subRunning(t) {
				s.subs[b.subID] = b.uid
			}
		}
	}
	m.inlineAdvance()
	if len(lines) == 0 {
		return nil
	}
	s.batches = append(s.batches, inlineBatch{from: from, first: first, lines: lines})
	return []tea.Cmd{tea.Println(strings.Join(lines, "\n")), func() tea.Msg { return inlinePrintedMsg{} }}
}

// inlineBlockLines is block i as printed: its rows, after a blank row unless
// it hangs under the block before it.
func (m *model) inlineBlockLines(i, w int) []string {
	s := m.inl
	var out []string
	if s.printedAny && !gluedToPrev(m.blocks, i) {
		out = append(out, "")
	}
	s.printedAny = true
	return append(out, inlineRows(&m.blocks[i], w, m.ancientMode)...)
}

// inlineRows draws a copy of b for the scrollback, without the cues that
// only mean something on screen (hover, the Ctrl+E target).
func inlineRows(b *block, w int, ancient bool) []string {
	c := *b
	c.hover, c.expandKey = false, false
	c.rows, c.plain, c.md, c.stale = nil, nil, nil, true
	return strings.Split(sanitizeView(strings.Join(c.linesAt(w, ancient), "\n")), "\n")
}

// inlineWidth is the width blocks are drawn at: the transcript's, but never
// wider than the terminal, which would wrap printed rows.
func (m *model) inlineWidth() int {
	w := m.layoutWidth()
	if m.width >= minRenderWidth && w > m.width {
		w = m.width
	}
	return w
}

// inlineEnsureUIDs gives blocks the uid refreshVP would, for those it has
// not laid out yet.
func (m *model) inlineEnsureUIDs() {
	tx := m.txs()
	for i := range m.blocks {
		if m.blocks[i].uid == 0 {
			tx.seq++
			m.blocks[i].uid = tx.seq
		}
	}
}

// inlineAdvance moves done past the printed prefix, starting over when
// the blocks under it changed.
func (m *model) inlineAdvance() {
	s := m.inl
	if s.done > len(m.blocks) || (s.done > 0 && !m.inlineDone(s.done-1)) {
		s.done = 0
	}
	for s.done < len(m.blocks) && m.inlineDone(s.done) {
		s.done++
	}
}

func (m *model) inlineDone(i int) bool {
	b := &m.blocks[i]
	if b.kind == blockBanner {
		return true
	}
	_, ok := m.inl.printed[b.uid]
	return ok
}

// inlineTailStart is the first block of the trailing run of tool rows.
func (m *model) inlineTailStart() int {
	i := len(m.blocks)
	for i > 0 && toolFamily(m.blocks[i-1]) {
		i--
	}
	return i
}

// inlineHeld reports whether block i is still changing and stays in the
// live region.
func (m *model) inlineHeld(i, tail int) bool {
	b := &m.blocks[i]
	switch {
	case b.kind == blockThinking && b.thought != nil && b.thought.live,
		b.kind == blockToolQueued,
		b.kind == blockToolRun && b.toolRunning,
		b.kind == blockAssistant && m.inMsg && i == len(m.blocks)-1:
		return true
	case b.kind == blockHostedSearch:
		v := b.viewMeta()
		return v != nil && v.search != nil && v.search.Status == "searching"
	}
	if !m.busy || i < tail {
		return false
	}
	// The trailing tool run of a busy turn: a finished read-only call may
	// still fold into a group with the next one, and a header waits for its
	// structured result (duration, exit status).
	if toolGroupBlock(b) || (toolHead(*b) && groupableTool(b.toolName)) {
		return true
	}
	return b.kind == blockToolRun && b.toolID != "" && !m.inl.results[b.toolID]
}

// inlineExpanded prints again, expanded, the printed blocks Ctrl+E has
// opened since.
func (m *model) inlineExpanded(w int) []string {
	s := m.inl
	var out []string
	for i := 0; i < s.done && i < len(m.blocks); i++ {
		b := &m.blocks[i]
		rec, ok := s.printed[b.uid]
		if !ok || rec.expanded == b.expanded {
			continue
		}
		rec.expanded = b.expanded
		s.printed[b.uid] = rec
		if b.expanded {
			out = append(out, "")
			out = append(out, inlineRows(b, w, m.ancientMode)...)
		}
	}
	return out
}

// inlineSubsDone prints the row of each subagent that finished since its row
// was printed, as it ended.
func (m *model) inlineSubsDone(w int) []string {
	s := m.inl
	var out []string
	for id, uid := range s.subs {
		t, ok := m.subTask(id)
		if ok && m.subRunning(t) {
			continue
		}
		delete(s.subs, id)
		for i := len(m.blocks) - 1; i >= 0; i-- {
			b := &m.blocks[i]
			if b.uid != uid {
				continue
			}
			if rec := s.printed[uid]; b.text != rec.text {
				out = append(out, inlineRows(b, w, m.ancientMode)...)
			}
			break
		}
	}
	return out
}

// inlinePendingView is the live region's share of the transcript: the
// blocks not printed yet, or printed but not yet on the terminal.
func (m *model) inlinePendingView() string {
	s := m.inl
	s.glued = false
	w := m.inlineWidth()
	if m.bannerOnly() {
		return strings.Join(m.blocks[0].linesAt(w, m.ancientMode), "\n")
	}
	start := s.done
	if len(s.batches) > 0 {
		if bt := s.batches[0]; bt.from >= 0 && bt.from < len(m.blocks) && m.blocks[bt.from].uid == bt.first {
			start = bt.from
		}
	}
	start = min(start, len(m.blocks))
	var rows []string
	for i := start; i < len(m.blocks); i++ {
		b := &m.blocks[i]
		if b.kind == blockBanner {
			continue
		}
		if len(rows) == 0 {
			s.glued = s.printedAny && gluedToPrev(m.blocks, i)
		} else if !gluedToPrev(m.blocks, i) {
			rows = append(rows, "")
		}
		rows = append(rows, b.linesAt(w, m.ancientMode)...)
	}
	if m.height > 0 && len(rows) > m.height {
		rows = rows[len(rows)-m.height:]
	}
	return sanitizeView(strings.Join(rows, "\n"))
}

// inlineView is View in inline mode: the live region, as tall as its
// content, under one blank row that separates it from the scrollback.
func (m *model) inlineView() string {
	s := m.inl
	if m.width == 0 {
		// Nothing is laid out before the first size arrives.
		return ""
	}
	if m.wantFullScreen() {
		// Until bubbletea has switched screens, keep the inline frame: a
		// full-screen view drawn into the normal screen would push its rows
		// into the scrollback.
		return s.lastFrame
	}
	m.syncInputPlaceholder()
	lines := strings.Split(m.viewBody(), "\n")
	lead := 0
	for lead < len(lines)-1 && blankRow(lines[lead]) {
		lead++
	}
	lines = lines[lead:]
	if s.printedAny && !s.glued {
		lines = slices.Insert(lines, 0, "")
	}
	if m.height > 0 && len(lines) > m.height {
		lines = lines[len(lines)-m.height:]
	}
	// The mouse is off in inline mode: no row offsets to keep.
	m.viewTop, m.viewH = 0, len(lines)
	m.frameH, m.frameTop = len(lines), 0
	m.inputTop, m.inputLines, m.effortY, m.queueTop = -1, nil, -1, -1<<20
	s.lastFrame = strings.Join(lines, "\n")
	s.frameRows = len(lines)
	return s.lastFrame
}

// inlineFinish runs after the program has stopped: it clears the live
// region and prints what never left it, so the scrollback holds the whole
// conversation.
func (m *model) inlineFinish(out io.Writer) {
	s := m.inl
	if s == nil || m.width == 0 {
		return
	}
	var b strings.Builder
	if s.frameRows > 1 {
		b.WriteString(ansi.CursorUp(s.frameRows - 1))
	}
	b.WriteString("\r" + ansi.EraseScreenBelow)
	var lines []string
	// Prints still in flight when the program stopped never reached it.
	for _, bt := range s.batches {
		lines = append(lines, bt.lines...)
	}
	s.batches = nil
	w := m.inlineWidth()
	m.inlineEnsureUIDs()
	for i := range m.blocks {
		bl := &m.blocks[i]
		if bl.kind == blockBanner {
			continue
		}
		if _, ok := s.printed[bl.uid]; ok {
			continue
		}
		s.printed[bl.uid] = inlineRec{}
		lines = append(lines, m.inlineBlockLines(i, w)...)
	}
	for _, l := range lines {
		b.WriteString(l + ansi.ResetStyle + "\r\n")
	}
	if s.printedAny {
		// A blank row between the conversation and the shell.
		b.WriteString("\r\n")
	}
	_, _ = io.WriteString(out, b.String())
}

// toggleInlineSetting flips the inline config key. The screen mode is chosen
// at startup, so it takes effect the next time kn starts.
func (m *model) toggleInlineSetting() {
	if m.cfg == nil {
		return
	}
	on := m.cfg.Inline == nil || !*m.cfg.Inline
	m.cfg.Inline = &on
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
		return
	}
	note := "takes effect the next time kn starts"
	if on == (m.inl != nil) {
		note = "this session already runs that way"
	}
	m.append(dimStyle.Render("◌ inline mode: " + onOff(on) + " — " + note))
}

// screenDoctor is /doctor's row for the screen kn draws on.
func (m *model) screenDoctor() string {
	if m.inl != nil {
		return "  screen               inline (finished transcript in the terminal's scrollback)"
	}
	return "  screen               alternate screen"
}
