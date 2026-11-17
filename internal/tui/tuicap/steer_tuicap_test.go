//go:build tuicap

package tuicap_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/tui/tuicap"
)

// A message typed while a tool call runs waits in the queue until the call
// ends, then joins the same turn: the next request carries it right after the
// tool result, the transcript shows it as steered, and the turn does not run
// a second time for it.
func TestQueuedMessageJoinsTurnAfterToolCall(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{
		Width: 100, Height: 30, Timeout: 30 * time.Second,
		Replies: []tuicap.Reply{
			tuicap.Calls(tuicap.ToolCall{ID: "call_slow", Name: "bash", Args: jsonArgs(t, map[string]any{
				"command": "sleep 2; echo slow-done",
			})}),
			tuicap.Text("Switched to the second approach."),
		},
	})
	s.WaitFor("Ask k-brain anything")
	s.Type("start")
	s.Press(tuicap.Enter)
	s.WaitFor("sleep 2")
	s.Type("use the second approach")
	s.Press(tuicap.Enter)
	queued := s.WaitFor("queued (1)")
	if !strings.Contains(queued, "sent after the current tool call") {
		t.Errorf("the queue does not say when it is sent:\n%s", queued)
	}

	screen := s.WaitFor("Switched to the second approach.")
	s.WaitGone("Esc to interrupt")
	t.Logf("after the turn:\n%s", screen)
	if !strings.Contains(screen, "use the second approach  (steered)") {
		t.Errorf("the message is not shown as steered into the turn:\n%s", screen)
	}
	if strings.Contains(screen, "queued (") {
		t.Errorf("the message is still queued:\n%s", screen)
	}

	reqs := s.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want 2 (the tool round and its follow-up)", len(reqs))
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(reqs[1].Body), &body); err != nil {
		t.Fatal(err)
	}
	n := len(body.Messages)
	if n < 2 || body.Messages[n-2].Role != "tool" || body.Messages[n-1].Role != "user" ||
		!strings.Contains(stringify(body.Messages[n-1].Content), "use the second approach") {
		t.Fatalf("the follow-up request does not end with the tool result and the queued message: %+v", body.Messages)
	}
}

func stringify(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
