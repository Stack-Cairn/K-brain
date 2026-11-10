package tui

// Tool views: how a tool call draws in the transcript, one renderer per tool
// family. A call is two blocks — its header (blockToolRun: queued, running,
// finished) and its result (blockTool) — and both dispatch on the family of
// the tool's name:
//
//	shell   bash and the process tools: exit badge, head and tail of the output
//	edit    edit and write: the change as a diff (diff.go)
//	read    read, list, image: folded to a count
//	search  grep and glob: folded to their own match counts
//	fetch   web_fetch, plus hosted web searches with their sources
//	mcp     mcp__server__tool: "server:tool" header, output capped at 10 lines
//	generic everything else: a three-line preview
//
// What the TUI learns about a finished call beyond its text — exit status,
// timeout, the tool's own duration, image parts — arrives with OnToolResult
// right after OnToolEnd and lands on the header's run (shared with its result
// block), or comes back from history on resume. Consecutive read-only calls
// fold into one verb group ("Read 3 files, searched 2 patterns",
// toolview_search.go); todo calls draw in the plan pane (todo_pane.go)
// instead of the transcript.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type toolKind int

const (
	tkGeneric toolKind = iota
	tkShell
	tkEdit
	tkRead
	tkSearch
	tkFetch
	tkMCP
	tkSubagent
	tkTodo
)

// toolKindOf is the family a tool name renders as.
func toolKindOf(name string) toolKind {
	switch name {
	case "bash", "Bash", "ManagedProcess", "ProcessWait", "ProcessStop", "TerminalSession":
		return tkShell
	case "edit", "Edit", "write", "Write":
		return tkEdit
	case "read", "Read", "Image", "list", "List", "ReadTerminal":
		return tkRead
	case "grep", "Grep", "glob", "Glob":
		return tkSearch
	case "web_fetch", "WebFetch":
		return tkFetch
	case "subagent", "subagent_steer":
		return tkSubagent
	case "todowrite":
		return tkTodo
	}
	if _, _, ok := mcp.ParseToolName(name); ok {
		return tkMCP
	}
	return tkGeneric
}

// hiddenToolCall reports calls that draw somewhere other than the
// transcript: the plan lives in the plan pane.
func hiddenToolCall(name string) bool { return toolKindOf(name) == tkTodo }

// toolViewMeta is what the tool views know about a call beyond its text. It
// rides on toolRunMeta.view, shared by the header and its result block.
type toolViewMeta struct {
	// hasDiff marks an edit whose result carried a diff; added and removed
	// are its line counts, shown on the header.
	hasDiff        bool
	added, removed int
	// images are the chips for image and file parts of the result.
	images []string
	// group is a verb group's calls, oldest first; non-nil marks the block
	// as a group (toolview_search.go).
	group []groupMember
	// search is a hosted web search's latest state (toolview_fetch.go).
	search *ai.HostedSearch
}

// viewMeta is b's view metadata, nil when it has none.
func (b *block) viewMeta() *toolViewMeta {
	if b.run == nil {
		return nil
	}
	return b.run.view
}

// ensureView is b's view metadata, created on first use.
func (b *block) ensureView() *toolViewMeta {
	if b.run == nil {
		b.run = &toolRunMeta{}
	}
	if b.run.view == nil {
		b.run.view = &toolViewMeta{}
	}
	return b.run.view
}

// toolViewState is the tool views' model state.
type toolViewState struct {
	// ticking is set while a one-second tick is scheduled to advance the
	// elapsed time on running tool rows.
	ticking bool
	// todos is the plan as the agent last reported it, and todoAgent the
	// agent it came from (the agent is replaced on /new, resume and fork).
	todos     []agent.Todo
	todoAgent *agent.Agent
}

// tvs is the tool view state, created on first use.
func (m *model) tvs() *toolViewState {
	if m.tv == nil {
		m.tv = &toolViewState{}
	}
	return m.tv
}

