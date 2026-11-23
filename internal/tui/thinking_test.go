package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// thinkingModel is a model with thinking shown and a clock the test moves.
func thinkingModel(w, h int) (*model, *time.Time) {
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m := compactCmdModel()
	m.now = func() time.Time { return clock }
	m.showThinking = true
	m.busy = true
	m.Update(mkWinSize(w, h))
	return m, &clock
}

// While reasoning streams, its block shows only the last rows, the line
// still arriving included, with no "Thinking…" header: the status row's
// spinner is the one live label. The live area stays empty.
func TestThinkingShowsLiveTail(t *testing.T) {
	m, _ := thinkingModel(80, 24)
	m.Update(thinkMsg("first I read"))
	b := m.liveThinking()
	if b == nil {
		t.Fatal("no live thinking block for a partial line")
	}
	if got := ansi.Strip(b.render(80)); got != "  first I read" {
		t.Fatalf("partial line = %q", got)
	}
	for i := range 8 {
		m.Update(thinkMsg(fmt.Sprintf(" step %d\nthen", i)))
	}
	got := ansi.Strip(m.blocks[len(m.blocks)-1].render(80))
	want := "  then step 6\n  then step 7\n  then"
	if got != want {
		t.Fatalf("live tail = %q, want %q", got, want)
	}
	if m.thinkViewCapped() != "" || m.liveAreaRows() != 0 {
		t.Fatal("the live area repeats the thinking line")
	}
	m.layout()
	if h := lipgloss.Height(m.View()); h > m.height {
		t.Fatalf("frame %d rows on a %d-row terminal", h, m.height)
	}
}

// When the response starts the run folds to how long it took; toggling it,
// by click or key, opens the whole text.
func TestThinkingFoldsToDuration(t *testing.T) {
	m, clock := thinkingModel(80, 24)
	m.Update(thinkMsg("look at the cache\nthen the config\n"))
	*clock = clock.Add(4200 * time.Millisecond)
	m.Update(thinkMsg("decide"))
	m.Update(textMsg("The cache is stale.\n"))

	var b *block
	for i := range m.blocks {
		if m.blocks[i].kind == blockThinking {
			b = &m.blocks[i]
		}
	}
	if b == nil || b.thought.live {
		t.Fatalf("thinking block still live: %+v", b)
	}
	if got := ansi.Strip(b.render(80)); got != "◉ Thought for 4s" {
		t.Fatalf("folded = %q", got)
	}
	if b.text != "look at the cache\nthen the config\ndecide" {
		t.Fatalf("text = %q", b.text)
	}

	// A click on its header opens it.
	m.busy = false
	m.layout()
	m.View()
	y := m.viewportTop + b.y0 - m.vp.YOffset
	m.clickAt(2, y)
	if !b.expanded {
		t.Fatal("click did not open the thinking block")
	}
	if got := ansi.Strip(b.render(80)); !strings.Contains(got, "then the config") || !strings.HasPrefix(got, "◉ Thought for 4s") {
		t.Fatalf("expanded = %q", got)
	}
}

// A run that ends before any whole line, and one with nothing in it, leave
// no empty block behind.
func TestThinkingEdgeRuns(t *testing.T) {
	m, clock := thinkingModel(80, 24)
	m.Update(thinkMsg("only a fragment"))
	*clock = clock.Add(2 * time.Second)
	m.Update(textMsg("answer\n"))
	if b := m.blocks[0]; b.kind != blockThinking || b.text != "only a fragment" {
		t.Fatalf("fragment run = %+v", b)
	}

	m, _ = thinkingModel(80, 24)
	m.Update(thinkMsg("   "))
	m.Update(textMsg("answer\n"))
	for _, b := range m.blocks {
		if b.kind == blockThinking {
			t.Fatalf("blank run left a block: %+v", b)
		}
	}
}

