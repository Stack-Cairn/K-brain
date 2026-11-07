package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// composerModel is a laid-out model whose composer holds val, with a clock
// tests move by hand so clicks only count as double clicks on purpose.
func composerModel(t *testing.T, val string) (*model, *time.Time) {
	t.Helper()
	m := selTestModel()
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m.now = func() time.Time { return clock }
	m.setComposer(val, len([]rune(val)))
	tm, _ := m.Update(mkWinSize(80, 30))
	m = tm.(*model)
	m.View()
	if m.inputTop < 0 {
		t.Fatal("the composer must be on screen")
	}
	return m, &clock
}

func mouse(m *model, action tea.MouseAction, x, y int) tea.Cmd {
	_, cmd := m.Update(tea.MouseMsg{Action: action, Button: tea.MouseButtonLeft, X: x, Y: y})
	return cmd
}

// clickInput clicks the composer at content cell col of input row row
// (col 0 is the first cell after the prompt).
func clickInput(m *model, row, col int) {
	m.View()
	x, y := m.inputLeft+ansi.StringWidth(m.input.Prompt)+col, m.inputTop+row
	mouse(m, tea.MouseActionPress, x, y)
	mouse(m, tea.MouseActionRelease, x, y)
}

// dragInput drags over the composer between two content cells and returns
// the release command.
func dragInput(m *model, row0, col0, row1, col1 int) tea.Cmd {
	m.View()
	pw := m.inputLeft + ansi.StringWidth(m.input.Prompt)
	mouse(m, tea.MouseActionPress, pw+col0, m.inputTop+row0)
	mouse(m, tea.MouseActionMotion, pw+col1, m.inputTop+row1)
	return mouse(m, tea.MouseActionRelease, pw+col1, m.inputTop+row1)
}

func typeKey(m *model, msg tea.KeyMsg) {
	m.Update(msg)
}

func cursorAt(m *model) int { return m.cursorOffset(m.input.Value()) }

// The layout the composer maps clicks through must wrap exactly as the
// textarea draws, wide characters and runs of spaces included.
func TestTextareaWrapMatchesTextarea(t *testing.T) {
	cases := []string{
		"",
		"hello world",
		strings.Repeat("word ", 40),
		strings.Repeat("你好世界", 30),
		"mixed 中文 and English 文字 wrapping " + strings.Repeat("长", 50) + " tail",
		"averyveryveryveryveryveryverylongwordthatcannotwrapatallbecauseithasnospaces",
		"spaces    in   a     row    " + strings.Repeat("x ", 30),
	}
	for _, width := range []int{7, 20, 33, 78} {
		for _, val := range cases {
			ta := textarea.New()
			ta.Prompt = ""
			ta.ShowLineNumbers = false
			ta.SetWidth(width)
			ta.SetHeight(200)
			ta.MaxHeight = 0
			ta.SetValue(val)
			ta.Blur()
			want := strings.Split(ansi.Strip(ta.View()), "\n")
			got := textareaWrap([]rune(val), width)
			if len(got) > len(want) {
				t.Fatalf("width %d %q: %d rows, the textarea shows %d", width, val, len(got), len(want))
			}
			for i, row := range got {
				if g, w := strings.TrimRight(string(row), " "), strings.TrimRight(want[i], " "); g != w {
					t.Fatalf("width %d %q row %d:\n got %q\nwant %q", width, val, i, g, w)
				}
			}
			n := 0
			for _, row := range got {
				n += len(row)
			}
			if n != len([]rune(val))+1 {
				t.Fatalf("width %d %q: rows hold %d runes, want the line's %d plus one", width, val, n, len([]rune(val))+1)
			}
		}
	}
}

func TestComposerClickMovesCursor(t *testing.T) {
	m, _ := composerModel(t, "hello world")
	clickInput(m, 0, 6)
	if got := cursorAt(m); got != 6 {
		t.Fatalf("a click on 'w' should put the cursor at 6, got %d", got)
	}
	typeKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("big ")})
	if got := m.input.Value(); got != "hello big world" {
		t.Fatalf("typing after the click should land there, got %q", got)
	}
	clickInput(m, 0, 60)
	if got := cursorAt(m); got != len("hello big world") {
		t.Fatalf("a click past the end should go to the end, got %d", got)
	}
	clickInput(m, 0, -2) // on the prompt
	if got := cursorAt(m); got != 0 {
		t.Fatalf("a click on the prompt should go to the start, got %d", got)
	}
}