// Messages the running turn sends the tool views.
type (
	// toolResultMsg is a finished call's structured result (OnToolResult).
	toolResultMsg struct {
		id, name string
		result   tools.Result
	}
	// toolTickMsg advances the elapsed time on running rows.
	toolTickMsg struct{}
	// hostedSearchMsg is a provider-side web search's state (OnHostedSearch).
	hostedSearchMsg ai.HostedSearch
	// todosMsg is the agent's plan after a todowrite call (SetOnTodos).
	todosMsg struct {
		agent *agent.Agent
		items []agent.Todo
	}
)

// toolViewEvents wires the agent callbacks the tool views consume into a
// turn's events. It runs on the turn goroutine, so it only touches the
// agent and send.
func (m *model) toolViewEvents(ev *agent.Events, send func(tea.Msg)) {
	ev.OnToolResult = func(id, name string, r tools.Result) {
		r.Parts = append([]ai.ContentPart(nil), r.Parts...)
		send(toolResultMsg{id: id, name: name, result: r})
	}
	ev.OnHostedSearch = func(s ai.HostedSearch) {
		s.Queries = append([]string(nil), s.Queries...)
		s.Sources = append([]ai.SearchSource(nil), s.Sources...)
		send(hostedSearchMsg(s))
	}
	if a := m.agent; a != nil {
		a.SetOnTodos(func(items []agent.Todo) { send(todosMsg{agent: a, items: items}) })
	}
}

// onToolView dispatches the tool views' own messages.
func (m *model) onToolView(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case toolResultMsg:
		m.onToolResult(msg)
	case toolTickMsg:
		return m, m.onToolTick()
	case hostedSearchMsg:
		m.onHostedSearch(ai.HostedSearch(msg))
	case todosMsg:
		m.onTodos(msg)
	}
	return m, nil
}

// onToolResult settles a finished call's header with what its structured
// result says: exit status, timeout, cancellation, the tool's own duration,
// and image parts. The text result arrived just before as toolEndMsg.
func (m *model) onToolResult(msg toolResultMsg) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		b := &m.blocks[i]
		switch {
		case b.kind == blockToolRun && !b.toolRunning && b.toolID == msg.id:
			applyToolResult(b, msg.result)
			if i+1 < len(m.blocks) && m.blocks[i+1].run == b.run {
				m.blocks[i+1].invalidate()
			}
			m.refreshVP()
			return
		case b.kind == blockTool:
			if v := b.viewMeta(); v != nil && v.group != nil {
				for j := range v.group {
					if v.group[j].id == msg.id {
						applyRunMeta(v.group[j].run, msg.result)
						b.invalidate()
						m.refreshVP()
						return
					}
				}
			}
		}
	}
}

// applyToolResult records r on a finished header.
func applyToolResult(b *block, r tools.Result) {
	if b.run == nil {
		b.run = &toolRunMeta{}
	}
	applyRunMeta(b.run, r)
	if r.Failed && !r.Cancelled && !b.toolFailed {
		b.toolFailed = true
		b.text = toolHeaderRow(b.toolName, b.toolArgs, true)
	}
	if imgs := partChips(r.Parts); len(imgs) > 0 {
		b.ensureView().images = imgs
	}
	b.invalidate()
}

func applyRunMeta(run *toolRunMeta, r tools.Result) {
	if run == nil {
		return
	}
	if r.ExitCode != nil {
		code := *r.ExitCode
		run.exitCode = &code
	}
	run.timedOut = run.timedOut || r.TimedOut
	run.cancelled = run.cancelled || r.Cancelled
	if r.Duration > 0 {
		run.duration = r.Duration
	}
}

// partChips labels a result's image and file parts: "[image 1 · png · 640×480]".
func partChips(parts []ai.ContentPart) []string {
	var out []string
	n := 0
	for _, p := range parts {
		switch {
		case p.ImageURL != nil || p.Type == "image_url" || p.Type == "image":
			n++
			label := fmt.Sprintf("image %d", n)
			if kind := imageKind(p); kind != "" {
				label += " · " + kind
			}
			if p.W > 0 && p.H > 0 {
				label += fmt.Sprintf(" · %d×%d", p.W, p.H)
			}
			out = append(out, "["+label+"]")
		case p.FileURL != nil:
			name := p.FileURL.Filename
			if name == "" {
				name = "file"
			}
			out = append(out, "["+name+"]")
		}
	}
	return out
}

