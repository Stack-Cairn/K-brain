package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

var altUp = tea.KeyMsg{Type: tea.KeyUp, Alt: true}

// focusModel is a short conversation: a prompt, a reasoning run, a long
// reply, a tool call with a long result, and a closing reply.
func focusModel(w, h int) *model {
	m := compactCmdModel()
	m.showThinking = true
	m.Update(mkWinSize(w, h))
	m.appendRaw(blockUser, "Why does the cache miss after a resize?")
	m.blocks = append(m.blocks, block{kind: blockThinking, text: "The key holds the width.\nA resize changes it.", thought: &thinkMeta{}})
	m.appendAssistantBlock("The cache is keyed by **width**.\n\nA resize changes the width, so every entry misses once.\n\nAfter that the new width is cached again.")
	m.blocks = append(m.blocks, block{kind: blockToolRun, text: toolHeaderRow("bash", `{"command":"grep -n width cache.go"}`, false), toolName: "bash", toolArgs: `{"command":"grep -n width cache.go"}`})
	var out []string
	for i := range 12 {
		out = append(out, fmt.Sprintf("cache.go:%d: key := width + %d", 10+i, i))
	}
	out = append(out, "cache.go:99: // the stale entry is dropped here")
	m.blocks = append(m.blocks, block{kind: blockTool, text: strings.Join(out, "\n"), toolName: "bash"})
	m.appendAssistantBlock("Keying by width is intended.")
	m.refreshVP()
	m.layout()
	m.View()
	return m
}

func keys(t *testing.T, m *model, ks ...string) *model {
	t.Helper()
	for _, k := range ks {
		var msg tea.KeyMsg
		switch k {
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "alt+up":
			msg = altUp
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		default:
			msg = keyRunes(k)
		}
		tm, _ := m.Update(msg)
		m = tm.(*model)
	}
	return m
}

func (m *model) focused() *block {
	if bi := m.focusIndex(); bi >= 0 {
		return &m.blocks[bi]
	}
	return nil
}

func TestAltUpFocusesTranscriptAndEscReturns(t *testing.T) {
	m := focusModel(80, 40)
	m = keys(t, m, "alt+up")
	if b := m.focused(); b == nil || b.text != "Keying by width is intended." {
		t.Fatalf("Alt+↑ focused %+v, want the last reply", b)
	}
	v := stripView(m.View())
	if !strings.Contains(v, "┃ transcript 6/6 · reply") || !strings.Contains(v, "j/k move") {
		t.Fatalf("no focus bar:\n%s", v)
	}
	// The reply's glyph turns to reverse video.
	if !strings.Contains(m.View(), "\x1b[7m●\x1b[27m") {
		t.Fatal("the focused block is not marked")
	}
	m = keys(t, m, "k")
	if !strings.Contains(stripView(m.View()), "┃ └ cache.go:10") {
		t.Fatalf("no rail on the focused tool output:\n%s", stripView(m.View()))
	}
	m = keys(t, m, "k")
	if b := m.focused(); b.kind != blockToolRun {
		t.Fatalf("k k focused %v, want the tool call", b.kind)
	}
	m = keys(t, m, "g")
	if b := m.focused(); b.kind != blockUser {
		t.Fatalf("g focused %v", b.kind)
	}
	m = keys(t, m, "G", "j")
	if b := m.focused(); b.text != "Keying by width is intended." {
		t.Fatal("j past the end moved off the last block")
	}
	// Keys the transcript does not bind do nothing while it has focus.
	m = keys(t, m, "x")
	if m.input.Value() != "" {
		t.Fatalf("typing reached the composer: %q", m.input.Value())
	}
	m = keys(t, m, "esc", "x")
	if m.tx.focus != nil || m.input.Value() != "x" {
		t.Fatalf("Esc did not give the composer the keyboard back: focus=%v input=%q", m.tx.focus, m.input.Value())
	}
	if strings.Contains(stripView(m.View()), "j/k move") {
		t.Fatal("focus bar left behind")
	}
}

