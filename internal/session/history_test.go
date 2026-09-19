package session

import (
	"os"
	"reflect"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestHistoryRepeatedCompactionAndResume(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.Create("project", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	initial := []ai.Message{{Role: "system", Content: "sys"}}
	h, msgs, err := st.History(id, initial)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"q1", "a1", "q2", "a2", "q3", "a3"} {
		role := "user"
		if text[0] == 'a' {
			role = "assistant"
		}
		msgs = append(msgs, ai.Message{Role: role, Content: text})
	}
	if err := st.SaveHistory(id, h, msgs, "m", "p"); err != nil {
		t.Fatal(err)
	}
	original := st.RawMessages(id)
	for _, summary := range []string{"first summary", "second summary"} {
		if err := h.Observe(msgs); err != nil {
			t.Fatal(err)
		}
		if err := h.Compact(summary, 3, "compact-model", ai.Usage{PromptTokens: 100}); err != nil {
			t.Fatal(err)
		}
		msgs = append([]ai.Message{msgs[0], {Role: "system", Content: "Summary of the conversation so far:\n\n" + summary}}, msgs[3:]...)
		msgs = append(msgs, ai.Message{Role: "user", Content: "after " + summary}, ai.Message{Role: "assistant", Content: "answer " + summary})
		if err := st.SaveHistory(id, h, msgs, "m", "p"); err != nil {
			t.Fatal(err)
		}
		_, loaded, err := st.History(id, initial)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(loaded, msgs) {
			t.Fatalf("restored context differs:\n%+v\n%+v", loaded, msgs)
		}
		h, loaded, err = st.History(id, initial)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(loaded, msgs) {
			t.Fatalf("resumed context differs: %+v", loaded)
		}
		msgs = loaded
	}
	raw := st.RawMessages(id)
	if len(raw) != len(original)+4 || !reflect.DeepEqual(raw[:len(original)], original) {
		t.Fatalf("raw history overwritten: %+v", raw)
	}
	if cs := st.Compactions(id); len(cs) != 2 || cs[1].Model != "compact-model" || cs[1].Usage.PromptTokens != 100 {
		t.Fatalf("compactions: %+v", cs)
	}
}

func TestHistorySaveFailureRetainsPendingCompaction(t *testing.T) {
	st, id := seeded(t)
	h, msgs, err := st.History(id, []ai.Message{{Role: "system", Content: "sys"}})
	if err != nil {
		t.Fatal(err)
	}
	msgs = append(msgs, ai.Message{Role: "user", Content: "new question"}, ai.Message{Role: "assistant", Content: "new answer"})
	if err := h.Observe(msgs); err != nil {
		t.Fatal(err)
	}
	if err := h.Compact("summary", len(msgs), "m", ai.Usage{}); err != nil {
		t.Fatal(err)
	}
	msgs = []ai.Message{msgs[0], {Role: "system", Content: "Summary of the conversation so far:\n\nsummary"}}
	path := st.TranscriptPath(id)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveHistory(id, h, msgs, "m", "p"); err == nil {
		t.Fatal("save accepted corrupt transcript")
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := st.SaveHistory(id, h, msgs, "m", "p"); err != nil {
			t.Fatal(err)
		}
	}
	if len(st.Compactions(id)) != 1 {
		t.Fatal("compaction lost or duplicated")
	}
	_, loaded, err := st.History(id, []ai.Message{{Role: "system", Content: "sys"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, msgs) {
		t.Fatalf("retry context: %+v", loaded)
	}
}

func TestHistoryRestoresSparseMessagesAndInterruptedTools(t *testing.T) {
	st, id := seeded(t)
	if err := st.ClearMessages(id); err != nil {
		t.Fatal(err)
	}
	call := ai.ToolCall{ID: "call", Type: "function"}
	call.Function.Name = "read"
	if err := st.Save(id, 1, []ai.Message{{}, {Role: "user", Content: "before"}, {}, {Role: "assistant", ToolCalls: []ai.ToolCall{call}}, {}, {Role: "user", Content: "after"}}, "m", "p"); err != nil {
		t.Fatal(err)
	}
	initial := []ai.Message{{Role: "system", Content: "sys"}}
	h, msgs, err := st.History(id, initial)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 || msgs[3].ToolCallID != "call" {
		t.Fatalf("tool repair: %+v", msgs)
	}
	msgs = append(msgs, ai.Message{Role: "user", Content: "next"}, ai.Message{Role: "assistant", Content: "done"})
	if err := st.SaveHistory(id, h, msgs, "m", "p"); err != nil {
		t.Fatal(err)
	}
	_, loaded, err := st.History(id, initial)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, msgs) {
		t.Fatalf("resume after tool repair: %+v", loaded)
	}
	if len(st.RawMessages(id)) != 5 {
		t.Fatal("sparse raw history overwritten or virtual tool result persisted")
	}
}

func TestHistoryBatchesCompactionsAndUpdatesRetainedMessages(t *testing.T) {
	st, id := seeded(t)
	initial := []ai.Message{{Role: "system", Content: "sys"}}
	h, msgs, err := st.History(id, initial)
	if err != nil {
		t.Fatal(err)
	}
	msgs = append(msgs, ai.Message{Role: "user", Content: "q3"}, ai.Message{Role: "assistant", Content: "a3"})
	for _, summary := range []string{"first", "second"} {
		if err := h.Observe(msgs); err != nil {
			t.Fatal(err)
		}
		if err := h.Compact(summary, 3, "m", ai.Usage{}); err != nil {
			t.Fatal(err)
		}
		msgs = append([]ai.Message{initial[0], {Role: "system", Content: "Summary of the conversation so far:\n\n" + summary}}, msgs[3:]...)
	}
	msgs[len(msgs)-1].Content = "updated retained answer"
	msgs = append(msgs, ai.Message{Role: "user", Content: "q4"})
	if err := st.SaveHistory(id, h, msgs, "m", "p"); err != nil {
		t.Fatal(err)
	}
	if cs := st.Compactions(id); len(cs) != 2 || cs[0].Summary != "first" || cs[1].Summary != "second" {
		t.Fatalf("batched compactions: %+v", cs)
	}
	_, restored, err := st.History(id, initial)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, msgs) {
		t.Fatalf("batched restore: %+v", restored)
	}
	raw := st.RawMessages(id)
	if len(raw) != 7 || raw[0].Content != "q1" || raw[5].Content != "updated retained answer" || raw[6].Content != "q4" {
		t.Fatalf("batched raw history: %+v", raw)
	}
}
