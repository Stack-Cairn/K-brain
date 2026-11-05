package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

type askRequest struct {
	req   tools.AskRequest
	reply chan askAnswer
}

type askAnswer struct {
	answers []string
	ok      bool
}

// askDialog is the question card. A request in the single-question form is
// one tab answered with the chosen labels; the questions form gets a tab per
// question (and a Submit tab when there are several) and is answered with one
// entry per question.
type askDialog struct {
	req     tools.AskRequest
	reply   chan askAnswer
	tabs    []askTab
	tab     int // current tab; len(tabs) is the Submit tab
	editing bool
	// warn is a note for the Submit tab, such as an unanswered question.
	warn string
}

// askTab is one question and the user's progress on it.
type askTab struct {
	q      tools.AskQuestion
	sel    int
	picked map[int]bool
	custom string
	answer string // the answer once given
	done   bool
}

// openAsk shows the question card for msg.
func (m *model) openAsk(msg askRequest) {
	m.askDialog = newAskDialog(msg.req, msg.reply)
}

func newAskDialog(req tools.AskRequest, reply chan askAnswer) *askDialog {
	d := &askDialog{req: req, reply: reply}
	qs := req.Questions
	if len(qs) == 0 {
		qs = []tools.AskQuestion{{Prompt: req.Question, Options: req.Options, Multiple: req.Multiple}}
	}
	for _, q := range qs {
		d.tabs = append(d.tabs, askTab{q: q, picked: map[int]bool{}})
	}
	return d
}

// multi reports the questions form, answered one entry per question.
func (d *askDialog) multi() bool { return len(d.req.Questions) > 0 }

// submitTab reports whether the card has a Submit tab and it is current.
func (d *askDialog) onSubmit() bool { return d.tab >= len(d.tabs) }

// tabCount is the tabs the user moves between: the questions, plus Submit
// when there are several.
func (d *askDialog) tabCount() int {
	if len(d.tabs) > 1 {
		return len(d.tabs) + 1
	}
	return len(d.tabs)
}

type askClose struct{ reply chan askAnswer }

func (m *model) installAskHook() {
	var mu sync.Mutex
	tools.Ask = func(ctx context.Context, req tools.AskRequest) ([]string, bool) {
		if m.prog == nil {
			return nil, false
		}
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() != nil {
			return nil, false
		}
		reply := make(chan askAnswer, 1)
		m.prog.Send(askRequest{req: req, reply: reply})
		select {
		case ans := <-reply:
			return ans.answers, ans.ok
		case <-ctx.Done():
			m.prog.Send(askClose{reply: reply})
			return nil, false
		}
	}
	m.installPlanHook()
}

