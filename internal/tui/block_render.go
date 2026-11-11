package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// renderBlock draws one transcript block. It is block.render's body; the
// transcript has no boxes, so structure comes from glyphs, rails and bands.
func renderBlock(b block, width int) string {
	switch b.kind {
	case blockBanner:
		return renderBannerBox(b.text, width)
	case blockUser:
		return renderUserBand(b.text, width)
	case blockAssistant:
		w := width - 2
		if w <= 0 {
			w = 80
		}
		body := indentLines(renderMarkdown(b.text, w), 2)
		return botStyle.Render(glyphAssistant) + dropLeadSpaces(body, 2)
	case blockTool:
		return renderToolResult(b, width)
	case blockToolQueued:
		if b.toolName == "" {
			return wrap(b.text, width)
		}
		// A queued row is restated in place as its arguments stream in, and
		// only its text follows along, so the subject comes from the text.
		tag, subject := queuedRowParts(b.text)
		if strings.HasPrefix(strings.TrimSpace(subject), "{") {
			subject = rowSubject(b.toolName, subject)
		}
		row := toolRowText(textMutedStyle.Render(glyphs.ToolQueued), b.toolName, subject, textMutedStyle, tag)
		return withMeta(textMutedStyle.Render(ansi.Strip(row)), textMutedStyle.Render("queued"), width)
	case blockToolRun:
		return renderToolRun(b, width)
	default:
		if out, ok := renderNotice(b.text, width); ok {
			return out
		}
		return wrap(chromeGlyphs(b.text), width)
	}
}

// renderNotice restyles the one-line error and warning notices the event loop
// appends. A turn error becomes a small block on an error rail — a bold title,
// then the cause — and a warning leads with the plain "!" in the warning
// colour (the emoji-style ⚠ is two cells wide in some terminals).
func renderNotice(text string, width int) (string, bool) {
	if out, ok := renderErrorBanner(text, width); ok {
		return out, true
	}
	if strings.Contains(text, "\n") {
		return "", false
	}
	plain := ansi.Strip(text)
	switch {
	case strings.HasPrefix(plain, "error: "):
		rail := errorTitleStyle.Render(glyphs.Band) + " "
		cause := wrap(strings.TrimPrefix(plain, "error: "), max(width-2, 1))
		out := rail + errorTitleStyle.Render("Error")
		for l := range strings.SplitSeq(cause, "\n") {
			out += "\n" + rail + textSecondaryStyle.Render(l)
		}
		return out, true
	case strings.HasPrefix(plain, "⚠ "):
		body := strings.TrimPrefix(plain, "⚠ ")
		return wrapHanging(warnStyle.Bold(true).Render(glyphs.Warn)+" ", warnStyle.Render(chromeGlyphs(body)), width), true
	}
	return "", false
}

// asciiChrome maps the Unicode chrome glyphs that older call sites still write
// literally onto the ASCII set, so ASCII mode holds across every notice.
var asciiChrome = strings.NewReplacer(
	"↑↓", "Up/Down",
	"⚒", "*", "⎿", "`", "◎", "o", "⚠", "!", "✦", "*", "⚙", "*", "●", "*",
	"❯", ">", "◌", "~", "◇", "o", "◆", "*", "⋯", "...", "✓", "v", "✗", "x",
	"…", "...", "·", "-", "—", "-", "→", "->", "←", "<-", "↑", "Up", "↓", "Down",
	"│", "|", "└", "`", "▌", "|", "▰", "#", "▱", "-", "⇣", "v", "◐", "o", "−", "-", "×", "x", "○", "o",
	"▸", ">", "▾", "v", "☰", "=", "✻", "*",
)

// chromeGlyphs is s with chrome glyphs swapped for ASCII when that mode is on.
func chromeGlyphs(s string) string {
	if !asciiMode {
		return s
	}
	return asciiChrome.Replace(s)
}

// renderUserBand is the user's message: an accent rail and caret on a
// full-width band. Where the terminal cannot tint a background the rail alone
// marks the band.
func renderUserBand(text string, width int) string {
	const lead = 3 // rail, caret, gap
	body := wrap(text, max(width-lead, 1))
	tinted := backgroundsActive()
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		prefix := userRailStyle.Render(glyphs.Band) + youStyle.Render(glyphs.User) + " "
		if i > 0 {
			prefix = userRailStyle.Render(glyphs.Band) + "  "
		}
		if !tinted {
			lines[i] = prefix + l
			continue
		}
		row := withBG(userRailStyle, bandStyle).Render(glyphs.Band)
		if i == 0 {
			row += withBG(youStyle, bandStyle).Render(glyphs.User) + bandStyle.Render(" ")
		} else {
			row += bandStyle.Render("  ")
		}
		row += bandStyle.Render(l)
		lines[i] = row + bandStyle.Render(strings.Repeat(" ", max(width-lipgloss.Width(row), 0)))
	}
	return strings.Join(lines, "\n")
}

// rowSubject is the subject a queued or running row shows. Streaming tool
// calls carry incomplete JSON until the call is complete; until then the row
// shows the leading string field as far as it has arrived.
func rowSubject(name, args string) string {
	if json.Valid([]byte(args)) {
		return toolSubject(name, args)
	}
	if m := partialFieldRE.FindStringSubmatch(args); m != nil {
		if v, err := strconvUnquote(m[1]); err == nil {
			return v
		}
		return m[1]
	}
	return queuedSubject(name, args)
}

var partialFieldRE = regexp.MustCompile(`^\s*\{\s*"(?:command|path|file_path|url|pattern|description|query)"\s*:\s*"((?:[^"\\]|\\.)*)`)

