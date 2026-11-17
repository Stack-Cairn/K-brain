package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// AtToolBoundary runs once per round of tool calls, after the results and
// before the steered messages are read, so a message handed over there
// reaches the model in the very next request, right after the tool results.
func TestAtToolBoundarySteersIntoTheNextRequest(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		call++
		w.Header().Set("Content-Type", "text/event-stream")
		switch call {
		case 1:
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"no_such_tool","arguments":"{}"}}]}}]}`+"\n\n")
		case 2:
			n := len(req.Messages)
			if n < 2 || req.Messages[n-2].Role != "tool" || req.Messages[n-1].Role != "user" || req.Messages[n-1].Content != "typed during the call" {
				t.Errorf("the steered message does not follow the tool result: %+v", req.Messages)
			}
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"}}]}`+"\n\n")
		default:
			t.Errorf("unexpected request %d", call)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	boundaries := 0
	var steered []string
	final, err := ag.Turn(context.Background(), "go", Events{
		AtToolBoundary: func() {
			boundaries++
			if boundaries == 1 {
				ag.Steer("typed during the call")
			}
		},
		OnSteer: func(s string) { steered = append(steered, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if final != "done" || call != 2 {
		t.Fatalf("final %q after %d requests", final, call)
	}
	if boundaries != 1 {
		t.Errorf("AtToolBoundary ran %d times, want once (one round of tool calls)", boundaries)
	}
	if len(steered) != 1 || steered[0] != "typed during the call" {
		t.Errorf("OnSteer: %v", steered)
	}
}

// SteerImages with no turn running goes to OnOrphanedSteer like Steer, rather
// than waiting in pending for whatever turn comes next.
func TestSteerImagesOnIdleAgentFiresOrphanHook(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	var got []string
	ag.OnOrphanedSteer = func(s string) { got = append(got, s) }
	ag.SteerImages("see this", []ai.ContentPart{ai.ImagePart("png", []byte("x"))})
	if len(got) != 1 || got[0] != "see this" {
		t.Fatalf("orphan hook got %v", got)
	}
	if p := ag.drainPending(); len(p) != 0 {
		t.Fatalf("left in pending: %+v", p)
	}
}