// A turn's thinking runs, one before each tool call, fold into one row
// that sums their time; runs adding up to under a second leave no row.
func TestThinkingMergesPerTurn(t *testing.T) {
	m, clock := thinkingModel(100, 30)
	m.appendRaw(blockUser, "run the four steps")
	run := func(reason string, took time.Duration, id string) {
		m.Update(thinkMsg(reason))
		*clock = clock.Add(took)
		m.Update(toolStartMsg{id: id, name: "bash", args: `{"command":"echo ` + id + `"}`})
		m.Update(toolEndMsg{id: id, name: "bash", result: id})
	}
	run("first step\n", 300*time.Millisecond, "c1")
	if n := countThinking(m); n != 0 {
		t.Fatalf("a 0.3s thought left %d rows", n)
	}
	run("second step\n", 400*time.Millisecond, "c2")
	run("a closer look\nat the third\n", 4*time.Second, "c3")
	run("fourth step\n", 200*time.Millisecond, "c4")
	m.Update(thinkMsg("all done\n"))
	*clock = clock.Add(100 * time.Millisecond)
	m.Update(textMsg("All four steps ran.\n"))

	var rows []*block
	for i := range m.blocks {
		if m.blocks[i].kind == blockThinking {
			rows = append(rows, &m.blocks[i])
		}
	}
	if len(rows) != 1 {
		t.Fatalf("%d thinking rows for one turn, want 1", len(rows))
	}
	b := rows[0]
	if got := ansi.Strip(b.render(100)); got != "◉ Thought for 5s" {
		t.Fatalf("turn row = %q", got)
	}
	want := "first step\nsecond step\na closer look\nat the third\nfourth step\nall done"
	if b.text != want {
		t.Fatalf("turn row text = %q, want every run's text %q", b.text, want)
	}
	// The row sits where the turn's thinking first ran past a second: above
	// the third call, which follows the first two.
	var order []string
	for _, x := range m.blocks {
		switch x.kind {
		case blockThinking:
			order = append(order, "thought")
		case blockToolRun:
			order = append(order, x.toolID)
		}
	}
	if got := strings.Join(order, " "); got != "c1 c2 thought c3 c4" {
		t.Fatalf("order = %q", got)
	}

	// The next turn starts a row of its own, and a short one shows nothing.
	m.appendRaw(blockUser, "again")
	m.Update(thinkMsg("quick\n"))
	*clock = clock.Add(200 * time.Millisecond)
	m.Update(textMsg("ok\n"))
	if n := countThinking(m); n != 1 {
		t.Fatalf("a short second turn changed the rows: %d", n)
	}
	m.appendRaw(blockUser, "and once more")
	m.Update(thinkMsg("slow\n"))
	*clock = clock.Add(2 * time.Second)
	m.Update(textMsg("ok\n"))
	if n := countThinking(m); n != 2 {
		t.Fatalf("a long third turn should add its own row, have %d", n)
	}
}

func countThinking(m *model) int {
	n := 0
	for _, b := range m.blocks {
		if b.kind == blockThinking {
			n++
		}
	}
	return n
}