// With a queued message selected, Alt+↑ is the queue's: it moves the message.
func TestAltUpYieldsToQueuePane(t *testing.T) {
	m := focusModel(80, 40)
	m.busy = true
	m.queue = []string{"first", "second"}
	m.queueSel = 1
	m = keys(t, m, "alt+up")
	if m.tx.focus != nil {
		t.Fatal("Alt+↑ took focus from the queue pane")
	}
	if strings.Join(m.queue, ",") != "second,first" {
		t.Fatalf("Alt+↑ did not move the queued message: %q", m.queue)
	}
}

func TestFocusFoldKeys(t *testing.T) {
	m := focusModel(80, 60)
	m = keys(t, m, "alt+up", "k")
	res := m.focused()
	if res.kind != blockTool || res.expanded {
		t.Fatalf("focused %v expanded=%v", res.kind, res.expanded)
	}
	m = keys(t, m, "l")
	if !res.expanded || !strings.Contains(stripView(m.View()), "the stale entry") {
		t.Fatal("l did not open the tool output")
	}
	m = keys(t, m, "h")
	if res.expanded {
		t.Fatal("h did not fold the tool output")
	}
	m = keys(t, m, "e")
	if !res.expanded {
		t.Fatal("e did not flip the tool output")
	}

	// A reply has no toggle of its own: it folds to its first row.
	m = keys(t, m, "k", "k")
	reply := m.focused()
	if reply.kind != blockAssistant {
		t.Fatalf("focused %v", reply.kind)
	}
	m = keys(t, m, "h")
	if reply.height() != 2 || !strings.Contains(ansi.Strip(reply.rows[1]), "⋯ 4 more lines") {
		t.Fatalf("folded reply = %q", reply.rows)
	}
	// A click opens it again.
	m.clickAt(4, m.viewportTop+reply.y0-m.vp.YOffset)
	if reply.folded || reply.height() < 3 {
		t.Fatal("a click did not open the folded reply")
	}

	// E folds every open toggle, then opens them all.
	m = keys(t, m, "E")
	for _, b := range m.blocks {
		if hasToggle(&b) && b.expanded {
			t.Fatalf("E left %v open", b.kind)
		}
	}
	m = keys(t, m, "E")
	for _, b := range m.blocks {
		if hasToggle(&b) && !b.expanded {
			t.Fatalf("second E left %v folded", b.kind)
		}
	}
}

// Moving focus scrolls the block into view.
func TestFocusScrollsBlockIntoView(t *testing.T) {
	m := focusModel(80, 16)
	m = keys(t, m, "alt+up", "g")
	if !m.vp.AtTop() || m.follow {
		t.Fatalf("g did not scroll to the first block: offset %d follow %v", m.vp.YOffset, m.follow)
	}
	m = keys(t, m, "G")
	if !m.vp.AtBottom() {
		t.Fatal("G did not scroll back to the bottom")
	}
}

func TestFindStepsThroughMatches(t *testing.T) {
	m := focusModel(80, 60)
	m.command("/find width")
	f := m.tx.focus
	if f == nil || f.find == nil || len(f.find.hits) == 0 {
		t.Fatalf("/find found nothing: %+v", f)
	}
	last := f.find.hits[len(f.find.hits)-1]
	if f.find.cur != len(f.find.hits)-1 || m.focused().uid != last.uid {
		t.Fatalf("/find landed on %d of %d", f.find.cur, len(f.find.hits))
	}
	v := m.View()
	if !strings.Contains(v, "\x1b[7mwidth\x1b[27m") {
		t.Fatal("matches are not highlighted")
	}
	if !strings.Contains(stripView(v), fmt.Sprintf(`"width" %d/%d`, len(f.find.hits), len(f.find.hits))) {
		t.Fatalf("bar does not count matches:\n%s", stripView(v))
	}
	m = keys(t, m, "n")
	if f.find.cur != len(f.find.hits)-2 {
		t.Fatalf("n moved to %d", f.find.cur)
	}
	m = keys(t, m, "N", "N")
	if f.find.cur != 0 {
		t.Fatalf("N past the newest match should wrap to the first, at %d", f.find.cur)
	}

	// Smart case: a capital makes the search case-sensitive.
	m = keys(t, m, "/", "W", "i", "d", "t", "h", "enter")
	if len(f.find.hits) != 0 || !strings.Contains(stripView(m.View()), "no match") {
		t.Fatalf("Width matched %d rows", len(f.find.hits))
	}
}

