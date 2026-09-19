package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestClosedStoreDegradesGracefully(t *testing.T) {
	st, id := seeded(t)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	errCases := map[string]func() error{
		"LoadTasks":   func() error { _, err := st.LoadTasks(id); return err },
		"Save":        func() error { return st.Save(id, 0, []ai.Message{{Role: "user", Content: "x"}}, "m", "p") },
		"Load":        func() error { _, _, err := st.Load(id); return err },
		"Recent":      func() error { _, err := st.Recent(10); return err },
		"UserHistory": func() error { _, err := st.UserHistory(10); return err },
		"DeleteFrom":  func() error { return st.DeleteFrom(id, 1) },
		"Fork":        func() error { _, err := st.Fork(id, 2, "x"); return err },
		"ForksOf":     func() error { _, err := st.ForksOf(id); return err },
		"ForkTitle":   func() error { _, err := st.ForkTitle("base"); return err },
		"AddSchedule": func() error { _, err := st.AddSchedule(id, "@every 1m", "p", time.Now()); return err },
	}
	for name, fn := range errCases {
		if err := fn(); err == nil {
			t.Errorf("%s on a closed store should error", name)
		}
	}

	if got := st.Snapshots(id); got != nil {
		t.Errorf("Snapshots on a closed store = %v, want nil", got)
	}
	if got := st.Schedules(id); got != nil {
		t.Errorf("Schedules on a closed store = %v, want nil", got)
	}
	if got := st.Compactions(id); got != nil {
		t.Errorf("Compactions on a closed store = %v, want nil", got)
	}
	if got := st.RawMessages(id); got != nil {
		t.Errorf("RawMessages on a closed store = %v, want nil", got)
	}
	if got := st.Todos(id); got != "" {
		t.Errorf("Todos on a closed store = %q, want empty", got)
	}
}

func TestSaveSkipsPlaceholderRows(t *testing.T) {
	st, id := seeded(t)
	msgs := []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q1", Authored: true},
		{Role: "assistant", Content: "a1"},
		{},
		{Role: "assistant", Content: "a2"},
	}
	if err := st.Save(id, 3, msgs, "m", "p"); err != nil {
		t.Fatal(err)
	}
	raw := st.RawMessages(id)
	if len(raw) != 4 {
		t.Fatalf("placeholder should not add or replace a row: %d rows", len(raw))
	}
	if raw[2].Content != "q2" {
		t.Fatalf("seq 3 should keep its original content, got %q", raw[2].Content)
	}
}

func TestForkTitleUnwrapsExistingSuffix(t *testing.T) {
	st, id := seeded(t)
	if _, err := st.Fork(id, 2, "notes (fork #1)"); err != nil {
		t.Fatal(err)
	}
	got, err := st.ForkTitle("notes (fork #1)")
	if err != nil {
		t.Fatal(err)
	}
	if got != "notes (fork #2)" {
		t.Fatalf("forking a fork should increment, got %q", got)
	}

	got, err = st.ForkTitle("notes (fork #9) draft")
	if err != nil {
		t.Fatal(err)
	}
	if got != "notes (fork #9) draft (fork #1)" {
		t.Fatalf("only an exact suffix unwraps, got %q", got)
	}
}

func TestApplyCompactionKeepsPriorSummary(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q1", Authored: true},
		{Role: "system", Content: "Summary of the conversation so far:\n\nfirst gen"},
		{Role: "user", Content: "q2", Authored: true},
		{Role: "assistant", Content: "a2"},
	}
	if err := st.Save(id, 1, msgs, "m", "p"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordCompaction(id, 3, "second gen", "", ai.Usage{}); err != nil {
		t.Fatal(err)
	}
	_, got, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 4 {
		t.Fatalf("compacted view: %d msgs %+v", len(got), got)
	}
	if !strings.Contains(got[1].Content, "second gen") {
		t.Fatalf("the newest summary should come first: %q", got[1].Content)
	}
	if !strings.Contains(got[2].Content, "first gen") {
		t.Fatalf("the prior summary must be kept: %q", got[2].Content)
	}
	if got[3].Content != "a2" {
		t.Fatalf("raw tail lost: %+v", got[3:])
	}
}

func TestOpenRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions")
	if err := os.WriteFile(path, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if st, err := Open(path); err == nil {
		st.Close()
		t.Fatal("expected directory error")
	}
}

func TestCorruptTranscriptIsNotOverwritten(t *testing.T) {
	for _, content := range []string{"", "{bad", "{\"type\":\"message\",\"payload\":{}}\n", "{\"type\":\"session_meta\",\"payload\":null}\n"} {
		t.Run(content, func(t *testing.T) {
			st, id := seeded(t)
			path := st.TranscriptPath(id)
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.Load(id); err == nil {
				t.Fatal("Load accepted corruption")
			}
			if err := st.SetTitle(id, "overwrite"); err == nil {
				t.Fatal("write accepted corruption")
			}
			if fork, err := st.Fork(id, 2, "copy"); err == nil || fork != "" {
				t.Fatal("fork accepted corruption")
			}
			if _, err := st.Recent(10); err == nil {
				t.Fatal("Recent hid corruption")
			}
			if _, err := st.UserHistory(10); err == nil {
				t.Fatal("history hid corruption")
			}
			if st.RawMessages(id) != nil || st.Snapshots(id) != nil || st.Schedules(id) != nil {
				t.Fatal("invalid data exposed")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != content {
				t.Fatal("corrupt file was overwritten")
			}
		})
	}
}

func TestSaveFailurePreservesTranscript(t *testing.T) {
	st, id := seeded(t)
	path := st.TranscriptPath(id)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = st.SaveTask(id, Task{ID: "bad", StartedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err == nil {
		t.Fatal("expected encoding error")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed transaction changed file")
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".session-*.tmp"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary files: %v %v", leftovers, err)
	}
	if _, msgs, err := st.Load(id); err != nil || len(msgs) != 4 {
		t.Fatalf("original session lost: %d %v", len(msgs), err)
	}
}
