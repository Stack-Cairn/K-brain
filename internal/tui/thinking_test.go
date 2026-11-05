package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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

// While reasoning streams, its block shows the header and only the last
// rows, the line still arriving included; the live area stays empty.
func TestThinkingShowsLiveTail(t *testing.T) {
	m, _ := thinkingModel(80, 24)
	m.Update(thinkMsg("first I read"))
	b := m.liveThinking()
	if b == nil {
		t.Fatal("no live thinking block for a partial line")
	}
	if got := ansi.Strip(b.render(80)); got != "◌ Thinking…\n  first I read" {
		t.Fatalf("partial line = %q", got)
	}
	for i := range 8 {
		m.Update(thinkMsg(fmt.Sprintf(" step %d\nthen", i)))
	}
	got := ansi.Strip(m.blocks[len(m.blocks)-1].render(80))
	want := "◌ Thinking…\n  then step 6\n  then step 7\n  then"
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
	if got := ansi.Strip(b.render(80)); got != "◌ Thought for 4s · 3 lines" {
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
	if got := ansi.Strip(b.render(80)); !strings.Contains(got, "then the config") || !strings.HasPrefix(got, "◌ Thought for 4s") {
		t.Fatalf("expanded = %q", got)
	}
}

// A run that ends before any whole line, and one with nothing in it, leave
// no empty block behind.
func TestThinkingEdgeRuns(t *testing.T) {
	m, _ := thinkingModel(80, 24)
	m.Update(thinkMsg("only a fragment"))
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
