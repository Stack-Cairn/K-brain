package session

import (
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestHistoryNavigationAfterRepeatedCompaction(t *testing.T) {
	for _, storedSystem := range []bool{false, true} {
		t.Run(fmt.Sprint(storedSystem), func(t *testing.T) {
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
			if storedSystem {
				if err := st.Save(id, 0, initial, "m", "p"); err != nil {
					t.Fatal(err)
				}
			}
			h, msgs, err := st.History(id, initial)
			if err != nil {
				t.Fatal(err)
			}
			for i := range 4 {
				msgs = append(msgs, ai.Message{Role: "user", Content: fmt.Sprint("q", i), Authored: true}, ai.Message{Role: "assistant", Content: fmt.Sprint("a", i)})
			}
			if err := st.SaveHistory(id, h, msgs, "m", "p"); err != nil {
				t.Fatal(err)
			}
			original := st.RawMessages(id)
			var firstView []ai.Message
			for i := range 2 {
				summary := fmt.Sprint("summary", i)
				cut := 3
				if i == 1 {
					cut = 4
				}
				if err := h.Compact(summary, cut, "m", ai.Usage{}); err != nil {
					t.Fatal(err)
				}
				msgs = append([]ai.Message{initial[0], {Role: "system", Content: "Summary of the conversation so far:\n\n" + summary}}, msgs[cut:]...)
				if err := st.SaveHistory(id, h, msgs, "m", "p"); err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					firstView = append([]ai.Message(nil), msgs...)
				}
			}
			seq, ok := h.Sequence(2)
			if !ok {
				t.Fatal("retained user has no sequence")
			}
			if err := st.SetSnapshot(id, seq, "checkpoint"); err != nil {
				t.Fatal(err)
			}
			boundary, err := h.Boundary(3)
			if err != nil {
				t.Fatal(err)
			}
			fork, err := st.Fork(id, boundary-1, "branch")
			if err != nil {
				t.Fatal(err)
			}
			_, forkMsgs, err := st.History(fork, initial)
			if err != nil || !reflect.DeepEqual(forkMsgs, msgs[:3]) {
				t.Fatalf("fork: %+v, %v", forkMsgs, err)
			}
			if st.Snapshots(fork)[seq] != "checkpoint" {
				t.Fatal("fork lost snapshot mapping")
			}
			undo, undoMsgs, err := st.UndoCompaction(id, initial)
			if err != nil || !reflect.DeepEqual(undoMsgs, firstView) {
				t.Fatalf("undo: %+v, %v", undoMsgs, err)
			}
			if _, ok := undo.Sequence(1); ok {
				t.Fatal("summary should be virtual")
			}
			if !reflect.DeepEqual(st.RawMessages(id), original) {
				t.Fatal("undo changed original history")
			}
			if err := st.TruncateHistory(id, undo, 4); err != nil {
				t.Fatal(err)
			}
			_, loaded, err := st.History(id, initial)
			if err != nil || !reflect.DeepEqual(loaded, firstView[:4]) {
				t.Fatalf("rewind: %+v, %v", loaded, err)
			}
			if len(st.Snapshots(id)) != 0 {
				t.Fatal("removed turn retained snapshot")
			}
			if used, err := st.SnapshotReferenced("checkpoint"); err != nil || !used {
				t.Fatalf("fork still needs snapshot: %v, %v", used, err)
			}
			if err := st.ClearSnapshots(fork); err != nil {
				t.Fatal(err)
			}
			if used, err := st.SnapshotReferenced("checkpoint"); err != nil || used {
				t.Fatalf("removed snapshot still referenced: %v, %v", used, err)
			}
			if err := st.SaveHistory(id, undo, firstView, "m", "p"); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(st.RawMessages(id), original) {
				t.Fatal("redo corrupted history")
			}
			if err := st.TruncateHistory(id, undo, 1); err != nil {
				t.Fatal(err)
			}
			_, loaded, err = st.History(id, initial)
			if err != nil || !reflect.DeepEqual(loaded, initial) || len(st.Compactions(id)) != 0 {
				t.Fatalf("rewind to start: %+v, %v", loaded, err)
			}
		})
	}
}

func TestHistoryNavigationFailureRetainsMapping(t *testing.T) {
	st, id := seeded(t)
	initial := []ai.Message{{Role: "system", Content: "sys"}}
	h, msgs, err := st.History(id, initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Compact("summary", 3, "m", ai.Usage{}); err != nil {
		t.Fatal(err)
	}
	msgs = append([]ai.Message{initial[0], {Role: "system", Content: "Summary of the conversation so far:\n\nsummary"}}, msgs[3:]...)
	if err := st.SaveHistory(id, h, msgs, "m", "p"); err != nil {
		t.Fatal(err)
	}
	path := st.TranscriptPath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := st.TruncateHistory(id, h, 2); err == nil {
		t.Fatal("rewind accepted corrupt transcript")
	}
	if _, _, err := st.UndoCompaction(id, initial); err == nil {
		t.Fatal("undo accepted corrupt transcript")
	}
	if _, ok := h.Sequence(2); !ok {
		t.Fatal("failed rewind changed mapping")
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveHistory(id, h, msgs, "m", "p"); err != nil {
		t.Fatal(err)
	}
	if len(st.Compactions(id)) != 1 {
		t.Fatal("failure removed compaction")
	}
	for _, cut := range []int{-1, 0, len(msgs) + 1} {
		if err := st.TruncateHistory(id, h, cut); err == nil {
			t.Fatalf("accepted boundary %d", cut)
		}
	}
}
