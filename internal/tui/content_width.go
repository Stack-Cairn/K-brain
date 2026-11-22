package tui

import (
	"fmt"
	"slices"
	"strconv"
)

// maxContentWidth caps the width the transcript is laid out at, for wide
// windows where long lines are hard to read. 0, the default, is off.
//
// The capped transcript stays left-aligned rather than centred. Every
// transcript row then still starts at screen column 0, as the composer, the
// status rows and the cards below it do, so the caret, the rails and the
// tool-row glyphs keep lining up with the chrome, and a mouse column maps to
// the same text column it always did: selection, click-to-open and
// click-to-copy need no offset. Centring would move the transcript away from
// everything framed at full width and make the gutter part of every
// selection.
//
// Only the transcript and the streaming reply are capped; the chrome (cards,
// composer, status rows, pickers) keeps the full width.

// minContentWidth is the narrowest cap; a smaller setting is raised to it.
const minContentWidth = 40

// contentWidthSteps is what ←/→ walks on the settings row.
var contentWidthSteps = []int{0, 80, 100, 120, 160}

// maxContentWidth is the effective cap, 0 when off.
func (m *model) maxContentWidth() int {
	if m.cfg == nil || m.cfg.MaxContentWidth <= 0 {
		return 0
	}
	return max(m.cfg.MaxContentWidth, minContentWidth)
}

// contentWidth caps a transcript width.
func (m *model) contentWidth(w int) int {
	if c := m.maxContentWidth(); c > 0 && w > c {
		return c
	}
	return w
}

// contentWidthLabel is the settings value.
func (m *model) contentWidthLabel() string {
	if c := m.maxContentWidth(); c > 0 {
		return fmt.Sprintf(m.tr("%d columns"), c)
	}
	return m.tr("off")
}

// stepContentWidth moves the cap one notch along contentWidthSteps.
func (m *model) stepContentWidth(delta int) {
	cur := m.maxContentWidth()
	i := slices.Index(contentWidthSteps, cur)
	if i < 0 {
		// A hand-written value: step to the neighbour on that side.
		i, _ = slices.BinarySearch(contentWidthSteps, cur)
		if delta > 0 {
			i--
		}
	}
	n := len(contentWidthSteps)
	m.setMaxContentWidth(contentWidthSteps[((i+delta)%n+n)%n])
}

// setMaxContentWidth saves a cap and lays the transcript out again.
func (m *model) setMaxContentWidth(n int) {
	if m.cfg == nil {
		return
	}
	m.cfg.MaxContentWidth = max(n, 0)
	if m.cfgExtra == nil {
		m.cfgExtra = map[string]string{}
	}
	m.cfgExtra["maxContentWidth"] = strconv.Itoa(n)
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
	}
	m.refreshVP()
}
