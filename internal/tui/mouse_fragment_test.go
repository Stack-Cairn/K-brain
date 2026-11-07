package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// chunkReader hands the input reader exactly the chunks given, one per
// read, the way a pty can deliver a burst in pieces.
type chunkReader struct {
	mu     sync.Mutex
	chunks []string
	done   chan struct{}
}

func (c *chunkReader) Read(p []byte) (int, error) {
	c.mu.Lock()
	if len(c.chunks) == 0 {
		c.mu.Unlock()
		<-c.done
		return 0, io.EOF
	}
	s := c.chunks[0]
	n := copy(p, s)
	if n < len(s) {
		c.chunks[0] = s[n:]
	} else {
		c.chunks = c.chunks[1:]
	}
	c.mu.Unlock()
	// Let the reader hand over what it has before the next piece.
	time.Sleep(2 * time.Millisecond)
	return n, nil
}

type msgRecorder struct {
	mu   *sync.Mutex
	msgs *[]tea.Msg
}

func (r msgRecorder) Init() tea.Cmd { return nil }
func (r msgRecorder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case tea.KeyMsg, tea.MouseMsg:
		r.mu.Lock()
		*r.msgs = append(*r.msgs, msg)
		r.mu.Unlock()
	}
	return r, nil
}
func (r msgRecorder) View() string { return "" }

// parsedInput is what bubbletea's own input reader makes of chunks, so the
// tests below feed the model what a real terminal session would.
func parsedInput(t *testing.T, chunks ...string) []tea.Msg {
	t.Helper()
	var mu sync.Mutex
	var msgs []tea.Msg
	cr := &chunkReader{chunks: chunks, done: make(chan struct{})}
	p := tea.NewProgram(msgRecorder{&mu, &msgs}, tea.WithInput(cr), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	go func() {
		for i := 0; i < 200; i++ {
			cr.mu.Lock()
			left := len(cr.chunks)
			cr.mu.Unlock()
			if left == 0 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		p.Quit()
		close(cr.done)
	}()
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]tea.Msg(nil), msgs...)
}

func describe(msgs []tea.Msg) string {
	var parts []string
	for _, msg := range msgs {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			parts = append(parts, fmt.Sprintf("key%q", msg.String()))
		case tea.MouseMsg:
			parts = append(parts, "mouse")
		}
	}
	return strings.Join(parts, " ")
}

// fragModel is a busy session with a long transcript scrolled to the end
// and the fragment filter on, as in a terminal.
func fragModel(t *testing.T) (*model, context.Context) {
	t.Helper()
	m := selTestModel()
	for i := range 80 {
		m.append(fmt.Sprintf("transcript line %d", i))
	}
	m.Update(mkWinSize(80, 24))
	m.View()
	m.vp.GotoBottom()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m.busy, m.cancel, m.queueSel = true, cancel, -1
	m.fragFilter = true
	return m, ctx
}

// feed runs msgs through Update and then lets every pending flush fire.
func feed(m *model, msgs []tea.Msg) {
	for _, msg := range msgs {
		m.Update(msg)
	}
	m.Update(mouseFragFlush{seq: m.frag.seq})
}

const wheelUp = "\x1b[<64;40;5M"

// Without the filter a fast wheel burst types its cut report into the
// composer: this pins down the reader behaviour the filter is for.
func TestWheelBurstSplitsReports(t *testing.T) {
	msgs := parsedInput(t, strings.Repeat(wheelUp, 30))
	if d := describe(msgs); !strings.Contains(d, `key"alt+["`) {
		t.Skipf("the input reader no longer cuts reports at its buffer edge: %s", d)
	}
	m, _ := fragModel(t)
	m.fragFilter = false
	feed(m, msgs)
	if m.input.Value() == "" {
		t.Fatal("expected the unfiltered burst to leak into the composer")
	}
}

func TestWheelBurstLeavesComposerAlone(t *testing.T) {
	bursts := map[string][]string{
		"buffer edge": {strings.Repeat(wheelUp, 30)},
		"long burst":  {strings.Repeat(wheelUp, 100)},
	}
	for cut := 1; cut < len(wheelUp); cut++ {
		bursts[fmt.Sprintf("short read at %d", cut)] = []string{wheelUp + wheelUp[:cut], wheelUp[cut:] + wheelUp}
	}
	for name, chunks := range bursts {
		msgs := parsedInput(t, chunks...)
		m, ctx := fragModel(t)
		before := m.vp.YOffset
		feed(m, msgs)
		if got := m.input.Value(); got != "" {
			t.Errorf("%s: the wheel typed %q into the composer (%s)", name, got, describe(msgs))
		}
		if ctx.Err() != nil {
			t.Errorf("%s: a cut report must not interrupt the turn as an Esc (%s)", name, describe(msgs))
		}
		if m.vp.YOffset >= before {
			t.Errorf("%s: the wheel should still scroll up (offset %d -> %d)", name, before, m.vp.YOffset)
		}
	}
}

