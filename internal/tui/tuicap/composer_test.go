//go:build tuicap

package tuicap_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/tui/tuicap"
)

// A large bracketed paste lands as one chip in the composer and reaches the
// provider expanded; Ctrl+R then finds the earlier prompts of this directory.
func TestPasteChipAndHistorySearch(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{Width: 100, Height: 30})
	s.WaitFor("Ask k-brain anything")

	var b strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "log line %02d: request served\n", i)
	}
	s.Type("summarise ")
	s.Paste(b.String())
	screen := s.WaitFor("[Pasted 30 lines]")
	if strings.Contains(screen, "log line 02") {
		t.Fatalf("the paste should be a chip, not text:\n%s", screen)
	}
	t.Logf("composer with a paste chip:\n%s", screen)

	s.Press(tuicap.Enter)
	s.WaitFor("ok")
	s.WaitGone("Esc to interrupt")
	reqs := s.Requests()
	if len(reqs) != 1 || !reqs[0].Contains("log line 30: request served") || reqs[0].Contains("[Pasted") {
		t.Fatalf("the provider should get the pasted text, not the chip: %+v", reqs)
	}

	s.Type("deploy the staging server")
	s.Press(tuicap.Enter)
	s.WaitGone("Esc to interrupt")

	s.Press(tuicap.Ctrl('r'))
	s.WaitFor("history · this directory")
	s.Type("sum")
	screen = s.WaitUntil("one match", func(screen string) bool {
		return strings.Contains(screen, "1/1") && strings.Contains(screen, "summarise")
	})
	t.Logf("history search:\n%s", screen)
	s.Press(tuicap.Enter)
	s.WaitGone("history · this directory")
	screen = s.WaitFor("summarise log line 01")
	t.Logf("accepted:\n%s", screen)
}
