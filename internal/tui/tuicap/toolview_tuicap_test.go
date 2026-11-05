//go:build tuicap

package tuicap_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/tui/tuicap"
)

func jsonArgs(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A turn runs a failing shell command and an edit through kn's real tools:
// the command's header carries its exit badge and duration and its output
// folds in the middle; the edit's header carries the change's counts and its
// body the numbered diff.
func TestToolRowsShowExitBadgeAndDiff(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{
		Width: 100, Height: 40, Timeout: 20 * time.Second,
		Replies: []tuicap.Reply{
			tuicap.Calls(
				tuicap.ToolCall{ID: "call_bash", Name: "bash", Args: jsonArgs(t, map[string]any{
					"command": "for i in 1 2 3 4 5 6 7 8 9 10; do echo line $i; done; echo boom; exit 3",
				})},
				tuicap.ToolCall{ID: "call_read", Name: "read", Args: jsonArgs(t, map[string]any{"path": "greet.go"})},
			),
			tuicap.Calls(tuicap.ToolCall{ID: "call_edit", Name: "edit", Args: jsonArgs(t, map[string]any{
				"path": "greet.go", "old_string": `fmt.Println("hello")`, "new_string": `fmt.Println("hello, world")`,
			})}),
			tuicap.Text("Changed the greeting."),
		},
	})
	src := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
	if err := os.WriteFile(filepath.Join(s.Dir, "greet.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	s.WaitFor("Ask k-brain anything")
	s.Type("fix the greeting")
	s.Press(tuicap.Enter)
	screen := s.WaitFor("Changed the greeting.")
	screen = s.WaitGone("Esc to interrupt")
	t.Logf("after the turn:\n%s", screen)

	for _, want := range []string{
		"Bash  for i in", "exit 3 · ", // the exit badge and the tool's own time
		"line 1", "line 2", "+6 lines · ctrl+e", "line 10", "boom", // head and tail around the fold
		"Read  greet.go",
		"Edit  greet.go", "+1 −1", // counts on the header
		`6  - fmt.Println("hello")`, `6  + fmt.Println("hello, world")`,
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("screen missing %q", want)
		}
	}
	if strings.Contains(screen, "(exit: exit status 3)") {
		t.Error("the badge says the exit status; the output should not repeat it")
	}
	got, err := os.ReadFile(filepath.Join(s.Dir, "greet.go"))
	if err != nil || !strings.Contains(string(got), "hello, world") {
		t.Fatalf("the edit did not land: %q %v", got, err)
	}
}

// The plan the agent records shows in the pane above the composer, and its
// todowrite calls stay out of the transcript.
func TestTodoPaneShowsAgentPlan(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{
		Width: 100, Height: 40, Timeout: 20 * time.Second,
		Replies: []tuicap.Reply{
			tuicap.Calls(tuicap.ToolCall{ID: "call_todo", Name: "todowrite", Args: jsonArgs(t, map[string]any{
				"todos": []map[string]any{
					{"content": "Read the config loader", "status": "completed"},
					{"content": "Add the --json flag", "status": "in_progress"},
					{"content": "Run the package tests", "status": "pending"},
				},
			})}),
			tuicap.Text("Working on the flag."),
		},
	})
	s.WaitFor("Ask k-brain anything")
	s.Type("plan the change")
	s.Press(tuicap.Enter)
	s.WaitFor("Working on the flag.")
	screen := s.WaitGone("Esc to interrupt")
	t.Logf("after the turn:\n%s", screen)
	for _, want := range []string{"Plan  1/3", "✓ Read the config loader", "◐ Add the --json flag", "○ Run the package tests"} {
		if !strings.Contains(screen, want) {
			t.Errorf("screen missing %q", want)
		}
	}
	if strings.Contains(screen, "todowrite") || strings.Contains(screen, "Plan updated") {
		t.Error("todowrite calls should stay out of the transcript")
	}
}
