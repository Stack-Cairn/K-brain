package session

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// prefixFixture is a legacy source whose file was still being appended to
// while it was imported: each version holds one more message.
func prefixFixture() []ai.Message {
	sent := time.Date(2026, 1, 2, 3, 4, 5, 6, time.FixedZone("UTC+8", 8*3600))
	call := ai.ToolCall{ID: "call-1", Type: "function"}
	call.Function.Name, call.Function.Arguments = "lookup", `{"q": "x"}`
	return []ai.Message{
		{ID: "u1", Role: "user", Content: "hello", SentAt: &sent, Parts: []ai.ContentPart{{Type: "image_url", ImageURL: &struct {
			URL string `json:"url"`
		}{URL: "data:image/png;base64,AAAA"}}}},
		{ID: "a1", Role: "assistant", Content: "looking", Reasoning: "think", ToolCalls: []ai.ToolCall{call}, Model: "m @ p", Usage: &ai.Usage{PromptTokens: 3, CompletionTokens: 4}},
		{ID: "t1", Role: "tool", Content: "result", ToolCallID: "call-1", Name: "lookup"},
		{ID: "a2", Role: "assistant", Content: "answer"},
		{ID: "u2", Role: "user", Content: "thanks"},
	}
}

type prefixImport struct {
	source, fingerprint, content, title, metadata string
	updated                                       time.Time
	messages                                      []ai.Message
	boundary                                      *Compaction
}

func (p prefixImport) run(st *Store, id string) (ImportResult, error) {
	return st.ImportOrUpdateHistory(id, p.source, p.fingerprint, p.content, p.metadata, "", "m", "p", p.title, time.Unix(100, 0).UTC(), p.updated, true, true, true, "share-token", p.messages, p.boundary)
}

func prefixVersion(n int) prefixImport {
	return prefixImport{
		source: "pi-source", fingerprint: "source-v" + string(rune('0'+n)), content: "content-v" + string(rune('0'+n)),
		title: "source title", metadata: `{"version":` + string(rune('0'+n)) + `}`,
		updated: time.Unix(int64(200+n), 0).UTC(), messages: prefixFixture()[:n],
	}
}

func openPrefixStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "sessions")
	st, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, root
}

func wantConflict(t *testing.T, err error, reason string) {
	t.Helper()
	var conflict *ImportConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, ErrImportConflict) || conflict.Reason != reason {
		t.Fatalf("err = %v, want conflict %s", err, reason)
	}
	if err.Error() != `import source "pi-source" conflicts with existing session` {
		t.Fatalf("conflict text changed: %q", err.Error())
	}
}

