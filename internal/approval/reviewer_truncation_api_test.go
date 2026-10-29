package approval

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// Each provider reports that the reviewer hit its output limit, yet the visible text is a
// complete low-risk object (e.g. the cut landed on the trailing newline after "}"). The
// change claims a truncated review always fails closed; check every non-chat API path.
func TestTruncatedReviewFailsClosedOnEveryAPI(t *testing.T) {
	body := func(api string) string {
		text, _ := json.Marshal(low + "\n")
		switch api {
		case "anthropic-messages":
			return fmt.Sprintf(`{"type":"message","role":"assistant","content":[{"type":"text","text":%s}],"stop_reason":"max_tokens","usage":{"input_tokens":400,"output_tokens":4096}}`, text)
		case "openai-responses":
			return fmt.Sprintf(`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":%s}]}],"usage":{"input_tokens":400,"output_tokens":4096}}`, text)
		default:
			return fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"text":%s}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":400,"candidatesTokenCount":40,"thoughtsTokenCount":4056}}`, text)
		}
	}
	for _, api := range []string{"anthropic-messages", "openai-responses", "google-gemini"} {
		t.Run(api, func(t *testing.T) {
			t.Setenv("K_BRAIN_HOME", t.TempDir())
			calls := new(atomic.Int32)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "streamGenerateContent") || r.URL.Path == "/v1/models" {
					http.NotFound(w, r)
					return
				}
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body(api)))
			}))
			t.Cleanup(srv.Close)
			cfg := &config.Config{
				Providers:  map[string]config.Provider{"reviewer": {API: api, BaseURL: srv.URL + "/v1", APIKey: "test-key", ActiveModels: []string{"review-model"}}},
				Models:     map[string]config.Model{"review-model": {Providers: []string{"reviewer"}}},
				AIApproval: config.AIApprovalConfig{Mode: "auto", Provider: "reviewer", Model: "review-model"},
			}
			r := New(t.Context(), cfg)
			report := r.Review(t.Context(), Evidence{Phase: "tool_output", Tool: "Bash", Action: `{"command":"cargo build"}`, Output: cargoE0308})
			if calls.Load() == 0 {
				t.Fatalf("provider never called: %+v", report)
			}
			if report.Safe() {
				t.Fatalf("truncated %s review approved: %+v", api, report)
			}
		})
	}
}
