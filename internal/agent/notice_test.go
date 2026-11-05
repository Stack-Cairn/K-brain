package agent

import (
	"context"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// Notices the runtime delivers on its own are kept apart from what the user wrote:
// the history marks them with ai.OriginBackground, the model request does not carry
// the marker, and user steers stay unmarked.
func TestNoticeIsMarkedBackgroundAndSteerIsNot(t *testing.T) {
	srv := textServer(t, func(n int, req ai.Request) string {
		for _, m := range req.Messages {
			if m.Origin != "" {
				t.Errorf("origin reached the model request: %+v", m)
			}
		}
		return "ok"
	})
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.Notify("[wait w1 done] condition met")
	ag.Steer("also do this")
	if _, err := ag.Turn(context.Background(), "go", Events{}); err != nil {
		t.Fatal(err)
	}
	origins := map[string]string{}
	for _, m := range ag.MessagesSnapshot() {
		if m.Role == "user" {
			origins[m.TextContent()] = m.Origin
		}
	}
	if got := origins["[wait w1 done] condition met"]; got != ai.OriginBackground {
		t.Fatalf("notice origin = %q, want %q (messages %v)", got, ai.OriginBackground, origins)
	}
	if got, ok := origins["also do this"]; !ok || got != "" {
		t.Fatalf("user steer origin = %q (present %v), want unmarked", got, ok)
	}
	if got := origins["go"]; got != "" {
		t.Fatalf("user turn origin = %q, want unmarked", got)
	}
}

// A turn started for a background notice is not authored by the user and carries
// ai.OriginBackground.
func TestBackgroundNoticeTurnIsNotAuthored(t *testing.T) {
	srv := textServer(t, func(int, ai.Request) string { return "ok" })
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	if _, err := ag.TurnParts(WithBackgroundNotice(context.Background()), "[process p1 completed exit_code=0]", nil, Events{}); err != nil {
		t.Fatal(err)
	}
	msgs := ag.MessagesSnapshot()
	var user *ai.Message
	for i := range msgs {
		if msgs[i].Role == "user" {
			user = &msgs[i]
		}
	}
	if user == nil {
		t.Fatal("no user message")
	}
	if user.Authored || user.Origin != ai.OriginBackground {
		t.Fatalf("notice turn message authored=%v origin=%q", user.Authored, user.Origin)
	}
}
