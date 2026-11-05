package ai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamTextAppendsAndFollowsOverwrites(t *testing.T) {
	var acc streamText
	field := ""
	for _, delta := range []string{"a", "bc", "", "def"} {
		field = acc.append(field, delta)
	}
	if field != "abcdef" {
		t.Fatalf("field = %q", field)
	}
	kept := field
	field = acc.append(field, "g")
	if kept != "abcdef" || field != "abcdefg" {
		t.Fatalf("earlier value changed: kept=%q field=%q", kept, field)
	}
	// The field was set elsewhere (e.g. a final "done" event): continue from it.
	field = "{\"x\":"
	field = acc.append(field, "1}")
	if field != "{\"x\":1}" {
		t.Fatalf("after overwrite field = %q", field)
	}
	// An equal string held in other memory also restarts from the field's value.
	field = acc.append(strings.Clone(field), "!")
	if field != "{\"x\":1}!" {
		t.Fatalf("after copy field = %q", field)
	}
}

func TestChatCompletionsStreamAccumulatesTextAndArguments(t *testing.T) {
	const n = 300
	body := chatCompletionsStream(n)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	var text strings.Builder
	var seen []string
	msg, _, err := New(srv.URL, "test-key").Stream(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}},
		func(d string) { text.WriteString(d) }, nil, func(_, _, args string) { seen = append(seen, args) })
	if err != nil {
		t.Fatal(err)
	}
	var wantText, wantArgs strings.Builder
	wantArgs.WriteString(`{"content":"`)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&wantText, "tok%d ", i%10)
		fmt.Fprintf(&wantArgs, "line%d ", i%10)
	}
	wantArgs.WriteString(`"}`)
	if msg.Content != wantText.String() || text.String() != wantText.String() {
		t.Fatalf("content mismatch: %d bytes, want %d", len(msg.Content), wantText.Len())
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Arguments != wantArgs.String() {
		t.Fatalf("tool calls = %+v", msg.ToolCalls)
	}
	// Each callback saw the arguments so far, and later deltas did not alter them.
	if len(seen) != n+2 {
		t.Fatalf("got %d tool callbacks, want %d", len(seen), n+2)
	}
	for i, args := range seen {
		if !strings.HasPrefix(wantArgs.String(), args) || (i > 0 && len(args) <= len(seen[i-1])) {
			t.Fatalf("callback %d args %q is not a growing prefix", i, args)
		}
	}
}

// chatCompletionsStream is a Chat Completions SSE body of n text deltas, then a tool
// call whose arguments arrive in n pieces.
func chatCompletionsStream(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "data: {\"choices\":[{\"delta\":{\"content\":\"tok%d \"}}]}\n\n", i%10)
	}
	fmt.Fprintf(&b, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"Write\",\"arguments\":\"{\\\"content\\\":\\\"\"}}]}}]}\n\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"line%d \"}}]}}]}\n\n", i%10)
	}
	fmt.Fprintf(&b, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	return b.Bytes()
}

func BenchmarkChatCompletionsStreamAccumulate(b *testing.B) {
	for _, n := range []int{2000, 16000} {
		b.Run(fmt.Sprintf("deltas=%d", n), func(b *testing.B) {
			body := chatCompletionsStream(n)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write(body)
			}))
			defer srv.Close()
			c := New(srv.URL, "test-key")
			req := Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				msg, _, err := c.Stream(context.Background(), req, func(string) {}, nil, func(string, string, string) {})
				if err != nil || len(msg.ToolCalls) != 1 {
					b.Fatalf("err=%v calls=%d", err, len(msg.ToolCalls))
				}
			}
		})
	}
}
