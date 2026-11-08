package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/elicit"
)

// ── MCP elicitation ──────────────────────────────────────────────────────
//
// An MCP server may ask the user to fill in a form while one of its tools
// runs. The card names the asking server in its title (the name the user
// gave it in the mcp config, never one the server reports), shows the
// server's message, one row per field and an Accept · Decline · Cancel row.
// A field that reads like a secret is masked and the card warns that MCP
// servers must not ask for secrets. Text from the server is shown as plain
// text: escapes and control characters are dropped.
//
// Accept checks every field (elicit.Validate, as the MCP layer will) and,
// when something is wrong, keeps the card open with a message under each
// field at fault. Decline tells the server no; Cancel (Esc) closes the form
// without a choice.

type elicitRequest struct {
	req   elicit.Request
	reply chan elicit.Response
}

type elicitClose struct{ reply chan elicit.Response }

const (
	elicitAccept = iota
	elicitDecline
	elicitCancel
)

type elicitDialog struct {
	req    elicit.Request
	reply  chan elicit.Response
	inputs []elicitInput
	// focus is the current row: a field, or len(inputs) for the actions.
	focus  int
	action int
	// tried is set by the first Accept: from then on every field shows what
	// is wrong with it.
	tried  bool
	scroll cardScroll
	follow bool
}

// elicitInput is one field and the user's progress on it.
type elicitInput struct {
	f ai.ElicitField
	// text is the value of a text or number field as typed.
	text string
	// choice is the picked option of a single choice; -1 for none.
	choice int
	on     bool
	picked map[int]bool
	// sub is the option under the cursor in a multiple choice.
	sub     int
	touched bool
}

func (in *elicitInput) kind() string {
	switch {
	case in.f.Type == "boolean":
		return "bool"
	case in.f.Type == "array":
		return "multi"
	case len(in.f.Options) > 0:
		return "choice"
	}
	return "text"
}

func newElicitDialog(req elicit.Request, reply chan elicit.Response) *elicitDialog {
	d := &elicitDialog{req: req, reply: reply, follow: true}
	for _, f := range req.Fields {
		in := elicitInput{f: f, choice: -1, picked: map[int]bool{}}
		switch v := f.Default.(type) {
		case string:
			if i := optionIndex(f, v); i >= 0 {
				in.choice = i
			} else if len(f.Options) == 0 {
				in.text = v
			}
		case float64:
			in.text = strconv.FormatFloat(v, 'f', -1, 64)
		case int:
			in.text = strconv.Itoa(v)
		case bool:
			in.on = v
		case []any:
			for _, x := range v {
				if s, ok := x.(string); ok {
					if i := optionIndex(f, s); i >= 0 {
						in.picked[i] = true
					}
				}
			}
		case []string:
			for _, s := range v {
				if i := optionIndex(f, s); i >= 0 {
					in.picked[i] = true
				}
			}
		}
		d.inputs = append(d.inputs, in)
	}
	if len(d.inputs) == 0 {
		d.focus = 0 // straight to the actions
	}
	return d
}

func optionIndex(f ai.ElicitField, v string) int {
	for i, o := range f.Options {
		if o.Value == v {
			return i
		}
	}
	return -1
}

func (m *model) elicitOpen() bool { return m.cards != nil && m.cards.elicit != nil }

// installElicitHook makes the TUI the front end for MCP elicitations made
// during this session's tool calls. One form is open at a time; a request
// made while another is open waits its turn.
func (m *model) installElicitHook() {
	var mu sync.Mutex
	elicit.Default = func(ctx context.Context, req elicit.Request) elicit.Response {
		if m.prog == nil {
			return elicit.Response{Action: elicit.Decline, Reason: elicit.ReasonNoClient}
		}
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() != nil {
			return elicit.Response{Action: elicit.Cancel, Reason: elicit.ReasonCancelled}
		}
		reply := make(chan elicit.Response, 1)
		m.post(elicitRequest{req: req, reply: reply})
		select {
		case r := <-reply:
			return r
		case <-ctx.Done():
			m.post(elicitClose{reply: reply})
			return elicit.Response{Action: elicit.Cancel, Reason: elicit.ReasonCancelled}
		}
	}
}

// elicitMsg opens or closes the form card.
func (m *model) elicitMsg(msg tea.Msg) {
	c := m.cardsState()
	switch msg := msg.(type) {
	case elicitRequest:
		c.elicit = newElicitDialog(msg.req, msg.reply)
	case elicitClose:
		if c.elicit != nil && c.elicit.reply == msg.reply {
			c.elicit = nil
		}
	}
}

