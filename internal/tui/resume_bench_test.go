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

func BenchmarkAppendStream(b *testing.B) {
	m := compactCmdModel()
	m.Update(mkWinSize(120, 40))
	m.seedTranscript(benchTranscript(200), 1)
	b.ResetTimer()
	b.ReportAllocs()
	for i := range b.N {
		m.append(fmt.Sprintf("streamed line %d", i))
	}
}
