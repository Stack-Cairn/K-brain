package tui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// cardState is the interactive cards' state beyond the permission and
// question dialogs: the plan review card and the mode a plan returns to.
type cardState struct {
	// prePlan is the permission mode plan mode was entered from; approving a
	// plan returns to it.
	prePlan string
	plan    *planDialog
	// elicit is an MCP server's form (elicit_card.go).
	elicit *elicitDialog
}

func (m *model) cardsState() *cardState {
	if m.cards == nil {
		m.cards = &cardState{}
	}
	return m.cards
}

// ── Plan review ──────────────────────────────────────────────────────────
//
// In plan mode the agent ends its research with ExitPlanMode(plan). The card
// shows the plan with a line cursor: the user approves it (plan mode ends and
// the agent implements), asks for changes with a note and comments on single
// lines (the agent revises and asks again), or closes the card (the turn
// ends and plan mode stays on).

type planAnswer struct {
	verdict tools.PlanVerdict
	ok      bool
}

type planRequest struct {
	plan  string
	reply chan planAnswer
}

type planClose struct{ reply chan planAnswer }

// planEdit is what the card's text box is editing.
type planEdit int

const (
	planEditNone planEdit = iota
	planEditComment
	planEditFeedback
)

type planDialog struct {
	reply    chan planAnswer
	lines    []string
	cursor   int // the plan line comments attach to
	sel      int // 0 approve, 1 request changes
	comments map[int]string
	editing  planEdit
	draft    string
	scroll   cardScroll
	// follow asks the next render to scroll the cursor line into view.
	follow bool
	// starts[i] is the first body line of plan line i at the last render,
	// so keys can scroll a line into view.
	starts []int
}

// installPlanHook lets the agent's ExitPlanMode tool ask for a verdict on
// its plan through the card.
func (m *model) installPlanHook() {
	var mu sync.Mutex
	tools.PlanReview = func(ctx context.Context, plan string) (tools.PlanVerdict, bool) {
		if m.prog == nil {
			return tools.PlanVerdict{}, false
		}
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() != nil {
			return tools.PlanVerdict{}, false
		}
		reply := make(chan planAnswer, 1)
		m.post(planRequest{plan: plan, reply: reply})
		select {
		case a := <-reply:
			return a.verdict, a.ok
		case <-ctx.Done():
			m.post(planClose{reply: reply})
			return tools.PlanVerdict{}, false
		}
	}
}

// planMsg opens or closes the plan card.
func (m *model) planMsg(msg tea.Msg) {
	c := m.cardsState()
	switch msg := msg.(type) {
	case planRequest:
		plan := strings.TrimRight(strings.ReplaceAll(msg.plan, "\r\n", "\n"), "\n")
		if strings.TrimSpace(plan) == "" {
			plan = m.tr("(empty plan)")
		}
		c.plan = &planDialog{reply: msg.reply, lines: strings.Split(plan, "\n"), comments: map[int]string{}}
	case planClose:
		if c.plan != nil && c.plan.reply == msg.reply {
			c.plan = nil
		}
	}
}

func (m *model) planOpen() bool { return m.cards != nil && m.cards.plan != nil }

// planWaiting is the busy row's activity while the plan card is open.
func (m *model) planWaiting() string {
	return accentFgStyle.Bold(true).Render(glyphs.ToolQueued) + " " + accentFgStyle.Render(m.tr("Waiting for your plan review"))
}

// planExitMode is the mode approving the plan switches to: the one plan mode
// was entered from, or normal when the session started in plan mode.
func (m *model) planExitMode() string {
	if c := m.cardsState(); c.prePlan != "" && c.prePlan != "plan" {
		return c.prePlan
	}
	return "normal"
}

