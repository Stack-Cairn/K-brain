package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type rewindEntry struct {
	cut    int
	text   string
	when   *time.Time
	future bool
}

type rewindState struct {
	entries []rewindEntry
	sel     int
	savedVP int
	// choosing is set while the "what to rewind" choice is open for the
	// selected entry; choice is the highlighted option.
	choosing bool
	choice   int
}

// rewindMode says what a rewind restores.
type rewindMode int

const (
	// rewindBoth rewinds the conversation and restores the workspace snapshot.
	rewindBoth rewindMode = iota
	// rewindConversation rewinds the conversation and leaves files alone.
	rewindConversation
	// rewindCode restores the files and keeps the conversation.
	rewindCode
)

// rewindModes are the choice's options, in order.
var rewindModes = []struct {
	mode  rewindMode
	label string
	desc  string
}{
	{rewindBoth, "Conversation and code", "rewind the conversation and restore files from the snapshot"},
	{rewindConversation, "Conversation only", "rewind the conversation, keep files as they are"},
	{rewindCode, "Code only", "restore files from the snapshot, keep the conversation"},
}

type escArmMsg struct{}

func (m *model) rewindEntries() []rewindEntry {
	var out []rewindEntry
	for i, msg := range m.agent.Messages {
		if msg.Role == "user" && msg.Authored {
			out = append(out, rewindEntry{cut: i, text: oneLine(msg.TextContent()), when: msg.SentAt})
		}
	}
	for i, msg := range m.future {
		if msg.Role == "user" && msg.Authored {
			out = append(out, rewindEntry{
				cut: len(m.agent.Messages) + i, text: oneLine(msg.TextContent()), when: msg.SentAt, future: true,
			})
		}
	}
	return out
}

func oneLine(s string) string { return truncLine(strings.Join(strings.Fields(s), " "), 100) }

func firstLine(s string) string {
	for l := range strings.Lines(s) {
		if l = strings.TrimSpace(l); l != "" {
			return truncLine(strings.Join(strings.Fields(l), " "), 120)
		}
	}
	return "(no output)"
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var kept []string
	for i := len(lines) - 1; i >= 0 && len(kept) < n; i-- {
		if l := strings.TrimRight(lines[i], "\r \t"); l != "" {
			kept = append([]string{truncLine(l, 200)}, kept...)
		}
	}
	return strings.Join(kept, "\n  ")
}

func toolVerb(name string) string {
	switch name {
	case "read":
		return "Reading"
	case "grep", "glob", "list":
		return "Searching"
	case "web_fetch":
		return "Fetching"
	case "write":
		return "Writing"
	case "edit":
		return "Editing"
	case "bash":
		return "Running"
	case "subagent", "task":
		return "Delegating"
	case "remember", "forget":
		return "Remembering"
	case "todowrite":
		return "Planning"
	default:
		return name
	}
}

func (m *model) batchSuffix(name, self string) string {
	var ids []string
	for _, b := range m.blocks {
		if (b.kind == blockToolQueued || b.kind == blockToolRun) && b.toolID != "" && b.toolName == name {
			ids = append(ids, b.toolID)
		}
	}
	if !slices.Contains(ids, self) {
		ids = append(ids, self)
	}
	if len(ids) < 2 {
		return ""
	}
	slices.Sort(ids)
	return " " + strconv.Itoa(slices.Index(ids, self)+1) + "/" + strconv.Itoa(len(ids))
}

func (m *model) scrollToMsg(msgIdx int) {
	if msgIdx < 0 || msgIdx >= len(m.msgBlock) {
		return
	}
	bi := m.msgBlock[msgIdx]
	if bi < 0 || bi >= len(m.blocks) {
		return
	}
	m.follow = false
	m.vp.SetYOffset(max(m.blocks[bi].y0-1, 0))
}

func (m *model) openRewind() {
	entries := m.rewindEntries()
	if len(entries) == 0 {
		m.append(dimStyle.Render("(nothing to rewind to yet)"))
		return
	}
	m.rew = &rewindState{entries: entries, sel: len(entries) - 1, savedVP: m.vp.YOffset}
	m.scrollToMsg(entries[len(entries)-1].cut)
}

func (m *model) rewindKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	r := m.rew
	sel := func() rewindEntry { return r.entries[r.sel] }
	if r.choosing {
		m.rewindChoiceKey(msg)
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.vp.SetYOffset(r.savedVP)
		m.rew = nil
	case tea.KeyUp:
		r.sel = max(r.sel-1, 0)
		m.scrollToMsg(sel().cut)
	case tea.KeyDown:
		r.sel = min(r.sel+1, len(r.entries)-1)
		m.scrollToMsg(sel().cut)
	case tea.KeyEnter:
		e := sel()
		if _, ok := m.rewindSnapshot(e.cut); ok && !e.future {
			r.choosing, r.choice = true, 0
			return m, nil
		}
		m.finishRewind(e, rewindBoth)
	case tea.KeyRunes:
		if string(msg.Runes) == "f" {
			e := sel()
			m.rew = nil
			m.openForkPrompt(e.cut, true)
			return m, nil
		}
	}
	return m, nil
}

