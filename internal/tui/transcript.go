package tui

// The transcript engine.
//
// # Blocks
//
// The transcript is model.blocks, an ordered list of blocks. A block is one
// thing the reader sees as a unit: a user prompt, an assistant reply, a tool
// call header, its result, a notice. Its kind picks how it draws.
//
// # The block API
//
// Rendering. A block kind draws itself through its blockKindSpec in
// blockKinds; a kind without a spec (or with a nil render) falls back to
// renderBlock in block_render.go. A renderer is a function of the block's
// fields, the width in cells and the global style state (theme, glyph set):
//
//	render func(b *block, width int) string
//
// It returns the block's rows joined by "\n" — at least one row, none wider
// than width — with no leading or trailing separator: spacing between
// blocks is the engine's job (see gluedToPrev). A new kind is a new
// blockKind constant plus a registerBlockKind call in the file that owns it:
//
//	const blockBashRun blockKind = 100 // clear of the constants below
//	func init() {
//		registerBlockKind(blockBashRun, blockKindSpec{
//			render: renderBashRun,
//			toggle: toggleExpanded, // foldable with click / ctrl+e
//		})
//	}
//
// Kind-specific data rides on the block: the generic tool fields (toolID,
// toolName, toolArgs, toolRunning, toolFailed, live, expanded) and run, how a
// finished tool call ran (exit status, duration, cancellation).
//
// Caching. Each block caches its rendered rows keyed by (width, ancient
// mode); (*block).linesAt returns them, rendering only on a miss, and
// (*block).height reports how many there are. A change to anything the
// renderer reads must be followed by (*block).invalidate() — which is just
// stale = true — and then model.refreshVP. Appending, inserting, deleting or
// replacing blocks needs no invalidation: refreshVP notices by each block's
// uid. A resize or an ancient-mode switch drops only these width-keyed
// caches; block state (expansion, tool data, uids) is kept.
//
// A block that changes while it is on screen (a running tool's live output,
// a reply growing) is updated the same way: change its fields, invalidate,
// refreshVP. Only that block renders again. The renderer runs once per cache
// miss, not once per frame, so it must not depend on where the block sits or
// on the clock unless something invalidates it when that changes.
//
// Hit testing. model.blockAtRow maps a transcript row (as select.go computes
// it from a mouse position) to the block drawn there.
//
// # Layout and the visible window
//
// refreshVP lays the blocks out as rows in a txIndex: one entry per shown
// block with its first row y0, its separator gap and its cached rows — the
// prefix sums of block heights. It walks the blocks once comparing uids and
// cache keys, and re-lays out only from the first block that changed, so an
// append costs the new block's render plus a cheap scan, not a rebuild of
// the whole transcript string. It also writes each block's y0/y1 (first and
// last row; -1 while hidden), which mouse selection (select.go) and clickAt
// read.
//
// The scroll window is model.vp, a transcriptView. It keeps the bubbles
// viewport's methods but draws only rows [YOffset, YOffset+Height) out of the
// index, found by binary search.
//
// # Scroll anchor
//
// While following (model.follow) the window sticks to the bottom. Otherwise,
// before each re-layout refreshVP anchors the top row to (block uid, row in
// block) and restores it afterwards, so rows changing above the window, or a
// resize reflowing every block, keep the same content on screen. A block
// that reflows to a different height keeps the same fraction of itself above
// the window.

import (
	"slices"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/charmbracelet/x/ansi"
)

