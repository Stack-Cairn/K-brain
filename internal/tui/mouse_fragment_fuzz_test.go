package tui

import (
	"context"
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fuzzKeys turns fuzz bytes into the keys the input reader can hand over
// around a cut mouse report: Esc, Alt+[, and runs of report characters and
// ordinary text.
func fuzzKeys(data []byte) []tea.KeyMsg {
	const alphabet = "[<0123456789;Mmab "
	var keys []tea.KeyMsg
	for i := 0; i < len(data); i++ {
		switch b := data[i]; b % 6 {
		case 0:
			keys = append(keys, tea.KeyMsg{Type: tea.KeyEsc})
		case 1:
			keys = append(keys, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("["), Alt: true})
		default:
			n := int(b>>3)%4 + 1
			var runes []rune
			for j := 0; j < n && i < len(data); j++ {
				runes = append(runes, rune(alphabet[int(data[i])%len(alphabet)]))
				i++
			}
			i--
			keys = append(keys, tea.KeyMsg{Type: tea.KeyRunes, Runes: runes})
		}
	}
	return keys
}

// fuzzFragModel is fragModel built once per fuzz target: a model per input
// would start a test server per input and run out of ports. reset puts it
// back to an idle composer and a fresh turn before each input.
func fuzzFragModel(f *testing.F) (m *model, reset func(t *testing.T, busy bool) context.Context) {
	m, _ = fragModel(f)
	m.vp.GotoBottom()
	return m, func(t *testing.T, busy bool) context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		m.busy, m.cancel, m.queueSel = busy, cancel, -1
		m.menu = nil
		m.setComposer("", 0)
		m.frag = mouseFrag{}
		m.lastUp = time.Time{}
		return ctx
	}
}

// Typed at human speed — every key well apart, every hold run out — the
// filter must be invisible: the composer and the turn end up exactly as they
// would with no filter at all. Nothing typed is lost, doubled or reordered.
func FuzzMouseFragmentKeepsSlowTyping(f *testing.F) {
	for _, seed := range []string{"\x00\x02[", "\x00[<1;2;3M", "\x01<65;4", "ab\x00\x00cd", "\x00", "\x01", "\x00\x08"} {
		f.Add([]byte(seed))
	}
	filtered, resetFiltered := fuzzFragModel(f)
	plain, resetPlain := fuzzFragModel(f)
	plain.fragFilter = false
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64 {
			return
		}
		keys := fuzzKeys(data)
		filteredCtx, plainCtx := resetFiltered(t, false), resetPlain(t, false)
		clock := time.Now()
		filtered.now = func() time.Time { return clock }
		plain.now = func() time.Time { return clock }
		for _, k := range keys {
			filtered.Update(k)
			clock = clock.Add(time.Second)
			filtered.Update(mouseFragFlush{seq: filtered.frag.seq})
			plain.Update(k)
			plain.Update(mouseFragFlush{seq: plain.frag.seq})
		}
		if filtered.frag.held != nil || filtered.frag.replaying {
			t.Fatalf("%s: the filter is still holding %v", describeKeys(keys), filtered.frag.held)
		}
		if got, want := filtered.input.Value(), plain.input.Value(); got != want {
			t.Fatalf("%s: composer %q with the filter, %q without", describeKeys(keys), got, want)
		}
		if (filteredCtx.Err() == nil) != (plainCtx.Err() == nil) {
			t.Fatalf("%s: the filter changed whether the turn was interrupted", describeKeys(keys))
		}
	})
}

// A whole report, cut anywhere into an Esc and pieces that arrive back to
// back, is a mouse event: nothing reaches the composer and the turn keeps
// running.
func FuzzMouseFragmentReassemblesCutReports(f *testing.F) {
	f.Add(uint16(64), uint16(40), uint16(5), uint8(3), uint8(2), true)
	f.Add(uint16(0), uint16(1), uint16(1), uint8(0), uint8(9), false)
	m, reset := fuzzFragModel(f)
	f.Fuzz(func(t *testing.T, button, x, y uint16, cut1, cut2 uint8, press bool) {
		final := "M"
		if !press {
			final = "m"
		}
		report := fmt.Sprintf("[<%d;%d;%d%s", button%256, x%500, y%500, final)
		a := int(cut1) % len(report)
		b := a + int(cut2)%(len(report)-a)
		ctx := reset(t, true)
		clock := time.Now()
		m.now = func() time.Time { return clock }
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		for _, piece := range []string{report[:a], report[a:b], report[b:]} {
			if piece != "" {
				m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(piece)})
			}
		}
		m.Update(mouseFragFlush{seq: m.frag.seq})
		if got := m.input.Value(); got != "" {
			t.Fatalf("%q cut at %d,%d typed %q", report, a, b, got)
		}
		if ctx.Err() != nil {
			t.Fatalf("%q cut at %d,%d interrupted the turn as an Esc", report, a, b)
		}
		if m.frag.held != nil {
			t.Fatalf("%q cut at %d,%d is still held", report, a, b)
		}
	})
}

func describeKeys(keys []tea.KeyMsg) string {
	msgs := make([]tea.Msg, len(keys))
	for i, k := range keys {
		msgs[i] = k
	}
	return describe(msgs)
}

func TestEscThenBracketAsSeparateKeysKeepsTheBracket(t *testing.T) {
	m, _ := fragModel(t)
	m.busy = false
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	m.Update(mouseFragFlush{seq: m.frag.seq})
	plain, _ := fragModel(t)
	plain.busy, plain.fragFilter = false, false
	for _, k := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune("a")}, {Type: tea.KeyEsc}, {Type: tea.KeyRunes, Runes: []rune("[")}} {
		plain.Update(k)
	}
	if got, want := m.input.Value(), plain.input.Value(); got != want {
		t.Fatalf("Esc then a separate [: composer %q, want %q as without the filter", got, want)
	}
}

func TestZeroCoordinateReportStaysOnScreen(t *testing.T) {
	ev, ok := parseSGRMouse("[<0;0;0M")
	if !ok || ev.X < 0 || ev.Y < 0 {
		t.Fatalf("got %+v ok=%v, want non-negative cells", ev, ok)
	}
}
