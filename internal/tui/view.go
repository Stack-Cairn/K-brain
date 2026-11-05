package tui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// layout sizes the transcript viewport to whatever the chrome sections and
// the live area leave of the terminal.
func (m *model) layout() {
	m.growInput()
	if m.taskVP != nil {
		m.refreshTaskVP()
	}
	m.measureDock()

	chrome := m.fixedChrome()
	// The live area (the reply or thinking still streaming) is not a chrome
	// section: its cap is derived from the fixed chrome, so it comes last.
	if liveRows := m.liveAreaRows(); liveRows > 0 {
		chrome += liveRows + 1
	}

	w, h := max(m.width, minRenderWidth), max(m.height-chrome, 1)
	if m.vp.Width != w || m.vp.Height != h {
		m.vp.Width, m.vp.Height = w, h
		m.refreshVP()
	}
}

// measureDock renders the subagent dock once to learn its height, which the
// "dock" chrome section and dockTop read back.
func (m *model) measureDock() {
	m.dockRows = 0
	if dock := m.tasksDock(); dock != "" {
		m.dockRows = lipgloss.Height(dock)

		m.dockSkip = 0
		if m.tasksFocus {
			m.dockSkip++
		}
	}
}

// chromeSection is one fixed region of the frame outside the transcript and
// the live area. rows reports how many rows it takes right now, its separator
// included, and 0 while it is not shown.
type chromeSection struct {
	name string
	rows func(m *model) int
}

// chromeSectionList is every chrome section. layout and the live-area cap
// both sum this one list, so a section added here is accounted for
// everywhere. The rows below follow viewBody's drawing order, but the sum does
// not depend on it: new sections are appended at the end so concurrent
// additions merge cleanly.
var chromeSectionList = []chromeSection{
	{"interactive", func(m *model) int {
		if m.iactive == nil {
			return 0
		}
		return lipgloss.Height(m.interactiveView()) + 1
	}},
	{"permission", func(m *model) int {
		if m.permDialog == nil {
			return 0
		}
		return lipgloss.Height(m.permView()) + 1
	}},
	{"ask", func(m *model) int {
		if m.askDialog == nil {
			return 0
		}
		return lipgloss.Height(m.askView()) + 1
	}},
	{"busy", func(m *model) int {
		if !m.busy {
			return 0
		}
		return 2
	}},
	{"queue", (*model).queueRows},
	{"rewind", func(m *model) int {
		if m.rew == nil {
			return 0
		}
		return lipgloss.Height(m.rewindView()) + 1
	}},
	// The composer frame and the footer row.
	{"composer", func(m *model) int { return fixedFrameRows + m.composerRows() }},
	{"menu", func(m *model) int {
		if m.menu == nil {
			return 0
		}
		return lipgloss.Height(m.menuView()) + 1
	}},
	// Set by measureDock during layout.
	{"dock", func(m *model) int { return m.dockRows }},
	{"quitArm", func(m *model) int {
		if !m.quit1 {
			return 0
		}
		return 1
	}},
	{"escArm", func(m *model) int {
		if !m.escClr && (!m.esc1 || m.rew != nil || m.namePrompt != nil) {
			return 0
		}
		return 1
	}},
	{"composerPanel", (*model).composerPanelRows},
	// The plan pane above the composer (todo_pane.go).
	{"todo", (*model).todoPaneRows},
	{"transcriptBar", (*model).transcriptBarRows},
	{"usage", (*model).usageCardRows},
	// The plan review card (plan_view.go).
	{"plan", (*model).planRows},
}

func (m *model) chromeSections() []chromeSection { return chromeSectionList }

// fixedChrome is the rows every chrome section takes together.
func (m *model) fixedChrome() int {
	n := 0
	for _, s := range chromeSectionList {
		n += s.rows(m)
	}
	return n
}

func (m *model) streamCap(fixedChrome int) int {
	avail := m.height - fixedChrome - 1
	if avail < 2 {
		return 0
	}
	floor := min(minTranscriptRows, avail-1)
	return avail - floor
}

func (m *model) liveAreaRows() int {

	switch {
	case m.curThink != "":
		if cv := m.thinkViewCapped(); cv != "" {
			return lipgloss.Height(cv)
		}
	case m.current != "":
		if cv := m.currentViewCapped(); cv != "" {
			return lipgloss.Height(cv)
		}
	}
	return 0
}

