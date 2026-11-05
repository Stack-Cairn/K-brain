package ai

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestRequestHistoryScopesReplayWithoutChangingStoredHistory(t *testing.T) {
	for _, source := range []string{APIChatCompletions, APIResponses, APIMessages, APIGemini} {
		messages := []Message{{Role: "user", Content: "question"}, {Role: "assistant", Content: "answer", Replay: &ProviderReplay{API: source, Endpoint: "https://origin.test", Model: "m", Blocks: []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"private"}`)}}}}
		before, _ := json.Marshal(messages)
		for _, target := range []string{APIChatCompletions, APIResponses, APIMessages, APIGemini} {
			out := prepareRequestHistory(messages, target, "https://origin.test", "m")
			if (out[1].Replay != nil) != (target == source) {
				t.Fatalf("replay crossed protocol %s -> %s", source, target)
			}
			if prepareRequestHistory(messages, target, "https://other.test", "m")[1].Replay != nil || prepareRequestHistory(messages, target, "https://origin.test", "other")[1].Replay != nil {
				t.Fatal("replay crossed endpoint/model")
			}
		}
		after, _ := json.Marshal(messages)
		if string(before) != string(after) {
			t.Fatal("stored history mutated")
		}
	}
}

func TestLateDuplicateToolResultsAreContextNotUnpairedCalls(t *testing.T) {
	call := ToolCall{ID: "call"}
	call.Function.Name = "lookup"
	messages := []Message{{Role: "assistant", ToolCalls: []ToolCall{call}}, {Role: "tool", ToolCallID: "call", Content: "first"}, {Role: "user", Content: "next"}, {Role: "tool", ToolCallID: "call", Content: "late"}}
	out := repairToolHistory(messages)
	if out[1].Name != "lookup" || out[3].Role != "user" || out[3].ToolCallID != "" {
		t.Fatalf("invalid repaired history: %+v", out)
	}
	if !reflect.DeepEqual(out, repairToolHistory(out)) {
		t.Fatal("repair must be idempotent")
	}
	missing := repairToolHistory([]Message{{Role: "assistant", ToolCalls: []ToolCall{call}}, {Role: "user", Content: "interrupted"}, {Role: "tool", ToolCallID: "call", Content: "late"}})
	if len(missing) != 4 || missing[1].Role != "tool" || missing[3].Role != "user" {
		t.Fatalf("missing synthetic result: %+v", missing)
	}
}

func toolCallHistory(ids ...string) []Message {
	var calls []ToolCall
	msgs := []Message{{Role: "user", Content: "go"}}
	for _, id := range ids {
		call := ToolCall{ID: id, Type: "function"}
		call.Function.Name = "lookup"
		call.Function.Arguments = "{}"
		calls = append(calls, call)
	}
	msgs = append(msgs, Message{Role: "assistant", ToolCalls: calls})
	for _, id := range ids {
		msgs = append(msgs, Message{Role: "tool", ToolCallID: id, Name: "lookup", Content: "ok"})
	}
	return msgs
}

func wireIDs(msgs []Message) (calls, results []string) {
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			calls = append(calls, tc.ID)
		}
		if m.ToolCallID != "" {
			results = append(results, m.ToolCallID)
		}
	}
	return calls, results
}

func TestRequestHistoryKeepsValidToolCallIDs(t *testing.T) {
	ids := []string{"call_00ssMPSA89avT2VQMofCfJNG", "toolu_01PD6wGTCkySbXFuTuXTtoUf", "gemini-call-0123456789abcdef0123456789abcdef"}
	for _, api := range []string{APIChatCompletions, APIResponses, APIMessages, APIGemini} {
		calls, results := wireIDs(prepareRequestHistory(toolCallHistory(ids...), api, "https://e.test", "m"))
		if !reflect.DeepEqual(calls, ids) || !reflect.DeepEqual(results, ids) {
			t.Fatalf("%s rewrote valid IDs: %v %v", api, calls, results)
		}
	}
}

func TestRequestHistoryRewritesImportedCompositeToolCallIDs(t *testing.T) {
	composite := "call_00ssMPSA89avT2VQMofCfJNG|fc_0935037566037f86016aa9d987589087d0b36f7eba05ee5372"
	messages := toolCallHistory(composite)
	before, _ := json.Marshal(messages)
	for _, api := range []string{APIResponses, APIMessages} {
		calls, results := wireIDs(prepareRequestHistory(messages, api, "https://e.test", "m"))
		if calls[0] != "call_00ssMPSA89avT2VQMofCfJNG" || results[0] != calls[0] {
			t.Fatalf("%s composite ID not mapped to its call_id: %v %v", api, calls, results)
		}
	}
	for _, api := range []string{APIChatCompletions, APIGemini} {
		if calls, _ := wireIDs(prepareRequestHistory(messages, api, "https://e.test", "m")); calls[0] != composite {
			t.Fatalf("%s must keep stored ID, got %q", api, calls[0])
		}
	}
	if after, _ := json.Marshal(messages); string(before) != string(after) {
		t.Fatal("stored history mutated")
	}
}

func TestRequestHistoryHashesUnusableToolCallIDs(t *testing.T) {
	long := strings.Repeat("x", 80)
	colliding := "call_00ssMPSA89avT2VQMofCfJNG"
	ids := []string{long, "kbrain-subagent:1", colliding, colliding + "|fc_dup"}
	for _, api := range []string{APIResponses, APIMessages} {
		calls, results := wireIDs(prepareRequestHistory(toolCallHistory(ids...), api, "https://e.test", "m"))
		if !reflect.DeepEqual(calls, results) {
			t.Fatalf("%s call/result pairing broken: %v %v", api, calls, results)
		}
		seen := map[string]bool{}
		for _, id := range calls {
			if !validWireToolCallID(id, api) || seen[id] {
				t.Fatalf("%s produced invalid or duplicate ID %q in %v", api, id, calls)
			}
			seen[id] = true
		}
		if calls[2] != colliding {
			t.Fatalf("%s rewrote the first owner of a colliding ID: %v", api, calls)
		}
	}
	again, _ := wireIDs(prepareRequestHistory(toolCallHistory(ids...), APIResponses, "https://e.test", "m"))
	if first, _ := wireIDs(prepareRequestHistory(toolCallHistory(ids...), APIResponses, "https://e.test", "m")); !reflect.DeepEqual(first, again) {
		t.Fatal("wire IDs must be deterministic across requests")
	}
}

func TestResponsesInputUsesWireToolCallIDs(t *testing.T) {
	composite := "call_00ssMPSA89avT2VQMofCfJNG|fc_0935037566037f86016aa9d987589087d0b36f7eba05ee5372"
	input, err := responsesInput(prepareRequestHistory(toolCallHistory(composite), APIResponses, "https://e.test", "m"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(input)
	if strings.Contains(string(raw), "|fc_") || strings.Count(string(raw), `"call_id":"call_00ssMPSA89avT2VQMofCfJNG"`) != 2 {
		t.Fatalf("unexpected Responses input: %s", raw)
	}
}

func TestRequestHistoryResolvesHashNamespaceCollisions(t *testing.T) {
	const imported = "|fc_imported"
	for _, api := range []string{APIResponses, APIMessages} {
		t.Run(api, func(t *testing.T) {
			// Stored IDs can occupy both the first hash and later retry candidates.
			reserved := []string{hashedToolCallID(imported)}
			for attempt := 1; attempt <= 3; attempt++ {
				reserved = append(reserved, hashedToolCallID(imported+"\x00"+strconv.Itoa(attempt)))
			}
			for _, ids := range [][]string{
				append(append([]string{}, reserved...), imported),
				append([]string{imported}, reserved...),
			} {
				messages := toolCallHistory(ids...)
				before, _ := json.Marshal(messages)
				calls, results := wireIDs(prepareRequestHistory(messages, api, "https://e.test", "m"))
				if !reflect.DeepEqual(calls, results) {
					t.Fatalf("call/result pairing broken: %v %v", calls, results)
				}
				seen := map[string]bool{}
				for _, id := range calls {
					if !validWireToolCallID(id, api) || seen[id] {
						t.Fatalf("invalid or duplicate wire ID %q in %v", id, calls)
					}
					seen[id] = true
				}
				again, _ := wireIDs(prepareRequestHistory(messages, api, "https://e.test", "m"))
				if !reflect.DeepEqual(calls, again) {
					t.Fatalf("mapping changed across requests: %v %v", calls, again)
				}
				if after, _ := json.Marshal(messages); string(before) != string(after) {
					t.Fatal("stored history mutated")
				}
				if ids[0] == reserved[0] && !reflect.DeepEqual(calls[:len(reserved)], reserved) {
					t.Fatalf("existing owners changed: %v", calls)
				}
			}
		})
	}
}

func TestRequestHistoryRendersCancelledReplies(t *testing.T) {
	call := ToolCall{ID: "call", Type: "function"}
	call.Function.Name = "lookup"
	call.Function.Arguments = "{}"
	messages := []Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "partial", Reasoning: "seen", StopReason: StopReasonCancelled},
		{Role: "user", Content: "second"},
		{Role: "assistant", Reasoning: "only thinking", StopReason: StopReasonCancelled},
		{Role: "user", Content: "third"},
		{Role: "assistant", ToolCalls: []ToolCall{call}, StopReason: StopReasonToolUse},
		{Role: "tool", ToolCallID: "call", Content: "Error: cancelled", StopReason: StopReasonCancelled},
		{Role: "user", Content: "fourth"},
	}
	before, _ := json.Marshal(messages)
	for _, api := range []string{APIChatCompletions, APIResponses, APIMessages, APIGemini} {
		out := prepareRequestHistory(messages, api, "https://e.test", "m")
		if len(out) != len(messages)-1 {
			t.Fatalf("%s: thinking-only cancelled reply was sent: %+v", api, out)
		}
		if out[1].Content != "partial"+cancelledReplyNote || out[1].Reasoning != "" || out[1].StopReason != "" {
			t.Fatalf("%s: cancelled reply = %+v", api, out[1])
		}
		if out[2].Content != "second" || out[3].Content != "third" {
			t.Fatalf("%s: order changed: %+v", api, out)
		}
		if out[5].Role != "tool" || out[5].Content != "Error: cancelled" {
			t.Fatalf("%s: cancelled tool result changed: %+v", api, out[5])
		}
	}
	if after, _ := json.Marshal(messages); string(before) != string(after) {
		t.Fatal("stored history mutated")
	}
	plain := []Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}}
	if got := cancelledReplies(plain); &got[0] != &plain[0] {
		t.Fatal("history without cancelled replies must not be copied")
	}
}