// A match in a folded block's hidden text opens the block.
func TestFindOpensFoldedMatch(t *testing.T) {
	m := focusModel(80, 60)
	m.command("/find width + 5")
	res := m.focused()
	if res == nil || res.kind != blockTool || !res.expanded {
		t.Fatalf("the match in the folded output did not open it: %+v", res)
	}
	if h := m.tx.focus.find.hits[m.tx.focus.find.cur]; h.row < 0 {
		t.Fatal("landed on the folded match, not the row")
	}
}

func TestFindCommandWithoutTextTypesQuery(t *testing.T) {
	m := focusModel(80, 40)
	m.command("/find")
	m = keys(t, m, "c", "a", "c", "h", "e", "backspace", "e")
	if !strings.Contains(stripView(m.View()), "/ cache") {
		t.Fatalf("query not shown:\n%s", stripView(m.View()))
	}
	m = keys(t, m, "enter")
	if m.tx.focus.find == nil || m.tx.focus.find.query != "cache" || len(m.tx.focus.find.hits) == 0 {
		t.Fatalf("find = %+v", m.tx.focus.find)
	}
	if !busyCmd("/find x") {
		t.Fatal("/find should run while a turn is busy")
	}
}

func TestBlockViewer(t *testing.T) {
	m := focusModel(80, 12)
	m = keys(t, m, "alt+up", "k", "v")
	v := stripView(m.View())
	lines := strings.Split(strings.TrimSuffix(v, "\n"), "\n")
	if len(lines) != 12 || !strings.HasPrefix(lines[0], "┃ tool output · 13 lines") {
		t.Fatalf("viewer frame:\n%s", v)
	}
	if strings.Contains(v, "Keying by width") {
		t.Fatal("the transcript shows through the viewer")
	}
	m = keys(t, m, "G")
	if v := stripView(m.View()); !strings.Contains(v, "the stale entry") || !strings.Contains(v, "end") {
		t.Fatalf("G did not reach the end:\n%s", v)
	}
	m = keys(t, m, "g", "/", "9", "9", "enter")
	vw := m.tx.viewer
	if len(vw.hits) != 1 || vw.off == 0 {
		t.Fatalf("viewer search: hits %v off %d", vw.hits, vw.off)
	}
	// The wheel scrolls the viewer, and a click does not reach the transcript.
	m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 2, Y: 3})
	if m.sel != nil {
		t.Fatal("a click in the viewer started a selection")
	}
	m = keys(t, m, "esc")
	if m.tx.viewer != nil || m.tx.focus == nil {
		t.Fatal("Esc should close the viewer and keep focus")
	}
}

// A reply still streaming grows in the viewer.
func TestBlockViewerFollowsStreamingBlock(t *testing.T) {
	m := focusModel(80, 20)
	m.busy = true
	m.Update(textMsg("first line\n"))
	m = keys(t, m, "alt+up", "G", "v")
	m.Update(textMsg("second line\n"))
	if v := stripView(m.View()); !strings.Contains(v, "second line") {
		t.Fatalf("viewer did not follow the stream:\n%s", v)
	}
}

// TestGoldenTranscriptFocus pins the focus surfaces: the rail on a focused
// block, a folded reply, search highlights with the bar, and the viewer.
func TestGoldenTranscriptFocus(t *testing.T) {
	t.Chdir(t.TempDir())
	defer SetLightTheme(false)
	for _, mode := range goldenModes {
		for _, size := range goldenSizes {
			for _, scene := range []string{"focus", "viewer"} {
				name := fmt.Sprintf("transcript_%s_%s_%dx%d", scene, mode.name, size[0], size[1])
				t.Run(name, func(t *testing.T) {
					restore := useProfile(t, mode.profile, mode.dark)
					defer restore()
					SetLightTheme(!mode.dark)
					if mode.mono {
						setMonoMode(true, true)
					}
					setASCIIGlyphs(mode.ascii)
					m := focusModel(size[0], size[1])
					m.applyAppearance()
					m.refreshVP()
					if scene == "focus" {
						m = keys(t, m, "alt+up", "k", "k", "k", "h")
						m.command("/find width")
						m = keys(t, m, "n")
					} else {
						m = keys(t, m, "alt+up", "k", "v", "/", "w", "i", "d", "t", "h", "enter")
					}
					m.layout()
					v := m.View()
					for r, l := range strings.Split(v, "\n") {
						if w := lipgloss.Width(l); w > size[0] {
							t.Errorf("row %d is %d cells wide on a %d-column screen", r, w, size[0])
						}
					}
					if h := lipgloss.Height(v); h > size[1] {
						t.Errorf("frame is %d rows on a %d-row screen", h, size[1])
					}
					checkGolden(t, name+".ansi", v)
					checkGolden(t, name+".txt", stripView(v))
				})
			}
		}
	}
}