func (m *model) askKey(msg tea.KeyMsg) bool {
	d := m.askDialog
	if d == nil {
		return false
	}
	if !d.editing && cardRunes(msg, func(k tea.KeyMsg) bool { m.askKey(k); return m.askDialog == d }) {
		return true
	}
	answer := func(a askAnswer) {
		d.reply <- a
		m.askDialog = nil
	}
	if msg.Type == tea.KeyCtrlC {
		answer(askAnswer{ok: false})
		if m.busy && m.cancel != nil {
			m.cancel()
		}
		return true
	}
	// finish records the current question's answer and moves on: to the next
	// open question, to Submit, or straight to the reply for a lone question.
	finish := func(t *askTab, text string, labels []string) {
		if !d.multi() {
			answer(askAnswer{answers: labels, ok: true})
			return
		}
		t.answer, t.done = text, true
		if len(d.tabs) == 1 {
			answer(askAnswer{answers: []string{text}, ok: true})
			return
		}
		d.warn = ""
		from := d.tab
		d.tab = len(d.tabs)
		for i := 1; i < len(d.tabs); i++ {
			if j := (from + i) % len(d.tabs); !d.tabs[j].done {
				d.tab = j
				break
			}
		}
	}
	// confirm answers a multi-select question with its picks and own words.
	confirm := func(t *askTab) {
		var labels []string
		for i, o := range t.q.Options {
			if t.picked[i] {
				labels = append(labels, o.Label)
			}
		}
		if c := strings.TrimSpace(t.custom); c != "" {
			labels = append(labels, c)
		}
		if len(labels) > 0 {
			finish(t, strings.Join(labels, ", "), labels)
		}
	}
	switchTab := func(delta int) {
		if n := d.tabCount(); n > 1 {
			d.tab = (d.tab + delta + n) % n
			d.editing = false
		}
	}

	if d.onSubmit() {
		switch msg.Type {
		case tea.KeyEnter:
			out := make([]string, len(d.tabs))
			for i, t := range d.tabs {
				if !t.done {
					d.warn = fmt.Sprintf(m.tr("question %d is not answered yet"), i+1)
					return true
				}
				out[i] = t.answer
			}
			answer(askAnswer{answers: out, ok: true})
		case tea.KeyLeft, tea.KeyShiftTab:
			switchTab(-1)
		case tea.KeyRight, tea.KeyTab:
			switchTab(1)
		case tea.KeyEsc:
			answer(askAnswer{ok: false})
		}
		return true
	}

	t := &d.tabs[d.tab]
	n := len(t.q.Options)
	rows := n + 1
	choose := func(i int) {
		if i == n {
			d.editing = true
			return
		}
		if t.q.Multiple {
			t.picked[i] = !t.picked[i]
			return
		}
		finish(t, t.q.Options[i].Label, []string{t.q.Options[i].Label})
	}
	if d.editing {
		switch msg.Type {
		case tea.KeyEnter:
			d.editing = false
			if t.q.Multiple {
				confirm(t)
			} else if c := strings.TrimSpace(t.custom); c != "" {
				finish(t, c, []string{c})
			}
		case tea.KeyEsc:
			d.editing = false
		case tea.KeyBackspace:
			if r := []rune(t.custom); len(r) > 0 {
				t.custom = string(r[:len(r)-1])
			}
		case tea.KeyRunes, tea.KeySpace:
			t.custom += string(msg.Runes)
		}
		return true
	}
	switch msg.Type {
	case tea.KeyUp:
		t.sel = (t.sel + rows - 1) % rows
	case tea.KeyDown:
		t.sel = (t.sel + 1) % rows
	case tea.KeyTab:
		if d.tabCount() > 1 {
			switchTab(1)
		} else {
			t.sel = (t.sel + 1) % rows
		}
	case tea.KeyShiftTab, tea.KeyLeft:
		switchTab(-1)
	case tea.KeyRight:
		switchTab(1)
	case tea.KeyEnter:
		if t.q.Multiple && t.sel != n {
			confirm(t)
			return true
		}
		choose(t.sel)
	case tea.KeySpace:
		choose(t.sel)
	case tea.KeyEsc:
		answer(askAnswer{ok: false})
	case tea.KeyRunes:
		s := string(msg.Runes)
		switch s {
		case "k":
			t.sel = (t.sel + rows - 1) % rows
		case "j":
			t.sel = (t.sel + 1) % rows
		default:
			if i, err := strconv.Atoi(s); err == nil && i >= 1 && i <= rows {
				t.sel = i - 1
				choose(t.sel)
			}
		}
	}
	return true
}

// cur is the current question's index (the last one on the Submit tab).
func (d *askDialog) cur() int { return min(d.tab, len(d.tabs)-1) }