// value is the input's answer: present is false when the field is left
// empty; problem is set when the text is not a number.
func (in *elicitInput) value() (v any, present bool, problem string) {
	switch in.kind() {
	case "bool":
		return in.on, true, ""
	case "choice":
		if in.choice < 0 {
			return nil, false, ""
		}
		return in.f.Options[in.choice].Value, true, ""
	case "multi":
		var out []any
		for i, o := range in.f.Options {
			if in.picked[i] {
				out = append(out, o.Value)
			}
		}
		if len(out) == 0 && !in.f.Required {
			return nil, false, ""
		}
		if out == nil {
			out = []any{}
		}
		return out, true, ""
	}
	if in.f.Type == "number" || in.f.Type == "integer" {
		t := strings.TrimSpace(in.text)
		if t == "" {
			return nil, false, ""
		}
		x, err := strconv.ParseFloat(t, 64)
		if err != nil {
			if in.f.Type == "integer" {
				return nil, false, "must be a whole number"
			}
			return nil, false, "must be a number"
		}
		return x, true, ""
	}
	if in.text == "" {
		return nil, false, ""
	}
	return in.text, true, ""
}

// problem is what is wrong with the input's value ("" when nothing is). An
// empty required field only counts once the user has tried to accept.
func (d *elicitDialog) problem(in *elicitInput) string {
	v, present, bad := in.value()
	switch {
	case bad != "":
		return bad
	case !present:
		if in.f.Required && d.tried {
			return "is required"
		}
		return ""
	}
	return elicit.CheckField(in.f, v)
}

// content is the answer for Accept, with what is wrong per field.
func (d *elicitDialog) content() (map[string]any, elicit.Errors) {
	content := map[string]any{}
	errs := elicit.Errors{}
	for i := range d.inputs {
		in := &d.inputs[i]
		v, present, bad := in.value()
		if bad != "" {
			errs[in.f.Name] = bad
			continue
		}
		if present {
			content[in.f.Name] = v
		}
	}
	out, verr := elicit.Validate(d.req.Fields, content)
	for k, v := range verr {
		if _, ok := errs[k]; !ok {
			errs[k] = v
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return out, nil
}

func (m *model) elicitKey(msg tea.KeyMsg) {
	d := m.cards.elicit
	reply := func(r elicit.Response) {
		r.Reason = elicit.ReasonUser
		d.reply <- r
		m.cards.elicit = nil
	}
	if msg.Type == tea.KeyCtrlC {
		reply(elicit.Response{Action: elicit.Cancel})
		if m.busy && m.cancel != nil {
			m.cancel()
		}
		return
	}
	if cardScrollKey(&d.scroll, msg) {
		return
	}
	actions := d.focus >= len(d.inputs)
	var in *elicitInput
	if !actions {
		in = &d.inputs[d.focus]
	}
	if in != nil && in.kind() != "text" && cardRunes(msg, func(k tea.KeyMsg) bool { m.elicitKey(k); return m.cards.elicit == d }) {
		return
	}
	move := func(delta int) {
		n := len(d.inputs) + 1
		d.focus = (d.focus + delta + n) % n
		d.follow = true
		if d.focus == len(d.inputs) {
			d.action = elicitAccept
		}
	}
	accept := func() {
		content, errs := d.content()
		if errs == nil {
			reply(elicit.Response{Action: elicit.Accept, Content: content})
			return
		}
		d.tried = true
		for i := range d.inputs {
			if _, bad := errs[d.inputs[i].f.Name]; bad {
				d.focus, d.follow = i, true
				return
			}
		}
	}
	switch msg.Type {
	case tea.KeyEsc:
		reply(elicit.Response{Action: elicit.Cancel})
		return
	case tea.KeyUp, tea.KeyShiftTab:
		move(-1)
		return
	case tea.KeyDown, tea.KeyTab:
		move(1)
		return
	case tea.KeyEnter:
		switch {
		case !actions:
			move(1)
		case d.action == elicitAccept:
			accept()
		case d.action == elicitDecline:
			reply(elicit.Response{Action: elicit.Decline})
		default:
			reply(elicit.Response{Action: elicit.Cancel})
		}
		return
	}
	if actions {
		switch msg.Type {
		case tea.KeyLeft:
			d.action = (d.action + 2) % 3
		case tea.KeyRight:
			d.action = (d.action + 1) % 3
		}
		return
	}
	switch in.kind() {
	case "text":
		switch msg.Type {
		case tea.KeyBackspace:
			if r := []rune(in.text); len(r) > 0 {
				in.text = string(r[:len(r)-1])
			}
		case tea.KeyCtrlU:
			in.text = ""
		case tea.KeyRunes, tea.KeySpace:
			text := strings.Map(func(r rune) rune {
				if r == '\n' || r == '\r' || r == '\t' {
					return ' '
				}
				return r
			}, string(msg.Runes))
			if msg.Type == tea.KeySpace {
				text = " "
			}
			in.text += text
		default:
			return
		}
	case "choice":
		// An optional choice can go back to none (-1).
		lo, n := 0, len(in.f.Options)
		if !in.f.Required {
			lo = -1
		}
		switch msg.Type {
		case tea.KeyLeft:
			if in.choice--; in.choice < lo {
				in.choice = n - 1
			}
		case tea.KeyRight, tea.KeySpace:
			if in.choice++; in.choice >= n {
				in.choice = lo
			}
		default:
			return
		}
	case "bool":
		switch msg.Type {
		case tea.KeyLeft, tea.KeyRight, tea.KeySpace:
			in.on = !in.on
		default:
			return
		}
	case "multi":
		n := len(in.f.Options)
		switch msg.Type {
		case tea.KeyLeft:
			in.sub = (in.sub + n - 1) % n
		case tea.KeyRight:
			in.sub = (in.sub + 1) % n
		case tea.KeySpace:
			in.picked[in.sub] = !in.picked[in.sub]
		default:
			return
		}
	}
	in.touched = true
}

// plainText is text from an MCP server as the card shows it: no escapes,
// no control or bidirectional formatting characters, tabs as spaces;
// newlines only where keepLines.
func plainText(s string, keepLines bool) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' && keepLines:
			return r
		case r == '\t' || r == '\n' || r == '\r':
			return ' '
		case r < 0x20 || (r >= 0x7f && r < 0xa0):
			return -1
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e, r == 0x200f:
			return -1
		}
		return r
	}, s)
}

