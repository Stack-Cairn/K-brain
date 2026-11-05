//go:build tuicap

package tuicap_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/tui/tuicap"
)

// altUp is Alt+↑ as xterm-style terminals send it.
const altUp = "\x1b[1;3A"

// A markdown reply streams in line by line; then Alt+↑ focuses it, h folds
// it, v opens it full screen, / searches the transcript and Esc hands the
// keyboard back to the composer.
func TestTranscriptFocusFoldViewerFind(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{
		Width: 80, Height: 24, Timeout: 20 * time.Second,
		Replies: []tuicap.Reply{tuicap.Lines(2*time.Millisecond,
			"## Cache keys",
			"",
			"The cache is keyed by **width**, so a resize drops it.",
			"",
			"```go",
			"key := fmt.Sprint(width)",
			"",
			"return cache[key]",
			"```",
			"",
			"- the first entry misses",
			"- later entries hit the cache",
			"",
			"That is the whole story.",
		)},
	})
	s.WaitFor("Ask k-brain anything")
	s.Type("why does the cache miss?")
	s.Press(tuicap.Enter)
	s.WaitFor("That is the whole story.")
	streamed := s.WaitGone("Esc to interrupt")
	t.Logf("streamed reply:\n%s", streamed)

	s.Press(altUp)
	focused := s.WaitFor("┃ transcript")
	t.Logf("Alt+↑ focuses the reply:\n%s", focused)

	s.Type("h")
	folded := s.WaitFor("more lines")
	if strings.Contains(folded, "That is the whole story.") {
		t.Fatalf("h did not fold the reply:\n%s", folded)
	}
	t.Logf("h folds it:\n%s", folded)
	s.Type("l")
	s.WaitFor("That is the whole story.")

	s.Type("v")
	viewer := s.WaitFor("┃ reply")
	if strings.Contains(viewer, "Ask k-brain anything") {
		t.Fatalf("the viewer does not cover the screen:\n%s", viewer)
	}
	t.Logf("v opens the viewer:\n%s", viewer)
	s.Press(tuicap.Esc)
	s.WaitFor("┃ transcript")

	s.Type("/cache")
	s.Press(tuicap.Enter)
	found := s.WaitFor(`"cache" `)
	t.Logf("/ searches the transcript:\n%s", found)

	s.Press(tuicap.Esc)
	s.WaitGone("┃ transcript")
	s.Type("back in the composer")
	s.WaitFor("back in the composer")
}

// Reasoning streams as a block showing its last lines, then folds to how
// long it took once the reply starts; Alt+↑ and e open it.
func TestThinkingFoldsAfterRun(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{
		Width: 80, Height: 24, Timeout: 20 * time.Second,
		Config: map[string]any{"thinking": true},
		Replies: []tuicap.Reply{{
			Reasoning: []string{"The user asks about the cache.\n", "It is keyed by width.\n", "A resize changes the width.\n", "So every entry misses once.\n", "Then it fills again.\n"},
			Chunks:    []string{"Every entry misses once ", "after a resize."},
			Delay:     150 * time.Millisecond,
		}},
	})
	s.WaitFor("Ask k-brain anything")
	s.Type("why does the cache miss?")
	s.Press(tuicap.Enter)
	live := s.WaitFor("Thinking…")
	t.Logf("while it reasons:\n%s", live)
	done := s.WaitFor("after a resize.")
	s.WaitGone("Esc to interrupt")
	done = s.WaitFor("Thought for")
	if strings.Contains(done, "keyed by width") {
		t.Fatalf("the finished run did not fold:\n%s", done)
	}
	t.Logf("folded after the reply:\n%s", done)

	s.Press(altUp)
	s.WaitFor("┃ transcript")
	s.Type("k")
	s.WaitFor("· thinking")
	s.Type("e")
	opened := s.WaitFor("It is keyed by width.")
	t.Logf("opened with e:\n%s", opened)
}
