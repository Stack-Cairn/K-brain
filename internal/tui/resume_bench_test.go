package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func benchTranscript(n int) []ai.Message {
	msgs := make([]ai.Message, 0, n*3)
	for i := range n {
		msgs = append(msgs,
			ai.Message{Role: "user", Content: fmt.Sprintf("question %d: how do I do the thing?", i)},
			ai.Message{Role: "assistant", Content: strings.Repeat("Here is **some** `answer` with text. ", 20)},
			func() ai.Message {
				var tc ai.ToolCall
				tc.Function.Name = "bash"
				tc.Function.Arguments = fmt.Sprintf(`{"command":"ls %d"}`, i)
				return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{tc}}
			}(),
		)
	}
	return msgs
}

func BenchmarkSeedTranscript(b *testing.B) {
	msgs := benchTranscript(200)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		m := compactCmdModel()
		m.Update(mkWinSize(120, 40))
		m.seedTranscript(msgs, 1)
	}
}

func BenchmarkAppendStream(b *testing.B) { benchAppend(b, 200) }

// benchAppend measures appending one notice line to a session of the given
// number of turns. Every 100 appends the transcript is cut back to the
// seeded session, outside the timer, so the session length stays what the
// benchmark's name says however large b.N grows.
func benchAppend(b *testing.B, turns int) {
	m := compactCmdModel()
	m.Update(mkWinSize(120, 40))
	m.seedTranscript(benchTranscript(turns), 1)
	base := len(m.blocks)
	b.ResetTimer()
	b.ReportAllocs()
	for i := range b.N {
		if i%100 == 99 {
			b.StopTimer()
			m.blocks = m.blocks[:base]
			m.refreshVP()
			b.StartTimer()
		}
		m.append(fmt.Sprintf("streamed line %d", i))
	}
}

// benchReplyLines is a long assistant reply as it streams in: prose, a list
// and a fenced code block, repeated until it reaches n lines.
func benchReplyLines(n int) []string {
	unit := []string{
		"Here is **some** `answer` text that wraps across the screen when the terminal is narrow enough.",
		"",
		"- a bullet with a [link](https://example.com) and `code`",
		"- another bullet",
		"",
		"```go",
		"func f(x int) int { return x * 2 }",
		"```",
		"",
	}
	lines := make([]string, 0, n)
	for len(lines) < n {
		lines = append(lines, unit[len(lines)%len(unit)])
	}
	return lines
}

// BenchmarkStreamReply2000Lines streams one 2,000-line reply a line at a
// time, the way the coalesced stream delivers it, rendering a frame after
// every delta as bubbletea does.
func BenchmarkStreamReply2000Lines(b *testing.B) {
	lines := benchReplyLines(2000)
	b.ReportAllocs()
	for b.Loop() {
		m := compactCmdModel()
		m.Update(mkWinSize(120, 40))
		m.busy = true
		for _, l := range lines {
			m.Update(textMsg(l + "\n"))
			_ = m.View()
		}
		m.flushCurrent()
	}
}

func BenchmarkSeedTranscript1000Turns(b *testing.B) {
	msgs := benchTranscript(1000)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		m := compactCmdModel()
		m.Update(mkWinSize(120, 40))
		m.seedTranscript(msgs, 1)
	}
}

func BenchmarkAppendStream1000Turns(b *testing.B) { benchAppend(b, 1000) }
