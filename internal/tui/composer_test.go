package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Stack-Cairn/K-brain/internal/session"
)

func init() {
	// Recent slash commands stay in memory in tests unless a test points the
	// file somewhere of its own.
	slashMRUPath = func() (string, error) { return "", errors.New("no recent-command file in tests") }
}

func typeKeys(t *testing.T, m *model, s string) *model {
	t.Helper()
	for _, r := range s {
		if r == ' ' {
			m = press(t, m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
			continue
		}
		m = press(t, m, keyRunes(string(r)))
	}
	return m
}

func ctrlKey(k tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: k} }

func altRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Alt: true}
}

func pasteMsg(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Paste: true}
}

func TestComposerUndoRedoGroupsWords(t *testing.T) {
	m := compactCmdModel()
	m = typeKeys(t, m, "hello world")
	if m.input.Value() != "hello world" {
		t.Fatalf("typed %q", m.input.Value())
	}
	m = press(t, m, ctrlKey(tea.KeyCtrlZ))
	if got := m.input.Value(); got != "hello " {
		t.Fatalf("first undo should drop the last word, got %q", got)
	}
	m = press(t, m, ctrlKey(tea.KeyCtrlZ))
	if got := m.input.Value(); got != "" {
		t.Fatalf("second undo should drop the first word, got %q", got)
	}
	m = press(t, m, ctrlKey(tea.KeyCtrlZ))
	if got := m.input.Value(); got != "" {
		t.Fatalf("undo past the start is a no-op, got %q", got)
	}
	m = press(t, m, altRunes("z"))
	if got := m.input.Value(); got != "hello " {
		t.Fatalf("redo should bring back the first word, got %q", got)
	}
	m = press(t, m, altRunes("z"))
	if got := m.input.Value(); got != "hello world" {
		t.Fatalf("redo should bring back the second word, got %q", got)
	}

	// A new edit after an undo drops the redo history.
	m = press(t, m, ctrlKey(tea.KeyCtrlZ))
	m = typeKeys(t, m, "x")
	m = press(t, m, altRunes("z"))
	if got := m.input.Value(); got != "hello x" {
		t.Fatalf("redo after a new edit must do nothing, got %q", got)
	}
	// Ctrl+_ is undo too (what many terminals send for Ctrl+/).
	m = press(t, m, tea.KeyMsg{Type: tea.KeyCtrlUnderscore})
	if got := m.input.Value(); got != "hello " {
		t.Fatalf("Ctrl+_ should undo, got %q", got)
	}
}

func TestComposerUndoClearsAfterSend(t *testing.T) {
	m := busyQueueModel()
	m = typeKeys(t, m, "first")
	m = press(t, m, ctrlKey(tea.KeyEnter))
	if len(m.queue) != 1 || m.queue[0] != "first" {
		t.Fatalf("queue = %q", m.queue)
	}
	m = press(t, m, ctrlKey(tea.KeyCtrlZ))
	if m.input.Value() != "" {
		t.Fatalf("undo after a send must not bring the sent text back, got %q", m.input.Value())
	}
}

