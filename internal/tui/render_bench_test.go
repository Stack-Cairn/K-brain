package tui

// Frame benchmarks: what one Update plus the View that bubbletea draws after
// it costs with a long, realistic session on screen. Every benchmark here
// renders a full frame, as the program does after each message, so a cost
// that grows with the session shows up as a frame that grows with it.
//
//	go test ./internal/tui -run '^$' -bench 'Frame' -benchmem

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// benchReply is an assistant reply with the markdown a real one carries:
// a heading, prose with inline code and links, a list, a fenced code block
// and a table. Every turn's text and code differ, so no cache can serve one
// turn from another.
func benchReply(i int) string {
	return fmt.Sprintf(`## Step %d

The handler in `+"`internal/server/handler.go`"+` reads the **whole body** before it checks the size, so a large upload is buffered in memory. See [the docs](https://example.com/docs/%d) for the limits.

- read the header first
- reject anything over `+"`MaxBytes`"+`
- stream the rest to disk

`+"```go"+`
func limit%d(r *http.Request, n int64) io.Reader {
	if r.ContentLength > n {
		return nil
	}
	return io.LimitReader(r.Body, n)
}
`+"```"+`

| field | before | after |
|-------|--------|-------|
| peak  | 512MB  | 4MB   |
| time  | 1.2s   | 0.3s  |

That keeps memory flat however large the request is.`, i, i, i)
}

// benchShellOutput is a long command output, as a test run prints it.
func benchShellOutput(i, n int) string {
	var b strings.Builder
	for j := range n {
		fmt.Fprintf(&b, "=== RUN   TestHandler%d/case_%03d\n--- PASS: TestHandler%d/case_%03d (0.%02ds)\n", i, j, i, j, j%100)
	}
	b.WriteString("PASS\nok  \tgithub.com/example/server\t1.234s")
	return b.String()
}

// benchDiff is an edit result with a diff of n changed lines.
func benchDiff(i, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Replaced 1 occurrence(s) in internal/server/handler%d.go\n```diff\n", i)
	for j := range n {
		fmt.Fprintf(&b, "%d   \tctx := r.Context() // turn %d, line %d\n", 40+j*3, i, j)
		fmt.Fprintf(&b, "%d - \tbody%d, err := io.ReadAll(r.Body)\n", 41+j*3, i)
		fmt.Fprintf(&b, "%d + \tbody%d, err := io.ReadAll(io.LimitReader(r.Body, maxBytes%d))\n", 41+j*3, i, j)
	}
	b.WriteString("```")
	return b.String()
}