func (m *model) dockTop() int {
	bottom := m.height
	if m.viewH > 0 {
		bottom = m.viewTop + m.viewH
	}
	return bottom - fixedFrameRows - m.dockRows + m.dockSkip
}

// padAboveTranscript inserts n blank rows at the very top of the frame, moving
// every row offset down with it.
func (m *model) padAboveTranscript(v string, n int) string {
	if n <= 0 {
		return v
	}
	v = strings.Repeat("\n", n) + v
	m.composerTop += n
	m.inputBodyOff += n
	m.viewportTop += n
	if m.effortRow >= 0 {
		m.effortRow += n
	}
	m.viewH += n
	return v
}

// padAboveComposer inserts n blank rows directly above the composer's top rule
// and moves every row offset that sits below the insertion point. Inserting at
// inputBodyOff instead would land inside the frame and strand its top rule up
// with the transcript.
func (m *model) padAboveComposer(v string, n int) string {
	if n <= 0 {
		return v
	}
	lines := strings.Split(v, "\n")
	at := max(min(m.composerTop, len(lines)), 0)
	v = strings.Join(slices.Concat(lines[:at], make([]string, n), lines[at:]), "\n")
	m.composerTop += n
	m.inputBodyOff += n
	if m.effortRow >= 0 {
		m.effortRow += n
	}
	m.viewH += n
	return v
}

func (m *model) View() string {
	m.syncInputPlaceholder()
	v := m.viewBody()

	// An empty transcript leaves blank rows at the top of the body. Drop them so
	// the banner really is the first row on screen.
	if lead := len(v) - len(strings.TrimLeft(v, "\n")); lead > 0 {
		v = v[lead:]
		m.inputBodyOff -= lead
		m.composerTop -= lead
		m.effortRow -= lead
		m.viewportTop -= lead
	}

	if m.height > 0 {
		m.viewH = lipgloss.Height(v)

		m.frameH, m.frameTop = m.height, 0
		if dropped := m.viewH - m.height; dropped > 0 {
			// Once the frame outgrows the terminal it clips from the top, which
			// pins the composer to the bottom exactly as a full screen should.
			v = strings.Join(strings.Split(v, "\n")[dropped:], "\n")
			m.inputBodyOff -= dropped
			m.composerTop -= dropped
			m.effortRow -= dropped
			m.viewportTop -= dropped
			m.vpLead += dropped
			m.viewH = m.height
		}

		// Mid-turn the frame can briefly shrink — a markdown block re-wraps as a
		// fence opens, the live area empties on a flush — which would bounce the
		// composer upwards while tokens stream. Hold the turn's high-water mark
		// and make up the difference just above the composer.
		if m.busy {
			m.turnFloorH = max(m.turnFloorH, m.viewH)
		} else {
			m.turnFloorH = 0
		}
		v = m.padAboveComposer(v, min(m.turnFloorH, m.height)-m.viewH)

		// Bottom-anchored composer: spare rows go between the transcript and the
		// composer, so the input box and its info rows hug the bottom. While the
		// startup card is the only thing in the transcript, half those rows move
		// above it instead, which centres the card in the empty region.
		m.viewTop = 0
		spare := m.height - m.viewH
		if spare > 0 && m.bannerOnly() {
			v = m.padAboveTranscript(v, spare/2)
			spare -= spare / 2
		}
		v = m.padAboveComposer(v, spare)
	}
	m.viewportTop += m.viewTop

	m.effortY = -1
	if m.effortRow >= 0 {
		m.effortY = m.viewTop + m.effortRow
	}

	if m.iactive != nil || m.height == 0 || m.palette != nil || m.picker != nil || m.mpicker != nil || m.taskVP != nil || m.surfaceOpen() {
		m.inputTop = -1
		m.inputLines = nil
	} else {
		m.inputTop = m.viewTop + m.inputBodyOff

		iv := m.input.View()
		if m.namePrompt != nil && m.namePrompt.mask {
			iv = m.namePrompt.label + " ┃ " + m.namePrompt.maskedValue(m.input.Value())
		}
		if m.ancientInput() {
			iv = m.ancientize(iv)
		}
		raw := strings.Split(iv, "\n")
		m.inputLines = make([]string, len(raw))
		for i, ln := range raw {
			m.inputLines[i] = strings.TrimRight(ansi.Strip(ln), " \t")
		}
	}
	return v
}

