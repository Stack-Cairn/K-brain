//go:build tuicap

package tuicap_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/tui/tuicap"
)

// Ctrl+B on a running shell command moves it to the background: the call
// returns at once with the output so far, the composer is free, and the
// command's exit later wakes the agent with the rest of its output.
func TestCtrlBMovesRunningCommandToBackground(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "gate")
	s := tuicap.Start(t, tuicap.Options{
		Width: 100, Height: 30, Timeout: 30 * time.Second,
		Replies: []tuicap.Reply{
			tuicap.Calls(tuicap.ToolCall{ID: "call_bg", Name: "bash", Args: jsonArgs(t, map[string]any{
				"command": "echo first; while [ ! -f '" + gate + "' ]; do sleep 0.1; done; echo done",
			})}),
			tuicap.Text("It keeps running in the background."),
			tuicap.Text("The background command finished."),
		},
	})
	s.WaitFor("Ask k-brain anything")
	s.Type("run it")
	s.Press(tuicap.Enter)
	screen := s.WaitFor("ctrl+b to background")
	t.Logf("running:\n%s", screen)

	s.Press(tuicap.Ctrl('b'))
	screen = s.WaitFor("It keeps running in the background.")
	screen = s.WaitGone("Esc to interrupt")
	t.Logf("after ctrl+b:\n%s", screen)
	if strings.Contains(screen, "ctrl+b to background") {
		t.Fatalf("hint outlived the backgrounded call:\n%s", screen)
	}
	reqs := s.Requests()
	if len(reqs) < 2 || !reqs[1].Contains("moved this command to the background") || !reqs[1].Contains("first") {
		t.Fatalf("the tool result after ctrl+b is not the background notice: %+v", reqs)
	}

	// Idle, Ctrl+B is cursor left again.
	s.Type("ab")
	s.Press(tuicap.Ctrl('b'))
	s.Type("X")
	s.WaitFor("aXb")

	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	screen = s.WaitFor("The background command finished.")
	t.Logf("after exit:\n%s", screen)
	reqs = s.Requests()
	if last := reqs[len(reqs)-1]; !last.Contains("done") || !last.Contains("[process bash-") {
		t.Fatalf("the exit notice did not reach the agent: %s", last.Body)
	}
}