func TestComposerKillAndYank(t *testing.T) {
	m := compactCmdModel()
	m = typeKeys(t, m, "one two three")

	m = press(t, m, ctrlKey(tea.KeyCtrlW))
	m = press(t, m, ctrlKey(tea.KeyCtrlW))
	if got := m.input.Value(); got != "one " {
		t.Fatalf("two Ctrl+W should cut two words, got %q", got)
	}
	m = press(t, m, ctrlKey(tea.KeyCtrlY))
	if got := m.input.Value(); got != "one two three" {
		t.Fatalf("Ctrl+Y should bring both words back as one kill, got %q", got)
	}

	// Ctrl+A then Alt+K cuts the line; Ctrl+Y then Alt+Y cycles the ring.
	m = press(t, m, ctrlKey(tea.KeyCtrlA))
	m = press(t, m, altRunes("d"))
	if got := m.input.Value(); got != " two three" {
		t.Fatalf("Alt+D should cut the next word, got %q", got)
	}
	m = press(t, m, altRunes("k"))
	if got := m.input.Value(); got != "" {
		t.Fatalf("Alt+K should cut to the end of the line, got %q", got)
	}
	m = press(t, m, ctrlKey(tea.KeyCtrlY))
	if got := m.input.Value(); got != "one two three" {
		t.Fatalf("Alt+D then Alt+K are one forward kill, got %q", got)
	}
	m = press(t, m, altRunes("y"))
	if got := m.input.Value(); got != "two three" {
		t.Fatalf("Alt+Y should swap in the older kill, got %q", got)
	}
	m = press(t, m, altRunes("y"))
	if got := m.input.Value(); got != "one two three" {
		t.Fatalf("Alt+Y should cycle back round, got %q", got)
	}

	// Ctrl+U cuts to the line start; Alt+Backspace cuts one word of letters.
	m.setComposer("keep\npath/to/file", len([]rune("keep\npath/to/file")))
	m = press(t, m, tea.KeyMsg{Type: tea.KeyBackspace, Alt: true})
	if got := m.input.Value(); got != "keep\npath/to/" {
		t.Fatalf("Alt+Backspace should stop at punctuation, got %q", got)
	}
	m = press(t, m, ctrlKey(tea.KeyCtrlU))
	if got := m.input.Value(); got != "keep\n" {
		t.Fatalf("Ctrl+U should cut to the start of the line only, got %q", got)
	}
	m = press(t, m, ctrlKey(tea.KeyCtrlZ))
	if got := m.input.Value(); got != "keep\npath/to/" {
		t.Fatalf("a kill is undoable, got %q", got)
	}
}

func TestComposerCtrlKStillClears(t *testing.T) {
	m := compactCmdModel()
	m = typeKeys(t, m, "draft")
	m = press(t, m, altRunes("k"))
	if m.input.Value() != "draft" {
		t.Fatalf("Alt+K at the end of the line has nothing to cut, got %q", m.input.Value())
	}
}

func TestComposerMultilineMode(t *testing.T) {
	m := busyQueueModel()
	m = press(t, m, altRunes("m"))
	if !m.multiline() {
		t.Fatal("Alt+M should turn multiline on")
	}
	m = typeKeys(t, m, "a")
	m = press(t, m, ctrlKey(tea.KeyEnter))
	m = typeKeys(t, m, "b")
	if got := m.input.Value(); got != "a\nb" {
		t.Fatalf("Enter in multiline mode should add a line, got %q", got)
	}
	if len(m.queue) != 0 {
		t.Fatalf("nothing should be sent yet: %q", m.queue)
	}
	m = press(t, m, ctrlKey(tea.KeyCtrlJ))
	if len(m.queue) != 1 || m.queue[0] != "a\nb" {
		t.Fatalf("Ctrl+J in multiline mode should send, queue = %q", m.queue)
	}
	m = press(t, m, altRunes("m"))
	if m.multiline() {
		t.Fatal("Alt+M again should turn multiline off")
	}
	m = typeKeys(t, m, "c")
	m = press(t, m, ctrlKey(tea.KeyEnter))
	if len(m.queue) != 2 || m.queue[1] != "c" {
		t.Fatalf("Enter should send again, queue = %q", m.queue)
	}
}

func TestComposerDraftStash(t *testing.T) {
	m := compactCmdModel()
	m = typeKeys(t, m, "half a thought")
	m = press(t, m, ctrlKey(tea.KeyCtrlS))
	if m.input.Value() != "" {
		t.Fatalf("Ctrl+S should clear the composer, got %q", m.input.Value())
	}
	m.syncInputPlaceholder()
	if !strings.Contains(m.input.Placeholder, "Ctrl+S") {
		t.Fatalf("the placeholder should say a draft is stashed, got %q", m.input.Placeholder)
	}
	m = typeKeys(t, m, "quick question")
	m = press(t, m, ctrlKey(tea.KeyCtrlS))
	m = press(t, m, ctrlKey(tea.KeyCtrlS))
	if m.input.Value() != "quick question" {
		t.Fatalf("Ctrl+S on an empty composer restores the newest stash, got %q", m.input.Value())
	}
	m.input.Reset()
	m = press(t, m, ctrlKey(tea.KeyCtrlS))
	if m.input.Value() != "half a thought" {
		t.Fatalf("then the older one, got %q", m.input.Value())
	}
	m.input.Reset()
	m = press(t, m, ctrlKey(tea.KeyCtrlS))
	if m.input.Value() != "" {
		t.Fatalf("an empty stash restores nothing, got %q", m.input.Value())
	}
}