// seedTranscript rebuilds the blocks for messages restored from history
// (resume, rewind, fork), as close as the record allows to how the live turn
// drew them: thinking (when shown), the reply, each tool call's header with
// its outcome and duration, and the result under it.
func (m *model) seedTranscript(msgs []ai.Message, base int) {
	// headers maps a tool call id to its header block, so its result, which
	// arrives in a later message, lands directly under it as it did live.
	headers := map[string]int{}
	insertAt := func(at int, b block) {
		m.blocks = slices.Insert(m.blocks, at, b)
		for id, j := range headers {
			if j >= at {
				headers[id] = j + 1
			}
		}
		for j := range m.msgBlock {
			if m.msgBlock[j] >= at {
				m.msgBlock[j]++
			}
		}
	}
	for i, msg := range msgs {
		bi := -1
		switch msg.Role {
		case "user":
			if !msg.Authored && reminderOnly(msg.TextContent()) {
				// Agent-written nudges (output-limit continuation, …) are not
				// the user's words; the live transcript never showed them.
				break
			}
			bi = len(m.blocks)
			if msg.Origin == ai.OriginBackground {
				m.blocks = append(m.blocks, m.noticeBlocks(msg.TextContent())...)
				break
			}
			m.blocks = append(m.blocks, block{kind: blockUser, text: linkifyFilePaths(msg.TextContent(), realFileExists)})
		case "assistant":
			if r := strings.TrimSpace(msg.Reasoning); r != "" && m.showThinking {
				m.blocks = append(m.blocks, thinkingBlock(r))
			}
			if strings.TrimSpace(msg.TextContent()) != "" {
				bi = len(m.blocks)
				m.blocks = append(m.blocks, block{kind: blockAssistant, text: strings.TrimRight(msg.TextContent(), "\n")})
			}
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" {
					headers[tc.ID] = len(m.blocks)
				}
				m.blocks = append(m.blocks, seededToolHeader(tc))
			}
		case "tool":
			hdr, ok := headers[msg.ToolCallID]
			if ok {
				delete(headers, msg.ToolCallID)
			}
			var head *block
			if ok {
				head = &m.blocks[hdr]
			}
			result, show := seededToolResult(msg, head)
			if !show {
				break
			}
			if ok {
				insertAt(hdr+1, result)
			} else {
				m.blocks = append(m.blocks, result)
			}
		}
		for len(m.msgBlock) <= base+i {
			m.msgBlock = append(m.msgBlock, -1)
		}
		m.msgBlock[base+i] = bi
	}
	m.follow = true
	m.refreshVP()
}

// thinkingBlock is a reasoning run as appendThink leaves it once streamed.
func thinkingBlock(text string) block {
	return block{kind: blockText, think: true, text: thinkingStyle.Render("◌ " + strings.ReplaceAll(text, "\n", "\n  "))}
}

// seededToolHeader is a finished tool call's header row, rebuilt from the
// call as history recorded it.
func seededToolHeader(tc ai.ToolCall) block {
	name, args := tc.Function.Name, tc.Function.Arguments
	run := &toolRunMeta{exitCode: tc.ExitStatus, timedOut: tc.TimedOut}
	if tc.DurationMs > 0 {
		run.duration = time.Duration(tc.DurationMs) * time.Millisecond
	}
	failed := tc.ExitCode != 0 || tc.TimedOut
	return block{kind: blockToolRun, text: toolHeaderRow(name, args, failed), toolID: tc.ID, toolName: name, toolArgs: args, toolFailed: failed, run: run}
}

// seededToolResult is the block a restored tool result shows as, settling
// its header's outcome on the way. show is false for results the live
// transcript did not show either.
func seededToolResult(msg ai.Message, head *block) (result block, show bool) {
	content := msg.TextContent()
	name := msg.Name
	if head != nil && head.toolName != "" {
		name = head.toolName
	}
	interrupted := strings.HasPrefix(content, "Error: tool call interrupted")
	if head != nil {
		if head.run == nil {
			head.run = &toolRunMeta{}
		}
		head.run.interrupted = interrupted
		head.run.cancelled = strings.Contains(content, "(exit: cancelled)")
		if !head.toolFailed && (interrupted || toolResultFailed(content)) {
			head.toolFailed = true
			head.text = toolHeaderRow(head.toolName, head.toolArgs, true)
		}
	}
	if name == "subagent" && backgroundSpawnID(content) != "" {
		// Live, a background spawn's result is replaced by the task's state
		// row; the task is long gone, and the result was written for the
		// model, not the reader.
		return block{}, false
	}
	return block{kind: blockTool, text: content, toolName: name}, true
}

// toolRunMeta is how a finished tool call ran, where it is known: restored
// from history's tool_calls[] record, or filled in by a renderer that learns
// it. Every field is optional.
type toolRunMeta struct {
	// exitCode is the exit status a shell tool reported, 0 included; nil
	// when unknown or not a shell tool.
	exitCode *int
	timedOut bool
	// duration is the tool's own wall time; 0 when unknown.
	duration    time.Duration
	cancelled   bool
	interrupted bool
}

type blockKind int

const (
	blockText blockKind = iota
	blockAssistant
	blockTool
	blockToolRun
	blockToolQueued
	blockUser
	blockBanner
)

// blockKindSpec is how one kind of block draws and behaves. See the package
// comment at the top of this file for the contract.
type blockKindSpec struct {
	// render draws b at width cells; nil means renderBlock.
	render func(b *block, width int) string
	// toggle flips b between its folded and expanded form and reports
	// whether it has one; nil means the kind does not fold.
	toggle func(b *block) bool
}