// benchFile is a read result: numbered source lines.
func benchFile(n int) string {
	var b strings.Builder
	for j := range n {
		fmt.Fprintf(&b, "%6d\tfunc helper%d(x int) int { return x*%d + 1 }\n", j+1, j, j)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func benchCall(id, name, args string) ai.ToolCall {
	var tc ai.ToolCall
	tc.ID = id
	tc.Function.Name = name
	tc.Function.Arguments = args
	tc.DurationMs = 1234
	return tc
}

// benchSession is a resumed session of n turns, each a question, a reply
// with markdown and three tool calls (a test run with long output, an edit
// with a diff, a file read) and a closing reply.
func benchSession(n int) []ai.Message {
	msgs := make([]ai.Message, 0, n*6)
	for i := range n {
		sh, ed, rd := fmt.Sprintf("s%d", i), fmt.Sprintf("e%d", i), fmt.Sprintf("r%d", i)
		msgs = append(msgs,
			ai.Message{Role: "user", Content: fmt.Sprintf("question %d: why does the handler use so much memory on large uploads?", i)},
			ai.Message{Role: "assistant", Content: benchReply(i), ToolCalls: []ai.ToolCall{
				benchCall(sh, "bash", `{"command":"go test ./internal/server/ -run TestHandler -v"}`),
				benchCall(ed, "edit", fmt.Sprintf(`{"path":"internal/server/handler%d.go","old_string":"a","new_string":"b"}`, i)),
				benchCall(rd, "read", `{"path":"internal/server/helpers.go"}`),
			}},
			ai.Message{Role: "tool", Name: "bash", ToolCallID: sh, Content: benchShellOutput(i, 60)},
			ai.Message{Role: "tool", Name: "edit", ToolCallID: ed, Content: benchDiff(i, 10)},
			ai.Message{Role: "tool", Name: "read", ToolCallID: rd, Content: benchFile(120)},
			ai.Message{Role: "assistant", Content: "Done. The **peak** drops from `512MB` to `4MB`; the tests pass."},
		)
	}
	return msgs
}

// benchFrameModel is a 120x40 terminal showing a resumed session of the
// given number of turns, scrolled to the bottom.
func benchFrameModel(b *testing.B, turns int) *model {
	b.Helper()
	m := compactCmdModel()
	m.spin = spinner.New(spinner.WithSpinner(spinner.Dot))
	m.now = time.Now
	m.follow = true
	m.Update(mkWinSize(120, 40))
	m.seedTranscript(benchSession(turns), 1)
	_ = m.View()
	return m
}

func frame(m *model, msg tea.Msg) {
	m.Update(msg)
	_ = m.View()
}

// BenchmarkFrameIdle is a redraw with nothing changed: what every message
// the transcript ignores (a tick, a mouse move) costs.
func BenchmarkFrameIdle1000Turns(b *testing.B) {
	m := benchFrameModel(b, 1000)
	b.ReportAllocs()
	for b.Loop() {
		_ = m.View()
	}
}

// BenchmarkFrameStreamToken is one coalesced stream delta landing on the
// reply being written, with the frame drawn after it. One delta in eight
// ends a line, which commits it to the transcript; the rest grow the live
// area. The reply is cut back every 400 deltas, outside the timer, so its
// length stays bounded however large b.N grows.
func BenchmarkFrameStreamToken200Turns(b *testing.B)  { benchStreamToken(b, 200) }
func BenchmarkFrameStreamToken1000Turns(b *testing.B) { benchStreamToken(b, 1000) }

func benchStreamToken(b *testing.B, turns int) {
	m := benchFrameModel(b, turns)
	m.busy = true
	m.turnStart = time.Now()
	base := len(m.blocks)
	deltas := []string{"The handler ", "reads the **whole** ", "body before ", "it checks ", "the `size`, so ", "a large upload ", "is buffered in ", "memory.\n"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if i%400 == 399 {
			b.StopTimer()
			m.current, m.inMsg = "", false
			m.blocks = m.blocks[:base]
			m.refreshVP()
			b.StartTimer()
		}
		frame(m, textMsg(deltas[i%len(deltas)]))
	}
}

// BenchmarkFrameSpinner is the busy row's spinner tick mid-turn.
func BenchmarkFrameSpinner1000Turns(b *testing.B) {
	m := benchFrameModel(b, 1000)
	m.busy = true
	m.turnStart = time.Now()
	tick := m.spin.Tick().(spinner.TickMsg)
	b.ReportAllocs()
	for b.Loop() {
		tick.ID = m.spin.ID()
		m.Update(tick)
		_ = m.View()
	}
}

// BenchmarkFrameScroll is one wheel notch up or down through the middle of
// the session.
func BenchmarkFrameScroll1000Turns(b *testing.B) {
	m := benchFrameModel(b, 1000)
	m.follow = false
	m.vp.SetYOffset(m.transcriptRows() / 2)
	up := tea.MouseMsg{X: 10, Y: 10, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp}
	down := up
	down.Button = tea.MouseButtonWheelDown
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		if i++; i%20 < 10 {
			frame(m, up)
		} else {
			frame(m, down)
		}
	}
}

// BenchmarkFrameTyping is one key typed into the composer. The input is
// cleared every 64 keys, outside the timer.
func BenchmarkFrameTyping1000Turns(b *testing.B) {
	m := benchFrameModel(b, 1000)
	key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	space := tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if i%64 == 63 {
			b.StopTimer()
			m.input.SetValue("")
			b.StartTimer()
		}
		if i%6 == 5 {
			frame(m, space)
		} else {
			frame(m, key)
		}
	}
}

// BenchmarkFrameResize is one terminal resize on its own: every block
// reflows to the new width.
func BenchmarkFrameResize200Turns(b *testing.B)  { benchResize(b, 200) }
func BenchmarkFrameResize1000Turns(b *testing.B) { benchResize(b, 1000) }

func benchResize(b *testing.B, turns int) {
	m := benchFrameModel(b, turns)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		i++
		forgetResizes(m)
		frame(m, mkWinSize(120-i%2*20, 40))
	}
}

// BenchmarkResizeDrag is dragging the terminal edge: twenty resizes in a
// row, a frame after each, then the frame once the drag stops.
func BenchmarkResizeDrag200Turns(b *testing.B)  { benchResizeDrag(b, 200) }
func BenchmarkResizeDrag1000Turns(b *testing.B) { benchResizeDrag(b, 1000) }

func benchResizeDrag(b *testing.B, turns int) {
	m := benchFrameModel(b, turns)
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		forgetResizes(m)
		b.StartTimer()
		for w := 100; w < 120; w++ {
			frame(m, mkWinSize(w, 40))
		}
		settleResizes(m)
		_ = m.View()
	}
}

// BenchmarkSeedSession1000Turns is resuming the realistic session in a
// fresh process: every block rendered once, nothing memoised yet.
func BenchmarkSeedSession1000Turns(b *testing.B) {
	msgs := benchSession(1000)
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		resetBenchMemos()
		b.StartTimer()
		m := compactCmdModel()
		m.Update(mkWinSize(120, 40))
		m.seedTranscript(msgs, 1)
		_ = m.View()
	}
}

// resetBenchMemos drops what earlier iterations left in process-wide
// memos, where there are any.
func resetBenchMemos() { resetSyntaxMemos() }

// forgetResizes makes the next resize the first of a burst.
func forgetResizes(m *model) { m.txs().hold.last = time.Time{} }

// settleResizes delivers the end of a burst of resizes, as its tick would.
func settleResizes(m *model) { frame(m, reflowSettleMsg{seq: m.txs().hold.seq}) }
