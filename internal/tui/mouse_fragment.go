package tui

import (
	"io"
	"os"
	"regexp"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Split mouse reports. The input reader parses SGR mouse reports
// (ESC [ < b ; x ; y M) only when a report arrives whole in one read. A fast
// wheel burst overruns its 256-byte read buffer, and a pty can hand a report
// over in two short reads; the cut report then surfaces as keys: Alt+[ (or a
// bare Esc) followed by text such as "<65;40;12M", which would be typed into
// the composer — and a bare Esc would also interrupt the turn.
//
// mouseFrag puts such reports back together. An Esc or Alt+[ is held for a
// moment; if what follows completes a mouse report, the report is handled
// as the mouse event it is, and otherwise the held key is replayed first,
// in order. Only text that follows an ESC within mouseFragWindow counts, so
// typing "[<1;2;3M" by hand still types it: a person cannot press Esc and
// type the rest within a few milliseconds, while the halves of one report
// arrive back to back.

// mouseFragWindow is how long an Esc or Alt+[ is held waiting for the rest
// of a report. Both halves of a cut report arrive within a millisecond or
// two; a held key that nothing follows is replayed when it runs out.
const mouseFragWindow = 30 * time.Millisecond

type mouseFrag struct {
	held      *tea.KeyMsg
	text      string // what of the report has arrived, from its "["
	at        time.Time
	seq       int
	replaying bool
}

// mouseFragFlush ends the wait for the rest of a report.
type mouseFragFlush struct{ seq int }

var (
	sgrPrefixRe = regexp.MustCompile(`^(\[|\[<|\[<\d+|\[<\d+;|\[<\d+;\d+|\[<\d+;\d+;|\[<\d+;\d+;\d+)?$`)
	sgrReportRe = regexp.MustCompile(`^\[<(\d+);(\d+);(\d+)([Mm])`)
)

// startsFrag reports whether k can be the cut-off head of a mouse report.
func startsFrag(k tea.KeyMsg) bool {
	if k.Paste {
		return false
	}
	return (k.Type == tea.KeyEsc && !k.Alt) ||
		(k.Type == tea.KeyRunes && k.Alt && len(k.Runes) == 1 && k.Runes[0] == '[')
}

// mouseFragment runs ahead of Update's dispatch. handled says msg was taken
// care of here, with cmd to run.
func (m *model) mouseFragment(msg tea.Msg) (handled bool, cmd tea.Cmd) {
	f := &m.frag
	if !m.fragFilter || f.replaying {
		return false, nil
	}
	switch msg := msg.(type) {
	case mouseFragFlush:
		if f.held == nil || msg.seq != f.seq {
			return true, nil
		}
		held, partial := *f.held, f.text
		f.held, f.text = nil, ""
		keys, ok := heldKeys(held, partial)
		if !ok {
			// A report that never finished: drop it rather than type it.
			return true, nil
		}
		return true, m.replayKeys(keys...)
	case tea.KeyMsg:
		if f.held == nil {
			if !startsFrag(msg) {
				return false, nil
			}
			k := msg
			f.held, f.at = &k, m.nowFn()
			f.text = ""
			if msg.Alt {
				f.text = "["
			}
			f.seq++
			seq := f.seq
			return true, tea.Tick(mouseFragWindow, func(time.Time) tea.Msg { return mouseFragFlush{seq: seq} })
		}
		if msg.Type == tea.KeyRunes && !msg.Alt && !msg.Paste && m.nowFn().Sub(f.at) <= 4*mouseFragWindow {
			text := f.text + string(msg.Runes)
			if mm := sgrReportRe.FindStringSubmatchIndex(text); mm != nil {
				ev, ok := parseSGRMouse(text[:mm[1]])
				rest := text[mm[1]:]
				f.held, f.text = nil, ""
				var cmds []tea.Cmd
				if ok {
					_, c := m.Update(ev)
					cmds = append(cmds, c)
				}
				if rest != "" {
					cmds = append(cmds, m.replayKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(rest)}))
				}
				return true, tea.Batch(cmds...)
			}
			if sgrPrefixRe.MatchString(text) {
				f.text, f.at = text, m.nowFn()
				return true, nil
			}
		}
		// Not a report after all: the held key goes first, then this one.
		held, partial := *f.held, f.text
		f.held, f.text = nil, ""
		keys, ok := heldKeys(held, partial)
		if !ok {
			// Part of a report had already arrived; it is not typing.
			return true, m.replayKeys(msg)
		}
		return true, m.replayKeys(append(keys, msg)...)
	}
	return false, nil
}

// heldKeys is what was held back, as the keys that arrived: the Esc or Alt+[
// and, after a bare Esc, a "[" that came as a key of its own (it was being
// held as the possible start of a report). ok is false when more of a
// report had arrived: that is a cut report, not typing.
func heldKeys(held tea.KeyMsg, partial string) (keys []tea.KeyMsg, ok bool) {
	own := ""
	if held.Alt {
		own = "["
	}
	switch partial {
	case own:
		return []tea.KeyMsg{held}, true
	case own + "[":
		if own == "" {
			return []tea.KeyMsg{held, {Type: tea.KeyRunes, Runes: []rune("[")}}, true
		}
	}
	return nil, false
}