// blockKinds is the renderer table, one row per kind that has a spec.
var blockKinds = map[blockKind]blockKindSpec{
	blockTool:    {toggle: toggleExpanded},
	blockToolRun: {toggle: toggleExpanded},
}

// registerBlockKind adds or replaces a kind's spec. Call it from init.
func registerBlockKind(k blockKind, spec blockKindSpec) { blockKinds[k] = spec }

// toggleExpanded is the toggle of kinds whose only fold is b.expanded.
func toggleExpanded(b *block) bool {
	b.expanded = !b.expanded
	return true
}

const toolPreviewLines = 3

const minRenderWidth = 8

type block struct {
	kind     blockKind
	text     string
	expanded bool

	toolID      string
	toolRunning bool
	toolFailed  bool

	toolName string
	toolArgs string

	live string

	// run is how a finished tool call ran, when known.
	run *toolRunMeta

	// subID binds a subagent tool row to the subagent it runs; subLine marks
	// the status row under a background spawn.
	subID   string
	subLine bool

	// think marks a thinking paragraph that later reasoning lines extend.
	think bool

	// y0 and y1 are the block's first and last transcript rows as of the
	// last refreshVP; -1 while the block is hidden.
	y0, y1 int

	hover bool

	// uid identifies the block across inserts and deletes; refreshVP assigns
	// it on first sight.
	uid uint64

	// The render cache: rendered and its rows, valid for width and ancient
	// unless stale.
	rendered string
	rows     []string
	lines    int
	width    int
	stale    bool
	ancient  bool
	// plain is rows without escapes, built on demand for selection.
	plain []string
}

// invalidate drops the block's render cache; call it after changing
// anything its renderer reads.
func (b *block) invalidate() { b.stale = true }

// height is the block's row count as of its last render.
func (b *block) height() int { return b.lines }

func (b *block) renderAt(width int) string {
	return b.renderAtMode(width, false)
}

// linesAt is the block's rows at width, from the cache when it is valid.
func (b *block) linesAt(width int, ancient bool) []string {
	b.renderAtMode(width, ancient)
	return b.rows
}

func (b *block) renderAtMode(width int, ancient bool) string {
	if !b.stale && b.width == width && b.ancient == ancient && b.rows != nil {
		return b.rendered
	}
	if ancient && (b.kind == blockAssistant || b.kind == blockUser) {
		b.rendered = accentStyle.Render(renderAncientText(ansi.Strip(b.render(width)), width))
	} else {
		b.rendered = b.render(width)
	}
	b.rows = splitRows(b.rendered)
	b.lines = len(b.rows)
	b.plain = nil
	b.width, b.ancient, b.stale = width, ancient, false
	return b.rendered
}

// plainRow is row r of the block without escapes or trailing blanks.
func (b *block) plainRow(r int) string {
	if b.plain == nil {
		b.plain = make([]string, len(b.rows))
		for i, l := range b.rows {
			b.plain[i] = strings.TrimRight(ansi.Strip(l), " \t")
		}
	}
	if r < 0 || r >= len(b.plain) {
		return ""
	}
	return b.plain[r]
}