// Keys that arrive together in one read still act one at a time.
func TestFocusKeysArrivingTogether(t *testing.T) {
	m := focusModel(80, 40)
	m = keys(t, m, "alt+up", "/width")
	f := m.tx.focus
	if !f.typing || f.draft != "width" {
		t.Fatalf("typing=%v draft=%q", f.typing, f.draft)
	}
}

// An approval that arrives while the viewer is open shows over it.
func TestBlockViewerGivesWayToDialogs(t *testing.T) {
	m := focusModel(80, 24)
	m = keys(t, m, "alt+up", "v")
	if _, ok := m.blockViewerView(); !ok {
		t.Fatal("viewer not shown")
	}
	m.permDialog = &permDialog{}
	if _, ok := m.blockViewerView(); ok {
		t.Fatal("the viewer hides a pending approval")
	}
}

// Focus and folding go through the tool renderers' own toggles: a verb
// group opens to its calls, an edit's diff to every line, and E covers both.
func TestFocusFoldsToolGroupsAndDiffs(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 60))
	clock := time.Now()
	s := toolStage{m: m, clock: &clock}
	m.appendRaw(blockUser, "fix the status flag")
	s.call("r1", "read", `{"path":"a.go"}`, 0, "1\n2", &tools.Result{})
	s.call("r2", "read", `{"path":"b.go"}`, 0, "1\n2", &tools.Result{})
	var diff strings.Builder
	for i := range 30 {
		fmt.Fprintf(&diff, "%d - old line %d\n%d + new line %d\n", 40+i, i, 40+i, i)
	}
	s.call("e1", "edit", `{"path":"status.go","old_string":"x","new_string":"y"}`, 0,
		"Replaced 1 occurrence(s) in status.go\n```diff\n"+strings.TrimSuffix(diff.String(), "\n")+"\n```", &tools.Result{})
	m.appendAssistantBlock("Done.")
	m.refreshVP()
	m.layout()
	m.View()

	var group, edit *block
	for i := range m.blocks {
		b := &m.blocks[i]
		if toolGroupBlock(b) {
			group = b
		}
		if b.kind == blockTool && strings.Contains(b.text, "```diff") {
			edit = b
		}
	}
	if group == nil || edit == nil {
		t.Fatalf("no group (%v) or diff (%v) block", group != nil, edit != nil)
	}

	m = keys(t, m, "alt+up")
	for m.focused() != edit {
		before := m.focused()
		m = keys(t, m, "k")
		if m.focused() == before {
			t.Fatal("never reached the diff")
		}
	}
	folded := edit.height()
	m = keys(t, m, "l")
	if !edit.expanded || edit.height() <= folded || !strings.Contains(ansi.Strip(edit.renderAt(m.width)), "new line 29") {
		t.Fatalf("l did not open the diff: %d -> %d rows", folded, edit.height())
	}
	for m.focused() != group {
		m = keys(t, m, "k")
	}
	m = keys(t, m, "l")
	if !group.expanded || !strings.Contains(ansi.Strip(group.renderAt(m.width)), "Read  b.go") {
		t.Fatal("l did not open the group")
	}
	m = keys(t, m, "E")
	if group.expanded || edit.expanded {
		t.Fatalf("E left the group (%v) or diff (%v) open", group.expanded, edit.expanded)
	}
	m = keys(t, m, "E")
	if !group.expanded || !edit.expanded {
		t.Fatal("second E did not open both")
	}
	m.command("/find new line 17")
	if m.focused() != edit {
		t.Fatalf("/find focused %v", m.focused().kind)
	}
}