// Ctrl+O says what it did in the footer for a moment; the transcript gets
// no "thinking tokens: off/on" rows.
func TestCtrlOToggleIsTransient(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m, _ := thinkingModel(80, 24)
	m.busy = false
	before := len(m.blocks)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if m.showThinking {
		t.Fatal("Ctrl+O did not hide thinking")
	}
	if len(m.blocks) != before {
		t.Fatalf("Ctrl+O added %d transcript rows", len(m.blocks)-before)
	}
	if !strings.Contains(m.transientNotice, "Thinking tokens hidden") || cmd == nil {
		t.Fatalf("footer notice = %q, cmd %v", m.transientNotice, cmd)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !m.showThinking || len(m.blocks) != before || !strings.Contains(m.transientNotice, "Thinking tokens shown") {
		t.Fatalf("second Ctrl+O: shown %v, rows %d, notice %q", m.showThinking, len(m.blocks)-before, m.transientNotice)
	}
}

// Reopening a session draws one thinking row per turn, however many replies
// the turn's reasoning came in.
func TestSeededThinkingMergesPerTurn(t *testing.T) {
	m, _ := thinkingModel(80, 24)
	m.busy = false
	m.blocks = nil
	var call ai.ToolCall
	call.ID, call.Type = "t1", "function"
	call.Function.Name, call.Function.Arguments = "bash", `{"command":"ls"}`
	m.seedTranscript([]ai.Message{
		{Role: "user", Content: "go", Authored: true},
		{Role: "assistant", Reasoning: "plan it", ToolCalls: []ai.ToolCall{call}},
		{Role: "tool", ToolCallID: "t1", Name: "bash", Content: "a.go"},
		{Role: "assistant", Reasoning: "read it", Content: "done"},
		{Role: "user", Content: "next", Authored: true},
		{Role: "assistant", Reasoning: "fresh turn", Content: "ok"},
	}, 0)
	var texts []string
	for _, b := range m.blocks {
		if b.kind == blockThinking {
			texts = append(texts, b.text)
		}
	}
	if got := strings.Join(texts, "|"); got != "plan it\nread it|fresh turn" {
		t.Fatalf("seeded thinking rows = %q", got)
	}
}

func TestThinkingHiddenWhenOff(t *testing.T) {
	m, _ := thinkingModel(80, 24)
	m.showThinking = false
	m.Update(thinkMsg("secret\nreasoning"))
	m.Update(textMsg("answer\n"))
	for _, b := range m.blocks {
		if b.kind == blockThinking {
			t.Fatal("thinking shown while off")
		}
	}
}

// TestGoldenThinking pins the three forms in one frame: a folded run, an
// opened one and one still streaming.
func TestGoldenThinking(t *testing.T) {
	t.Chdir(t.TempDir())
	defer SetLightTheme(false)
	for _, mode := range goldenModes {
		for _, size := range goldenSizes {
			name := fmt.Sprintf("thinking_%s_%dx%d", mode.name, size[0], size[1])
			t.Run(name, func(t *testing.T) {
				restore := useProfile(t, mode.profile, mode.dark)
				defer restore()
				SetLightTheme(!mode.dark)
				if mode.mono {
					setMonoMode(true, true)
				}
				setASCIIGlyphs(mode.ascii)
				m, clock := thinkingModel(size[0], size[1])
				m.applyAppearance()
				turn := func(q, reason, reply string, took time.Duration) {
					m.appendRaw(blockUser, q)
					m.Update(thinkMsg(reason))
					*clock = clock.Add(took)
					m.Update(textMsg(reply))
					m.flushCurrent()
				}
				turn("Why is the build slow?", "The build runs every test twice.\nOnce for coverage, once without.\n", "Coverage runs **twice**.", 3*time.Second)
				turn("Can we skip one?", "Dropping the plain run keeps coverage.\nCI reads only the coverage report.\n", "Yes: drop the plain run.", 12*time.Second)
				for i := range m.blocks {
					if m.blocks[i].kind == blockThinking && strings.HasPrefix(m.blocks[i].text, "Dropping") {
						m.blocks[i].toggle()
					}
				}
				m.appendRaw(blockUser, "And the lint step?")
				m.Update(thinkMsg("Lint runs on every package.\nIt could run on changed ones.\nThat needs the diff base.\nWhich CI already has"))
				m.refreshVP()
				m.layout()
				v := m.View()
				for r, l := range strings.Split(v, "\n") {
					if w := lipgloss.Width(l); w > size[0] {
						t.Errorf("row %d is %d cells wide on a %d-column screen", r, w, size[0])
					}
				}
				checkGolden(t, name+".ansi", v)
				checkGolden(t, name+".txt", stripView(v))
			})
		}
	}
}

// A finished thinking block does not wear the running glyph, and in ASCII
// the done, reply, OK and token glyphs all differ.
func TestDoneGlyphsAreDistinct(t *testing.T) {
	for _, set := range []glyphSet{unicodeGlyphs, asciiGlyphs} {
		if set.Thought == set.ToolRun {
			t.Errorf("finished thinking uses the running glyph %q", set.Thought)
		}
		if set.ToolDone == set.Assistant {
			t.Errorf("done tool and reply share %q", set.ToolDone)
		}
		if set.OK == set.Tokens {
			t.Errorf("OK and tokens share %q", set.OK)
		}
	}
	for from, to := range map[string]string{"◆": asciiGlyphs.ToolDone, "◉": asciiGlyphs.Thought, "⇣": asciiGlyphs.Tokens, "●": asciiGlyphs.Assistant} {
		setASCIIGlyphs(true)
		got := chromeGlyphs(from)
		setASCIIGlyphs(false)
		if got != to {
			t.Errorf("ASCII chrome maps %q to %q, want %q as in the glyph set", from, got, to)
		}
	}
}