func TestComposerClickWideChars(t *testing.T) {
	m, clock := composerModel(t, "你好世界abc")
	// 你 0-1, 好 2-3, 世 4-5, 界 6-7, a 8.
	for _, c := range []struct{ col, want int }{{0, 0}, {3, 1}, {5, 2}, {6, 3}, {8, 4}, {10, 6}} {
		*clock = clock.Add(time.Second)
		clickInput(m, 0, c.col)
		if got := cursorAt(m); got != c.want {
			t.Errorf("click at cell %d: cursor %d, want %d", c.col, got, c.want)
		}
	}
}

func TestComposerClickWrappedRows(t *testing.T) {
	for _, val := range []string{strings.Repeat("word ", 40), strings.Repeat("你好世界", 30)} {
		m, _ := composerModel(t, val)
		if len(m.inputLines) < 2 {
			t.Fatalf("%q should wrap, got %d rows", val, len(m.inputLines))
		}
		clickInput(m, 1, 4)
		li := m.input.LineInfo()
		wantCol := 4
		if strings.HasPrefix(val, "你") {
			wantCol = 2 // cell 4 is the third wide character
		}
		if m.input.Line() != 0 || li.RowOffset != 1 || li.ColumnOffset != wantCol {
			t.Fatalf("%q: click on row 1 cell 4 gave line %d row %d col %d, want 0/1/%d", val[:6], m.input.Line(), li.RowOffset, li.ColumnOffset, wantCol)
		}
	}
}

func TestComposerClickSecondLine(t *testing.T) {
	m, _ := composerModel(t, "line one\nline two")
	clickInput(m, 1, 5)
	if got := cursorAt(m); got != len("line one\n")+5 {
		t.Fatalf("click on the second line: cursor %d, want %d", got, len("line one\n")+5)
	}
}

func TestComposerDragSelectsAndCopies(t *testing.T) {
	var f fakeClipboard
	useFakeClipboard(t, &f)
	m, _ := composerModel(t, "hello brave world")
	cmd := dragInput(m, 0, 6, 0, 11)
	if got := m.composerSelText(); got != "brave" {
		t.Fatalf("drag selected %q, want brave", got)
	}
	if !strings.Contains(m.View(), "\x1b[7m") {
		t.Fatal("the selection should be highlighted")
	}
	if cmd == nil {
		t.Fatal("a finished drag copies")
	}
	cmd()
	if len(f.native) != 1 || f.native[0] != "brave" {
		t.Fatalf("copied %q, want [brave]", f.native)
	}
}

func TestComposerSelectionBackspaceDeletes(t *testing.T) {
	for _, k := range []tea.KeyType{tea.KeyBackspace, tea.KeyDelete} {
		m, _ := composerModel(t, "hello brave world")
		dragInput(m, 0, 11, 0, 6) // backwards
		typeKey(m, tea.KeyMsg{Type: k})
		if got := m.input.Value(); got != "hello  world" {
			t.Fatalf("%v with a selection should delete it, got %q", k, got)
		}
		if got := cursorAt(m); got != 6 {
			t.Fatalf("the cursor should stay where the selection was, got %d", got)
		}
		if _, _, ok := m.composerSelection(); ok {
			t.Fatal("the selection should be gone")
		}
		typeKey(m, tea.KeyMsg{Type: tea.KeyCtrlZ})
		if got := m.input.Value(); got != "hello brave world" {
			t.Fatalf("undo should bring the selection back, got %q", got)
		}
	}
}