func TestImportPrefixUpdateReplacesInterruptedImportAndSurvivesReload(t *testing.T) {
	st, root := openPrefixStore(t)
	if result, err := prefixVersion(3).run(st, "pi-source"); err != nil || result.Outcome != ImportCreated {
		t.Fatalf("first import = %+v %v", result, err)
	}
	// Local state the update must keep.
	if err := st.SetTitle("pi-source", "renamed locally"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetArchived("pi-source", true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSnapshot("pi-source", 0, "checkpoint-ref"); err != nil {
		t.Fatal(err)
	}
	result, err := prefixVersion(4).run(st, "pi-source")
	if err != nil || result.Outcome != ImportUpdated || result.PreviousMessages != 3 {
		t.Fatalf("prefix update = %+v %v", result, err)
	}
	check := func(st *Store, n int) {
		t.Helper()
		snap, err := st.HistorySnapshot("pi-source")
		if err != nil {
			t.Fatal(err)
		}
		meta := snap.Meta
		if len(snap.Messages) != n || snap.Messages[n-1].ID != prefixFixture()[n-1].ID {
			t.Fatalf("messages = %+v", snap.Messages)
		}
		if meta.ImportFingerprint != prefixVersion(n).fingerprint || meta.ImportContentFingerprint != prefixVersion(n).content || meta.ImportMetadata != prefixVersion(n).metadata {
			t.Fatalf("import identity not updated: %+v", meta)
		}
		if !meta.UpdatedAt.Equal(prefixVersion(n).updated) || !snap.CreatedAt.Equal(time.Unix(100, 0)) {
			t.Fatalf("timestamps = %v %v", meta.UpdatedAt, snap.CreatedAt)
		}
		if meta.ID != "pi-source" || meta.Title != "renamed locally" || !meta.Pinned || !meta.Archived || !meta.Shared || meta.ShareToken != "share-token" || !meta.ShareRedactTool {
			t.Fatalf("local state lost: %+v", meta)
		}
		if st.Snapshots("pi-source")[0] != "checkpoint-ref" {
			t.Fatal("snapshot lost")
		}
		if meta.ImportRevision == "" || meta.ImportRevision != snap.Revision {
			t.Fatalf("import revision %q != current %q", meta.ImportRevision, snap.Revision)
		}
	}
	check(st, 4)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	check(st, 4)
	if result, err := prefixVersion(4).run(st, "pi-source"); err != nil || result.Outcome != ImportAlready {
		t.Fatalf("identical retry after reload = %+v %v", result, err)
	}
	// The revision recorded by the update matches a reread transcript, so a
	// later append is still recognised as unmodified.
	if result, err := prefixVersion(5).run(st, "pi-source"); err != nil || result.Outcome != ImportUpdated || result.PreviousMessages != 4 {
		t.Fatalf("second update after reload = %+v %v", result, err)
	}
	check(st, 5)
	if already, err := st.ImportHistory("pi-source", "pi-source", prefixVersion(5).fingerprint, prefixVersion(5).content, "", "", "", "", "", time.Time{}, time.Time{}, false, false, false, "", nil, nil); err != nil || !already {
		t.Fatalf("ImportHistory wrapper = %v %v", already, err)
	}
}

func TestImportPrefixUpdateReplacesActiveContextAndTakesTitleWhenUntitled(t *testing.T) {
	st, _ := openPrefixStore(t)
	first := prefixVersion(2)
	first.title = ""
	first.boundary = &Compaction{Seq: 1, Cutoff: 1, Summary: "old summary", DropPrior: true}
	if _, err := first.run(st, "pi-source"); err != nil {
		t.Fatal(err)
	}
	next := prefixVersion(4)
	next.boundary = &Compaction{Seq: 1, Cutoff: 3, Summary: "new summary", DropPrior: true}
	if result, err := next.run(st, "pi-source"); err != nil || result.Outcome != ImportUpdated {
		t.Fatalf("update = %+v %v", result, err)
	}
	compactions := st.Compactions("pi-source")
	if len(compactions) != 1 || compactions[0].Summary != "new summary" || compactions[0].Cutoff != 3 {
		t.Fatalf("compactions = %+v", compactions)
	}
	meta, _, err := st.Load("pi-source")
	if err != nil || meta.Title != "source title" {
		t.Fatalf("title = %q %v", meta.Title, err)
	}
	last := prefixVersion(5)
	if result, err := last.run(st, "pi-source"); err != nil || result.Outcome != ImportUpdated || len(st.Compactions("pi-source")) != 0 {
		t.Fatalf("boundary removal = %+v %v %+v", result, err, st.Compactions("pi-source"))
	}
}

func TestImportPrefixUpdateIdenticalIsAlreadyImported(t *testing.T) {
	st, _ := openPrefixStore(t)
	if _, err := prefixVersion(3).run(st, "pi-source"); err != nil {
		t.Fatal(err)
	}
	before, err := st.HistorySnapshot("pi-source")
	if err != nil {
		t.Fatal(err)
	}
	if result, err := prefixVersion(3).run(st, "pi-source"); err != nil || result.Outcome != ImportAlready || result.PreviousMessages != 0 {
		t.Fatalf("identical = %+v %v", result, err)
	}
	after, err := st.HistorySnapshot("pi-source")
	if err != nil || after.Revision != before.Revision || !after.Meta.UpdatedAt.Equal(before.Meta.UpdatedAt) {
		t.Fatalf("identical import rewrote the session: %v", err)
	}
}

func TestImportPrefixUpdateRefusesDivergedHistory(t *testing.T) {
	cases := map[string]struct {
		edit   func(*prefixImport)
		reason string
	}{
		"changed message": {func(p *prefixImport) {
			p.messages = append([]ai.Message(nil), p.messages...)
			p.messages[1].Content = "rewritten"
		}, ImportConflictDiverged},
		"changed id": {func(p *prefixImport) {
			p.messages = append([]ai.Message(nil), p.messages...)
			p.messages[0].ID = "other"
		}, ImportConflictDiverged},
		"changed role": {func(p *prefixImport) {
			p.messages = append([]ai.Message(nil), p.messages...)
			p.messages[2].Role = "user"
		}, ImportConflictDiverged},
		"reordered": {func(p *prefixImport) {
			p.messages = append([]ai.Message(nil), p.messages...)
			p.messages[0], p.messages[1] = p.messages[1], p.messages[0]
		}, ImportConflictDiverged},
		"shorter":                        {func(p *prefixImport) { p.messages = p.messages[:2] }, ImportConflictDiverged},
		"same messages, new fingerprint": {func(p *prefixImport) { p.messages = p.messages[:3] }, ImportConflictNoNewMessages},
		"longer under the same fingerprint": {func(p *prefixImport) {
			p.fingerprint = prefixVersion(3).fingerprint
		}, ImportConflictFingerprintUnchanged},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			st, _ := openPrefixStore(t)
			if _, err := prefixVersion(3).run(st, "pi-source"); err != nil {
				t.Fatal(err)
			}
			next := prefixVersion(4)
			tc.edit(&next)
			_, err := next.run(st, "pi-source")
			wantConflict(t, err, tc.reason)
			if got := st.RawMessages("pi-source"); len(got) != 3 || got[0].ID != "u1" {
				t.Fatalf("conflict changed messages: %+v", got)
			}
		})
	}
}

