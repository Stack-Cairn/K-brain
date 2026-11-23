//go:build tuicap

package tuicap_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/tui/tuicap"
)

// The evidence screens for the quieter thinking display, the permission
// default, the non-Alt shortcuts, folded pastes and subagent approvals. They
// assert nothing: set TUICAP_EVIDENCE_DIR to write each screen there (named
// TUICAP_EVIDENCE_TAG-scenario.txt) and run them against two builds with
// TUICAP_KN to compare them.

func evidence(t *testing.T) (save func(name, screen string)) {
	t.Helper()
	dir := os.Getenv("TUICAP_EVIDENCE_DIR")
	if dir == "" {
		t.Skip("TUICAP_EVIDENCE_DIR not set")
	}
	tag := os.Getenv("TUICAP_EVIDENCE_TAG")
	if tag == "" {
		tag = "screen"
	}
	return func(name, screen string) {
		t.Helper()
		path := filepath.Join(dir, tag+"-"+name+".txt")
		if err := os.WriteFile(path, []byte(screen+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s:\n%s", name, screen)
	}
}

func settleAfter(s *tuicap.Session, text string) string {
	s.WaitFor(text)
	return settle(s)
}

func settle(s *tuicap.Session) string {
	time.Sleep(600 * time.Millisecond)
	return s.Snapshot()
}

func TestEvidenceThinkingTurn(t *testing.T) {
	save := evidence(t)
	call := func(id, cmd string) tuicap.ToolCall {
		return tuicap.ToolCall{ID: id, Name: "bash", Args: jsonArgs(t, map[string]any{"command": cmd})}
	}
	slow := []string{"Two steps ran; the third needs a closer look.\n", "Check the exit status first.\n", "Then compare the output with the expected line.\n", "That decides the last step.\n"}
	s := tuicap.Start(t, tuicap.Options{
		Width: 100, Height: 30, Timeout: 30 * time.Second,
		Replies: []tuicap.Reply{
			{Reasoning: []string{"Start with the first step.\n"}, ToolCalls: []tuicap.ToolCall{call("c1", "echo one")}},
			{Reasoning: []string{"Now the second.\n"}, ToolCalls: []tuicap.ToolCall{call("c2", "echo two")}},
			{Reasoning: slow, Delay: 450 * time.Millisecond, ToolCalls: []tuicap.ToolCall{call("c3", "echo three")}},
			{Reasoning: []string{"And the fourth.\n"}, ToolCalls: []tuicap.ToolCall{call("c4", "echo four")}},
			{Reasoning: []string{"All done.\n"}, Chunks: []string{"All four steps ran."}},
		},
	})
	s.WaitFor("Ask k-brain anything")
	s.Type("run the four steps")
	s.Press(tuicap.Enter)
	save("thinking-live", s.WaitFor("Then compare the output"))
	save("thinking-done", settleAfter(s, "All four steps ran."))
	s.Press(tuicap.Ctrl('o'))
	save("ctrlo-off", settle(s))
	s.Press(tuicap.Ctrl('o'))
	save("ctrlo-on", settle(s))
	time.Sleep(2500 * time.Millisecond)
	save("ctrlo-after-2s", s.Snapshot())
}

func TestEvidenceFooterNarrow(t *testing.T) {
	save := evidence(t)
	for _, w := range []int{100, 50, 40} {
		s := tuicap.Start(t, tuicap.Options{Width: w, Height: 12})
		save(fmt.Sprintf("footer-%d", w), settleAfter(s, "k-brain"))
		s.Close()
	}
}

func TestEvidenceFirstRun(t *testing.T) {
	save := evidence(t)
	s := tuicap.Start(t, tuicap.Options{
		Width: 100, Height: 30,
		Setup: func(home, _ string) { _ = os.Remove(filepath.Join(home, "config.json")) },
	})
	save("firstrun-welcome", settleAfter(s, "Welcome to k-brain"))
	s.Press(tuicap.Enter)
	save("firstrun-thinking", settleAfter(s, "Show thinking tokens?"))
}

func TestEvidenceHelpAndHints(t *testing.T) {
	save := evidence(t)
	var long []string
	for i := 1; i <= 40; i++ {
		long = append(long, fmt.Sprintf("line %d of the generated file", i))
	}
	s := tuicap.Start(t, tuicap.Options{
		Width: 100, Height: 40, Timeout: 20 * time.Second, Args: []string{"--cautious"},
		Replies: []tuicap.Reply{
			tuicap.Calls(tuicap.ToolCall{ID: "c_out", Name: "bash", Args: jsonArgs(t, map[string]any{"command": "seq 1 30"})}),
			tuicap.Calls(tuicap.ToolCall{ID: "c_w", Name: "write", Args: jsonArgs(t, map[string]any{
				"path": "gen.txt", "content": strings.Join(long, "\n") + "\n",
			})}),
			tuicap.Text("Wrote the file."),
		},
	})
	s.WaitFor("Ask k-brain anything")
	s.Type("generate")
	s.Press(tuicap.Enter)
	s.WaitFor("Run this command?")
	s.Type("1")
	save("approval-card-write", settleAfter(s, "Create this file?"))
	s.Type("1")
	save("tool-output-hint", settleAfter(s, "Wrote the file."))
	s.Press("\x1bOP") // F1
	save("help", settle(s))
}

func TestEvidencePaste(t *testing.T) {
	save := evidence(t)
	var lines []string
	for i := 1; i <= 2000; i++ {
		lines = append(lines, fmt.Sprintf("log line %04d: request served", i))
	}
	s := tuicap.Start(t, tuicap.Options{
		Width: 100, Height: 30, Timeout: 20 * time.Second,
		Replies: []tuicap.Reply{tuicap.Text("Read all 2000 lines.")},
	})
	s.WaitFor("Ask k-brain anything")
	s.Type("summarize this log ")
	s.Paste(strings.Join(lines, "\n"))
	save("paste-composer", settleAfter(s, "Pasted"))
	s.Press(tuicap.Enter)
	save("paste-sent", settleAfter(s, "Read all 2000 lines."))
	reqs := s.Requests()
	if len(reqs) == 0 || !reqs[0].Contains("log line 2000: request served") {
		t.Errorf("the model did not get the whole paste")
	}
}

func TestEvidenceSubagentApproval(t *testing.T) {
	save := evidence(t)
	sub := func(id, desc string) tuicap.ToolCall {
		return tuicap.ToolCall{ID: id, Name: "subagent", Args: jsonArgs(t, map[string]any{
			"description": desc, "prompt": "list the files", "worktree": false,
		})}
	}
	bash := func(id string) tuicap.Reply {
		return tuicap.Calls(tuicap.ToolCall{ID: id, Name: "bash", Args: jsonArgs(t, map[string]any{"command": "mkdir -p build_" + id})})
	}
	s := tuicap.Start(t, tuicap.Options{
		Width: 110, Height: 36, Timeout: 30 * time.Second, Args: []string{"--cautious"},
		Replies: []tuicap.Reply{
			tuicap.Calls(sub("s1", "Audit the changelog"), sub("s2", "Survey the tests")),
			bash("b1"),
		// Commands run one at a time, so the second subagent writes a
		// file: its request waits behind the first one's card.
		tuicap.Calls(tuicap.ToolCall{ID: "w2", Name: "write", Args: jsonArgs(t, map[string]any{"path": "notes.txt", "content": "tests\n"})}),
			tuicap.Text("listed"), tuicap.Text("listed"),
			tuicap.Text("Both subagents finished."),
		},
	})
	s.WaitFor("Ask k-brain anything")
	s.Type("audit")
	s.Press(tuicap.Enter)
	s.WaitFor("Esc reject")
	save("subagent-card", settle(s))
	time.Sleep(2 * time.Second)
	save("subagent-card-2s", s.Snapshot())
	s.Type("1")
	save("subagent-second", settle(s))
}