func (m *model) viewBody() string {
	var b strings.Builder
	m.viewportTop, m.viewportRows = 0, 0
	m.effortRow, m.effortX = -1, 0
	if m.palette != nil {

		b.WriteString(m.paletteView())
		return b.String()
	}
	if m.picker != nil {
		b.WriteString(m.pickerView())
		return b.String()
	}

	if m.mpicker != nil {
		b.WriteString(m.modelPickerView())
		return b.String()
	}
	if m.surfaceOpen() {
		b.WriteString(m.surfaceView())
		return b.String()
	}
	if m.taskVP != nil {
		b.WriteString(m.taskViewView())
		return b.String()
	}
	if s, ok := m.blockViewerView(); ok {
		return s
	}

	viewport := m.viewportView()
	m.viewportTop = strings.Count(b.String(), "\n")
	if viewport != "" {
		m.viewportRows = lipgloss.Height(viewport)
	}
	b.WriteString(viewport + "\n")

	if cv := m.thinkViewCapped(); m.curThink != "" && cv != "" {
		b.WriteString("\n" + m.ancientize(cv) + "\n")
	}

	if cv := m.currentViewCapped(); m.current != "" && cv != "" {
		b.WriteString("\n" + m.ancientize(cv) + "\n")
	}
	if m.iactive != nil {
		b.WriteString("\n" + m.interactiveView() + "\n")
	}
	if m.permDialog != nil {
		b.WriteString("\n" + m.permView() + "\n")
	}
	if m.askDialog != nil {
		b.WriteString("\n" + m.askView() + "\n")
	}
	b.WriteString(m.planSection())
	b.WriteString(m.todoPaneSection())
	b.WriteString(m.usageCardBlock())
	if m.busy {
		b.WriteString("\n" + m.busyRow() + "\n")
	}
	if m.queueRows() > 0 {
		b.WriteString(m.queueView())
	}
	if m.rew != nil {
		b.WriteString("\n" + m.rewindView() + "\n")
	}
	b.WriteString(m.transcriptBarView())

	m.inputBodyOff = strings.Count(b.String(), "\n")
	// composerTop is the first row of the composer *including* its top rule,
	// whereas inputBodyOff points at the input text one row inside it. Spare
	// rows must go above the rule or the frame splits in half.
	m.composerTop = m.inputBodyOff
	if m.iactive == nil {
		var inputView string
		if m.namePrompt != nil {
			if m.namePrompt.mask {
				inputView = m.highlightInput(sanitizeInputView("┃ " + m.namePrompt.maskedValue(m.input.Value())))
			} else {
				inputView = m.highlightInput(sanitizeInputView(m.composerInputView()))
			}
		} else {
			inputView = m.inputArgumentView(m.highlightInput(sanitizeInputView(m.composerInputView())))
		}
		frameWidth := max(m.width, 1)
		m.inputLeft = 0
		if m.ancientInput() {
			inputView = m.ancientize(inputView)
			b.WriteString(inputView)
		} else {
			frame := kbrainPromptFrame().Width(frameWidth)
			m.inputBodyOff += frame.GetBorderTopSize() + frame.GetPaddingTop()
			m.inputLeft = frame.GetBorderLeftSize() + frame.GetPaddingLeft()
			b.WriteString(m.titledRule(frame.Render(inputView)))
		}
	}
	if m.menu != nil {

		b.WriteString("\n" + m.menuView())
	}
	b.WriteString(m.composerPanel())

	if dock := m.tasksDock(); dock != "" {
		b.WriteString("\n" + dock)
	}
	hints, chipX := m.footerHintsAt()

	b.WriteString("\n")
	if chipX >= 0 {
		m.effortRow, m.effortX = strings.Count(b.String(), "\n"), chipX
	}
	b.WriteString(hints)
	return b.String()
}

func (m *model) ancientInput() bool {
	return m.ancientMode && m.namePrompt == nil && strings.TrimSpace(m.input.Value()) != "" && !strings.HasPrefix(strings.TrimSpace(m.input.Value()), "/")
}

func (m *model) ancientize(s string) string {
	if !m.ancientMode {
		return s
	}
	w := max(m.width-2, 8)
	return renderAncientText(ansi.Strip(s), w)
}