// rewindChoiceKey drives the "what to rewind" choice: ↑/↓ or 1-3 pick,
// Enter applies, Esc goes back to the list.
func (m *model) rewindChoiceKey(msg tea.KeyMsg) {
	r := m.rew
	switch msg.Type {
	case tea.KeyEsc:
		r.choosing = false
	case tea.KeyUp:
		r.choice = max(r.choice-1, 0)
	case tea.KeyDown:
		r.choice = min(r.choice+1, len(rewindModes)-1)
	case tea.KeyEnter:
		m.finishRewind(r.entries[r.sel], rewindModes[r.choice].mode)
	case tea.KeyRunes:
		if s := string(msg.Runes); len(s) == 1 && s[0] >= '1' && int(s[0]-'1') < len(rewindModes) {
			m.finishRewind(r.entries[r.sel], rewindModes[s[0]-'1'].mode)
		}
	}
}

// finishRewind applies the chosen rewind and closes the browser; a rewind
// that moved the conversation back puts the rewound prompt in the composer.
func (m *model) finishRewind(e rewindEntry, mode rewindMode) {
	text, ok := m.applyRewindMode(e.cut, mode)
	m.rew = nil
	if ok && !e.future && mode != rewindCode {
		m.input.SetValue(text)
		m.input.CursorEnd()
		m.growInput()
	}
}

// rewindSnapshot is the workspace snapshot a rewind to cut would restore:
// the earliest one taken at or after the cut's turn boundary.
func (m *model) rewindSnapshot(cut int) (string, bool) {
	base := len(m.agent.Messages)
	cut = max(cut, 1)
	if cut >= base || len(m.snapshots) == 0 {
		return "", false
	}
	boundary := cut
	if m.history != nil {
		b, err := m.history.Boundary(cut)
		if err != nil {
			return "", false
		}
		boundary = b
	}
	best, bestIdx := "", -1
	for idx, ref := range m.snapshots {
		if idx >= boundary && (bestIdx == -1 || idx < bestIdx) {
			best, bestIdx = ref, idx
		}
	}
	return best, best != ""
}

func (m *model) applyRewind(cut int) (string, bool) {
	return m.applyRewindMode(cut, rewindBoth)
}

// applyRewindMode rewinds to cut. rewindBoth is the full rewind;
// rewindConversation skips the workspace restore; rewindCode restores the
// workspace and leaves the conversation and its snapshots as they are.
func (m *model) applyRewindMode(cut int, mode rewindMode) (string, bool) {
	if m.busy {
		m.append(dimStyle.Render("(busy — rewind after this turn)"))
		return "", false
	}
	base := len(m.agent.Messages)
	if base == 0 || cut > base+len(m.future) {
		m.append(errStyle.Render("invalid rewind position"))
		return "", false
	}
	cut = max(cut, 1)
	if !m.persist() {
		return "", false
	}
	boundary := cut
	if m.history != nil && cut <= base {
		var err error
		boundary, err = m.history.Boundary(cut)
		if err != nil {
			m.append(errStyle.Render("rewind failed: " + err.Error()))
			return "", false
		}
	}
	restored := 0
	if mode == rewindCode {
		best, ok := m.rewindSnapshot(cut)
		if !ok || cut >= base {
			m.append(dimStyle.Render("(no workspace snapshot for this turn — nothing to restore)"))
			return "", false
		}
		n, err := restoreWorkspace(best)
		if err != nil {
			m.append(errStyle.Render("workspace rewind failed: " + err.Error()))
			return "", false
		}
		m.append(dimStyle.Render(fmt.Sprintf("⟲ workspace rewound — %d file(s) restored; conversation kept", n)))
		return "", true
	}
	switch {
	case cut > base:
		m.agent.Messages = append(m.agent.Messages, m.future[:cut-base]...)
		m.future = append([]ai.Message(nil), m.future[cut-base:]...)
	case cut < base:
		best, bestIdx := "", -1
		for idx, ref := range m.snapshots {
			if idx >= boundary && (bestIdx == -1 || idx < bestIdx) {
				best, bestIdx = ref, idx
			}
		}
		if best != "" && mode == rewindBoth {
			var err error
			restored, err = restoreWorkspace(best)
			if err != nil {
				m.append(errStyle.Render("workspace rewind failed: " + err.Error()))
				return "", false
			}
		}
		if m.store != nil && m.sessionID != "" {
			if err := m.store.TruncateHistory(m.sessionID, m.history, cut); err != nil {
				note := "session save failed: "
				if best != "" && mode == rewindBoth {
					note = "workspace restored, but session save failed; conversation and snapshot retained for retry: "
				}
				m.append(errStyle.Render(note + err.Error()))
				return "", false
			}
		}
		clipped := append([]ai.Message(nil), m.agent.Messages[cut:]...)
		m.future = append(clipped, m.future...)
		m.agent.Messages = m.agent.Messages[:cut]
		m.saved = min(m.saved, cut)
		for idx := range m.snapshots {
			if idx >= boundary {
				delete(m.snapshots, idx)
			}
		}
		if best != "" {
			retained := false
			for _, ref := range m.snapshots {
				retained = retained || ref == best
			}
			if !retained && m.store != nil {
				used, err := m.store.SnapshotReferenced(best)
				retained = used || err != nil
			}
			if !retained {
				dropSnapshot(best)
			}
		}
	}
	m.persist()
	m.rebuildTranscript()

	if restored > 0 {
		m.append(dimStyle.Render(fmt.Sprintf("⟲ workspace rewound — %d file(s) restored", restored)))
	}
	text := ""
	if cut < len(m.agent.Messages)+len(m.future) {
		if msg := m.messageAt(cut); msg.Role == "user" && msg.Authored {
			text = msg.TextContent()
		}
	}
	return text, true
}