// imageKind is a short format name for an image part: its MIME subtype, or
// the one its data URL declares.
func imageKind(p ai.ContentPart) string {
	mime := p.MimeType
	if mime == "" && p.ImageURL != nil {
		if rest, ok := strings.CutPrefix(p.ImageURL.URL, "data:"); ok {
			mime, _, _ = strings.Cut(rest, ";")
		}
	}
	_, sub, ok := strings.Cut(mime, "/")
	if !ok {
		return ""
	}
	return strings.TrimSuffix(sub, "+xml")
}

// ensureToolTick schedules the elapsed-time tick unless one is pending.
func (m *model) ensureToolTick() tea.Cmd {
	tv := m.tvs()
	if tv.ticking {
		return nil
	}
	tv.ticking = true
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return toolTickMsg{} })
}

// onToolTick redraws running rows so their elapsed time moves, and schedules
// the next tick while any call is still running.
func (m *model) onToolTick() tea.Cmd {
	tv := m.tvs()
	tv.ticking = false
	running := false
	for i := range m.blocks {
		b := &m.blocks[i]
		if b.kind == blockToolRun && b.toolRunning && b.toolName != "" && toolKindOf(b.toolName) != tkSubagent {
			b.invalidate()
			running = true
		}
	}
	if !running {
		return nil
	}
	m.refreshVP()
	return m.ensureToolTick()
}

// linkToolResult ties a result block to its header: the result learns the
// call's arguments (the path its diff highlights by, a new file's content)
// and shares the header's run, and the header learns the diff's counts.
func linkToolResult(hdr, res *block) {
	if toolKindOf(hdr.toolName) == tkSubagent {
		return
	}
	if hdr.run == nil {
		hdr.run = &toolRunMeta{}
	}
	res.run = hdr.run
	if strings.HasPrefix(res.text, "Error: tool call interrupted") {
		hdr.run.interrupted = true
	}
	res.toolArgs = hdr.toolArgs
	if toolKindOf(hdr.toolName) != tkEdit {
		return
	}
	if diff := editResultDiff(res); diff != "" {
		v := hdr.ensureView()
		v.hasDiff = true
		v.added, v.removed = diffCounts(diff)
		hdr.invalidate()
	}
}

// settleRunningTools ends the rows of calls a turn left running when it
// stopped (interrupted, or failed outright): they will not report back.
func (m *model) settleRunningTools() {
	changed := false
	for i := range m.blocks {
		b := &m.blocks[i]
		if b.kind != blockToolRun || !b.toolRunning || b.toolName == "" || toolKindOf(b.toolName) == tkSubagent {
			continue
		}
		toolTimer.finish(b.toolID)
		if b.run == nil {
			b.run = &toolRunMeta{}
		}
		b.run.interrupted = true
		b.toolRunning, b.toolFailed = false, true
		b.text = toolHeaderRow(b.toolName, b.toolArgs, true)
		b.invalidate()
		changed = true
	}
	if changed {
		m.refreshVP()
	}
}

// settleSeededTools links restored headers to their results and folds runs of
// read-only calls, as the live turn left them.
func (m *model) settleSeededTools() {
	for i := 0; i+1 < len(m.blocks); i++ {
		h, r := &m.blocks[i], &m.blocks[i+1]
		if h.kind == blockToolRun && r.kind == blockTool && h.toolName != "" && r.toolName == h.toolName && r.run == nil {
			linkToolResult(h, r)
		}
	}
	m.groupToolRuns()
}

// ── Header rows ──────────────────────────────────────────────────────────

// toolOutcome is how a finished call ended, as its header shows it.
type toolOutcome int

const (
	outcomeOK toolOutcome = iota
	outcomeFailed
	outcomeExit // a shell command exited non-zero
	outcomeTimedOut
	outcomeCancelled
	outcomeInterrupted
)

// toolOutcomeOf reads a finished header's outcome, and the exit code for
// outcomeExit.
func toolOutcomeOf(b *block) (toolOutcome, int) {
	if r := b.run; r != nil {
		switch {
		case r.interrupted:
			return outcomeInterrupted, 0
		case r.cancelled:
			return outcomeCancelled, 0
		case r.timedOut:
			return outcomeTimedOut, 0
		case r.exitCode != nil && *r.exitCode != 0:
			return outcomeExit, *r.exitCode
		}
	}
	if b.toolFailed {
		return outcomeFailed, 0
	}
	return outcomeOK, 0
}

