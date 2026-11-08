package session

import (
	"context"
	"testing"
)

func TestParseArchiveFilter(t *testing.T) {
	for raw, want := range map[string]ArchiveFilter{"": ArchiveExclude, "exclude": ArchiveExclude, "include": ArchiveInclude, "only": ArchiveOnly} {
		got, err := ParseArchiveFilter(raw)
		if err != nil || got != want {
			t.Fatalf("ParseArchiveFilter(%q) = %v, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"all", "true", "1", "Include"} {
		if _, err := ParseArchiveFilter(raw); err == nil {
			t.Fatalf("ParseArchiveFilter(%q) accepted", raw)
		}
	}
}

func TestListPageAndSnapshotArchiveFilter(t *testing.T) {
	st := openPageStore(t)
	visible := summarySession(t, st, t.TempDir())
	archived := summarySession(t, st, t.TempDir())
	if err := st.SetArchived(archived, true); err != nil {
		t.Fatal(err)
	}
	ids := func(metas []Meta) map[string]bool {
		out := map[string]bool{}
		for _, meta := range metas {
			out[meta.ID] = true
		}
		return out
	}
	for _, tc := range []struct {
		name string
		opts PageOptions
		want []string
	}{
		{"default", PageOptions{Limit: 10}, []string{visible}},
		{"include", PageOptions{Limit: 10, Archive: ArchiveInclude}, []string{visible, archived}},
		{"legacy include", PageOptions{Limit: 10, IncludeArchived: true}, []string{visible, archived}},
		{"only", PageOptions{Limit: 10, Archive: ArchiveOnly}, []string{archived}},
		{"only wins over legacy include", PageOptions{Limit: 10, Archive: ArchiveOnly, IncludeArchived: true}, []string{archived}},
	} {
		page, err := st.ListPage(context.Background(), tc.opts)
		if err != nil {
			t.Fatal(err)
		}
		got := ids(page.Sessions)
		if len(got) != len(tc.want) || page.Total != len(tc.want) {
			t.Fatalf("%s: %v (total %d), want %v", tc.name, got, page.Total, tc.want)
		}
		for _, id := range tc.want {
			if !got[id] {
				t.Fatalf("%s: missing %s in %v", tc.name, id, got)
			}
		}
	}
	for filter, want := range map[ArchiveFilter]int{ArchiveExclude: 1, ArchiveInclude: 2, ArchiveOnly: 1} {
		snapshot, err := st.SummarySnapshotFiltered(context.Background(), filter)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Items) != want {
			t.Fatalf("snapshot filter %d: %d items, want %d", filter, len(snapshot.Items), want)
		}
	}
}
