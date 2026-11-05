package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m *model) layout() {
	m.growInput()

	chrome := fixedFrameRows + m.composerRows()

	if m.busy {
		chrome += 2
	}

	if m.iactive != nil {
		chrome += lipgloss.Height(m.interactiveView()) + 1
	}
	if m.askDialog != nil {
		chrome += lipgloss.Height(m.askView()) + 1
	}
	if m.permDialog != nil {
		chrome += lipgloss.Height(m.permView()) + 1
	}
	if m.menu != nil {

		chrome += lipgloss.Height(m.menuView()) + 1
	}
	if len(m.queue) > 0 {
		chrome += len(m.queue) + 1
	}
	if m.rew != nil {
		chrome += lipgloss.Height(m.rewindView()) + 1
	}
	if m.quit1 {
		chrome++
	}
	if m.escClr || (m.esc1 && m.rew == nil && m.namePrompt == nil) {
		chrome++
	}
	if m.taskVP != nil {
		m.refreshTaskVP()
	}
	m.dockRows = 0
	if dock := m.tasksDock(); dock != "" {
		m.dockRows = lipgloss.Height(dock)

		m.dockSkip = 0
		if m.tasksFocus {
			m.dockSkip++
		}
		chrome += m.dockRows
	}

	if liveRows := m.liveAreaRows(); liveRows > 0 {
		chrome += liveRows + 1
	}

	w, h := max(m.width, minRenderWidth), max(m.height-chrome, 1)
	if m.vp.Width != w || m.vp.Height != h {
		m.vp.Width, m.vp.Height = w, h
		m.refreshVP()
	}
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

func (m *model) fixedChrome() int {
	chrome := fixedFrameRows + m.composerRows()

	if m.busy {
		chrome += 2
	}

	if m.iactive != nil {
		chrome += lipgloss.Height(m.interactiveView()) + 1
	}
	if m.permDialog != nil {
		chrome += lipgloss.Height(m.permView()) + 1
	}
	if m.menu != nil {
		chrome += lipgloss.Height(m.menuView()) + 1
	}
	if len(m.queue) > 0 {
		chrome += len(m.queue) + 1
	}
	if m.rew != nil {
		chrome += lipgloss.Height(m.rewindView()) + 1
	}
	if m.quit1 {
		chrome++
	}
	if m.escClr || (m.esc1 && m.rew == nil && m.namePrompt == nil) {
		chrome++
	}
	return chrome + m.dockRows
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

	if m.iactive != nil || m.height == 0 || m.palette != nil || m.picker != nil || m.mpicker != nil || m.taskVP != nil {
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
	if m.taskVP != nil {
		b.WriteString(m.taskViewView())
		return b.String()
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
	if m.busy {
		b.WriteString("\n" + m.busyRow() + "\n")
	}
	if len(m.queue) > 0 {
		nav := ""
		if m.busy && m.input.Value() == "" {
			nav = " · ↑/↓ Select · Del Removes"
		}
		b.WriteString(dimStyle.Render(fmt.Sprintf(m.tr("queued (%d) · Enter on empty input steers%s"), len(m.queue), nav)) + "\n")
		for i, q := range m.queue {

			line := ansi.Truncate(youStyle.Render(" "+glyphUser)+q, m.width, "…")
			if i == m.queueSel {
				line = ansi.Truncate(botStyle.Render(" → ")+q+dimStyle.Render("  (Del to remove)"), m.width, "…")
			}
			b.WriteString(line + "\n")
		}
	}
	if m.rew != nil {
		b.WriteString("\n" + m.rewindView() + "\n")
	}

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
				inputView = m.highlightInput(sanitizeInputView(m.input.View()))
			}
		} else {
			inputView = m.inputArgumentView(m.highlightInput(sanitizeInputView(m.input.View())))
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