func (m *model) planKey(msg tea.KeyMsg) {
	d := m.cards.plan
	if d.editing == planEditNone && cardRunes(msg, func(k tea.KeyMsg) bool { m.planKey(k); return m.cards.plan == d }) {
		return
	}
	answer := func(a planAnswer) {
		d.reply <- a
		m.cards.plan = nil
	}
	approve := func() {
		// Leave plan mode before the agent hears the verdict, so its next
		// request already offers the editing tools.
		m.setPermissionMode(m.planExitMode())
		answer(planAnswer{verdict: tools.PlanVerdict{Approved: true}, ok: true})
	}
	if msg.Type == tea.KeyCtrlC {
		answer(planAnswer{})
		if m.busy && m.cancel != nil {
			m.cancel()
		}
		return
	}
	if d.editing != planEditNone {
		switch msg.Type {
		case tea.KeyEnter:
			text := strings.TrimSpace(d.draft)
			if d.editing == planEditComment {
				if text == "" {
					delete(d.comments, d.cursor)
				} else {
					d.comments[d.cursor] = text
				}
				d.editing, d.draft = planEditNone, ""
				return
			}
			if text == "" && len(d.comments) == 0 {
				return // nothing to send yet
			}
			answer(planAnswer{verdict: tools.PlanVerdict{Feedback: d.feedback(text)}, ok: true})
		case tea.KeyEsc:
			d.editing, d.draft = planEditNone, ""
		case tea.KeyBackspace:
			if r := []rune(d.draft); len(r) > 0 {
				d.draft = string(r[:len(r)-1])
			}
		case tea.KeyRunes, tea.KeySpace:
			d.draft += string(msg.Runes)
		}
		return
	}
	if cardScrollKey(&d.scroll, msg) {
		// Keep the cursor on a line in view.
		if d.scroll.page > 0 {
			first, last := d.scroll.off, d.scroll.off+d.scroll.page-1
			if at := d.lineAt(first); d.cursor < at {
				d.cursor = at
			} else if at := d.lineAt(last); d.cursor > at {
				d.cursor = at
			}
		}
		return
	}
	move := func(delta int) {
		d.cursor = min(max(d.cursor+delta, 0), len(d.lines)-1)
		d.follow = true
	}
	requestChanges := func() {
		d.editing, d.draft = planEditFeedback, ""
	}
	switch msg.Type {
	case tea.KeyUp:
		move(-1)
	case tea.KeyDown:
		move(1)
	case tea.KeyHome:
		move(-len(d.lines))
	case tea.KeyEnd:
		move(len(d.lines))
	case tea.KeyLeft, tea.KeyRight, tea.KeyTab, tea.KeyShiftTab:
		d.sel = 1 - d.sel
	case tea.KeyEnter:
		if d.sel == 0 {
			approve()
		} else {
			requestChanges()
		}
	case tea.KeyEsc:
		answer(planAnswer{})
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "k":
			move(-1)
		case "j":
			move(1)
		case "1", "a", "y":
			approve()
		case "2", "r", "n":
			d.sel = 1
			requestChanges()
		case "c":
			d.editing, d.draft = planEditComment, d.comments[d.cursor]
		}
	}
}

// feedback is the note sent with "request changes": the user's words, then
// each line comment with the line it is about.
func (d *planDialog) feedback(note string) string {
	var b strings.Builder
	b.WriteString(note)
	if len(d.comments) > 0 {
		if note != "" {
			b.WriteString("\n\n")
		}
		b.WriteString("Comments on plan lines:")
		keys := make([]int, 0, len(d.comments))
		for k := range d.comments {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "\n- line %d (%s): %s", k+1, strings.TrimSpace(d.lines[k]), d.comments[k])
		}
	}
	return b.String()
}

// lineAt is the plan line drawn on body line row.
func (d *planDialog) lineAt(row int) int {
	s := d.starts
	if len(s) == 0 {
		return min(max(row, 0), len(d.lines)-1)
	}
	at := 0
	for i, start := range s {
		if start <= row {
			at = i
		}
	}
	return at
}

// planRows is the plan card's height in the chrome.
func (m *model) planRows() int {
	if !m.planOpen() {
		return 0
	}
	return strings.Count(m.planView(), "\n") + 2
}

// planSection is the plan card as drawn between the live area and the
// composer.
func (m *model) planSection() string {
	if !m.planOpen() {
		return ""
	}
	return "\n" + m.planView() + "\n"
}