// outcomeGlyph is the header glyph for an outcome: muted when it went fine,
// the error colour when it failed, the warning colour when it was stopped.
func outcomeGlyph(o toolOutcome) string {
	switch o {
	case outcomeOK:
		return textMutedStyle.Render(glyphs.ToolDone)
	case outcomeCancelled, outcomeInterrupted:
		return warnStyle.Bold(true).Render(glyphs.Fail)
	}
	return errorTitleStyle.Render(glyphs.Fail)
}

// outcomeMeta is the right-hand cell of a finished row: "✓ 3.2s",
// "exit 1 · 2.8s", "timed out · 30.0s", "cancelled · 0.4s". d < 0 means the
// duration is unknown.
func outcomeMeta(o toolOutcome, code int, d time.Duration) string {
	dur := ""
	if d >= 0 {
		dur = fmtToolDuration(d)
	}
	tail := func(s lipgloss.Style, label string) string {
		if dur == "" {
			return s.Render(label)
		}
		return s.Render(label) + textMutedStyle.Render(chromeGlyphs(" · ")+dur)
	}
	switch o {
	case outcomeFailed:
		return tail(errStyle, "failed")
	case outcomeExit:
		return tail(errStyle, fmt.Sprintf("exit %d", code))
	case outcomeTimedOut:
		return tail(errStyle, "timed out")
	case outcomeCancelled:
		return tail(warnStyle, "cancelled")
	case outcomeInterrupted:
		return tail(warnStyle, "interrupted")
	}
	if dur == "" {
		return ""
	}
	return okStyle.Render(glyphs.OK) + textMutedStyle.Render(" "+dur)
}

// toolDurationOf is how long a finished call took: the tool's own time when
// known, else the TUI's start-to-end timing, else -1.
func toolDurationOf(b *block) time.Duration {
	if b.run != nil && b.run.duration > 0 {
		return b.run.duration
	}
	if b.toolID != "" {
		if d, ok := toolTimer.finish(b.toolID); ok {
			return d
		}
	}
	return -1
}

// renderToolHeader draws a call's header row in the one grammar every state
// shares: glyph, tool name, subject, and a right-aligned meta cell. Running,
// the meta is the elapsed time and the live output tails on a rail below.
func renderToolHeader(b *block, width int) string {
	subject := rowSubject(b.toolName, b.toolArgs)
	if !b.toolRunning {
		subject = toolSubject(b.toolName, b.toolArgs)
	}
	if d := toolDetails(b.toolName, b.toolArgs); d != "" {
		subject += textMutedStyle.Render("  " + d)
	}
	if b.toolRunning {
		toolTimer.start(b.toolID)
		row := toolRowText(runningStyle.Bold(true).Render(glyphs.ToolRun), b.toolName, subject, textSecondaryStyle, batchTag(b.text))
		meta := ""
		if d, ok := toolTimer.running(b.toolID); ok && d >= time.Second {
			meta = textMutedStyle.Render(fmtElapsed(d))
		}
		if b.bgHint != "" {
			hint := textHintStyle.Render(b.bgHint)
			if meta != "" {
				hint += "  " + meta
			}
			meta = hint
		}
		out := withMeta(chromeGlyphs(row), meta, width)
		if b.live != "" {
			for l := range strings.SplitSeq(b.live, "\n") {
				l = strings.TrimLeft(l, " ")
				out += "\n" + truncCells(textMutedStyle.Render("  "+glyphs.Rail+" ")+textMutedStyle.Render(chromeGlyphs(l)), width)
			}
		}
		return out
	}
	o, code := toolOutcomeOf(b)
	row := toolRowText(outcomeGlyph(o), b.toolName, subject, textSecondaryStyle)
	if b.expanded {
		return wrap(chromeGlyphs(row), width)
	}
	meta := outcomeMeta(o, code, toolDurationOf(b))
	if v := b.viewMeta(); v != nil && v.hasDiff {
		counts := diffCountsCell(v.added, v.removed)
		if meta != "" {
			meta = counts + "  " + meta
		} else {
			meta = counts
		}
	}
	return withMeta(chromeGlyphs(row), meta, width)
}