func (m *model) messageAt(i int) ai.Message {
	if i < len(m.agent.Messages) {
		return m.agent.Messages[i]
	}
	return m.future[i-len(m.agent.Messages)]
}

func (m *model) rebuildTranscript() {
	m.resetTranscript()
	m.seedTranscript(m.agent.Messages[1:], 1)
}

func (m *model) rewindView() string {
	r := m.rew
	if r.choosing {
		return m.rewindChoiceView()
	}
	const maxRows = 8

	start := max(0, min(r.sel-maxRows/2, len(r.entries)-maxRows))
	end := min(start+maxRows, len(r.entries))
	var b strings.Builder
	b.WriteString(dimStyle.Render("⏪ rewind — enter: rewind here · f: fork from here · esc: cancel"))
	for row := start; row < end; row++ {
		e := r.entries[row]
		b.WriteString("\n")
		if row == r.sel {
			b.WriteString(youStyle.Render(glyphUser + e.text))
		} else if e.future {
			b.WriteString(dimStyle.Render("  " + e.text + " (rewound)"))
		} else {
			b.WriteString("  " + e.text)
		}
		b.WriteString("\n    " + m.rewindTurnMeta(row))
	}
	fmt.Fprintf(&b, "\n%s", dimStyle.Render(fmt.Sprintf("  (%d/%d) ↑ older · ↓ newer", r.sel+1, len(r.entries))))
	return b.String()
}

func (m *model) turnUsage(cut int) (sum, last ai.Usage, ok bool) {
	for i := cut + 1; i < len(m.agent.Messages)+len(m.future); i++ {
		msg := m.messageAt(i)
		if msg.Role == "user" {
			break
		}
		if msg.Role == "assistant" && msg.Usage != nil {
			ok = true
			last = *msg.Usage
			sum.Add(*msg.Usage)
		}
	}
	return sum, last, ok
}

func (m *model) rewindTurnMeta(row int) string {
	e := m.rew.entries[row]
	meta := dimStyle.Render(rewindWhen(e.when))
	sum, last, ok := m.turnUsage(e.cut)
	if !ok {
		return meta
	}
	meta += dimStyle.Render(" · turn " + fmtTurn(sum))
	if cost, ok := m.turnCost(e.cut); ok {
		meta += dimStyle.Render(" · " + fmtCost(cost))
	}
	ctxSeg := " · context " + fmtTok(last.PromptTokens)
	if prev, ok := m.prevContextTokens(row); ok {
		if delta := last.PromptTokens - prev; delta > 0 {
			ctxSeg += growStyle.Render(fmt.Sprintf(" (+%s)", fmtTok(delta)))
		}
	}
	return meta + dimStyle.Render(ctxSeg)
}

func (m *model) prevContextTokens(row int) (int, bool) {
	for i := row - 1; i >= 0; i-- {
		if _, last, ok := m.turnUsage(m.rew.entries[i].cut); ok {
			return last.PromptTokens, true
		}
	}
	return 0, false
}

func fmtTurn(u ai.Usage) string {
	in := fmtTok(u.PromptTokens) + " in"
	if c := u.Cached(); c > 0 {
		in += fmt.Sprintf(" (%s cached)", fmtTok(c))
	}
	return fmt.Sprintf("%s / %s out", in, fmtTok(u.CompletionTokens))
}

func rewindWhen(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04") + " · " + ago(*t)
}

func (m *model) discardFuture() { m.future = nil }

// rewindChoiceView is the "what to rewind" card for the selected entry.
func (m *model) rewindChoiceView() string {
	r := m.rew
	w := cardWidth(m.width)
	e := r.entries[r.sel]
	head := m.tr("rewind to") + " " + truncLine(e.text, 60)
	if e.when != nil {
		head += " · " + rewindWhen(e.when)
	}
	rows := []string{textSecondaryStyle.Render(chromeGlyphs(head)), ""}
	for i, opt := range rewindModes {
		label := m.tr(opt.label) + "  " + textMutedStyle.Render(m.tr(opt.desc))
		rows = append(rows, cardOption(i == r.choice, strconv.Itoa(i+1), chromeGlyphs(label), w))
	}
	rows = append(rows, "", textHintStyle.Render(chromeGlyphs(m.tr("↑/↓ select · 1-3 pick · Enter apply · Esc back"))))
	return renderCard(tokAccent, m.tr("Rewind"), rows, w)
}