func (m *model) planView() string {
	d := m.cards.plan
	w := cardWidth(m.width)
	inner := cardInner(w)
	head := cardTitleStyle.Inherit(accentFgStyle).Render(chromeGlyphs("◆")) + " " + cardTitleStyle.Render(m.tr("Plan ready for review"))

	// Body: the plan, numbered, with the cursor line marked and comments
	// under the line they are about.
	num := len(strconv.Itoa(len(d.lines)))
	var body []string
	starts := make([]int, len(d.lines))
	for i, l := range d.lines {
		starts[i] = len(body)
		mark := "  "
		if i == d.cursor {
			mark = accentFgStyle.Render(glyphs.User) + " "
		}
		lead := mark + textHintStyle.Render(fmt.Sprintf("%*d", num, i+1)) + " "
		text := planLineStyle(l)
		if i == d.cursor && d.editing == planEditComment {
			text = cardTitleStyle.Render(l)
		}
		body = append(body, cardLines([]string{wrapHanging(lead, text, inner)}, w)...)
		if c, ok := d.comments[i]; ok {
			pad := strings.Repeat(" ", num+3)
			body = append(body, cardLines([]string{wrapHanging(pad+accentFgStyle.Render(chromeGlyphs("└ ")), accentFgStyle.Render(c), inner)}, w)...)
		}
	}
	d.starts = starts

	// Tail: the choices, the text box and the hints stay in view; on a short
	// screen without the spacing, so the plan keeps a few rows.
	tail := m.planTail(d, w, false)
	if room := m.cardMaxHeight() - 2 - len(tail); room < min(len(body), permMinBody) {
		tail = m.planTail(d, w, true)
	}
	room := max(m.cardMaxHeight()-2-len(tail), 2)
	if d.follow && len(body) > room {
		// Bring the cursor line, with its comment, into the window the user
		// will see; its first row wins when it is taller than the window.
		vis, first, last := room-1, starts[d.cursor], len(body)-1
		if d.cursor+1 < len(starts) {
			last = starts[d.cursor+1] - 1
		}
		if last >= d.scroll.off+vis {
			d.scroll.off = last - vis + 1
		}
		if first < d.scroll.off {
			d.scroll.off = first
		}
	}
	d.follow = false
	rows := d.scroll.window(body, room)
	return renderCard(tokAccent, head, append(rows, tail...), w)
}

// planTail is the card below the plan: the two choices, the text box when
// one is open, and the key hints, one line each. compact leaves out the blank
// rows.
func (m *model) planTail(d *planDialog, w int, compact bool) []string {
	inner := cardInner(w)
	gap := []string{""}
	if compact {
		gap = nil
	}
	tail := append([]string(nil), gap...)
	back := m.modeLabel(m.planExitMode())
	tail = append(tail,
		cardOption(d.sel == 0 && d.editing == planEditNone, "1", fmt.Sprintf(m.tr("Yes, approve the plan and switch to %s"), back), w),
		cardOption(d.sel == 1 && d.editing == planEditNone, "2", m.tr("No, request changes"), w))
	if n := len(d.comments); n > 0 {
		note := m.tr("1 line comment is sent with the changes")
		if n > 1 {
			note = fmt.Sprintf(m.tr("%d line comments are sent with the changes"), n)
		}
		tail = append(tail, textSecondaryStyle.Render(wrapHanging("     ", note, inner)))
	}
	switch d.editing {
	case planEditComment:
		tail = append(append(tail, gap...),
			wrapHanging(accentFgStyle.Render(fmt.Sprintf(m.tr("comment on line %d: "), d.cursor+1)), d.draft+"█", inner),
			textHintStyle.Render(chromeGlyphs(m.tr("Enter saves (empty removes) · Esc Back"))))
	case planEditFeedback:
		tail = append(append(tail, gap...),
			wrapHanging(accentFgStyle.Render(m.tr("what should change: ")), d.draft+"█", inner),
			textHintStyle.Render(chromeGlyphs(m.tr("Enter sends · Esc Back"))))
	default:
		hint := m.tr("↑↓ line · c comment · 1 approve · 2 request changes · Esc close")
		if compact {
			hint = m.tr("↑↓ line · c comment · 1/2 · Esc")
		}
		tail = append(append(tail, gap...), textHintStyle.Render(chromeGlyphs(hint)))
	}
	return cardLines(tail, w)
}

// planLineStyle styles a plan line as light markdown: headings bold, the
// rest as written.
func planLineStyle(l string) string {
	if t := strings.TrimSpace(l); strings.HasPrefix(t, "#") {
		return cardTitleStyle.Render(strings.TrimSpace(strings.TrimLeft(t, "#")))
	}
	return l
}