// fmtElapsed is a running call's elapsed time in whole seconds.
func fmtElapsed(d time.Duration) string {
	s := int(d / time.Second)
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}

// toolDetails is the dim parenthetical after a header's subject: a read's
// line range, a process action.
func toolDetails(name, args string) string {
	var a map[string]any
	if json.Unmarshal([]byte(args), &a) != nil {
		return ""
	}
	num := func(k string) int { f, _ := a[k].(float64); return int(f) }
	switch name {
	case "read", "Read":
		off, lim := num("offset"), num("limit")
		if name == "Read" {
			off = num("start_line")
		}
		switch {
		case off > 0 && lim > 0:
			return fmt.Sprintf("lines %d-%d", off, off+lim-1)
		case off > 0:
			return fmt.Sprintf("from line %d", off)
		case lim > 0:
			return fmt.Sprintf("first %d lines", lim)
		}
	case "ManagedProcess":
		if act, _ := a["action"].(string); act != "" && act != "start" {
			return act
		}
	}
	return ""
}

// ── Result bodies ────────────────────────────────────────────────────────

// renderToolBody draws a call's result under its header by family, then
// any hook outcomes and image chips under it.
func renderToolBody(b *block, width int) string {
	if v := b.viewMeta(); v != nil && v.group != nil {
		return renderToolGroup(b, width)
	}
	text, notes := splitHookNotes(strings.TrimRight(b.text, "\n"))
	var body string
	switch {
	case text == "" && len(notes) > 0:
	case toolKindOf(b.toolName) == tkEdit || strings.Contains(text, "\n```diff\n"):
		if out, ok := renderEditBody(b, text, width); ok {
			body = out
		} else {
			body = renderGenericBody(b, text, toolPreviewLines, width)
		}
	case toolKindOf(b.toolName) == tkShell:
		body = renderShellBody(b, text, width)
	case toolKindOf(b.toolName) == tkMCP:
		body = renderGenericBody(b, text, mcpPreviewLines, width)
	case toolKindOf(b.toolName) == tkSubagent && !b.expanded:
		body = renderSubagentBody(text, width)
	default:
		body = renderGenericBody(b, text, toolPreviewLines, width)
	}
	var out []string
	if body != "" {
		out = append(out, body)
	}
	lead := func() string {
		if len(out) == 0 {
			return textMutedStyle.Render("  " + glyphs.Result + " ")
		}
		return "    "
	}
	for _, n := range notes {
		out = append(out, wrapHanging(lead()+warnStyle.Bold(true).Render(glyphs.Warn)+" ", warnStyle.Render(n), width))
	}
	if v := b.viewMeta(); v != nil && len(v.images) > 0 {
		out = append(out, wrapHanging(lead(), textHintStyle.Render(chromeGlyphs(strings.Join(v.images, " "))), width))
	}
	if len(out) == 0 {
		return textMutedStyle.Render("  " + glyphs.Result + " ")
	}
	return strings.Join(out, "\n")
}

const mcpPreviewLines = 10

// renderGenericBody is a result as text on an elbow: a read or search folded
// to its count, else the first preview lines and a fold that expands.
func renderGenericBody(b *block, text string, preview int, width int) string {
	lines := strings.Split(text, "\n")
	elbow := textMutedStyle.Render("  " + glyphs.Result + " ")
	failed := strings.HasPrefix(text, "Error")
	style := textSecondaryStyle
	if failed {
		// The header's glyph and meta already say it failed; the output itself
		// stays readable rather than all red.
		style = lipgloss.NewStyle()
	}
	// Reads and searches are context for the model, not for the reader: fold
	// them to a one-line count that still expands on demand.
	if !b.expanded && !failed && quietResultTool(b.toolName) && (len(lines) > 1 || toolKindOf(b.toolName) == tkFetch) {
		return wrap(elbow+textMutedStyle.Render(quietResultSummary(b.toolName, text, len(lines))), width)
	}
	var rows []string
	for i, l := range lines {
		lead := elbow
		if i > 0 {
			lead = "    " // continuation rows sit under the text, not the elbow
		}
		rows = append(rows, strings.Split(wrapHanging(lead, style.Render(l), width), "\n")...)
		if !b.expanded && i+1 >= preview {
			break
		}
	}
	hidden := len(lines) - min(len(lines), preview)
	if b.expanded {
		hidden = 0
	}
	// A few long lines can still fill the screen once wrapped: past twice the
	// preview in rows, the rest folds too, counted in rows.
	if limit := 2 * preview; !b.expanded && len(rows) > limit {
		hidden = len(rows) - limit + hidden
		rows = rows[:limit]
	}
	if hidden > 0 {
		rows = append(rows, foldHint(hidden, "lines"))
	}
	return strings.Join(rows, "\n")
}