func (m *model) askView() string {
	d := m.askDialog
	if d == nil {
		return ""
	}
	w := cardWidth(m.width)
	inner := cardInner(w)
	title := m.tr("Question")
	if len(d.tabs) > 1 {
		title = fmt.Sprintf(m.tr("Questions %d/%d"), d.cur()+1, len(d.tabs))
	}
	head := cardTitleStyle.Inherit(accentFgStyle).Render("?") + " " + cardTitleStyle.Render(title)
	var rows []string
	if d.tabCount() > 1 {
		rows = append(rows, m.askTabBar(d), "")
	}
	if d.onSubmit() {
		return renderCard(tokAccent, head, append(rows, m.askSummary(d, inner)...), w)
	}
	t := &d.tabs[d.tab]
	prompt := t.q.Prompt
	if t.q.Multiple {
		prompt += m.tr(" (select all that apply)")
	}
	rows = append(rows, cardTitleStyle.Render(wrap(prompt, inner)), "")
	for i, o := range t.q.Options {
		mark := ""
		if t.q.Multiple {
			mark = "[ ] "
			if t.picked[i] {
				mark = "[" + glyphs.OK + "] "
			}
		}
		rows = append(rows, cardOption(i == t.sel && !d.editing, strconv.Itoa(i+1), mark+o.Label, w))
		if o.Description != "" {
			rows = append(rows, textSecondaryStyle.Render("     "+strings.ReplaceAll(wrap(o.Description, max(inner-5, 1)), "\n", "\n     ")))
		}
	}
	n := strconv.Itoa(len(t.q.Options) + 1)
	custom := m.tr("type your own answer")
	if d.editing {
		rows = append(rows, accentFgStyle.Render(glyphs.User+" "+n+"  "+custom+": ")+t.custom+"█",
			"", textHintStyle.Render(chromeGlyphs(m.tr("Enter confirms · Esc Back"))))
		return renderCard(tokAccent, head, rows, w)
	}
	other := textMutedStyle.Render(custom)
	if c := strings.TrimSpace(t.custom); c != "" {
		other = custom + ": " + c
	}
	rows = append(rows, cardOption(t.sel == len(t.q.Options), n, other, w))
	hint := m.tr("↑↓ or 1-9 select · Enter picks · Esc dismisses")
	if t.q.Multiple {
		hint = m.tr("↑↓ or 1-9 toggle · Enter submits · Esc dismisses")
		if len(d.tabs) > 1 {
			hint = m.tr("↑↓ or 1-9 toggle · Enter confirms · Esc dismisses")
		}
	}
	if d.tabCount() > 1 {
		hint = m.tr("←→ questions · ") + hint
	}
	rows = append(rows, "", textHintStyle.Render(chromeGlyphs(hint)))
	return renderCard(tokAccent, head, rows, w)
}

// askTabBar is the row of question tabs: each header with its state, the
// current one marked, then Submit.
func (m *model) askTabBar(d *askDialog) string {
	var parts []string
	for i, t := range d.tabs {
		label := t.q.Header
		if label == "" {
			label = fmt.Sprintf("Q%d", i+1)
		}
		mark := chromeGlyphs("○")
		if t.done {
			mark = glyphs.OK
		}
		parts = append(parts, m.askTabCell(mark+" "+ansi.Truncate(label, 16, "…"), i == d.tab, t.done))
	}
	parts = append(parts, m.askTabCell(m.tr("Submit"), d.onSubmit(), false))
	return strings.Join(parts, "  ")
}

func (m *model) askTabCell(text string, current, done bool) string {
	text = chromeGlyphs(text)
	switch {
	case current && monoMode:
		return cardTitleStyle.Render("[" + text + "]")
	case current:
		return optionSelStyle.Render(" " + text + " ")
	case done:
		return " " + okStyle.Render(text) + " "
	}
	return " " + textMutedStyle.Render(text) + " "
}

// askSummary is the Submit tab: every question with its answer so far.
func (m *model) askSummary(d *askDialog, inner int) []string {
	var rows []string
	for i, t := range d.tabs {
		rows = append(rows, wrapHanging(fmt.Sprintf("%d. ", i+1), t.q.Prompt, inner))
		ans := textMutedStyle.Render(m.tr("not answered yet"))
		if t.done {
			ans = accentFgStyle.Render(t.answer)
		}
		rows = append(rows, wrapHanging("   "+chromeGlyphs("→")+" ", ans, inner))
	}
	if d.warn != "" {
		rows = append(rows, "", warnStyle.Render(wrap(d.warn, inner)))
	}
	return append(rows, "", textHintStyle.Render(chromeGlyphs(m.tr("Enter submits · ←→ questions · Esc dismisses"))))
}