func TestComposerSelectionTypingReplaces(t *testing.T) {
	m, _ := composerModel(t, "hello brave world")
	typeKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")}) // an open typing run
	dragInput(m, 0, 6, 0, 11)
	for _, r := range "big" {
		typeKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if got := m.input.Value(); got != "hello big world!" {
		t.Fatalf("typing should replace the selection, got %q", got)
	}
	typeKey(m, tea.KeyMsg{Type: tea.KeyCtrlZ})
	if got := m.input.Value(); got != "hello brave world!" {
		t.Fatalf("one undo should restore the replaced text and keep the earlier typing, got %q", got)
	}
}

func TestComposerSelectionPasteReplaces(t *testing.T) {
	m, _ := composerModel(t, "hello brave world")
	dragInput(m, 0, 6, 0, 11)
	typeKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("new"), Paste: true})
	if got := m.input.Value(); got != "hello new world" {
		t.Fatalf("a paste should replace the selection, got %q", got)
	}
}

func TestComposerSelectionCtrlXCuts(t *testing.T) {
	var f fakeClipboard
	useFakeClipboard(t, &f)
	m, _ := composerModel(t, "hello brave world")
	dragInput(m, 0, 6, 0, 12)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	if got := m.input.Value(); got != "hello world" {
		t.Fatalf("Ctrl+X should cut the selection, got %q", got)
	}
	if cmd == nil {
		t.Fatal("Ctrl+X should copy what it cut")
	}
	cmd()
	if len(f.native) != 1 || f.native[0] != "brave " {
		t.Fatalf("cut copied %q, want [brave ]", f.native)
	}
	m.placeCursor(0)
	typeKey(m, tea.KeyMsg{Type: tea.KeyCtrlY})
	if got := m.input.Value(); got != "brave hello world" {
		t.Fatalf("Ctrl+Y should paste the cut text back, got %q", got)
	}
}