// renderSubagentBody is a finished subagent's result folded to one line: the
// report's first line, or for a failure the error's message instead of the
// provider's raw response body. ctrl+e opens the whole result.
func renderSubagentBody(text string, width int) string {
	elbow := textMutedStyle.Render("  " + glyphs.Result + " ")
	lines := strings.Split(text, "\n")
	first := strings.TrimSpace(lines[0])
	for _, l := range lines {
		if first = strings.TrimSpace(l); first != "" {
			break
		}
	}
	if strings.HasPrefix(text, "Error") {
		first = subagentErrorSummary(text)
	}
	row := textSecondaryStyle.Render(first)
	if strings.HasPrefix(text, "Error") {
		row = errStyle.Render(chromeGlyphs(first))
	}
	out := ansi.Truncate(elbow+row, max(width, 1), "…")
	if strings.TrimSpace(text) != first || lipgloss.Width(elbow+row) > width {
		out += "\n" + foldHint(max(len(lines)-1, 1), "lines")
	}
	return out
}

var errMessageRE = regexp.MustCompile(`"message"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// subagentErrorSummary is a subagent failure in one line: "Error: 400 Bad
// Request: {json}" becomes "400 Bad Request · <the message in the json>".
func subagentErrorSummary(text string) string {
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(text, "Error"), ":"))
	head, _, _ := strings.Cut(text, "\n")
	if i := strings.IndexAny(head, "{["); i >= 0 {
		status := strings.TrimRight(strings.TrimSpace(head[:i]), ":")
		if mm := errMessageRE.FindStringSubmatch(text); mm != nil {
			if msg, err := strconv.Unquote(`"` + mm[1] + `"`); err == nil {
				mm[1] = msg
			}
			if status == "" {
				return mm[1]
			}
			return status + " · " + mm[1]
		}
		if status != "" {
			return status
		}
	}
	return head
}

// foldHint is the row standing in for n folded rows: "⋯ +14 lines · ctrl+e".
func foldHint(n int, unit string) string {
	return textHintStyle.Render(chromeGlyphs(fmt.Sprintf("    %s +%d %s · ctrl+e", glyphs.Fold, n, unit)))
}

var hookNoteRE = regexp.MustCompile(`(?m)^Hook (PostToolUse|PostToolUseFailure) failed: (.*)$\n?`)

const preToolDenied = "Error: hook PreToolUse denied tool call: "

// splitHookNotes takes the hook outcomes the agent writes into a result out
// of its text: a PreToolUse hook that refused the call, and post-tool hooks
// that failed. They draw as annotations under the result instead.
func splitHookNotes(text string) (string, []string) {
	var notes []string
	if reason, ok := strings.CutPrefix(text, preToolDenied); ok {
		return "", []string{"PreToolUse hook denied the call: " + strings.TrimSpace(reason)}
	}
	if !strings.Contains(text, "Hook PostToolUse") {
		return text, nil
	}
	for _, m := range hookNoteRE.FindAllStringSubmatch(text, -1) {
		notes = append(notes, m[1]+" hook failed: "+strings.TrimSpace(m[2]))
	}
	return strings.TrimRight(hookNoteRE.ReplaceAllString(text, ""), "\n"), notes
}

// truncCells cuts a styled row to width cells.
func truncCells(s string, width int) string {
	return ansi.Truncate(s, max(width, 1), "…")
}
