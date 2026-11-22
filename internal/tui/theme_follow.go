package tui

import (
	"io"
	"reflect"
	"regexp"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Following the terminal's appearance at runtime. When the saved theme
// follows the terminal (auto, or a family with both variants), kn asks the
// terminal for its background again when the window regains focus and every
// themePollInterval, and also listens for the colour-scheme reports a
// terminal sends by itself once mode 2031 is on. A change switches the light
// and dark variant in place.
//
// Two answers come back:
//   - ESC [ ? 997 ; 1|2 n (dark|light), the reply to ESC [ ? 996 n and the
//     unsolicited report of mode 2031. The input reader passes it on as an
//     unknown CSI sequence, so it never reaches the composer.
//   - ESC ] 11 ; rgb:RRRR/GGGG/BBBB ST, the reply to OSC 11. The input reader
//     has no parser for OSC replies and splits it into keys: Alt+], the text
//     "11;rgb:…" and the terminator as Alt+\ (ST) or Ctrl+G (BEL). bgReply
//     takes those keys back out of the stream while a query is outstanding,
//     the way mouseFrag reassembles cut mouse reports, and replays anything
//     that turns out not to be a reply.
//
// The runtime query never includes a cursor-position request: its reply
// would also arrive as keys.

const (
	// bgReplyWindow is how long after a query its OSC 11 reply is expected.
	bgReplyWindow = 2 * time.Second
	// bgEscWindow is how long after a query a bare Esc may start a reply
	// (one cut right after its ESC). It is short, so an Esc typed to
	// interrupt a turn is not held back for long.
	bgEscWindow = 500 * time.Millisecond
	// bgReplyGap is how long a partial reply is held for its next piece.
	bgReplyGap = 150 * time.Millisecond
	// bgQueryMinGap throttles queries (focus can flap).
	bgQueryMinGap = 2 * time.Second
	// themePollInterval is the periodic re-query, for terminals that report
	// neither focus nor scheme changes.
	themePollInterval = 10 * time.Second
)

// bgRuntimeQuery asks for the background colour and the colour scheme.
const bgRuntimeQuery = "\x1b]11;?\x1b\\\x1b[?996n"

// Mode 2031 asks the terminal to report colour-scheme changes by itself.
const (
	schemeReportsOn  = "\x1b[?2031h"
	schemeReportsOff = "\x1b[?2031l"
)

type bgReply struct {
	armedUntil time.Time
	lastQuery  time.Time
	held       []tea.KeyMsg
	text       string // the reply so far, from its "]"
	at         time.Time
	seq        int
	replaying  bool
	// dropTerm eats a terminator that arrives after its reply was already
	// taken: the "\" of an ST split from its ESC, or a late ST or BEL.
	dropTerm time.Time
}

// bgReplyFlush ends the wait for the rest of a partial reply.
type bgReplyFlush struct{ seq int }

var (
	// oscBgPrefixRe matches every prefix of an OSC 11 colour reply.
	oscBgPrefixRe = regexp.MustCompile(`^(\](1(1(;[0-9A-Za-z:/#]*)?)?)?)?$`)
	scheme997Re   = regexp.MustCompile(`^\x1b\[\?997;([12])n$`)
)

// queryAppearance asks the terminal for its background, at most once every
// bgQueryMinGap, when the saved theme follows it.
func (m *model) queryAppearance() {
	w := m.status().note.out
	if w == nil || !tuiRunning || !m.themeFollowsTerminal() {
		return
	}
	now := m.nowFn()
	r := &m.bgr
	if !r.lastQuery.IsZero() && now.Sub(r.lastQuery) < bgQueryMinGap {
		return
	}
	r.lastQuery, r.armedUntil = now, now.Add(bgReplyWindow)
	_, _ = io.WriteString(w, bgRuntimeQuery)
}

// enableSchemeReports and disableSchemeReports switch mode 2031; a terminal
// that does not know the mode ignores it.
func enableSchemeReports(w io.Writer)  { _, _ = io.WriteString(w, schemeReportsOn) }
func disableSchemeReports(w io.Writer) { _, _ = io.WriteString(w, schemeReportsOff) }

// schemeReport reads a mode-2031 / DSR 996 report out of an unknown CSI
// sequence message, which the input reader keeps unexported.
func schemeReport(msg tea.Msg) (light, ok bool) {
	v := reflect.ValueOf(msg)
	if !v.IsValid() || v.Type().Name() != "unknownCSISequenceMsg" || v.Kind() != reflect.Slice || v.Type().Elem().Kind() != reflect.Uint8 {
		return false, false
	}
	mm := scheme997Re.FindSubmatch(v.Bytes())
	if mm == nil {
		return false, false
	}
	return mm[1][0] == '2', true
}

// appearanceMsg runs ahead of Update's dispatch. It starts a query on focus
// (without consuming the focus message), turns scheme reports into
// themeSyncMsg, and takes OSC 11 replies out of the key stream.
func (m *model) appearanceMsg(msg tea.Msg) (handled bool, cmd tea.Cmd) {
	switch msg := msg.(type) {
	case tea.FocusMsg:
		m.queryAppearance()
		return false, nil
	case bgReplyFlush:
		r := &m.bgr
		if msg.seq != r.seq || len(r.held) == 0 {
			return true, nil
		}
		if light, ok := r.complete(); ok {
			r.reset()
			r.dropTerm = m.nowFn().Add(bgReplyGap)
			return true, syncTheme(light)
		}
		return true, m.replayAppearanceKeys(r.take()...)
	case tea.KeyMsg:
		return m.bgReplyKey(msg)
	}
	if light, ok := schemeReport(msg); ok {
		return true, syncTheme(light)
	}
	return false, nil
}

func syncTheme(light bool) tea.Cmd {
	return func() tea.Msg { return themeSyncMsg{light: light, ok: true} }
}

// bgReplyKey handles one key while an OSC 11 reply may be arriving.
func (m *model) bgReplyKey(k tea.KeyMsg) (bool, tea.Cmd) {
	r := &m.bgr
	now := m.nowFn()
	if r.replaying || k.Paste {
		return false, nil
	}
	if len(r.held) == 0 {
		if now.Before(r.dropTerm) && (k.Type == tea.KeyCtrlG || (k.Type == tea.KeyRunes && string(k.Runes) == "\\")) {
			r.dropTerm = time.Time{}
			return true, nil
		}
		if now.After(r.armedUntil) {
			return false, nil
		}
		switch {
		case k.Type == tea.KeyRunes && k.Alt && string(k.Runes) == "]":
			r.text = "]"
		case k.Type == tea.KeyEsc && !k.Alt && !now.After(r.armedUntil.Add(bgEscWindow-bgReplyWindow)):
			r.text = ""
		default:
			return false, nil
		}
		r.held = []tea.KeyMsg{k}
		return true, r.wait(now)
	}

	// A terminator ends a complete reply: Alt+\ (ST), Ctrl+G (BEL), or an
	// ESC whose "\" follows on its own.
	term := (k.Type == tea.KeyRunes && k.Alt && string(k.Runes) == "\\") || k.Type == tea.KeyCtrlG
	escTerm := k.Type == tea.KeyEsc && !k.Alt
	if term || escTerm {
		if light, ok := r.complete(); ok {
			r.reset()
			if escTerm {
				r.dropTerm = now.Add(bgReplyGap)
			}
			return true, syncTheme(light)
		}
	}
	if k.Type == tea.KeyRunes && !k.Alt {
		if text := r.text + string(k.Runes); oscBgPrefixRe.MatchString(text) {
			r.text = text
			r.held = append(r.held, k)
			return true, r.wait(now)
		}
	}
	// Not a reply after all: what was held goes first, then this key.
	return true, m.replayAppearanceKeys(append(r.take(), k)...)
}

// wait restarts the gap timer for the next piece of a reply.
func (r *bgReply) wait(now time.Time) tea.Cmd {
	r.at = now
	r.seq++
	seq := r.seq
	return tea.Tick(bgReplyGap, func(time.Time) tea.Msg { return bgReplyFlush{seq: seq} })
}

// complete parses the held text as a whole OSC 11 reply.
func (r *bgReply) complete() (light, ok bool) {
	if len(r.text) < 4 || r.text[:4] != "]11;" {
		return false, false
	}
	red, green, blue, ok := parseOSCBgRGB(r.text[4:])
	if !ok {
		return false, false
	}
	return rgbIsLight(red, green, blue), true
}

func (r *bgReply) take() []tea.KeyMsg {
	held := r.held
	r.reset()
	return held
}

func (r *bgReply) reset() {
	r.held, r.text = nil, ""
	r.seq++
}

// replayAppearanceKeys runs keys through Update in order, past the reply
// check (the mouse-fragment check still sees them).
func (m *model) replayAppearanceKeys(keys ...tea.KeyMsg) tea.Cmd {
	m.bgr.replaying = true
	defer func() { m.bgr.replaying = false }()
	var cmds []tea.Cmd
	for _, k := range keys {
		_, c := m.Update(k)
		cmds = append(cmds, c)
	}
	return tea.Batch(cmds...)
}