func TestImportPrefixUpdateRefusesDifferentSource(t *testing.T) {
	st, _ := openPrefixStore(t)
	other := prefixVersion(2)
	other.source = "other-source"
	if _, err := other.run(st, "pi-source"); err != nil {
		t.Fatal(err)
	}
	_, err := prefixVersion(3).run(st, "pi-source")
	wantConflict(t, err, ImportConflictDifferentSource)

	native, err := st.Create("", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(native, 0, prefixFixture()[:1], "m", "p"); err != nil {
		t.Fatal(err)
	}
	_, err = prefixVersion(3).run(st, native)
	wantConflict(t, err, ImportConflictDifferentSource)
}

func TestImportPrefixUpdateRefusesSessionModifiedAfterImport(t *testing.T) {
	cases := map[string]func(*Store) error{
		"turn appended": func(st *Store) error {
			return st.Save("pi-source", 0, append(prefixFixture()[:3], ai.Message{Role: "user", Content: "local turn"}), "m", "p")
		},
		// A truncation leaves a prefix; only the import revision reveals it.
		"rewound to an earlier message": func(st *Store) error { return st.TruncateAtSequence("pi-source", 2) },
		"compacted":                     func(st *Store) error { return st.RecordCompaction("pi-source", 2, "summary", "m", ai.Usage{}) },
		"cleared":                       func(st *Store) error { return st.ClearMessages("pi-source") },
	}
	for name, modify := range cases {
		t.Run(name, func(t *testing.T) {
			st, _ := openPrefixStore(t)
			if _, err := prefixVersion(3).run(st, "pi-source"); err != nil {
				t.Fatal(err)
			}
			before := st.RawMessages("pi-source")
			if err := modify(st); err != nil {
				t.Fatal(err)
			}
			after := st.RawMessages("pi-source")
			_, err := prefixVersion(5).run(st, "pi-source")
			wantConflict(t, err, ImportConflictModifiedAfterImport)
			if got := st.RawMessages("pi-source"); len(got) != len(after) {
				t.Fatalf("conflict changed messages: %d -> %d (imported %d)", len(after), len(got), len(before))
			}
		})
	}
	t.Run("metadata only is not a modification", func(t *testing.T) {
		st, _ := openPrefixStore(t)
		if _, err := prefixVersion(3).run(st, "pi-source"); err != nil {
			t.Fatal(err)
		}
		if err := st.SetPinned("pi-source", false); err != nil {
			t.Fatal(err)
		}
		if err := st.SetTags("pi-source", []string{"kept"}); err != nil {
			t.Fatal(err)
		}
		if err := st.SetGoal("pi-source", "goal"); err != nil {
			t.Fatal(err)
		}
		if result, err := prefixVersion(4).run(st, "pi-source"); err != nil || result.Outcome != ImportUpdated {
			t.Fatalf("update = %+v %v", result, err)
		}
		meta, _, _ := st.Load("pi-source")
		if meta.Pinned || len(meta.Tags) != 1 || meta.Goal != "goal" {
			t.Fatalf("local metadata lost: %+v", meta)
		}
	})
}

// Imports written before ImportRevision existed fall back to the shape an
// import leaves behind.
func TestImportPrefixUpdateLegacyImportWithoutRevision(t *testing.T) {
	legacy := func(t *testing.T, boundary *Compaction) *Store {
		t.Helper()
		st, _ := openPrefixStore(t)
		first := prefixVersion(3)
		first.boundary = boundary
		if _, err := first.run(st, "pi-source"); err != nil {
			t.Fatal(err)
		}
		if err := st.update("pi-source", func(d *sessionData) error { d.Meta.ImportRevision = ""; return nil }); err != nil {
			t.Fatal(err)
		}
		return st
	}
	t.Run("unmodified", func(t *testing.T) {
		st := legacy(t, &Compaction{Seq: 1, Cutoff: 1, Summary: "s", DropPrior: true})
		if result, err := prefixVersion(4).run(st, "pi-source"); err != nil || result.Outcome != ImportUpdated {
			t.Fatalf("legacy update = %+v %v", result, err)
		}
		if meta, _, _ := st.Load("pi-source"); meta.ImportRevision == "" {
			t.Fatal("update did not record the import revision")
		}
	})
	modified := map[string]func(*Store) error{
		"compacted": func(st *Store) error { return st.RecordCompaction("pi-source", 2, "summary", "m", ai.Usage{}) },
		"edited": func(st *Store) error {
			snap, err := st.HistorySnapshot("pi-source")
			if err != nil {
				return err
			}
			_, err = st.MutateHistory("pi-source", "u1", snap.Revision, "", &ai.Message{Role: "user", Content: "hello"})
			return err
		},
		"subagent task": func(st *Store) error { return st.SaveTask("pi-source", Task{ID: "task-1", Status: "done"}) },
		"gap in sequence": func(st *Store) error {
			return st.update("pi-source", func(d *sessionData) error {
				d.Messages[7] = d.Messages[2]
				delete(d.Messages, 2)
				return nil
			})
		},
	}
	for name, modify := range modified {
		t.Run(name, func(t *testing.T) {
			st := legacy(t, nil)
			if err := modify(st); err != nil {
				t.Fatal(err)
			}
			_, err := prefixVersion(4).run(st, "pi-source")
			wantConflict(t, err, ImportConflictModifiedAfterImport)
		})
	}
}

func TestImportPrefixUpdateKeepsDeletedHistoryDeleted(t *testing.T) {
	st, _ := openPrefixStore(t)
	if _, err := prefixVersion(3).run(st, "pi-source"); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete("pi-source"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{3, 4} {
		if _, err := prefixVersion(n).run(st, "pi-source"); !errors.Is(err, ErrHistoryDeleted) {
			t.Fatalf("v%d after delete = %v", n, err)
		}
	}
}

func TestImportPrefixUpdateWaitsForRunningTurn(t *testing.T) {
	st, _ := openPrefixStore(t)
	if _, err := prefixVersion(3).run(st, "pi-source"); err != nil {
		t.Fatal(err)
	}
	lease, err := st.AcquireRun("pi-source", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prefixVersion(4).run(st, "pi-source"); !errors.Is(err, ErrImportBusy) {
		t.Fatalf("update during own run = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if result, err := prefixVersion(4).run(st, "pi-source"); err != nil || result.Outcome != ImportUpdated {
		t.Fatalf("update after run = %+v %v", result, err)
	}
}