// replayKeys runs keys through Update in order, past the fragment check.
func (m *model) replayKeys(keys ...tea.KeyMsg) tea.Cmd {
	m.frag.replaying = true
	defer func() { m.frag.replaying = false }()
	var cmds []tea.Cmd
	for _, k := range keys {
		_, c := m.Update(k)
		cmds = append(cmds, c)
	}
	return tea.Batch(cmds...)
}

// parseSGRMouse decodes "[<b;x;yM" (or a trailing "m" for a release)
// exactly as the input reader decodes a whole report.
func parseSGRMouse(s string) (tea.MouseMsg, bool) {
	mm := sgrReportRe.FindStringSubmatch(s)
	if mm == nil {
		return tea.MouseMsg{}, false
	}
	e, err1 := strconv.Atoi(mm[1])
	x, err2 := strconv.Atoi(mm[2])
	y, err3 := strconv.Atoi(mm[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return tea.MouseMsg{}, false
	}
	var ev tea.MouseEvent
	switch {
	case e&0b1000_0000 != 0:
		ev.Button = tea.MouseButtonBackward + tea.MouseButton(e&0b11)
	case e&0b0100_0000 != 0:
		ev.Button = tea.MouseButtonWheelUp + tea.MouseButton(e&0b11)
	default:
		ev.Button = tea.MouseButtonLeft + tea.MouseButton(e&0b11)
		if e&0b11 == 0b11 {
			ev.Action, ev.Button = tea.MouseActionRelease, tea.MouseButtonNone
		}
	}
	if e&0b0010_0000 != 0 && !ev.IsWheel() {
		ev.Action = tea.MouseActionMotion
	}
	ev.Alt, ev.Ctrl, ev.Shift = e&0b1000 != 0, e&0b1_0000 != 0, e&0b100 != 0
	ev.Type = legacyMouseType(ev)
	if ev.Action != tea.MouseActionMotion && !ev.IsWheel() && mm[4] == "m" {
		ev.Action, ev.Type = tea.MouseActionRelease, tea.MouseRelease
	}
	// A malformed report can carry 0 coordinates; nothing downstream
	// expects a negative cell.
	ev.X, ev.Y = max(0, x-1), max(0, y-1)
	return tea.MouseMsg(ev), true
}

// legacyMouseType is the deprecated event type the input reader also fills in.
func legacyMouseType(ev tea.MouseEvent) tea.MouseEventType {
	press := ev.Action == tea.MouseActionPress
	switch {
	case ev.Button == tea.MouseButtonLeft && press:
		return tea.MouseLeft
	case ev.Button == tea.MouseButtonMiddle && press:
		return tea.MouseMiddle
	case ev.Button == tea.MouseButtonRight && press:
		return tea.MouseRight
	case ev.Button == tea.MouseButtonNone && ev.Action == tea.MouseActionRelease:
		return tea.MouseRelease
	case ev.Button == tea.MouseButtonWheelUp && press:
		return tea.MouseWheelUp
	case ev.Button == tea.MouseButtonWheelDown && press:
		return tea.MouseWheelDown
	case ev.Button == tea.MouseButtonWheelLeft && press:
		return tea.MouseWheelLeft
	case ev.Button == tea.MouseButtonWheelRight && press:
		return tea.MouseWheelRight
	case ev.Button == tea.MouseButtonBackward && press:
		return tea.MouseBackward
	case ev.Button == tea.MouseButtonForward && press:
		return tea.MouseForward
	case ev.Action == tea.MouseActionMotion:
		switch ev.Button { //nolint:exhaustive
		case tea.MouseButtonLeft:
			return tea.MouseLeft
		case tea.MouseButtonMiddle:
			return tea.MouseMiddle
		case tea.MouseButtonRight:
			return tea.MouseRight
		case tea.MouseButtonBackward:
			return tea.MouseBackward
		case tea.MouseButtonForward:
			return tea.MouseForward
		}
		return tea.MouseMotion
	}
	return tea.MouseUnknown
}

// termModeOut is where terminal mode switches are written; tests swap it.
var termModeOut = func() io.Writer { return os.Stdout }

// restoreTerminalModes turns mouse reporting and the keyboard protocol back
// on after a program that had the terminal (an external editor) gave it
// back: handing the terminal over turns mouse reporting off, and without it
// the wheel reaches kn as arrow keys that scroll through prompt history.
func (m *model) restoreTerminalModes() {
	if !tuiRunning {
		return
	}
	w := termModeOut()
	if m.mouseOn {
		enableClickWheelMouse(w)
	}
	// Pop before pushing so each editor round trip leaves the keyboard
	// protocol stack one entry deep instead of growing it by one.
	disableKeyboardEnhancement(w)
	enableKeyboardEnhancement(w)
}
