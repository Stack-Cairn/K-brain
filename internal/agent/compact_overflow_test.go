package agent

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// overflowServer rejects any request body larger than maxBody with the relay
// error users hit in the wild, and otherwise answers with a numbered summary.
type overflowServer struct {
	maxBody  int
	errorMsg string

	mu       sync.Mutex
	accepted []string // bodies of accepted requests, in order
	rejected int
}

func (s *overflowServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		body := string(raw)
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(body) > s.maxBody {
			s.rejected++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":{"message":%q,"type":"bad_response_status_code","param":"","code":"bad_response_status_code"}}`, s.errorMsg)
			return
		}
		s.accepted = append(s.accepted, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":"summary-%d"},"finish_reason":"stop"}]}`, len(s.accepted))
	}
}

const relayOverflow = "Failed to start sampling: [input_too_large] The prompt is too long for this model's context window (664630 tokens > 500000 tokens)"

func TestIsContextLimitRecognizesRelayOverflow(t *testing.T) {
	err := &ai.HTTPError{Status: "400 Bad Request", StatusCode: 400,
		Body: `{"error":{"message":"` + relayOverflow + `","type":"bad_response_status_code"}}`}
	if !ai.IsContextLimit(err) {
		t.Fatal("relay input_too_large error must count as a context-limit error")
	}
	if got := ai.ContextLimitFromError(err); got != 500000 {
		t.Fatalf("ContextLimitFromError = %d, want 500000", got)
	}
	anthropic := &ai.HTTPError{Status: "400 Bad Request", StatusCode: 400,
		Body: `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`}
	if got := ai.ContextLimitFromError(anthropic); got != 200000 {
		t.Fatalf("Anthropic form: got %d, want 200000", got)
	}
	openai := &ai.HTTPError{Status: "400 Bad Request", StatusCode: 400,
		Body: `{"error":{"message":"This model's maximum context length is 128000 tokens. However, you requested 130000 tokens.","code":"context_length_exceeded"}}`}
	if got := ai.ContextLimitFromError(openai); got != 128000 {
		t.Fatalf("OpenAI form: got %d, want 128000", got)
	}
	if got := ai.ContextLimitFromError(&ai.HTTPError{Status: "400 Bad Request", StatusCode: 400, Body: "bad tool schema"}); got != 0 {
		t.Fatalf("unrelated 400 reported a window of %d", got)
	}
}

// The catalog advertises a 1M window but the endpoint accepts 500K: the cached
// same-route summary request is rejected. Compaction must fall back to the
// bounded transcript prompt, succeed, and adopt the endpoint's real window.
func TestCompactionFallsBackWhenCachedSummaryOverflows(t *testing.T) {
	srv := &overflowServer{maxBody: 60_000, errorMsg: relayOverflow}
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	a := New(ai.New(server.URL, "fixture"), "fixture", 100, "stable")
	a.Tools = []tools.Tool{echoTool()}
	a.ContextLimit = 1_000_000
	for i := range 4 {
		a.Messages = append(a.Messages,
			ai.Message{Role: "user", Content: fmt.Sprintf("turn-%d ", i) + strings.Repeat("历史记录 ", 6000)},
			ai.Message{Role: "assistant", Content: "answer"})
	}

	summary, _, _, err := a.compact(t.Context())
	if err != nil {
		t.Fatalf("compaction failed instead of falling back: %v", err)
	}
	if srv.rejected != 1 {
		t.Fatalf("rejected = %d, want exactly the one cached attempt", srv.rejected)
	}
	if a.ContextLimit != 500000 {
		t.Fatalf("ContextLimit = %d, want the endpoint's 500000", a.ContextLimit)
	}
	if summary != fmt.Sprintf("summary-%d", len(srv.accepted)) {
		t.Fatalf("summary = %q, want the last accepted response", summary)
	}
	last := srv.accepted[len(srv.accepted)-1]
	if strings.Contains(last, `"tools"`) {
		t.Fatal("fallback must use the plain transcript prompt, not the cached prefix with tools")
	}
}

// A dedicated compaction model with an unknown window gets an oversized
// transcript rejected. It must re-chunk to the reported window and fold every
// chunk into the running summary, in order, without losing history.
func TestTranscriptSummaryChunksAfterOverflow(t *testing.T) {
	srv := &overflowServer{maxBody: 20_000, errorMsg: "prompt is too long: 30000 tokens > 12000 maximum"}
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	a := New(ai.New(server.URL, "fixture"), "fixture", 100, "stable")
	a.CompactClient = ai.New(server.URL, "fixture")
	// A small main window keeps the verbatim tail short, so most turns land in
	// the history being summarized. The dedicated model's window stays unknown.
	a.ContextLimit = 8000
	const turns = 30
	for i := range turns {
		a.Messages = append(a.Messages,
			ai.Message{Role: "user", Content: fmt.Sprintf("marker-%02d ", i) + strings.Repeat("x", 1500)},
			ai.Message{Role: "assistant", Content: "ok"})
	}
	summary, cutoff, _, err := a.compact(t.Context())
	if err != nil {
		t.Fatalf("chunked compaction failed: %v", err)
	}
	if srv.rejected == 0 {
		t.Fatal("fixture should have rejected the first, unchunked request")
	}
	if len(srv.accepted) < 2 {
		t.Fatalf("accepted %d requests, want the history split into several chunks", len(srv.accepted))
	}
	if summary != fmt.Sprintf("summary-%d", len(srv.accepted)) {
		t.Fatalf("summary = %q, want the final folded summary", summary)
	}
	// Each chunk after the first must carry the previous chunk's summary.
	for i := 1; i < len(srv.accepted); i++ {
		if !strings.Contains(srv.accepted[i], fmt.Sprintf("summary-%d", i)) {
			t.Fatalf("chunk %d does not merge the running summary from chunk %d", i+1, i)
		}
	}
	// Every history turn (messages 1..cutoff-1, two per turn) reaches the
	// summarizer exactly once, in order; the verbatim tail is not summarized.
	historyTurns := (cutoff - 1) / 2
	if historyTurns < 10 {
		t.Fatalf("only %d turns in history; fixture too small to exercise chunking", historyTurns)
	}
	all := strings.Join(srv.accepted, "\n")
	if strings.Contains(all, fmt.Sprintf("marker-%02d", turns-1)) {
		t.Fatal("the verbatim tail was sent to the summarizer")
	}
	lastIndex := -1
	for i := range historyTurns {
		marker := fmt.Sprintf("marker-%02d", i)
		if n := strings.Count(all, marker); n != 1 {
			t.Fatalf("%s sent %d times, want 1", marker, n)
		}
		idx := strings.Index(all, marker)
		if idx < lastIndex {
			t.Fatalf("%s summarized out of order", marker)
		}
		lastIndex = idx
	}
}

func TestTruncateFieldKeepsUTF8Valid(t *testing.T) {
	s := strings.Repeat("压缩摘要", 400)
	for _, n := range []int{2, 3, 4, 5, 100, 2000} {
		got := truncateField(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("truncateField(_, %d) produced invalid UTF-8", n)
		}
		if len(got) > n+len("…") {
			t.Fatalf("truncateField(_, %d) returned %d bytes", n, len(got))
		}
	}
}
