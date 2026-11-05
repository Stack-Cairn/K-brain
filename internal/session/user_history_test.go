package session

import (
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestUserHistoryEntriesKeepDirectory(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a, _ := st.Create("/proj/a", "m", "p")
	_ = st.Save(a, 0, []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "shared prompt", Authored: true},
		{Role: "user", Content: "only in a", Authored: true},
	}, "m", "p")
	b, _ := st.Create("/proj/b", "m", "p")
	_ = st.Save(b, 0, []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "shared prompt", Authored: true},
		{Role: "user", Content: "injected", Authored: false},
		{Role: "user", Content: "shared prompt", Authored: true},
	}, "m", "p")

	entries, err := st.UserHistoryEntries(0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, e := range entries {
		got[e.CWD+"|"+e.Text]++
		if e.At.IsZero() {
			t.Errorf("entry %q has no time", e.Text)
		}
	}
	want := map[string]int{"/proj/a|shared prompt": 1, "/proj/a|only in a": 1, "/proj/b|shared prompt": 1}
	if len(got) != len(want) {
		t.Fatalf("entries = %+v", entries)
	}
	for k, n := range want {
		if got[k] != n {
			t.Fatalf("%s listed %d times, want %d: %+v", k, got[k], n, entries)
		}
	}
	if lim, _ := st.UserHistoryEntries(2); len(lim) != 2 {
		t.Fatalf("limit: got %d", len(lim))
	}
}
