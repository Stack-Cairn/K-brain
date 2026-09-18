package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestOnToolOutputStreamsBash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")

		last := req.Messages[len(req.Messages)-1]
		if last.Role == "tool" {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\n")
		} else {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tc1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo early; sleep 0.3; echo late\"}"}}]}}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.Tools = tools.All()

	var mu sync.Mutex
	var ids []string
	var snaps []string
	final, err := ag.Turn(context.Background(), "go", Events{
		OnToolOutput: func(id, soFar string) {
			mu.Lock()
			ids = append(ids, id)
			snaps = append(snaps, soFar)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if final != "done" {
		t.Fatalf("final: %q", final)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(snaps) == 0 {
		t.Fatal("OnToolOutput never fired for a slow bash call")
	}
	for _, id := range ids {
		if id != "tc1" {
			t.Fatalf("OnToolOutput carried the wrong tool-call id: %q", id)
		}
	}
	if !strings.Contains(strings.Join(snaps, ""), "early") {
		t.Fatalf("snapshots missed the in-flight output: %v", snaps)
	}
}