// splitRows splits rendered text into rows, as the viewport it replaced did.
func splitRows(s string) []string {
	if strings.Contains(s, "\r\n") {
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	return strings.Split(s, "\n")
}

// render draws the block through its kind's renderer.
func (b block) render(width int) string {
	if spec, ok := blockKinds[b.kind]; ok && spec.render != nil {
		return spec.render(&b, width)
	}
	return renderBlock(b, width)
}

// toggle folds or unfolds the block and reports whether its kind folds.
func (b *block) toggle() bool {
	spec, ok := blockKinds[b.kind]
	if !ok || spec.toggle == nil || !spec.toggle(b) {
		return false
	}
	b.invalidate()
	return true
}

func (m *model) append(blocks ...string) {
	for _, s := range blocks {
		m.appendRaw(blockText, s)
	}
}

func (m *model) appendAssistantBlock(s string) {
	m.appendRaw(blockAssistant, s)
}

func (m *model) wantFollow() bool {
	return m.follow || m.vp.AtBottom()
}

func (m *model) keepFollow() {
	if m.wantFollow() {
		m.follow = true
	}
}

func (m *model) appendRaw(kind blockKind, text string) {
	m.keepFollow()
	m.blocks = append(m.blocks, block{kind: kind, text: text})
	m.refreshVP()
}

// transcriptState is the transcript engine's state: the line index the
// scroll window reads, and the uid counter.
type transcriptState struct {
	ix  txIndex
	seq uint64
}

// txs is the transcript state, created on first use (models built directly
// in tests start without one).
func (m *model) txs() *transcriptState {
	if m.tx == nil {
		m.tx = &transcriptState{}
	}
	return m.tx
}

// refreshVP brings the line index up to date with m.blocks and settles the
// scroll position: at the bottom while following, else on the anchor taken
// before the re-layout. See the package comment for the costs.
func (m *model) refreshVP() {
	if m.width == 0 {
		return
	}
	tx := m.txs()
	ix := &tx.ix
	width := max(m.width, minRenderWidth)

	var anchor txAnchor
	if !m.follow && m.vp.ix == ix {
		anchor = ix.anchorAt(m.vp.YOffset)
	}

	// The startup card only exists while the conversation is empty: as soon
	// as any message lands the card leaves the transcript entirely.
	hideBanner := len(m.blocks) > 1

	// Find the first block whose layout may have changed. Everything above
	// it keeps its rows and positions.
	start := 0
	if ix.valid && ix.width == width && ix.ancient == m.ancientMode && ix.bannerHidden == hideBanner {
		n := min(len(ix.uids), len(m.blocks))
		for start < n {
			b := &m.blocks[start]
			if b.uid == 0 || b.uid != ix.uids[start] {
				break
			}
			if ix.ent[start] >= 0 && (b.stale || b.width != width || b.ancient != m.ancientMode || b.rows == nil) {
				break
			}
			start++
		}
	}
	ix.width, ix.ancient, ix.bannerHidden, ix.valid = width, m.ancientMode, hideBanner, true

	// Drop the entries of start and below.
	keep := 0
	for j := start - 1; j >= 0; j-- {
		if ix.ent[j] >= 0 {
			keep = ix.ent[j] + 1
			break
		}
	}
	clear(ix.entries[keep:])
	ix.entries = ix.entries[:keep]
	ix.uids = slices.Grow(ix.uids[:start], len(m.blocks)-start)[:len(m.blocks)]
	ix.ent = slices.Grow(ix.ent[:start], len(m.blocks)-start)[:len(m.blocks)]

	line := 0
	if keep > 0 {
		line = ix.entries[keep-1].end()
	}
	anchorRow := -1
	if anchor.ok && anchor.at < keep && ix.entries[anchor.at].uid == anchor.uid {
		// The anchor block sits above every change: it has not moved.
		anchorRow = anchor.rowOf(&ix.entries[anchor.at])
	}
	for i := start; i < len(m.blocks); i++ {
		b := &m.blocks[i]
		if b.uid == 0 {
			tx.seq++
			b.uid = tx.seq
		}
		ix.uids[i] = b.uid
		if b.kind == blockBanner && hideBanner {
			b.y0, b.y1 = -1, -1
			ix.ent[i] = -1
			continue
		}
		gap := 0
		if len(ix.entries) > 0 && !gluedToPrev(m.blocks, i) {
			gap = 1
		}
		rows := b.linesAt(width, m.ancientMode)
		e := txEntry{uid: b.uid, bi: i, gap: gap, y0: line + gap, lines: rows}
		b.y0, b.y1 = e.y0, e.y0+len(rows)-1
		line = e.end()
		ix.ent[i] = len(ix.entries)
		ix.entries = append(ix.entries, e)
		if anchor.ok && anchorRow < 0 && b.uid == anchor.uid {
			anchorRow = anchor.rowOf(&ix.entries[len(ix.entries)-1])
		}
	}
	ix.total = line

	m.vp.ix, m.vp.static = ix, nil
	// The window only clamps against the content height; a stale offset on
	// a shrunk transcript would otherwise leave its top scrolled off.
	if anchorRow >= 0 {
		m.vp.SetYOffset(anchorRow)
	} else {
		m.vp.SetYOffset(m.vp.YOffset)
	}
	switch {
	case m.follow:
		m.vp.GotoBottom()
	case m.vp.AtBottom():
		m.follow = true
	}
}

// transcriptRows is how many rows the rendered blocks occupy.
func (m *model) transcriptRows() int { return m.vp.TotalLineCount() }

// blockAtRow is the index of the block drawn on transcript row r, or -1 for
// a separator row or a row outside the transcript.
func (m *model) blockAtRow(r int) int {
	if m.tx == nil {
		return -1
	}
	ix := &m.tx.ix
	i := ix.find(r)
	if i < 0 || r < ix.entries[i].y0 {
		return -1
	}
	bi := ix.entries[i].bi
	if bi >= len(m.blocks) || m.blocks[bi].uid != ix.entries[i].uid {
		return -1
	}
	return bi
}

func (m *model) viewportView() string {
	s := sanitizeView(m.vp.View())
	if m.sel != nil {
		s = m.highlightSelection(s)
	}

	lines := strings.Split(s, "\n")
	m.vpLead = 0

	// Keep exactly the transcript rows the viewport is showing and drop only the
	// viewport's own filler. Trimming blank rows instead would also swallow real
	// separator rows, which makes the composer wobble a row per scroll notch.
	visible := max(min(m.transcriptRows()-m.vp.YOffset, len(lines)), 0)
	return strings.Join(lines[:visible], "\n")
}

func (m *model) appendAssistant(s string) {
	if m.inMsg && len(m.blocks) > 0 && m.blocks[len(m.blocks)-1].kind == blockAssistant {
		// The stream handler cuts each chunk at its last newline, so one
		// newline restores the text exactly; two would double-space code.
		m.blocks[len(m.blocks)-1].text += "\n" + s
		m.blocks[len(m.blocks)-1].stale = true
		m.keepFollow()
		m.refreshVP()
		return
	}
	m.appendAssistantBlock(s)
	m.inMsg = true
}

func indentLines(s string, n int) string {
	const docMargin = 2
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(ansi.Strip(l)) == "" {
			lines[i] = ""
			continue
		}
		lead := len(l) - len(strings.TrimLeft(l, " "))
		shift := max(n+lead-docMargin, 0)
		lines[i] = strings.Repeat(" ", shift) + strings.TrimLeft(l, " ")
	}
	return strings.Join(lines, "\n")
}

