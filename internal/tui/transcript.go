package tui

import (
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m *model) seedTranscript(msgs []ai.Message, base int) {
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
			if strings.TrimSpace(msg.TextContent()) != "" {
				bi = len(m.blocks)
				m.blocks = append(m.blocks, block{kind: blockAssistant, text: strings.TrimRight(msg.TextContent(), "\n")})
			}
			for _, tc := range msg.ToolCalls {
				m.blocks = append(m.blocks, block{kind: blockText, text: toolHeaderRow(tc.Function.Name, tc.Function.Arguments, false), toolName: tc.Function.Name})
			}
		case "tool":

			switch {
			case strings.HasPrefix(msg.Content, "Error: tool call interrupted"):
				m.blocks = append(m.blocks, block{kind: blockText, text: errStyle.Render("⚒ "+msg.Name+" ") + dimStyle.Render("— interrupted: session ended before a result was recorded")})
			default:
				if diff, _ := extractDiff(strings.TrimRight(msg.Content, "\n")); diff != "" {
					m.blocks = append(m.blocks, block{kind: blockTool, text: msg.Content})
				}
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

	// subID binds a subagent tool row to the subagent it runs; subLine marks
	// the status row under a background spawn.
	subID   string
	subLine bool

	// think marks a thinking paragraph that later reasoning lines extend.
	think bool

	y0, y1 int

	hover bool

	rendered string
	lines    int
	width    int
	stale    bool
	ancient  bool
}

func (b *block) renderAt(width int) string {
	return b.renderAtMode(width, false)
}

func (b *block) renderAtMode(width int, ancient bool) string {
	if !b.stale && b.width == width && b.ancient == ancient {
		return b.rendered
	}
	if ancient && (b.kind == blockAssistant || b.kind == blockUser) {
		b.rendered = accentStyle.Render(renderAncientText(ansi.Strip(b.render(width)), width))
	} else {
		b.rendered = b.render(width)
	}
	b.lines = lipgloss.Height(b.rendered)
	b.width, b.ancient, b.stale = width, ancient, false
	return b.rendered
}

func (b block) render(width int) string { return renderBlock(b, width) }

func (b *block) toggle() bool {
	if b.kind != blockTool && b.kind != blockToolRun {
		return false
	}
	b.expanded = !b.expanded
	b.stale = true
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

func (m *model) refreshVP() {
	if m.width == 0 {
		return
	}

	width := max(m.width, minRenderWidth)
	var b strings.Builder
	if n := len(m.blocks); n > 0 {
		b.Grow(n*24 + 1<<20)
	}
	line := 0
	wrote := false
	for i := range m.blocks {
		// The startup card only exists while the conversation is empty: as
		// soon as any message lands the card leaves the transcript entirely.
		if m.blocks[i].kind == blockBanner && len(m.blocks) > 1 {
			m.blocks[i].y0, m.blocks[i].y1 = -1, -1
			continue
		}
		if wrote {
			if gluedToPrev(m.blocks, i) {
				b.WriteString("\n")
			} else {
				b.WriteString("\n\n")
				line++
			}
		}
		r := m.blocks[i].renderAtMode(width, m.ancientMode)
		m.blocks[i].y0 = line
		m.blocks[i].y1 = line + m.blocks[i].lines - 1
		b.WriteString(r)
		line = m.blocks[i].y1 + 1
		wrote = true
	}
	// The transcript is top-anchored: content starts at viewport row 0 and the
	// whole frame grows downwards, so there is no leading pad to insert here.
	m.vp.SetContent(b.String())

	// SetContent only clamps against the line count, never against maxYOffset.
	// Without the old bottom pad a stale offset would leave the top of a shrunk
	// transcript scrolled off with nothing to pull it back.
	m.vp.SetYOffset(m.vp.YOffset)
	switch {
	case m.follow:
		m.vp.GotoBottom()
	case m.vp.AtBottom():
		m.follow = true
	}
}

// transcriptRows is how many rows the rendered blocks occupy.
func (m *model) transcriptRows() int {
	if len(m.blocks) == 0 {
		return 0
	}
	return m.blocks[len(m.blocks)-1].y1 + 1
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
