package agent

import (
	"context"
	"strings"
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

// The new-context notice and the output-limit continuation reminder are written by
// the agent, not the user: the history marks them with ai.OriginBackground so clients
// do not show them as user messages, and the marker stays off the model request.
func TestAgentWrittenUserMessagesAreBackgroundNotices(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	srv, _ := lengthServer(t, func(n int, req ai.Request) []string {
		for _, m := range req.Messages {
			if m.Origin != "" {
				t.Errorf("origin reached the model request: %+v", m)
			}
		}
		if n == 1 {
			return []string{truncatedTextFrame}
		}
		return []string{finishedTextFrame}
	})
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys", WithOutputLimitContinuation())
	ag.Messages = append(ag.Messages, ai.Message{Role: "user", Content: "original task", Authored: true})
	ag.resetContextMessages(true)
	if _, err := ag.TurnAuthored(t.Context(), "write a lot", Events{}); err != nil {
		t.Fatal(err)
	}
	var notice, reminder, typed bool
	for _, m := range ag.MessagesSnapshot() {
		if m.Role != "user" {
			continue
		}
		switch text := m.TextContent(); {
		case strings.HasPrefix(text, "A new context window is active"):
			notice = true
			if m.Origin != ai.OriginBackground || m.Authored {
				t.Fatalf("new-context notice origin=%q authored=%v", m.Origin, m.Authored)
			}
		case strings.Contains(text, "<"+ReminderTag+">"):
			reminder = true
			if m.Origin != ai.OriginBackground || m.Authored {
				t.Fatalf("output-limit reminder origin=%q authored=%v", m.Origin, m.Authored)
			}
		case text == "write a lot":
			typed = true
			if m.Origin != "" || !m.Authored {
				t.Fatalf("user turn origin=%q authored=%v", m.Origin, m.Authored)
			}
		}
	}
	if !notice || !reminder || !typed {
		t.Fatalf("notice=%v reminder=%v typed=%v", notice, reminder, typed)
	}
}

// The notice mark belongs to the turn's own user message: a subagent the notice turn
// starts writes its prompt as an ordinary message.
func TestBackgroundNoticeDoesNotReachSubagents(t *testing.T) {
	client := &recordingClient{}
	parent := New(client, "m", 100, "sys")
	parent.WorkingDir = t.TempDir()
	if _, err := parent.TurnParts(WithBackgroundNotice(context.Background()), "[goal 2/5] list the files", nil, Events{}); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	parentMarked, subagentRan := false, false
	for _, request := range client.seen {
		for _, m := range request {
			switch {
			case m.Role != "user":
			case strings.HasPrefix(m.Content, "[goal 2/5]"):
				parentMarked = parentMarked || m.Origin == ai.OriginBackground
			case strings.HasPrefix(m.Content, "list files"):
				subagentRan = true
				if m.Origin != "" {
					t.Fatalf("subagent prompt inherited the notice mark: origin=%q", m.Origin)
				}
			}
		}
	}
	if !parentMarked || !subagentRan {
		t.Fatalf("parent marked=%v, subagent ran=%v", parentMarked, subagentRan)
	}
}
