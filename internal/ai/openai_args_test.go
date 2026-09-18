package ai

import (
	"context"
	"strings"
	"testing"
)

func TestStreamDiscardsToolCallWithInvalidJSONArgs(t *testing.T) {

	srv := sseServer(t,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"w1","type":"function","function":{"name":"write","arguments":"{\"path\":\"/tmp/x\""}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)
	defer srv.Close()

	msg, _, err := New(srv.URL, "test-key").Stream(context.Background(), Request{Model: "m"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 0 {
		t.Fatalf("tool call with invalid JSON arguments must be discarded before history, got %+v", msg.ToolCalls)
	}
	if !strings.Contains(msg.Content, "invalid JSON") {
		t.Fatalf("expected a discard note in content, got %q", msg.Content)
	}
}

func TestStreamDiscardsIncompleteArgsOnDroppedStream(t *testing.T) {
	srv := sseServer(t,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"w1","type":"function","function":{"name":"write","arguments":"{\"path\":\"/tmp/x\""}}]}}]}`,
	)
	defer srv.Close()

	msg, _, err := New(srv.URL, "test-key").Stream(context.Background(), Request{Model: "m"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 0 {
		t.Fatalf("incomplete tool call from a dropped stream must be discarded, got %+v", msg.ToolCalls)
	}
}

func TestStreamKeepsToolCallWithValidJSONArgs(t *testing.T) {
	srv := sseServer(t,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"w1","type":"function","function":{"name":"write","arguments":"{\"path\":\"/tmp/x\""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":",\"content\":\"hi\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)
	defer srv.Close()

	msg, _, err := New(srv.URL, "test-key").Stream(context.Background(), Request{Model: "m"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("valid tool call must be kept, got %+v", msg.ToolCalls)
	}
	tc := msg.ToolCalls[0]
	if tc.Function.Arguments != `{"path":"/tmp/x","content":"hi"}` {
		t.Fatalf("arguments assembly changed: %q", tc.Function.Arguments)
	}
}

func TestStreamDropsOnlyMalformedKeepsValidSibling(t *testing.T) {
	srv := sseServer(t,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"good","type":"function","function":{"name":"read","arguments":"{\"path\":\"/a\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"bad","type":"function","function":{"name":"write","arguments":"{\"path\":\"/b\""}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)
	defer srv.Close()

	msg, _, err := New(srv.URL, "test-key").Stream(context.Background(), Request{Model: "m"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("only the malformed call should be dropped, got %+v", msg.ToolCalls)
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "good" || tc.Function.Name != "read" || tc.Function.Arguments != `{"path":"/a"}` {
		t.Fatalf("valid sibling changed: %+v", tc)
	}
	if !strings.Contains(msg.Content, "write") || !strings.Contains(msg.Content, "invalid JSON") {
		t.Fatalf("discard note should name the dropped call, got %q", msg.Content)
	}
}

func TestStreamKeepsToolCallWithEmptyArgs(t *testing.T) {
	srv := sseServer(t,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"status"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)
	defer srv.Close()

	msg, _, err := New(srv.URL, "test-key").Stream(context.Background(), Request{Model: "m"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "c1" {
		t.Fatalf("no-arg call must be kept, got %+v", msg.ToolCalls)
	}
}