func TestComposerEscEscClearStashesDraft(t *testing.T) {
	m := compactCmdModel()
	m.queueSel = -1
	m = typeKeys(t, m, "almost lost")
	m = press(t, m, ctrlKey(tea.KeyEsc))
	m = press(t, m, ctrlKey(tea.KeyEsc))
	if m.input.Value() != "" {
		t.Fatalf("Esc Esc should clear, got %q", m.input.Value())
	}
	m = press(t, m, ctrlKey(tea.KeyCtrlS))
	if m.input.Value() != "almost lost" {
		t.Fatalf("a draft cleared with Esc Esc can be restored with Ctrl+S, got %q", m.input.Value())
	}
}

func TestPasteChipIsAtomic(t *testing.T) {
	body := "l1\nl2\nl3\nl4\nl5"
	m := busyQueueModel()
	m = typeKeys(t, m, "see ")
	m = press(t, m, pasteMsg(body))
	m = typeKeys(t, m, " ok")
	chip := "[Pasted 5 lines" + chipSentinel + "]"
	if got := m.input.Value(); got != "see "+chip+" ok" {
		t.Fatalf("value = %q", got)
	}

	// ← from after the chip jumps over all of it.
	for range 3 {
		m = press(t, m, ctrlKey(tea.KeyLeft))
	}
	if off := m.composerSnap().off; off != len([]rune("see "+chip)) {
		t.Fatalf("cursor after three ← = %d", off)
	}
	m = press(t, m, ctrlKey(tea.KeyLeft))
	if off := m.composerSnap().off; off != len("see ") {
		t.Fatalf("← should skip the whole chip, cursor at %d", off)
	}
	m = press(t, m, ctrlKey(tea.KeyRight))
	m = press(t, m, ctrlKey(tea.KeyBackspace))
	if got := m.input.Value(); got != "see  ok" {
		t.Fatalf("Backspace should delete the whole chip, got %q", got)
	}
	m = press(t, m, ctrlKey(tea.KeyCtrlZ))
	if got := m.input.Value(); got != "see "+chip+" ok" {
		t.Fatalf("undo should bring the chip back, got %q", got)
	}

	m = press(t, m, ctrlKey(tea.KeyEnter))
	if len(m.queue) != 1 || m.queue[0] != "see "+body+" ok" {
		t.Fatalf("sending should expand the chip, queue = %q", m.queue)
	}
	m = typeKeys(t, m, "x")
	if len(m.cmp.pastes) != 0 {
		t.Fatalf("a sent paste is forgotten: %v", m.cmp.pastes)
	}
}

func TestPasteChipsNumberAndSizes(t *testing.T) {
	m := compactCmdModel()
	m = press(t, m, pasteMsg("a\nb\nc\nd"))
	m = press(t, m, pasteMsg(strings.Repeat("x", 12*1024)))
	want := "[Pasted 4 lines" + chipSentinel + "][Pasted 12 KB #2" + chipSentinel + "]"
	if got := m.input.Value(); got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
	if got := m.expandPasteChips(m.input.Value()); got != "a\nb\nc\nd"+strings.Repeat("x", 12*1024) {
		t.Fatalf("expanded to %d bytes", len(got))
	}
	// A short paste lands as text.
	m.input.Reset()
	m = press(t, m, pasteMsg("short\ntext"))
	if m.input.Value() != "short\ntext" {
		t.Fatalf("a short paste is not a chip: %q", m.input.Value())
	}
}