func (in *elicitInput) label() string {
	label := plainText(in.f.Title, false)
	if strings.TrimSpace(label) == "" {
		label = plainText(in.f.Name, false)
	}
	return label
}

func (m *model) elicitRows() int {
	if !m.elicitOpen() {
		return 0
	}
	return strings.Count(m.elicitView(), "\n") + 2
}

func (m *model) elicitSection() string {
	if !m.elicitOpen() {
		return ""
	}
	return "\n" + m.elicitView() + "\n"
}

func (m *model) elicitView() string {
	d := m.cards.elicit
	w := cardWidth(m.width)
	inner := cardInner(w)
	server := plainText(d.req.Server, false)
	head := cardTitleStyle.Inherit(accentFgStyle).Render("?") + " " +
		cardTitleStyle.Render(fmt.Sprintf(m.tr("%s asks for input"), accentFgStyle.Render(server)))

	var body []string
	add := func(rows ...string) { body = append(body, cardLines(rows, w)...) }
	from := fmt.Sprintf(m.tr("MCP server %s asks; only this server receives your answers."), server)
	if tool := plainText(d.req.Tool, false); tool != "" {
		from = fmt.Sprintf(m.tr("MCP server %s asks while running its tool %s; only this server receives your answers."), server, tool)
	}
	add(textSecondaryStyle.Render(wrap(from, inner)))
	add("")
	if msg := strings.TrimSpace(plainText(d.req.Message, true)); msg != "" {
		add(cardTitleStyle.Render(wrap(msg, inner)))
	}
	var secret []string
	for i := range d.inputs {
		if d.inputs[i].f.Sensitive {
			secret = append(secret, d.inputs[i].label())
		}
	}
	if len(secret) > 0 {
		add("", warnStyle.Render(wrapHanging(chromeGlyphs("⚠")+" ", fmt.Sprintf(m.tr("This form asks for what looks like a secret (%s). MCP servers must not ask for secrets: answer only if you trust %s."), strings.Join(secret, ", "), server), inner)))
	}
	if len(d.inputs) > 0 {
		add("")
	}
	starts := make([]int, len(d.inputs)+1)
	for i := range d.inputs {
		starts[i] = len(body)
		add(m.elicitFieldRows(d, i, w)...)
	}
	starts[len(d.inputs)] = len(body)

	tail := m.elicitTail(d, w, false)
	if room := m.cardMaxHeight() - 2 - len(tail); room < min(len(body), permMinBody) {
		tail = m.elicitTail(d, w, true)
	}
	room := max(m.cardMaxHeight()-2-len(tail), 2)
	if d.follow && len(body) > room && d.focus < len(d.inputs) {
		vis, first, last := room-1, starts[d.focus], starts[d.focus+1]-1
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

// elicitFieldRows is field i: its label and value on one row, then its
// description and what is wrong with it.
func (m *model) elicitFieldRows(d *elicitDialog, i, w int) []string {
	inner := cardInner(w)
	in := &d.inputs[i]
	focused := d.focus == i
	lead := "  "
	if focused {
		lead = accentFgStyle.Render(glyphs.User) + " "
	}
	label := in.label()
	if in.f.Required {
		label += " *"
	}
	if focused {
		label = cardTitleStyle.Render(label)
	}
	value := m.elicitValue(in, focused)
	rows := []string{wrapHanging(lead, label+textHintStyle.Render(": ")+value, inner)}
	if desc := strings.TrimSpace(plainText(in.f.Description, true)); desc != "" {
		rows = append(rows, textSecondaryStyle.Render(wrapHanging("    ", desc, inner)))
	}
	if p := d.problem(in); p != "" && (d.tried || (in.touched && !focused)) {
		rows = append(rows, errStyle.Render(wrapHanging("    "+glyphs.Warn+" ", m.tr(p), inner)))
	}
	return rows
}

// elicitValue is the input's value as its row shows it.
func (m *model) elicitValue(in *elicitInput, focused bool) string {
	switch in.kind() {
	case "bool":
		if in.on {
			return "[" + glyphs.OK + "] " + m.tr("yes")
		}
		return "[ ] " + m.tr("no")
	case "choice":
		cur := textMutedStyle.Render(m.tr("not chosen"))
		if in.choice >= 0 {
			cur = accentFgStyle.Render(plainText(in.f.Options[in.choice].Label, false))
		}
		if !focused {
			return cur
		}
		pos := ""
		if in.choice >= 0 {
			pos = textHintStyle.Render(fmt.Sprintf(" %d/%d", in.choice+1, len(in.f.Options)))
		}
		return textHintStyle.Render("< ") + cur + textHintStyle.Render(" >") + pos
	case "multi":
		parts := make([]string, len(in.f.Options))
		for j, o := range in.f.Options {
			mark := "[ ]"
			if in.picked[j] {
				mark = "[" + glyphs.OK + "]"
			}
			text := mark + " " + plainText(o.Label, false)
			switch {
			case focused && j == in.sub && monoMode:
				text = cardTitleStyle.Render(">" + text)
			case focused && j == in.sub:
				text = optionSelStyle.Render(text)
			case in.picked[j]:
				text = accentFgStyle.Render(text)
			}
			parts[j] = text
		}
		return strings.Join(parts, "  ")
	}
	text := in.text
	if in.f.Sensitive {
		dot := "•"
		if asciiMode {
			dot = "*"
		}
		text = strings.Repeat(dot, len([]rune(text)))
	}
	if focused {
		return text + elicitCursor()
	}
	if text == "" {
		return textMutedStyle.Render(m.elicitPlaceholder(in.f))
	}
	return text
}

// elicitCursor marks the end of the text being typed.
func elicitCursor() string {
	if asciiMode {
		return "_"
	}
	return "█"
}

// elicitPlaceholder is what an empty text or number field shows: the shape
// of the value it wants.
func (m *model) elicitPlaceholder(f ai.ElicitField) string {
	num := func(x *float64) string { return strconv.FormatFloat(*x, 'f', -1, 64) }
	switch {
	case f.Minimum != nil && f.Maximum != nil:
		return num(f.Minimum) + "-" + num(f.Maximum)
	case f.Minimum != nil:
		return ">= " + num(f.Minimum)
	case f.Maximum != nil:
		return "<= " + num(f.Maximum)
	case f.Type == "number" || f.Type == "integer":
		return m.tr("a number")
	}
	switch f.Format {
	case "email":
		return "name@example.com"
	case "uri":
		return "https://..."
	case "date":
		return "YYYY-MM-DD"
	case "date-time":
		return "YYYY-MM-DDThh:mm:ssZ"
	}
	return m.tr("empty")
}

// elicitTail is the card below the fields: the three actions and the key
// hints. compact leaves out the blank row.
func (m *model) elicitTail(d *elicitDialog, w int, compact bool) []string {
	inner := cardInner(w)
	var tail []string
	if !compact {
		tail = append(tail, "")
	}
	onActions := d.focus >= len(d.inputs)
	cur := -1
	lead := "  "
	if onActions {
		cur = d.action
		lead = accentFgStyle.Render(glyphs.User) + " "
	}
	tail = append(tail, lead+cardChoice([]string{m.tr("Accept"), m.tr("Decline"), m.tr("Cancel")}, cur))
	var hint string
	switch {
	case onActions:
		hint = m.tr("←→ choose · Enter confirms · Decline refuses, Cancel just closes")
	case d.inputs[d.focus].kind() == "choice":
		hint = m.tr("←→ choose · ↑↓ fields · Enter next · Esc cancels")
	case d.inputs[d.focus].kind() == "bool":
		hint = m.tr("Space toggles · ↑↓ fields · Enter next · Esc cancels")
	case d.inputs[d.focus].kind() == "multi":
		hint = m.tr("←→ move · Space picks · ↑↓ fields · Esc cancels")
	default:
		hint = m.tr("type to edit · ↑↓ fields · Enter next · Esc cancels")
	}
	tail = append(tail, textHintStyle.Render(wrap(chromeGlyphs(hint), inner)))
	return cardLines(tail, w)
}
