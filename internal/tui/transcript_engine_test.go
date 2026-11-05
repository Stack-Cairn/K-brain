package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// referenceRows lays the transcript out the way refreshVP did before the
// line index: every block rendered and joined into one string, glued blocks
// on adjacent rows and the rest a blank row apart.
func referenceRows(m *model) []string {
	width := max(m.width, minRenderWidth)
	var b strings.Builder
	wrote := false
	for i := range m.blocks {
		if m.blocks[i].kind == blockBanner && len(m.blocks) > 1 {
			continue
		}
		if wrote {
			if gluedToPrev(m.blocks, i) {
				b.WriteString("\n")
			} else {
				b.WriteString("\n\n")
			}
		}
		blk := m.blocks[i]
		blk.stale = true
		b.WriteString(blk.renderAtMode(width, m.ancientMode))
		wrote = true
	}
	if !wrote {
		return nil
	}
	return strings.Split(b.String(), "\n")
}

// checkIndex compares every window position against the reference layout,
// and each block's y0/y1 against where its first and last rows landed.
func checkIndex(t *testing.T, m *model, step string) {
	t.Helper()
	want := referenceRows(m)
	if got := m.transcriptRows(); got != len(want) {
		t.Fatalf("%s: %d transcript rows, want %d", step, got, len(want))
	}
	got := m.tx.ix.rows(0, len(want))
	for r := range want {
		if got[r] != want[r] {
			t.Fatalf("%s: row %d = %q, want %q", step, r, ansi.Strip(got[r]), ansi.Strip(want[r]))
		}
	}
	for i := range m.blocks {
		b := &m.blocks[i]
		if b.y0 < 0 {
			continue
		}
		if first := strings.Split(b.rendered, "\n")[0]; want[b.y0] != first {
			t.Fatalf("%s: block %d y0=%d holds %q, want its first row %q", step, i, b.y0, ansi.Strip(want[b.y0]), ansi.Strip(first))
		}
		if b.y1-b.y0+1 != b.height() {
			t.Fatalf("%s: block %d spans y0=%d..y1=%d but is %d rows", step, i, b.y0, b.y1, b.height())
		}
	}
}

func mixedTranscript(m *model) {
	for i := range 6 {
		m.appendRaw(blockUser, fmt.Sprintf("question %d: why does the build take so long on CI?", i))
		m.appendAssistantBlock(fmt.Sprintf("Answer %d with **bold** text and a list:\n\n- one\n- two\n\n```go\nfunc f%d() {}\n```", i, i))
		m.Update(toolStartMsg{id: fmt.Sprintf("t%d", i), name: "bash", args: fmt.Sprintf(`{"command":"go test ./pkg%d"}`, i)})
		m.Update(toolEndMsg{id: fmt.Sprintf("t%d", i), name: "bash", result: "line one\nline two\nline three\nline four\nline five"})
		m.append(dimStyle.Render(fmt.Sprintf("◎ notice %d", i)))
	}
}

func TestTranscriptIndexMatchesFullLayout(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))
	mixedTranscript(m)
	checkIndex(t, m, "built")

	// Every window position draws the same rows the full string did.
	want := referenceRows(m)
	for off := 0; off+m.vp.Height <= len(want); off++ {
		m.vp.SetYOffset(off)
		got := m.vp.visibleLines()
		if !slices.Equal(got, want[off:off+m.vp.Height]) {
			t.Fatalf("window at %d differs from the full layout", off)
		}
	}

	// A change in the middle: a result expands.
	for i := range m.blocks {
		if m.blocks[i].kind == blockTool {
			m.blocks[i].toggle()
			break
		}
	}
	m.refreshVP()
	checkIndex(t, m, "expanded")

	// A text change marked stale.
	m.blocks[3].text += "\n\nmore text"
	m.blocks[3].invalidate()
	m.refreshVP()
	checkIndex(t, m, "edited")

	// Inserting and deleting in the middle shift everything below.
	m.blocks = slices.Insert(m.blocks, 5, block{kind: blockText, text: "inserted row"})
	m.refreshVP()
	checkIndex(t, m, "inserted")
	m.blocks = slices.Delete(m.blocks, 2, 4)
	m.refreshVP()
	checkIndex(t, m, "deleted")

	// Truncating, then a resize and an ancient-mode switch.
	m.blocks = m.blocks[:10]
	m.refreshVP()
	checkIndex(t, m, "truncated")
	m.Update(mkWinSize(41, 24))
	checkIndex(t, m, "resized")
	m.ancientMode = true
	for i := range m.blocks {
		m.blocks[i].invalidate()
	}
	m.refreshVP()
	checkIndex(t, m, "ancient")
}

// countingKind renders a fixed row and counts how often it is drawn.
const countingKind blockKind = 900

var countingRenders int

func init() {
	registerBlockKind(countingKind, blockKindSpec{render: func(b *block, width int) string {
		countingRenders++
		return wrap(b.text, width)
	}})
}

