package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// The runtime writes the "[goal n/m] …" prompt of every continuation round, so the
// round's user message is a background notice — live, in history and in the stored
// transcript — and stays out of prompt recall. The /goal command the user typed stays
// an ordinary, authored user message.
func TestGoalContinuationRoundsAreBackgroundNotices(t *testing.T) {
	client := &replyQueue{replies: []string{"first pass", "more work", "GOAL_MET — shipped"}}
	store, base := newGoalServer(t, client)
	sess := createTestSession(t, base)

	if status, state := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "ship the feature", Run: &protocol.PromptRequest{ClientRequestID: "goal-origin-1"}}); status != http.StatusOK || state.Run == nil {
		t.Fatalf("set: %d %+v", status, state)
	}
	waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })

	var live []protocol.Message
	for _, e := range allSessionEvents(t, base, sess.ID) {
		if e.Type != protocol.EventUserMessage {
			continue
		}
		var msg protocol.Message
		if err := json.Unmarshal(e.Payload, &msg); err != nil {
			t.Fatal(err)
		}
		live = append(live, msg)
	}
	assertGoalRoundOrigins(t, "live", live)

	var page historyResponse
	if code := doJSON(t, http.MethodGet, base+"/v1/sessions/"+sess.ID+"/history", nil, &page); code != http.StatusOK {
		t.Fatalf("history status=%d", code)
	}
	assertGoalRoundOrigins(t, "history", page.Session.Messages)

	_, stored, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range stored {
		if m.Role != "user" {
			continue
		}
		round := strings.HasPrefix(m.Content, "[goal ")
		if round && (m.Authored || m.Origin != ai.OriginBackground) {
			t.Fatalf("stored goal round authored=%v origin=%q", m.Authored, m.Origin)
		}
		if !round && (!m.Authored || m.Origin != "") {
			t.Fatalf("stored /goal command authored=%v origin=%q: %q", m.Authored, m.Origin, m.Content)
		}
	}
	recalled, err := store.UserHistory(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recalled) != 1 || recalled[0] != "/goal ship the feature" {
		t.Fatalf("prompt recall must hold only the typed command: %q", recalled)
	}
}

// allSessionEvents reads the session's event log across runs; one events stream ends
// with the run it follows.
func allSessionEvents(t *testing.T, base, id string) []protocol.Event {
	t.Helper()
	var all []protocol.Event
	after := int64(0)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/sessions/"+id+"/events?after_seq="+strconv.FormatInt(after, 10), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		batch := readSSEUntil(ctx, resp)
		cancel()
		if len(batch) == 0 {
			return all
		}
		all = append(all, batch...)
		after = batch[len(batch)-1].Seq
	}
}

func assertGoalRoundOrigins(t *testing.T, where string, messages []protocol.Message) {
	t.Helper()
	var commands, rounds int
	for _, m := range messages {
		if m.Role != protocol.RoleUser {
			continue
		}
		var text strings.Builder
		for _, block := range m.Content {
			text.WriteString(block.Text)
		}
		switch {
		case text.String() == "/goal ship the feature":
			commands++
			if m.Origin != "" {
				t.Fatalf("%s: typed /goal command origin = %q", where, m.Origin)
			}
		case strings.HasPrefix(text.String(), "[goal ") && strings.HasSuffix(text.String(), "] ship the feature"):
			rounds++
			if m.Origin != ai.OriginBackground {
				t.Fatalf("%s: goal round %q origin = %q", where, text.String(), m.Origin)
			}
		default:
			t.Fatalf("%s: unexpected user message %q", where, text.String())
		}
	}
	if commands != 1 || rounds != 2 {
		t.Fatalf("%s: commands=%d rounds=%d", where, commands, rounds)
	}
}
