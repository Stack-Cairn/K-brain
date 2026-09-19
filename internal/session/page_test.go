package session

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func pageSession(t *testing.T, st *Store, cwd string, stamp time.Time) string {
	t.Helper()
	id, err := st.Create(cwd, "model", "provider")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.update(id, func(d *sessionData) error {
		d.Messages[0] = ai.Message{Role: "user", Content: id}
		d.Meta.UpdatedAt = stamp
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func openPageStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenProjectHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestListPageFiltersBeforeLimit(t *testing.T) {
	st := openPageStore(t)
	dir := t.TempDir()
	stamp := time.Now().UTC()
	want := pageSession(t, st, dir, stamp.Add(-time.Hour))
	pageSession(t, st, t.TempDir(), stamp)
	archived := pageSession(t, st, dir, stamp)
	if err := st.SetArchived(archived, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSubagentTranscript(want, "child", []ai.Message{{Role: "user", Content: "subtask"}}, "m", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create(dir, "m", "p"); err != nil {
		t.Fatal(err)
	}
	page, err := st.ListPage(context.Background(), PageOptions{CWD: &dir, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].ID != want || page.Next != nil {
		t.Fatalf("filtered page = %+v", page)
	}
}

func TestListPageTimestampTies(t *testing.T) {
	st := openPageStore(t)
	dir := t.TempDir()
	stamp := time.Now().UTC()
	var want []string
	for range 5 {
		want = append(want, pageSession(t, st, dir, stamp))
	}
	slices.Sort(want)
	var got []string
	var after *PageCursor
	for i := 0; i < 3; i++ {
		page, err := st.ListPage(context.Background(), PageOptions{After: after, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, meta := range page.Sessions {
			got = append(got, meta.ID)
		}
		if (page.Next != nil) != (i < 2) {
			t.Fatalf("page %d cursor = %+v", i, page.Next)
		}
		after = page.Next
	}
	if !slices.Equal(got, want) {
		t.Fatalf("paged IDs = %v, want %v", got, want)
	}
}

func TestListPageSurvivesDeletedAnchorAndNewSession(t *testing.T) {
	st := openPageStore(t)
	dir := t.TempDir()
	stamp := time.Now().UTC()
	var ids []string
	for i := range 5 {
		ids = append(ids, pageSession(t, st, dir, stamp.Add(-time.Duration(i)*time.Second)))
	}
	first, err := st.ListPage(context.Background(), PageOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if first.Next == nil || first.Next.ID != ids[1] {
		t.Fatalf("first page = %+v", first)
	}
	if err := st.Delete(ids[1]); err != nil {
		t.Fatal(err)
	}
	pageSession(t, st, dir, stamp.Add(time.Second))
	next, err := st.ListPage(context.Background(), PageOptions{After: first.Next, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Sessions) != 2 || next.Sessions[0].ID != ids[2] || next.Sessions[1].ID != ids[3] {
		t.Fatalf("next page shifted after changes: %+v", next)
	}
	last, err := st.ListPage(context.Background(), PageOptions{After: next.Next, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Sessions) != 1 || last.Sessions[0].ID != ids[4] || last.Next != nil {
		t.Fatalf("last page = %+v", last)
	}
}

func TestListPageErrors(t *testing.T) {
	st := openPageStore(t)
	for _, options := range []PageOptions{{Limit: 0}, {Limit: -1}, {Limit: 1001}, {Limit: 1, After: &PageCursor{ID: "x"}}, {Limit: 1, After: &PageCursor{ID: "../x", UpdatedAt: time.Now()}}} {
		if _, err := st.ListPage(context.Background(), options); err == nil {
			t.Fatalf("accepted options: %+v", options)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.ListPage(ctx, PageOptions{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query = %v", err)
	}
	id := pageSession(t, st, t.TempDir(), time.Now().UTC())
	if err := os.WriteFile(st.TranscriptPath(id), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ListPage(context.Background(), PageOptions{Limit: 1}); err == nil {
		t.Fatal("query hid a corrupt transcript")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ListPage(context.Background(), PageOptions{Limit: 1}); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed query = %v", err)
	}
}