// An append draws only the new block; earlier blocks come from their cache
// and a frame draws nothing at all. A resize redraws each block once.
func TestAppendRendersOnlyTheNewBlock(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))
	for i := range 50 {
		m.appendRaw(countingKind, fmt.Sprintf("row %d", i))
	}
	countingRenders = 0
	m.appendRaw(countingKind, "one more")
	if countingRenders != 1 {
		t.Fatalf("append rendered %d blocks, want 1", countingRenders)
	}
	countingRenders = 0
	m.layout()
	m.View()
	if countingRenders != 0 {
		t.Fatalf("a frame rendered %d blocks, want none", countingRenders)
	}
	m.Update(mkWinSize(60, 24))
	if countingRenders != 51 {
		t.Fatalf("resize rendered %d blocks, want each of the 51 once", countingRenders)
	}
	if !strings.Contains(ansi.Strip(m.vp.View()), "one more") {
		t.Fatalf("following window lost the last row:\n%s", ansi.Strip(m.vp.View()))
	}
}

// topRow is the first transcript row on screen, as plain text.
func topRow(m *model) string { return m.contentLine(m.vp.YOffset) }

// A scrolled-back window stays on the same content when blocks change above
// it: an insert, a result expanding, a block going away.
func TestScrollAnchorHoldsWhenRowsChangeAbove(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))
	mixedTranscript(m)
	target := len(m.blocks) / 2
	m.follow = false
	m.vp.SetYOffset(m.blocks[target].y0)
	want := topRow(m)
	uid := m.blocks[target].uid
	at := func() int {
		for i := range m.blocks {
			if m.blocks[i].uid == uid {
				return i
			}
		}
		t.Fatal("anchor block gone")
		return -1
	}

	m.blocks = slices.Insert(m.blocks, 1, block{kind: blockText, text: "a\nb\nc"})
	m.refreshVP()
	if got := topRow(m); got != want || m.vp.YOffset != m.blocks[at()].y0 {
		t.Fatalf("insert above moved the window: top %q (want %q)", got, want)
	}

	for i := range m.blocks[:at()] {
		if m.blocks[i].kind == blockTool {
			m.blocks[i].toggle()
		}
	}
	m.refreshVP()
	if got := topRow(m); got != want {
		t.Fatalf("expanding results above moved the window: top %q (want %q)", got, want)
	}

	m.blocks = slices.Delete(m.blocks, 0, 3)
	m.refreshVP()
	if got := topRow(m); got != want {
		t.Fatalf("deleting blocks above moved the window: top %q (want %q)", got, want)
	}
	if m.follow {
		t.Fatal("a scrolled-back window must not start following")
	}

	// Appending below changes nothing on screen.
	m.append("new tail row")
	if got := topRow(m); got != want {
		t.Fatalf("append moved the window: top %q (want %q)", got, want)
	}
}

// A resize reflows every block, but a scrolled-back window keeps the same
// block at its top, and a following window stays at the bottom.
func TestScrollAnchorSurvivesResize(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(120, 30))
	for i := range 30 {
		m.appendAssistantBlock(fmt.Sprintf("Reply %02d: %s", i, strings.Repeat("words that wrap at narrow widths ", 6)))
	}
	target := 12
	m.follow = false
	m.vp.SetYOffset(m.blocks[target].y0)
	uid := m.blocks[target].uid

	for _, w := range []int{80, 40, 57, 120} {
		m.Update(mkWinSize(w, 30))
		m.layout()
		if m.blocks[target].uid != uid {
			t.Fatal("blocks reordered")
		}
		if m.vp.YOffset != m.blocks[target].y0 {
			t.Fatalf("at %d cols the window starts on row %d, want block %d's first row %d", w, m.vp.YOffset, target, m.blocks[target].y0)
		}
		if got := topRow(m); !strings.Contains(got, fmt.Sprintf("Reply %02d", target)) {
			t.Fatalf("at %d cols the top row is %q", w, got)
		}
	}

	// Mid-block: the same fraction of the block stays above the window.
	m.vp.SetYOffset(m.blocks[target].y0 + m.blocks[target].height()/2)
	m.Update(mkWinSize(40, 30))
	m.layout()
	b := m.blocks[target]
	if off := m.vp.YOffset - b.y0; off < 1 || off >= b.height() {
		t.Fatalf("mid-block anchor fell outside the block: offset %d of %d rows", off, b.height())
	}

	m.vp.GotoBottom()
	m.follow = true
	m.Update(mkWinSize(90, 30))
	m.layout()
	if !m.vp.AtBottom() {
		t.Fatal("a following window must stay at the bottom across a resize")
	}
}

// Selection and click toggling read rows through the index.
func TestContentLineAndClickUseIndex(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))
	mixedTranscript(m)
	for i := range m.blocks {
		b := &m.blocks[i]
		for r := b.y0; r <= b.y1; r++ {
			want := strings.TrimRight(ansi.Strip(strings.Split(b.rendered, "\n")[r-b.y0]), " \t")
			if got := m.contentLine(r); got != want {
				t.Fatalf("contentLine(%d) = %q, want %q", r, got, want)
			}
		}
		if i > 0 && b.y0 > m.blocks[i-1].y1+1 {
			if got := m.blockAtRow(b.y0 - 1); got != -1 {
				t.Fatalf("separator row %d maps to block %d", b.y0-1, got)
			}
		}
	}
}

func BenchmarkTranscriptFrame1000Turns(b *testing.B) {
	m := compactCmdModel()
	m.Update(mkWinSize(120, 40))
	m.seedTranscript(benchTranscript(1000), 1)
	m.follow = false
	m.vp.SetYOffset(m.transcriptRows() / 2)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.viewportView()
	}
}