func TestSplitReportReassembled(t *testing.T) {
	m, _ := fragModel(t)
	var got []tea.MouseMsg
	for _, parts := range [][]tea.Msg{
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("["), Alt: true}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("<65;40;12M")}},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("["), Alt: true}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("<65")}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(";40;1")}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2M")}},
		{tea.KeyMsg{Type: tea.KeyEsc}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<65;40;12M")}},
	} {
		before := m.vp.YOffset
		m.vp.SetYOffset(10)
		for _, p := range parts {
			m.Update(p)
		}
		if m.input.Value() != "" || m.frag.held != nil {
			t.Fatalf("%v: composer %q, still holding %v", describe(parts), m.input.Value(), m.frag.held)
		}
		if m.vp.YOffset <= 10 {
			t.Fatalf("%v: a reassembled wheel-down should scroll down (offset %d)", describe(parts), m.vp.YOffset)
		}
		_ = before
		ev, ok := parseSGRMouse("[<65;40;12M")
		got = append(got, ev)
		if !ok || ev.Button != tea.MouseButtonWheelDown || ev.X != 39 || ev.Y != 11 {
			t.Fatalf("parse: %+v", ev)
		}
	}
}

func TestParseSGRMouseMatchesReader(t *testing.T) {
	for _, seq := range []string{
		"\x1b[<0;10;5M", "\x1b[<0;10;5m", "\x1b[<32;11;5M", "\x1b[<64;1;1M", "\x1b[<65;3;4M",
		"\x1b[<66;3;4M", "\x1b[<2;3;4M", "\x1b[<4;3;4M", "\x1b[<16;3;4m", "\x1b[<35;7;8M", "\x1b[<128;2;2M",
	} {
		msgs := parsedInput(t, seq)
		if len(msgs) != 1 {
			t.Fatalf("%q: reader gave %s", seq, describe(msgs))
		}
		want := msgs[0].(tea.MouseMsg)
		got, ok := parseSGRMouse(seq[1:])
		if !ok || got != want {
			t.Errorf("%q: got %+v, the reader says %+v", seq, got, want)
		}
	}
}

func TestEscStillWorksWithFilter(t *testing.T) {
	m, ctx := fragModel(t)
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if ctx.Err() != nil {
		t.Fatal("the Esc is held for a moment")
	}
	m.Update(mouseFragFlush{seq: m.frag.seq})
	if ctx.Err() == nil {
		t.Fatal("an Esc nothing follows interrupts once the hold runs out")
	}
}

func TestEscThenTypingKeepsOrder(t *testing.T) {
	m, _ := fragModel(t)
	m.busy = false
	m.setComposer("draft", 5)
	// Esc Esc clears a draft: the second Esc releases the first, in order.
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.input.Value() != "" {
		t.Fatalf("Esc Esc should clear the draft, composer %q", m.input.Value())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ab")})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.input.Value() != "abx" || m.frag.held != nil {
		t.Fatalf("a key after a held Esc is still typed, composer %q", m.input.Value())
	}
}

func TestTypedLookalikeIsTyped(t *testing.T) {
	m, _ := fragModel(t)
	m.busy = false
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<1;2;3M")})
	if got := m.input.Value(); got != "[<1;2;3M" {
		t.Fatalf("text without an ESC in front is typing, got %q", got)
	}
	// Alt+[ held and then a slow, unrelated key: both arrive, in order.
	m.setComposer("", 0)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("["), Alt: true})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.frag.held != nil || !strings.HasSuffix(m.input.Value(), "a") {
		t.Fatalf("a key that does not continue a report is typed, composer %q", m.input.Value())
	}
}

func TestStaleEscDoesNotSwallowText(t *testing.T) {
	m, ctx := fragModel(t)
	clock := time.Now()
	m.now = func() time.Time { return clock }
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	clock = clock.Add(time.Second)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<1;2;3M")})
	if ctx.Err() == nil {
		t.Fatal("the Esc should have run")
	}
	if got := m.input.Value(); got != "[<1;2;3M" {
		t.Fatalf("text long after an Esc is typing, got %q", got)
	}
}

func TestEditorReturnRestoresMouse(t *testing.T) {
	var out bytes.Buffer
	prev := termModeOut
	termModeOut = func() io.Writer { return &out }
	tuiRunning = true
	t.Cleanup(func() { termModeOut = prev; tuiRunning = false })
	m, _ := fragModel(t)
	m.mouseOn = true
	m.Update(brainEditedMsg{path: "/nonexistent"})
	if !strings.Contains(out.String(), "\x1b[?1006h\x1b[?1002h") {
		t.Fatalf("mouse reporting should be back on after an external editor, wrote %q", out.String())
	}
	out.Reset()
	m.mouseOn = false
	m.Update(systemEditedMsg{path: "/nonexistent"})
	if strings.Contains(out.String(), "?1002h") {
		t.Fatal("with mouse capture off it stays off")
	}
}