func TestComposerSelectAll(t *testing.T) {
	m, _ := composerModel(t, "line one\n全部 选中")
	typeKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a"), Alt: true})
	if got := m.composerSelText(); got != "line one\n全部 选中" {
		t.Fatalf("Alt+A selected %q", got)
	}
	if v := m.View(); strings.Count(v, "\x1b[7m") < 2 {
		t.Fatalf("both rows should be highlighted:\n%q", v)
	}
	typeKey(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if got := m.input.Value(); got != "" {
		t.Fatalf("Backspace after Alt+A should empty the composer, got %q", got)
	}
}

func TestComposerSelectAllKeepsCtrlA(t *testing.T) {
	m, _ := composerModel(t, "hello world")
	typeKey(m, tea.KeyMsg{Type: tea.KeyCtrlA})
	if got := cursorAt(m); got != 0 {
		t.Fatalf("Ctrl+A should still go to the line start, got %d", got)
	}
	if _, _, ok := m.composerSelection(); ok {
		t.Fatal("Ctrl+A must not select")
	}
}

func TestComposerDoubleAndTripleClick(t *testing.T) {
	m, clock := composerModel(t, "hello brave world")
	clickInput(m, 0, 7)
	*clock = clock.Add(100 * time.Millisecond)
	clickInput(m, 0, 7)
	if got := m.composerSelText(); got != "brave" {
		t.Fatalf("a double click should select the word, got %q", got)
	}
	*clock = clock.Add(100 * time.Millisecond)
	clickInput(m, 0, 7)
	if got := m.composerSelText(); got != "hello brave world" {
		t.Fatalf("a triple click should select everything, got %q", got)
	}
	*clock = clock.Add(time.Second)
	clickInput(m, 0, 2)
	if _, _, ok := m.composerSelection(); ok {
		t.Fatal("a later single click should drop the selection")
	}
	if got := cursorAt(m); got != 2 {
		t.Fatalf("and place the cursor, got %d", got)
	}
}

func TestComposerDragWideChars(t *testing.T) {
	m, _ := composerModel(t, "你好世界")
	// From the right half of 好 to the left half of 世: both are in.
	dragInput(m, 0, 3, 0, 5)
	if got := m.composerSelText(); got != "好世" {
		t.Fatalf("drag over wide characters selected %q, want 好世", got)
	}
	typeKey(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if got := m.input.Value(); got != "你界" {
		t.Fatalf("got %q, want 你界", got)
	}
}

func TestComposerDragAcrossWrappedRows(t *testing.T) {
	val := strings.Repeat("alpha beta ", 12)
	m, _ := composerModel(t, val)
	if len(m.inputLines) < 2 {
		t.Fatal("the draft should wrap")
	}
	row1 := strings.TrimSpace(strings.TrimPrefix(m.inputLines[1], strings.TrimSpace(m.input.Prompt)))
	dragInput(m, 1, 0, 1, 200)
	if got := strings.TrimSpace(m.composerSelText()); got != row1 {
		t.Fatalf("dragging over row 1 selected %q, want %q", got, row1)
	}
	dragInput(m, 0, 0, len(m.inputLines)-1, 200)
	if got := m.composerSelText(); got != val[:len(val)-1] && got != val {
		t.Fatalf("dragging over every row selected %q", got)
	}
}

func TestComposerSelectionDroppedByOtherKeys(t *testing.T) {
	m, _ := composerModel(t, "hello brave world")
	dragInput(m, 0, 6, 0, 11)
	typeKey(m, tea.KeyMsg{Type: tea.KeyLeft})
	if _, _, ok := m.composerSelection(); ok {
		t.Fatal("an arrow key should drop the selection")
	}
	if got := cursorAt(m); got != 6 {
		t.Fatalf("← should leave the cursor at the selection's start, got %d", got)
	}
	typeKey(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if got := m.input.Value(); got != "hellobrave world" {
		t.Fatalf("Backspace without a selection deletes one character, got %q", got)
	}
	typeKey(m, tea.KeyMsg{Type: tea.KeyEnd})
	if got := cursorAt(m); got != len("hellobrave world") {
		t.Fatalf("other keys act as usual once the selection is gone, cursor %d", got)
	}

	dragInput(m, 0, 6, 0, 10)
	typeKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if _, _, ok := m.composerSelection(); ok {
		t.Fatal("Esc should drop the selection")
	}
	if m.escClr {
		t.Fatal("Esc that drops a selection must not arm clearing the draft")
	}
	if got := m.input.Value(); got != "hellobrave world" {
		t.Fatalf("Esc should leave the draft alone, got %q", got)
	}
}

func TestComposerSelectionCtrlCCopies(t *testing.T) {
	var f fakeClipboard
	useFakeClipboard(t, &f)
	m, _ := composerModel(t, "hello brave world")
	typeKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a"), Alt: true})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("Ctrl+C with a selected draft should copy")
	}
	cmd()
	if len(f.native) != 1 || f.native[0] != "hello brave world" || m.quit1 {
		t.Fatalf("copied %q (quit armed %v)", f.native, m.quit1)
	}
}

// queueScreenModel is a laid-out busy model with queued messages.
func queueScreenModel(t *testing.T, queue ...string) (*model, context.Context) {
	t.Helper()
	m, _ := composerModel(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m.busy, m.cancel, m.queue, m.queueSel = true, cancel, queue, -1
	m.View()
	return m, ctx
}

// queueRowY finds the screen row that shows queued message text.
func queueRowY(t *testing.T, m *model, text string) int {
	t.Helper()
	for y, l := range strings.Split(ansi.Strip(m.View()), "\n") {
		if strings.Contains(l, text) && y > m.queueTop {
			return y
		}
	}
	t.Fatalf("%q is not on screen:\n%s", text, ansi.Strip(m.View()))
	return -1
}

func clickAt(m *model, x, y int) {
	mouse(m, tea.MouseActionPress, x, y)
	mouse(m, tea.MouseActionRelease, x, y)
}

func TestQueueClickSelectsThenEdits(t *testing.T) {
	m, ctx := queueScreenModel(t, "first", "second", "third")
	clickAt(m, 5, queueRowY(t, m, "second"))
	if m.queueSel != 1 {
		t.Fatalf("a click should select the second message, got %d", m.queueSel)
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Ctrl+E/click edit") {
		t.Fatalf("the selected queue should show how to edit:\n%s", v)
	}
	clickAt(m, 5, queueRowY(t, m, "second"))
	if got := m.input.Value(); got != "second" || m.editingQueued() == nil {
		t.Fatalf("a second click should edit it, composer %q", got)
	}
	typeKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" edited")})
	typeKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if strings.Join(m.queue, "|") != "first|second edited|third" {
		t.Fatalf("Enter should save it in place, queue %q", m.queue)
	}
	if ctx.Err() != nil {
		t.Fatal("editing a queued message must not interrupt the turn")
	}
}

