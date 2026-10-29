package approval

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const cargoE0308 = "   Compiling demo v0.1.0 (D:/tmp/demo)\nerror[E0308]: mismatched types\n --> src/main.rs:2:18\n  |\n2 |     let x: i32 = \"hello\";\n  |            ---   ^^^^^^^ expected `i32`, found `&str`\n  |            |\n  |            expected due to this\n\nFor more information about this error, try `rustc --explain E0308`.\nerror: could not compile `demo` (bin \"demo\") due to 1 previous error\n"

// thinkingReviewer serves an OpenAI-compatible reviewer whose hidden reasoning counts toward
// max_tokens, as deepseek-flash does: reasoning past the budget ends with finish_reason=length.
func thinkingReviewer(t *testing.T, reasoning, modelMaxOut int, content string, alwaysTruncate bool) (*Reviewer, *atomic.Int32) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	calls := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		var req struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if modelMaxOut > 0 && req.MaxTokens > modelMaxOut {
			http.Error(w, `{"error":{"message":"max_tokens exceeds model limit"}}`, http.StatusBadRequest)
			return
		}
		visible := (len(content) + 3) / 4
		finish, text, used := "stop", content, reasoning+visible
		if req.MaxTokens <= 0 || used > req.MaxTokens || alwaysTruncate {
			finish, used = "length", req.MaxTokens
			if reasoning >= req.MaxTokens {
				text = ""
			}
		}
		raw, _ := json.Marshal(text)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":%s,"reasoning_content":"thinking"},"finish_reason":%q}],"usage":{"prompt_tokens":411,"completion_tokens":%d,"completion_tokens_details":{"reasoning_tokens":%d}}}`, raw, finish, used, min(reasoning, used))
	}))
	t.Cleanup(srv.Close)
	cfg := &config.Config{
		Providers:  map[string]config.Provider{"reviewer": {API: "openai-completions", BaseURL: srv.URL + "/v1", APIKey: "test-key", ActiveModels: []string{"deepseek-flash"}}},
		Models:     map[string]config.Model{"deepseek-flash": {Providers: []string{"reviewer"}, MaxOut: modelMaxOut}},
		AIApproval: config.AIApprovalConfig{Mode: "auto", Provider: "reviewer", Model: "deepseek-flash"},
	}
	r := New(t.Context(), cfg)
	if r.unavailable {
		t.Fatal("reviewer unavailable")
	}
	return r, calls
}

func TestThinkingReviewerFitsOutputBudget(t *testing.T) {
	// Observed with deepseek-flash on a cargo E0308 build: up to ~760 completion tokens, mostly reasoning.
	r, calls := thinkingReviewer(t, 700, 0, low, false)
	run := NewRun("auto", "build my rust crate", r, func(string, string, Report, bool) {})
	h := run.Hooks(func(tools.GateRequest) (tools.GateDecision, string) { return tools.GateAllowOnce, "" })
	result := h.After(t.Context(), "Bash", json.RawMessage(`{"command":"cargo build"}`), tools.Result{Text: cargoE0308})
	if calls.Load() != 1 || result.Text != cargoE0308 {
		t.Fatalf("calls=%d output=%q", calls.Load(), result.Text)
	}
}

func TestTruncatedReviewNeverApproves(t *testing.T) {
	// Even a complete low-risk JSON object must not count when the provider reports truncation.
	for name, reasoning := range map[string]int{"complete json": 0, "reasoning only": MaxOutputTokens} {
		t.Run(name, func(t *testing.T) {
			r, calls := thinkingReviewer(t, reasoning, 0, low, true)
			report := r.Review(t.Context(), Evidence{Phase: "tool_output", Tool: "Bash", Action: `{"command":"cargo build"}`, Output: cargoE0308})
			if calls.Load() != 1 || report.Safe() || report.Reason != "Reviewer failed or timed out; manual approval required" {
				t.Fatalf("calls=%d report=%+v", calls.Load(), report)
			}
		})
	}
}

func TestReviewBudgetRespectsModelOutputLimit(t *testing.T) {
	// A reviewer model whose configured output limit is below the review budget kept working at 512.
	r, calls := thinkingReviewer(t, 300, 1024, low, false)
	report := r.Review(t.Context(), Evidence{Phase: "tool_output", Tool: "Bash", Action: `{"command":"cargo build"}`, Output: cargoE0308})
	if calls.Load() != 1 || !report.Safe() {
		t.Fatalf("calls=%d report=%+v", calls.Load(), report)
	}
}