func TestImageChipCap(t *testing.T) {
	m := compactCmdModel()
	var b strings.Builder
	for i := 1; i <= maxImageChips; i++ {
		b.WriteString(pastedImage{n: i, path: "/tmp/x.png"}.chipText())
	}
	m.setComposer(b.String(), len([]rune(b.String())))
	before := m.input.Value()
	_, cmd := m.key(ctrlKey(tea.KeyCtrlV))
	if cmd == nil {
		t.Fatal("Ctrl+V past the cap should show a notice")
	}
	if m.input.Value() != before {
		t.Fatal("no image is added past the cap")
	}
	if !strings.Contains(m.transientNotice, fmt.Sprint(maxImageChips)) {
		t.Fatalf("notice = %q", m.transientNotice)
	}
}

func TestHistorySearchFindsAndAccepts(t *testing.T) {
	m := compactCmdModel()
	m.hist = []string{"deploy the staging server", "write tests for the parser", "fix the deploy script"}
	m = typeKeys(t, m, "draft")
	m = press(t, m, ctrlKey(tea.KeyCtrlR))
	if m.cmp.hs == nil || m.input.Value() != "" {
		t.Fatal("Ctrl+R should open the search with an empty query")
	}
	if n := len(m.cmp.hs.matches); n != 3 {
		t.Fatalf("an empty query lists everything, got %d", n)
	}
	m = typeKeys(t, m, "dep")
	hs := m.cmp.hs
	if len(hs.matches) != 2 {
		t.Fatalf("dep should match two prompts, got %d", len(hs.matches))
	}
	if first := hs.entries[hs.matches[0].idx].text; first != "deploy the staging server" {
		t.Fatalf("a match at the start ranks first, got %q", first)
	}
	m = press(t, m, ctrlKey(tea.KeyDown))
	m = press(t, m, ctrlKey(tea.KeyEnter))
	if m.cmp.hs != nil {
		t.Fatal("Enter closes the search")
	}
	if m.input.Value() != "fix the deploy script" {
		t.Fatalf("Enter should put the selected prompt in the composer, got %q", m.input.Value())
	}
	m = press(t, m, ctrlKey(tea.KeyCtrlZ))
	if m.input.Value() != "draft" {
		t.Fatalf("undo after accepting brings the draft back, got %q", m.input.Value())
	}

	m = press(t, m, ctrlKey(tea.KeyCtrlR))
	m = typeKeys(t, m, "zzz")
	if len(m.cmp.hs.matches) != 0 {
		t.Fatal("zzz matches nothing")
	}
	m = press(t, m, ctrlKey(tea.KeyEsc))
	if m.cmp.hs != nil || m.input.Value() != "draft" {
		t.Fatalf("Esc closes and restores the draft, got %q", m.input.Value())
	}
}

func TestHistorySearchScopesToDirectory(t *testing.T) {
	m := compactCmdModel()
	m.agent.WorkingDir = "/proj/a"
	m = press(t, m, ctrlKey(tea.KeyCtrlR))
	now := time.Now()
	m.historyLoaded([]session.UserHistoryEntry{
		{Text: "build the a binary", CWD: "/proj/a", At: now},
		{Text: "build the b binary", CWD: "/proj/b", At: now},
		{Text: "unrelated", CWD: "/proj/b", At: now},
	}, nil)
	m = typeKeys(t, m, "build")
	hs := m.cmp.hs
	if len(hs.matches) != 1 || hs.entries[hs.matches[0].idx].text != "build the a binary" {
		t.Fatalf("this-directory scope should show only /proj/a")
	}
	m = press(t, m, ctrlKey(tea.KeyTab))
	if !m.cmp.hs.all || len(m.cmp.hs.matches) != 2 {
		t.Fatalf("Tab should widen to all directories, got %d", len(m.cmp.hs.matches))
	}
	view := m.historyPanelView()
	if !strings.Contains(view, "all directories") || !strings.Contains(view, "b") {
		t.Fatalf("panel:\n%s", view)
	}
}

