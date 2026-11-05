package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestPromptContextRemainsBackendOwned(t *testing.T) {
	m := ai.Message{Role: "user", Content: "visible question", PromptContext: "private request snapshot",
		PromptSnapshots: []ai.PromptSnapshot{{Source: "runtime-memory", Text: "owned snapshot"}}}
	canonical := FromAIMessage(m)
	encoded, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private request snapshot") || strings.Contains(string(encoded), "prompt_context") || strings.Contains(string(encoded), "owned snapshot") || strings.Contains(string(encoded), "prompt_snapshots") {
		t.Fatal("request context leaked into public history")
	}
	converted, err := canonical.ToAIMessage()
	if err != nil || converted.Content != m.Content || converted.PromptContext != "" {
		t.Fatalf("unexpected history projection: %+v %v", converted, err)
	}
}

func TestMessageRoundTripPreservesCanonicalSemantics(t *testing.T) {
	args := json.RawMessage(`{"command":"printf hi"}`)
	image := "https://example.test/image.png"
	original := ai.Message{
		Role:    "assistant",
		Content: "I will inspect the file.",
		Parts: []ai.ContentPart{{Type: "image_url", ImageURL: &struct {
			URL string `json:"url"`
		}{URL: image}}},
		ToolCalls: []ai.ToolCall{{ID: "call-1", Type: "function", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "bash", Arguments: args.String()}}},
		ToolCallID: "",
		Name:       "assistant",
		Model:      "gemini-3-flash",
		StopReason: ai.StopReasonToolUse,
	}

	canonical := FromAIMessage(original)
	if len(canonical.Content) != 2 || canonical.Content[0].Text != original.Content || canonical.Content[1].ImageURL != image {
		t.Fatalf("canonical content = %+v", canonical.Content)
	}
	if len(canonical.ToolCalls) != 1 || string(canonical.ToolCalls[0].Arguments) != args.String() {
		t.Fatalf("canonical tool calls = %+v", canonical.ToolCalls)
	}
	converted, err := canonical.ToAIMessage()
	if err != nil {
		t.Fatal(err)
	}
	if converted.Content != original.Content || converted.Model != original.Model || converted.StopReason != original.StopReason {
		t.Fatalf("converted message = %+v", converted)
	}
	if !reflect.DeepEqual(converted.Parts, original.Parts) || !reflect.DeepEqual(converted.ToolCalls, original.ToolCalls) {
		t.Fatalf("converted message lost structured fields: got %+v want %+v", converted, original)
	}
}

func TestEventVersionAndRequiredIdentity(t *testing.T) {
	e, err := NewEvent(1, "conversation-1", "run-1", EventTextDelta, TextDelta{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	if e.Version != Version || e.Seq != 1 || e.Type != EventTextDelta {
		t.Fatalf("event = %+v", e)
	}

	for _, tc := range []struct {
		name string
		e    Event
	}{
		{name: "wrong version", e: Event{Version: "v0", Seq: 1, ConversationID: "c", RunID: "r", Type: EventRunAccepted}},
		{name: "missing sequence", e: Event{Version: Version, ConversationID: "c", RunID: "r", Type: EventRunAccepted}},
		{name: "missing conversation", e: Event{Version: Version, Seq: 1, RunID: "r", Type: EventRunAccepted}},
		{name: "missing run", e: Event{Version: Version, Seq: 1, ConversationID: "c", Type: EventRunAccepted}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.e.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestPermissionAndSubagentPayloadsAreJSONStable(t *testing.T) {
	e, err := NewEvent(2, "conversation-1", "run-1", EventPermissionRequest, PermissionRequest{
		PermissionID: "permission-1",
		Tool:         "bash",
		Command:      "go test ./...",
		Options:      []PermissionOption{{ID: "allow_once", Label: "Allow once", Kind: "allow_once"}, {ID: "reject", Label: "Reject", Kind: "reject_once"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		PermissionID string             `json:"permission_id"`
		Tool         string             `json:"tool"`
		Options      []PermissionOption `json:"options"`
	}
	if err := json.Unmarshal(e.Payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.PermissionID != "permission-1" || decoded.Tool != "bash" || len(decoded.Options) != 2 {
		t.Fatalf("decoded permission = %+v", decoded)
	}

	task := Subagent{ID: "task-1", ParentID: "run-1", Description: "inspect", Status: "running", Model: ModelRef{Provider: "openai", Model: "gpt"}}
	if _, err := NewEvent(3, "conversation-1", "run-1", EventSubagentStarted, SubagentEvent{Subagent: task}); err != nil {
		t.Fatal(err)
	}
}

func TestMessageOriginOnlyMarksUserNotices(t *testing.T) {
	notice := FromAIMessage(ai.Message{Role: "user", Content: "[wait w1 done] condition met", Origin: ai.OriginBackground})
	if notice.Origin != ai.OriginBackground {
		t.Fatalf("protocol origin = %q", notice.Origin)
	}
	back, err := notice.ToAIMessage()
	if err != nil {
		t.Fatal(err)
	}
	if back.Origin != ai.OriginBackground {
		t.Fatalf("ai origin = %q", back.Origin)
	}
	for _, m := range []Message{
		{Role: RoleUser, Origin: "something-else", Content: []ContentBlock{{Type: ContentText, Text: "x"}}},
		{Role: RoleAssistant, Origin: ai.OriginBackground, Content: []ContentBlock{{Type: ContentText, Text: "x"}}},
	} {
		out, err := m.ToAIMessage()
		if err != nil {
			t.Fatal(err)
		}
		if out.Origin != "" {
			t.Fatalf("origin %q on %s must not be stored", out.Origin, m.Role)
		}
	}
}
