package session

import (
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func seeded(t *testing.T) (*Store, string) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id, err := st.Create("/tmp", "kimi-k3-fast", "inference")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q1", Authored: true},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2", Authored: true},
		{Role: "assistant", Content: "a2"},
	}
	if err := st.Save(id, 1, msgs, "kimi-k3-fast", "inference"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGoal(id, "build the thing"); err != nil {
		t.Fatal(err)
	}
	return st, id
}

func TestForkRecordsLinkage(t *testing.T) {
	st, id := seeded(t)

	newID, err := st.Fork(id, 2, "experiment")
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := st.Load(newID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ForkedFrom != id || meta.ForkSeq != 2 {
		t.Fatalf("fork linkage: %+v", meta)
	}

	forks, err := st.ForksOf(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(forks) != 1 || forks[0].ID != newID {
		t.Fatalf("ForksOf: %+v", forks)
	}

	root, _, _ := st.Load(id)
	if root.ForkedFrom != "" || root.ForkSeq != 0 {
		t.Fatalf("root should have no fork linkage: %+v", root)
	}
}

func TestSessionTagsAndPinned(t *testing.T) {
	st, id := seeded(t)

	if err := st.SetTags(id, []string{"work", "bug bash"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPinned(id, true); err != nil {
		t.Fatal(err)
	}
	meta, _, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Tags) != 2 || meta.Tags[0] != "work" || meta.Tags[1] != "bug bash" {
		t.Fatalf("tags: %+v", meta.Tags)
	}
	if !meta.Pinned {
		t.Fatal("session should be pinned")
	}

	if err := st.SetTags(id, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPinned(id, false); err != nil {
		t.Fatal(err)
	}
	meta, _, _ = st.Load(id)
	if len(meta.Tags) != 0 || meta.Pinned {
		t.Fatalf("cleared: tags=%v pinned=%v", meta.Tags, meta.Pinned)
	}
}

func TestForkCopiesPrefix(t *testing.T) {
	st, id := seeded(t)

	newID, err := st.Fork(id, 2, "experiment")
	if err != nil {
		t.Fatal(err)
	}
	if newID == id {
		t.Fatal("fork must get a fresh id")
	}
	meta, msgs, err := st.Load(newID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "experiment" || meta.Goal != "build the thing" ||
		meta.Model != "kimi-k3-fast" || meta.Provider != "inference" || meta.CWD != "/tmp" {
		t.Fatalf("meta not carried over: %+v", meta)
	}
	if len(msgs) != 2 || msgs[0].Content != "q1" || msgs[1].Content != "a1" {
		t.Fatalf("forked prefix: %+v", msgs)
	}

	_, src, err := st.Load(id)
	if err != nil || len(src) != 4 {
		t.Fatalf("source changed: %v %d", err, len(src))
	}
}

func TestForkFullHistory(t *testing.T) {
	st, id := seeded(t)

	newID, err := st.Fork(id, 5, "copy")
	if err != nil {
		t.Fatal(err)
	}
	_, msgs, err := st.Load(newID)
	if err != nil || len(msgs) != 4 || msgs[3].Content != "a2" {
		t.Fatalf("full fork: %v %+v", err, msgs)
	}
}

func TestForkTitle(t *testing.T) {
	st, id := seeded(t)

	title, err := st.ForkTitle("first question here")
	if err != nil || title != "first question here (fork #1)" {
		t.Fatalf("got %q %v", title, err)
	}
	if _, err := st.Fork(id, 4, title); err != nil {
		t.Fatal(err)
	}
	next, err := st.ForkTitle("first question here")
	if err != nil || next != "first question here (fork #2)" {
		t.Fatalf("increments: %q %v", next, err)
	}
	empty, err := st.ForkTitle("")
	if err != nil || empty != "session (fork #1)" {
		t.Fatalf("untitled fallback: %q %v", empty, err)
	}
}

func TestSetTitle(t *testing.T) {
	st, id := seeded(t)
	if err := st.SetTitle(id, "renamed"); err != nil {
		t.Fatal(err)
	}
	meta, _, err := st.Load(id)
	if err != nil || meta.Title != "renamed" {
		t.Fatalf("got %q %v", meta.Title, err)
	}
}

func TestDeleteFrom(t *testing.T) {
	st, id := seeded(t)

	if err := st.DeleteFrom(id, 3); err != nil {
		t.Fatal(err)
	}
	_, msgs, err := st.Load(id)
	if err != nil || len(msgs) != 2 || msgs[1].Content != "a1" {
		t.Fatalf("after rewind: %v %+v", err, msgs)
	}

	if err := st.DeleteFrom(id, 2); err != nil {
		t.Fatal(err)
	}
	if _, msgs, _ = st.Load(id); len(msgs) != 1 || msgs[0].Content != "q1" {
		t.Fatalf("middle cut: %+v", msgs)
	}

	if err := st.DeleteFrom(id, 2); err != nil {
		t.Fatal(err)
	}
	if _, msgs, _ = st.Load(id); len(msgs) != 1 {
		t.Fatalf("re-delete changed rows: %d", len(msgs))
	}
}
