package ai

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// A call whose arguments are not valid JSON is kept, marked BadArgs with an
// excerpt of what was sent and with replayable "{}" arguments, so the agent
// can answer it with an error the model can act on. Dropping it ended the
// turn silently.
func TestStreamKeepsToolCallWithInvalidJSONArgsAsBadArgs(t *testing.T) {
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
	if len(msg.ToolCalls) != 1 || msg.StopReason != StopReasonToolUse {
		t.Fatalf("malformed call must be kept for an error reply, got %+v (stop %q)", msg.ToolCalls, msg.StopReason)
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "w1" || tc.Function.Arguments != "{}" || tc.BadArgs != `{"path":"/tmp/x"` {
		t.Fatalf("malformed call: %+v", tc)
	}
	if strings.Contains(msg.Content, "invalid JSON") {
		t.Fatalf("no note in the reply text, got %q", msg.Content)
	}
	if got := stripAuthored([]Message{msg})[0].ToolCalls[0].BadArgs; got != "" {
		t.Fatalf("requests must not carry BadArgs, got %q", got)
	}
}

func TestStreamDiscardsIncompleteArgsOnDroppedStream(t *testing.T) {
	srv := sseServer(t,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"w1","type":"function","function":{"name":"write","arguments":"{\"path\":\"/tmp/x\""}}]}}]}`,
	)
	defer srv.Close()

	msg, _, err := New(srv.URL, "test-key").Stream(context.Background(), Request{Model: "m"}, nil, nil, nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected premature EOF, got %v", err)
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

func TestStreamMarksOnlyMalformedKeepsValidSibling(t *testing.T) {
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
	if len(msg.ToolCalls) != 2 {
		t.Fatalf("both calls should be kept, got %+v", msg.ToolCalls)
	}
	good, bad := msg.ToolCalls[0], msg.ToolCalls[1]
	if good.ID != "good" || good.Function.Arguments != `{"path":"/a"}` || good.BadArgs != "" {
		t.Fatalf("valid sibling changed: %+v", good)
	}
	if bad.ID != "bad" || bad.Function.Name != "write" || bad.BadArgs == "" || bad.Function.Arguments != "{}" {
		t.Fatalf("malformed sibling: %+v", bad)
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