// appendThink grows one thinking block per reasoning run instead of a block
// per streamed line, so the transcript shows it as a single quiet paragraph.
func (m *model) appendThink(s string) {
	body := strings.ReplaceAll(s, "\n", "\n  ")
	if m.inThink {
		if n := len(m.blocks); n > 0 && m.blocks[n-1].think {
			last := &m.blocks[n-1]
			last.text += "\n" + thinkingStyle.Render("  "+body)
			last.stale = true
			m.keepFollow()
			m.refreshVP()
			return
		}
		body = "  " + body
	} else {
		body = "◌ " + body
		m.inThink = true
	}
	m.keepFollow()
	m.blocks = append(m.blocks, block{kind: blockText, text: thinkingStyle.Render(body), think: true})
	m.refreshVP()
}

func (m *model) toggleThinking() {
	m.setThinking(!m.showThinking)
	m.append(dimStyle.Render("◌ thinking tokens: " + onOff(m.showThinking)))
}

func (m *model) setThinking(on bool) {
	m.showThinking = on
	if !on {
		m.flushThink()
	}
	b := on
	m.cfg.Thinking = &b
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
	}
}

func (m *model) flushThink() {

	cur := strings.TrimRight(m.curThink, " \n")
	m.curThink = ""
	if cur != "" {
		m.appendThink(cur)
	}
	m.inThink = false
}

func (m *model) thinkView() string {
	s := m.curThink
	if !m.inThink {
		s = "◌ " + s
	}
	return thinkingStyle.Render(wrap(s, m.width))
}

func (m *model) flushCurrent() {
	cur := strings.TrimRight(m.current, " \n")
	m.current = ""
	if cur != "" {
		m.appendAssistant(cur)
	}
	m.inMsg = false
}

func (m *model) currentView() string {
	s := m.current

	if m.busy {
		w := max(m.width-2, 1)
		body := indentPlain(wrap(s, w), 2)
		return botStyle.Render(glyphAssistant) + dropLeadSpaces(body, 2)
	}
	return wrap(s, m.width)
}

func indentPlain(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(ansi.Strip(l)) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

const minTranscriptRows = 5

func streamTail(s string, n int) string {
	if n < 1 {
		n = 1
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func (m *model) currentViewCapped() string {
	liveCap := m.streamCap(m.fixedChrome())
	if liveCap == 0 {
		return ""
	}
	return streamTail(m.currentView(), liveCap)
}

func (m *model) thinkViewCapped() string {
	liveCap := m.streamCap(m.fixedChrome())
	if liveCap == 0 {
		return ""
	}
	return streamTail(m.thinkView(), liveCap)
}

// bannerOnly reports whether the startup card is the whole transcript, which is
// when the card is centred vertically rather than pinned to the top.
func (m *model) bannerOnly() bool {
	return len(m.blocks) == 1 && m.blocks[0].kind == blockBanner
}