func TestQueueClickMarkRemoves(t *testing.T) {
	m, ctx := queueScreenModel(t, "first", "second")
	y := queueRowY(t, m, "first")
	clickAt(m, 5, y)
	if !strings.HasSuffix(strings.TrimRight(ansi.Strip(strings.Split(m.View(), "\n")[y]), " "), glyphs.Fail) {
		t.Fatalf("the selected row should end in a remove mark:\n%s", ansi.Strip(m.View()))
	}
	clickAt(m, m.width-2, y)
	if strings.Join(m.queue, "|") != "second" || m.queueSel != 0 {
		t.Fatalf("a click on the mark should remove the message, queue %q sel %d", m.queue, m.queueSel)
	}
	if ctx.Err() != nil {
		t.Fatal("removing a queued message must not interrupt the turn")
	}
}

func TestQueueClickWithDraftKeepsDraft(t *testing.T) {
	m, _ := queueScreenModel(t, "first")
	m.setComposer("my draft", 8)
	m.View()
	clickAt(m, 5, queueRowY(t, m, "first"))
	if m.queueSel != -1 || m.input.Value() != "my draft" {
		t.Fatalf("a click with a draft must not take over the composer (sel %d, %q)", m.queueSel, m.input.Value())
	}
}

func TestQueueEscDeselectsWithoutInterrupt(t *testing.T) {
	m, ctx := queueScreenModel(t, "first", "second")
	typeKey(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.queueSel != 1 {
		t.Fatalf("↑ should select the newest, got %d", m.queueSel)
	}
	typeKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.queueSel != -1 {
		t.Fatal("Esc should deselect")
	}
	if ctx.Err() != nil {
		t.Fatal("Esc on a selected queued message must not interrupt the turn")
	}
	typeKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if ctx.Err() == nil {
		t.Fatal("the next Esc interrupts")
	}
}

func TestQueueKeyboardEditCancelAndRemove(t *testing.T) {
	m, ctx := queueScreenModel(t, "first", "second")
	typeKey(m, tea.KeyMsg{Type: tea.KeyUp})
	typeKey(m, tea.KeyMsg{Type: tea.KeyCtrlE})
	if m.input.Value() != "second" {
		t.Fatalf("Ctrl+E should edit, composer %q", m.input.Value())
	}
	typeKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!!")})
	typeKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if strings.Join(m.queue, "|") != "first|second" || m.input.Value() != "" {
		t.Fatalf("Esc should put the original back, queue %q composer %q", m.queue, m.input.Value())
	}
	typeKey(m, tea.KeyMsg{Type: tea.KeyUp})
	typeKey(m, tea.KeyMsg{Type: tea.KeyDelete})
	if strings.Join(m.queue, "|") != "first" {
		t.Fatalf("Del should remove the selected message, queue %q", m.queue)
	}
	if ctx.Err() != nil {
		t.Fatal("none of this interrupts the turn")
	}
}

func TestQueueAltEEditsLast(t *testing.T) {
	m, _ := queueScreenModel(t, "first", "second")
	m.setComposer("half typed", 10)
	typeKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e"), Alt: true})
	if m.input.Value() != "second" || m.editingQueued() == nil {
		t.Fatalf("Alt+E should edit the last queued message, composer %q", m.input.Value())
	}
	if c := m.composer(); len(c.stash) == 0 || c.stash[len(c.stash)-1] != "half typed" {
		t.Fatal("the draft should go to the stash")
	}
	typeKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if strings.Join(m.queue, "|") != "first|second" {
		t.Fatalf("Esc puts it back, queue %q", m.queue)
	}
}
