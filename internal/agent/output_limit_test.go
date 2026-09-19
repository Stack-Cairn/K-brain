package agent

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestTruncatedCompactionPreservesHistory(t *testing.T) {
	for protocol, body := range map[string]string{
		"chat":      `{"choices":[{"message":{"content":"partial summary"},"finish_reason":"length"}],"usage":{"prompt_tokens":17,"completion_tokens":3}}`,
		"responses": `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"text":"partial summary"}]}],"usage":{"input_tokens":17,"output_tokens":3}}`,
		"anthropic": `{"content":[{"type":"text","text":"partial summary"}],"stop_reason":"max_tokens","usage":{"input_tokens":17,"output_tokens":3}}`,
	} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, body) }))
			defer srv.Close()
			var client ai.Client
			switch protocol {
			case "chat":
				client = ai.New(srv.URL, "key")
			case "responses":
				client = ai.NewResponses(srv.URL, "key")
			default:
				client = ai.NewAnthropic(srv.URL, "key")
			}
			ag := New(client, "model", 100, "system")
			ag.Provider = "provider"
			ag.Messages = append(ag.Messages, ai.Message{Role: "user", Content: "first"}, ai.Message{Role: "assistant", Content: "answer"}, ai.Message{Role: "user", Content: "follow up"}, ai.Message{Role: "assistant", Content: "latest", StopReason: ai.StopReasonLength})
			before := ag.MessagesSnapshot()
			compacted := false
			err := ag.ManualCompact(t.Context(), Events{OnCompacted: func(string, int, CompactInfo) { compacted = true }})
			var limit *ai.OutputLimitError
			if !errors.As(err, &limit) || compacted || calls.Load() != 1 || !reflect.DeepEqual(before, ag.MessagesSnapshot()) {
				t.Fatalf("compaction changed history or retried: calls=%d compacted=%v err=%v", calls.Load(), compacted, err)
			}
			u := ag.ModelUsage()["model @ provider"]
			if u.PromptTokens != 17 || u.CompletionTokens != 3 || ag.LastStopReason() != ai.StopReasonLength {
				t.Fatalf("lost usage/stop reason: %+v", u)
			}
		})
	}
}