func TestFuzzyMatchRanksWordStarts(t *testing.T) {
	s1, _, ok1 := fuzzyMatch("run the tests", "tests")
	s2, _, ok2 := fuzzyMatch("attestsuite", "tests")
	if !ok1 || !ok2 || s1 <= s2 {
		t.Fatalf("a word-start match should rank higher: %d vs %d", s1, s2)
	}
	if _, pos, ok := fuzzyMatch("git status", "gst"); !ok || !pos[0] || !pos[4] {
		t.Fatalf("subsequence positions = %v", pos)
	}
	if _, _, ok := fuzzyMatch("git status", "xyz"); ok {
		t.Fatal("xyz should not match")
	}
	if _, _, ok := fuzzyMatch("deploy staging", "stag dep"); !ok {
		t.Fatal("each word of the query matches on its own")
	}
}

func TestQueueEditMoveAndSendNow(t *testing.T) {
	m := busyQueueModel("first", "second", "third")
	cancelled := false
	m.cancel = func() { cancelled = true }

	m = press(t, m, ctrlKey(tea.KeyUp)) // select "third"
	m = press(t, m, tea.KeyMsg{Type: tea.KeyUp, Alt: true})
	if strings.Join(m.queue, ",") != "first,third,second" || m.queueSel != 1 {
		t.Fatalf("Alt+↑ should move the selection up: %q sel=%d", m.queue, m.queueSel)
	}

	m = press(t, m, ctrlKey(tea.KeyCtrlE))
	if m.input.Value() != "third" || strings.Join(m.queue, ",") != "first,second" {
		t.Fatalf("Ctrl+E should take the message into the composer: %q / %q", m.input.Value(), m.queue)
	}
	if v := m.queueView(); !strings.Contains(v, "editing #2") || !strings.Contains(v, "(editing)") {
		t.Fatalf("queue pane while editing:\n%s", v)
	}
	m = typeKeys(t, m, "!")
	m = press(t, m, ctrlKey(tea.KeyEnter))
	if strings.Join(m.queue, ",") != "first,third!,second" {
		t.Fatalf("an edit goes back to its place: %q", m.queue)
	}

	// Esc while editing puts the original back.
	m.queueSel = 0
	m = press(t, m, ctrlKey(tea.KeyCtrlE))
	m = typeKeys(t, m, " changed")
	m = press(t, m, ctrlKey(tea.KeyEsc))
	if strings.Join(m.queue, ",") != "first,third!,second" || m.input.Value() != "" {
		t.Fatalf("Esc should restore the queue: %q / %q", m.queue, m.input.Value())
	}
	if cancelled {
		t.Fatal("Esc while editing must not interrupt the turn")
	}

	m.queueSel = 2
	m = press(t, m, ctrlKey(tea.KeyEsc))
	if m.queueSel != -1 || cancelled {
		t.Fatal("Esc on a selected message leaves the queue first")
	}

	m.queueSel = 2
	m = press(t, m, ctrlKey(tea.KeyEnter))
	if !cancelled || strings.Join(m.queue, ",") != "second,first,third!" {
		t.Fatalf("Enter should move the message to the head and cancel the turn: %q cancelled=%v", m.queue, cancelled)
	}
}

func TestQueuePaneHintsFitNarrowScreens(t *testing.T) {
	m := busyQueueModel("first")
	m.queueSel = 0
	for _, w := range []int{40, 80, 120} {
		m.width = w
		for i, l := range strings.Split(strings.TrimRight(m.queueView(), "\n"), "\n") {
			if got := lipgloss.Width(l); got > w {
				t.Errorf("width %d row %d is %d wide: %q", w, i, got, l)
			}
		}
	}
	m.width = 40
	if v := m.queueView(); !strings.Contains(v, "Enter send now") {
		t.Fatalf("the most important hint survives at 40 columns:\n%s", v)
	}
}
