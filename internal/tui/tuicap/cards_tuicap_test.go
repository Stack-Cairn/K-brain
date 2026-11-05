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

// In normal mode an edit stops on the approval card, which previews the
// change the tool will make; 1 allows it once and the file changes.
func TestPermissionCardPreviewsEditAndApplies(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{
		Width: 100, Height: 34, Timeout: 20 * time.Second, Args: []string{"--cautious"},
		Replies: []tuicap.Reply{
			tuicap.Calls(tuicap.ToolCall{ID: "call_edit", Name: "edit", Args: jsonArgs(t, map[string]any{
				"path": "greet.go", "old_string": `fmt.Println("hello")`, "new_string": `fmt.Println("hello, world")`,
			})}),
			tuicap.Text("Changed the greeting."),
		},
	})
	src := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
	path := filepath.Join(s.Dir, "greet.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	s.WaitFor("Ask k-brain anything")
	s.Type("fix the greeting")
	s.Press(tuicap.Enter)
	screen := s.WaitFor("Apply this edit?")
	t.Logf("approval card:\n%s", screen)
	for _, want := range []string{
		"greet.go  +1 −1",
		"6  -", `fmt.Println("hello")`, "6  +", `fmt.Println("hello, world")`,
		"Normal mode confirms commands and file changes",
		"1  Yes, once (a)", "2  Yes, always allow Edit(greet.go)", "3  No, and tell the agent why (r)",
		"kept for this project",
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("card missing %q", want)
		}
	}
	if got, _ := os.ReadFile(path); string(got) != src {
		t.Fatal("the file changed before the edit was approved")
	}
	s.Type("1")
	s.WaitFor("Changed the greeting.")
	s.WaitGone("Apply this edit?")
	if got, err := os.ReadFile(path); err != nil || !strings.Contains(string(got), "hello, world") {
		t.Fatalf("the approved edit did not land: %q %v", got, err)
	}
}

// The agent asks several questions at once: the card shows a tab per
// question, takes a typed answer for one, and sends one answer per question.
func TestMultiQuestionCardAnswersEveryQuestion(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{
		Width: 100, Height: 34, Timeout: 20 * time.Second,
		Replies: []tuicap.Reply{
			tuicap.Calls(tuicap.ToolCall{ID: "call_q", Name: "question", Args: jsonArgs(t, map[string]any{
				"questions": []map[string]any{
					{"header": "Format", "question": "Which output format?", "options": []map[string]any{{"label": "Lines"}, {"label": "Array"}}},
					{"header": "Fields", "question": "Which fields?", "multiple": true, "options": []map[string]any{{"label": "name"}, {"label": "status"}}},
				},
			})}),
			tuicap.Text("Thanks, noted."),
		},
	})
	s.WaitFor("Ask k-brain anything")
	s.Type("add json output")
	s.Press(tuicap.Enter)
	screen := s.WaitFor("Which output format?")
	t.Logf("first question:\n%s", screen)
	if !strings.Contains(screen, "Format") || !strings.Contains(screen, "Fields") || !strings.Contains(screen, "Submit") {
		t.Errorf("tab bar missing:\n%s", screen)
	}
	s.Type("2")
	s.WaitFor("Which fields?")
	s.Type("1")
	s.Type("3") // the typed answer
	s.Type("ids")
	s.Press(tuicap.Enter)
	screen = s.WaitFor("Enter submits")
	t.Logf("submit tab:\n%s", screen)
	s.Press(tuicap.Enter)
	s.WaitFor("Thanks, noted.")
	reqs := s.Requests()
	last := reqs[len(reqs)-1]
	for _, want := range []string{"Which output format?", "Array", "name, ids"} {
		if !last.Contains(want) {
			t.Errorf("the answer sent to the model is missing %q", want)
		}
	}
}

// In plan mode the agent submits its plan for review; a line comment and a
// note send it back for changes, and approving the revised plan leaves plan
// mode for the mode it was entered from.
func TestPlanReviewCard(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{
		Width: 100, Height: 34, Timeout: 20 * time.Second, Args: []string{"--cautious"},
		Replies: []tuicap.Reply{
			tuicap.Calls(tuicap.ToolCall{ID: "call_p1", Name: "ExitPlanMode", Args: jsonArgs(t, map[string]any{
				"plan": "# Add --json\n1. Add the flag to cmd/kn/status.go\n2. Encode with encoding/json\n3. Run the tests",
			})}),
			tuicap.Calls(tuicap.ToolCall{ID: "call_p2", Name: "ExitPlanMode", Args: jsonArgs(t, map[string]any{
				"plan": "# Add --json (v2)\n1. Add the flag\n2. Keep text output as the default\n3. Run the tests",
			})}),
			tuicap.Text("Implementing the plan."),
		},
	})
	s.WaitFor("Ask k-brain anything")
	s.Press(tuicap.ShiftTab)
	s.WaitFor("read-only")
	s.Type("plan the json flag")
	s.Press(tuicap.Enter)
	s.WaitFor("Plan ready for review")
	s.Press(tuicap.Down, tuicap.Down)
	s.Type("c")
	s.Type("keep the text output")
	s.Press(tuicap.Enter)
	screen := s.WaitFor("1 line comment is sent")
	t.Logf("plan card with a line comment:\n%s", screen)
	s.Type("2")
	s.Type("smaller steps please")
	s.Press(tuicap.Enter)
	screen = s.WaitFor("(v2)")
	t.Logf("revised plan:\n%s", screen)
	s.Type("1")
	s.WaitFor("Implementing the plan.")
	screen = s.WaitGone("read-only")
	if !strings.Contains(screen, "Normal") {
		t.Errorf("approving returns to normal mode:\n%s", screen)
	}
	var sawFeedback bool
	for _, r := range s.Requests() {
		if r.Contains("smaller steps please") && r.Contains("keep the text output") {
			sawFeedback = true
		}
	}
	if !sawFeedback {
		t.Error("the requested changes and the line comment did not reach the model")
	}
}
