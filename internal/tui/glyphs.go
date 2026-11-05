package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// glyphSet is every chrome glyph the TUI draws, so a terminal without good
// Unicode coverage can swap the whole set at once. Each glyph is one cell wide
// in both sets.
type glyphSet struct {
	User       string // the caret before user text and the selected option
	Assistant  string // the reply marker
	ToolQueued string
	ToolRun    string
	ToolDone   string
	OK         string
	Fail       string
	Result     string // the elbow under a tool row
	Rail       string // live output and quote rail
	Band       string // the user band's left rail when there is no tint
	Fold       string
	Notice     string // system notices
	Warn       string
	Effort     string
	Tokens     string
	BarFull    string
	BarEmpty   string
	Minus      string // the removed-lines sign in counts
	Spinner    []string
	Border     lipgloss.Border
}

var unicodeGlyphs = glyphSet{
	User:       "❯",
	Assistant:  "●",
	ToolQueued: "◇",
	ToolRun:    "◌",
	ToolDone:   "◆",
	OK:         "✓",
	Fail:       "✗",
	Result:     "└",
	Rail:       "│",
	Band:       "▌",
	Fold:       "⋯",
	Notice:     "◎",
	Warn:       "!",
	Effort:     "✦",
	Tokens:     "⇣",
	BarFull:    "▰",
	BarEmpty:   "▱",
	Minus:      "−",
	Spinner:    []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧"},
	Border:     lipgloss.RoundedBorder(),
}

var asciiGlyphs = glyphSet{
	User:       ">",
	Assistant:  "*",
	ToolQueued: "o",
	ToolRun:    "~",
	ToolDone:   "*",
	OK:         "v",
	Fail:       "x",
	Result:     "`",
	Rail:       "|",
	Band:       "|",
	Fold:       "...",
	Notice:     "o",
	Warn:       "!",
	Effort:     "*",
	Tokens:     "v",
	BarFull:    "#",
	BarEmpty:   "-",
	Minus:      "-",
	Spinner:    []string{"|", "/", "-", "\\"},
	Border: lipgloss.Border{
		Top: "-", Bottom: "-", Left: "|", Right: "|",
		TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
		MiddleLeft: "+", MiddleRight: "+", Middle: "+", MiddleTop: "+", MiddleBottom: "+",
	},
}

// glyphs is the active set. Tests compare against its fields rather than
// literal characters so the same assertion holds in ASCII mode.
var glyphs = unicodeGlyphs

// asciiMode reports whether the ASCII set is active.
var asciiMode bool

// glyphUser and glyphAssistant are the caret and reply marker with their
// trailing gap, the form most call sites want.
var (
	glyphUser      = glyphs.User + " "
	glyphAssistant = glyphs.Assistant + " "
)

// setASCIIGlyphs switches the glyph set.
func setASCIIGlyphs(on bool) {
	asciiMode = on
	glyphs = unicodeGlyphs
	if on {
		glyphs = asciiGlyphs
	}
	glyphUser, glyphAssistant = glyphs.User+" ", glyphs.Assistant+" "
	buildStyles()
}

// wantASCII decides the glyph set: K_BRAIN_ASCII=1/0 forces it either way;
// otherwise a console known to lack the glyphs (the Linux VT, vt100, dumb) or
// a locale that names a non-UTF-8 charset switches to ASCII. An unset locale
// and the plain C/POSIX locale are left alone: most terminals that report them
// still render UTF-8 fine.
func wantASCII(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("K_BRAIN_ASCII"))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	switch strings.ToLower(getenv("TERM")) {
	case "linux", "vt100", "vt102", "vt220", "dumb":
		return true
	}
	loc := ""
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := getenv(k); v != "" {
			loc = v
			break
		}
	}
	if loc == "" || loc == "C" || loc == "POSIX" {
		return false
	}
	l := strings.ToLower(loc)
	return !strings.Contains(l, "utf-8") && !strings.Contains(l, "utf8")
}