// strconvUnquote decodes a JSON string body that may be cut off mid-escape.
func strconvUnquote(body string) (string, error) {
	var v string
	err := json.Unmarshal([]byte(`"`+strings.TrimSuffix(body, `\`)+`"`), &v)
	return v, err
}

var (
	batchTagRE   = regexp.MustCompile(`^\S+ \S+( \d+/\d+) `)
	queuedTextRE = regexp.MustCompile(`^\S+ \S+( \d+/\d+)? (.*)$`)
)

// queuedRowParts splits the event loop's "⋯ name[ i/n] subject" text into the
// batch tag and the raw subject.
func queuedRowParts(text string) (tag, subject string) {
	if m := queuedTextRE.FindStringSubmatch(ansi.Strip(text)); m != nil {
		return m[1], m[2]
	}
	return "", ""
}

// batchTag recovers the " 2/3" batch position the event loop wrote into a
// queued or running row's text.
func batchTag(text string) string {
	if m := batchTagRE.FindStringSubmatch(ansi.Strip(text)); m != nil {
		return m[1]
	}
	return ""
}

// renderToolRun draws a tool call header. Tool families draw through
// renderToolHeader (toolview.go); subagent rows, whose text the task
// tracker keeps current, and rows without a tool name draw their text.
func renderToolRun(b block, width int) string {
	if b.toolName != "" && toolKindOf(b.toolName) != tkSubagent {
		return renderToolHeader(&b, width)
	}
	if b.toolRunning {
		if b.toolName != "" {
			toolTimer.start(b.toolID)
		}
		row := b.text
		if b.toolName != "" {
			row = toolRowText(runningStyle.Bold(true).Render(glyphs.ToolRun), b.toolName, rowSubject(b.toolName, b.toolArgs), textSecondaryStyle, batchTag(b.text))
		}
		out := withMeta(chromeGlyphs(row), "", width)
		if b.live != "" {
			for l := range strings.SplitSeq(b.live, "\n") {
				l = strings.TrimLeft(l, " ")
				out += "\n" + ansi.Truncate(textMutedStyle.Render("  "+glyphs.Rail+" ")+textMutedStyle.Render(chromeGlyphs(l)), max(width, 1), "…")
			}
		}
		return out
	}
	meta := ""
	// A subagent's finished row already carries its own state, tool count,
	// duration and tokens.
	switch {
	case b.toolName == "subagent":
	case b.run != nil && b.run.duration > 0:
		// Restored from history, which recorded how long the tool ran.
		meta = toolDoneMeta(b.toolFailed, b.run.duration)
	case b.toolName != "" && b.toolID != "":
		if d, ok := toolTimer.finish(b.toolID); ok {
			meta = toolDoneMeta(b.toolFailed, d)
		}
	case b.toolFailed:
		meta = toolDoneMeta(true, -1)
	}
	if b.expanded {
		return wrap(chromeGlyphs(b.text), width)
	}
	return withMeta(chromeGlyphs(b.text), meta, width)
}

// toolDoneMeta is the right-hand cell of a finished row: "✓ 3.2s", or
// "failed · 2.8s" in the error colour. d < 0 means the duration is unknown.
func toolDoneMeta(failed bool, d time.Duration) string {
	dur := ""
	if d >= 0 {
		dur = fmtToolDuration(d)
	}
	if failed {
		if dur == "" {
			return errStyle.Render("failed")
		}
		return errStyle.Render("failed") + textMutedStyle.Render(chromeGlyphs(" · ")+dur)
	}
	if dur == "" {
		return ""
	}
	return okStyle.Render(glyphs.OK) + textMutedStyle.Render(" "+dur)
}

func fmtToolDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// renderToolResult draws a tool's output under its header, by tool family
// (renderToolBody in toolview.go).
func renderToolResult(b block, width int) string {
	return renderToolBody(&b, width)
}

// wrapHanging wraps text after a lead so wrapped rows indent under the text.
func wrapHanging(lead, text string, width int) string {
	lw := lipgloss.Width(lead)
	body := wrap(text, max(width-lw, 1))
	return lead + strings.ReplaceAll(body, "\n", "\n"+strings.Repeat(" ", lw))
}

// toolTimer times tool calls from the TUI's side: a call starts when its
// running row is first drawn and ends when its finished row is first drawn.
// Rows render as soon as their event lands, so the gap is the tool's wall
// time to within a frame. Ids can repeat across turns, so a start after an
// end begins a new span.
var toolTimer = &toolClock{now: time.Now, spans: map[string]*toolSpan{}}

type toolSpan struct{ start, end time.Time }

type toolClock struct {
	mu    sync.Mutex
	now   func() time.Time
	spans map[string]*toolSpan
}

func (c *toolClock) start(id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.spans[id]; ok && s.end.IsZero() {
		return
	}
	if len(c.spans) > 2048 {
		for k, s := range c.spans {
			if !s.end.IsZero() {
				delete(c.spans, k)
			}
		}
	}
	c.spans[id] = &toolSpan{start: c.now()}
}

// finish closes a span (once) and reports its length.
func (c *toolClock) finish(id string) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.spans[id]
	if !ok {
		return 0, false
	}
	if s.end.IsZero() {
		s.end = c.now()
	}
	return max(s.end.Sub(s.start), 0), true
}

// running reports how long a still-running call has been going.
func (c *toolClock) running(id string) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.spans[id]
	if !ok || !s.end.IsZero() {
		return 0, false
	}
	return max(c.now().Sub(s.start), 0), true
}

// set records a span directly; tests use it to pin durations.
func (c *toolClock) set(id string, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.spans[id] = &toolSpan{start: now.Add(-d), end: now}
}
