package tui

// Desktop notifications and the window title. When the terminal reports that
// it lost focus, a finished turn or a card waiting on the user raises a
// desktop notification through the terminal (OSC 9, OSC 777, or a plain BEL
// where neither is known to work). The window title carries the session and a
// spinner while a turn runs.
//
// Nothing is written unless a writer was attached by Run, so tests and
// non-terminal runs never emit to a real terminal.

import (
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Notification routes.
const (
	notifyOSC9   = "osc9"
	notifyOSC777 = "osc777"
	notifyBEL    = "bel"
)

type notifier struct {
	out io.Writer
	// mode is the notifications.desktop setting: auto (the default) notifies
	// only once the terminal has proved it reports focus; on also notifies
	// when focus is unknown; off never does; osc9, osc777 or bel force a
	// route and otherwise behave as on.
	mode  string
	route string
	tmux  bool

	focusKnown bool
	focused    bool

	title     bool
	lastTitle string
	pushed    bool
	sent      int // notifications written, for tests
}

// startStatus attaches the terminal the status area may write to: the
// notification route, the title, and the window-title stack so the
// terminal's own title comes back on exit.
func (m *model) startStatus(out io.Writer) {
	n := &m.status().note
	n.out = out
	n.mode, n.title = "auto", true
	if m.cfg != nil {
		if d := strings.ToLower(strings.TrimSpace(m.cfg.Notifications.Desktop)); d != "" {
			n.mode = d
		}
		if m.cfg.Notifications.Title != nil {
			n.title = *m.cfg.Notifications.Title
		}
	}
	n.route = notifyRoute(n.mode, os.Getenv)
	n.tmux = os.Getenv("TMUX") != ""
	if n.title && out != nil {
		_, _ = io.WriteString(out, "\x1b[22;0t") // push the terminal's title
		n.pushed = true
	}
}

// stopStatus restores the terminal's title.
func (m *model) stopStatus() {
	n := &m.status().note
	if n.out != nil && n.pushed {
		_, _ = io.WriteString(n.out, "\x1b[23;0t")
	}
	n.out = nil
}

// notifyRoute picks how a notification reaches the desktop.
func notifyRoute(mode string, getenv func(string) string) string {
	switch mode {
	case notifyOSC9, notifyOSC777, notifyBEL:
		return mode
	}
	prog := getenv("TERM_PROGRAM")
	if prog == "tmux" || prog == "screen" {
		prog = getenv("LC_TERMINAL")
	}
	term := getenv("TERM")
	switch {
	case strings.EqualFold(prog, "iTerm.app"), strings.EqualFold(prog, "iTerm2"), strings.EqualFold(prog, "WezTerm"),
		strings.EqualFold(prog, "ghostty"), getenv("KITTY_WINDOW_ID") != "", strings.Contains(term, "kitty"),
		strings.Contains(term, "ghostty"), getenv("WT_SESSION") != "":
		return notifyOSC9
	case getenv("VTE_VERSION") != "", strings.HasPrefix(term, "foot"), strings.HasPrefix(term, "rxvt"):
		return notifyOSC777
	}
	return notifyBEL
}

func (n *notifier) setFocus(focused bool) {
	n.focusKnown, n.focused = true, focused
}

// wants reports whether a notification should go out now.
func (n *notifier) wants() bool {
	if n.out == nil || n.mode == "off" {
		return false
	}
	if n.focusKnown {
		return !n.focused
	}
	return n.mode != "auto" && n.mode != ""
}

// notify raises a desktop notification.
func (n *notifier) notify(title, body string) {
	if !n.wants() {
		return
	}
	title, body = notifyText(title), notifyText(body)
	var seq string
	switch n.route {
	case notifyOSC9:
		msg := title
		if body != "" {
			msg += ": " + body
		}
		seq = "\x1b]9;" + msg + "\x07"
	case notifyOSC777:
		seq = "\x1b]777;notify;" + strings.ReplaceAll(title, ";", ",") + ";" + body + "\x07"
	default:
		seq = "\a"
	}
	if n.tmux && seq != "\a" {
		// tmux forwards an escape to the outer terminal only inside its
		// passthrough wrapper.
		seq = "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
	}
	_, _ = io.WriteString(n.out, seq)
	n.sent++
}

// notifyText makes s safe inside an OSC string: one line, no control bytes,
// bounded length.
func notifyText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, ansi.Strip(s))
	return truncLine(strings.Join(strings.Fields(s), " "), 160)
}

// notifyTitle heads every notification: the session title, else the app.
func (m *model) notifyTitle() string {
	if t := strings.TrimSpace(m.sessTitle); t != "" {
		return "k-brain · " + t
	}
	return "k-brain"
}

func (m *model) notifyTurnDone() {
	st := m.status()
	body := "Turn finished"
	if st.lastTurnFailed {
		body = "Turn failed"
	}
	if !st.turnStart.IsZero() {
		body += " after " + fmtSpan(m.nowFn().Sub(st.turnStart))
	}
	st.note.notify(m.notifyTitle(), body)
}

func (m *model) notifyApproval() {
	d := m.permDialog
	body := "Approval needed"
	if d != nil {
		subject := d.req.Command
		if subject == "" {
			subject = d.req.Description
		}
		body += ": " + d.req.Tool
		if subject != "" {
			body += " " + subject
		}
	}
	m.status().note.notify(m.notifyTitle(), body)
}

func (m *model) notifyQuestion() {
	body := "Question waiting"
	if d := m.askDialog; d != nil && d.req.Question != "" {
		body += ": " + d.req.Question
	}
	m.status().note.notify(m.notifyTitle(), body)
}

// titleFrame is how often the title spinner advances.
const titleFrame = 250 * time.Millisecond

// windowTitle is the title the terminal should show now: the session, led by
// a spinner while a turn runs or the waiting glyph when a card needs the user.
func (m *model) windowTitle(now time.Time) string {
	name := strings.TrimSpace(m.sessTitle)
	if name == "" {
		name = shortCWD()
	}
	t := "kn · " + notifyText(name)
	switch {
	case m.permDialog != nil || m.askDialog != nil:
		return glyphs.ToolQueued + " " + t
	case m.busy:
		frames := glyphs.Spinner
		return frames[int(now.UnixMilli()/titleFrame.Milliseconds())%len(frames)] + " " + t
	}
	return t
}

// syncTitle sets the window title when it changed. It goes through the
// program, so the escape is written between frames rather than into one.
func (m *model) syncTitle(now time.Time) tea.Cmd {
	n := &m.status().note
	if n.out == nil || !n.title {
		return nil
	}
	t := m.windowTitle(now)
	if t == n.lastTitle {
		return nil
	}
	n.lastTitle = t
	return tea.SetWindowTitle(t)
}
