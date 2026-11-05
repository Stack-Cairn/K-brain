package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

func journalTestRuntime(t testing.TB, streaming bool) *runtimeSession {
	t.Helper()
	rt := &runtimeSession{id: "journal-session", runID: "journal-run", eventDir: t.TempDir(), changed: make(chan struct{}), runs: map[string]runRecord{}}
	if streaming {
		rt.cancel = func() {}
	}
	return rt
}

func publishDeltas(t testing.TB, rt *runtimeSession, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := rt.publish(protocol.EventTextDelta, map[string]string{"text": "tok "}, ""); err != nil {
			t.Fatal(err)
		}
	}
}

func journalSeqs(t *testing.T, path string) []int64 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var seqs []int64
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var e protocol.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, e.Seq)
	}
	return seqs
}

func assertContiguous(t *testing.T, events []protocol.Event, first, last int64) {
	t.Helper()
	if want := int(last - first + 1); len(events) != want {
		t.Fatalf("got %d events, want %d (seq %d..%d)", len(events), want, first, last)
	}
	for i, e := range events {
		if e.Seq != first+int64(i) {
			t.Fatalf("event %d has seq %d, want %d", i, e.Seq, first+int64(i))
		}
	}
}

func TestJournalHandleKeptOnlyWhileRunStreams(t *testing.T) {
	if !keepJournalHandle {
		t.Skip("the journal is reopened for every event on this platform")
	}
	idle := journalTestRuntime(t, false)
	publishDeltas(t, idle, 3)
	if idle.journal != nil {
		t.Fatal("journal handle kept open without a streaming run")
	}

	rt := journalTestRuntime(t, true)
	publishDeltas(t, rt, 3)
	if rt.journal == nil {
		t.Fatal("journal handle was not kept while the run streams")
	}
	kept := rt.journal
	publishDeltas(t, rt, 3)
	if rt.journal != kept {
		t.Fatal("journal reopened between deltas of one run")
	}
	// A run that ended closes the handle on its next write.
	rt.mu.Lock()
	rt.runDone = true
	rt.mu.Unlock()
	publishDeltas(t, rt, 1)
	if rt.journal != nil {
		t.Fatal("journal handle still open after the run ended")
	}
	if got := journalSeqs(t, rt.journalFile(rt.eventDir)); len(got) != 7 || got[6] != 7 {
		t.Fatalf("journal seqs = %v", got)
	}
}

func TestJournalWriteFollowsReplacedOrRemovedFile(t *testing.T) {
	rt := journalTestRuntime(t, true)
	path := rt.journalFile(rt.eventDir)
	publishDeltas(t, rt, 2)

	// Replaced: the next write goes to the file now at the path, not the old inode.
	moved := path + ".old"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	publishDeltas(t, rt, 1)
	if got := journalSeqs(t, moved); len(got) != 2 {
		t.Fatalf("old journal got a write after it was replaced: %v", got)
	}
	if got := journalSeqs(t, path); len(got) != 1 || got[0] != 3 {
		t.Fatalf("new journal seqs = %v", got)
	}

	// Removed: the write recreates the journal, like an open per write did.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	publishDeltas(t, rt, 1)
	if got := journalSeqs(t, path); len(got) != 1 || got[0] != 4 {
		t.Fatalf("recreated journal seqs = %v", got)
	}

	// Unwritable: the publish fails and the event stays invisible.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.publish(protocol.EventTextDelta, map[string]string{"text": "x"}, ""); err == nil {
		t.Fatal("publish succeeded although the journal path is a directory")
	}
	if rt.nextSeq != 4 || rt.events[len(rt.events)-1].Seq != 4 {
		t.Fatalf("failed publish advanced the runtime: next=%d", rt.nextSeq)
	}
}

func TestJournalReplayFromMarksMatchesFullScan(t *testing.T) {
	const total = 2000
	rt := journalTestRuntime(t, true)
	publishDeltas(t, rt, total)
	if len(rt.journalMarks) == 0 {
		t.Fatal("no offset marks recorded while publishing")
	}
	cold := &runtimeSession{id: rt.id, changed: make(chan struct{})}
	if err := cold.loadJournal(rt.eventDir); err != nil {
		t.Fatal(err)
	}
	if cold.nextSeq != total || len(cold.journalMarks) != len(rt.journalMarks) {
		t.Fatalf("cold load: next=%d marks=%d, want %d marks", cold.nextSeq, len(cold.journalMarks), len(rt.journalMarks))
	}
	// The writer marks the offset after the newline and the scanner the one before it;
	// both start a decode at the next event.
	for i := range rt.journalMarks {
		if a, b := rt.journalMarks[i], cold.journalMarks[i]; a.seq != b.seq || a.offset-b.offset > 1 || b.offset > a.offset {
			t.Fatalf("mark %d: written %+v, scanned %+v", i, rt.journalMarks[i], cold.journalMarks[i])
		}
	}
	for _, after := range []int64{0, 1, 255, 256, 257, 511, 512, 1000, 1023, 1024, 1999, total} {
		for _, runtime := range []*runtimeSession{rt, cold} {
			all, err := runtime.journalEventsAfter(after, 0)
			if err != nil {
				t.Fatal(err)
			}
			assertContiguous(t, all, after+1, total)
			page, err := runtime.journalEventsAfter(after, maxCachedEvents)
			if err != nil {
				t.Fatal(err)
			}
			assertContiguous(t, page, after+1, min(after+maxCachedEvents, total))
		}
	}
}

func TestLaggingSubscriberReplayIsContiguous(t *testing.T) {
	const total = 1500
	rt := journalTestRuntime(t, true)
	publishDeltas(t, rt, total)
	if !rt.eventsTruncated {
		t.Fatal("event cache was not truncated")
	}
	cursor := int64(10)
	for cursor < total {
		rt.mu.Lock()
		events, err := rt.eventsAfterLocked(cursor)
		rt.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if len(events) == 0 {
			t.Fatalf("replay stalled at seq %d", cursor)
		}
		assertContiguous(t, events, cursor+1, events[len(events)-1].Seq)
		cursor = events[len(events)-1].Seq
	}
}

func TestJournalMarksDroppedWhenFileReplaced(t *testing.T) {
	rt := journalTestRuntime(t, false)
	publishDeltas(t, rt, 1200)
	path := rt.journalFile(rt.eventDir)

	// Rewrite the journal with fewer, differently sized events, as a history edit does.
	var data []byte
	for seq := int64(1); seq <= 600; seq++ {
		e, err := protocol.NewEvent(seq, rt.id, "edited-run", protocol.EventTextDelta, map[string]string{"text": strings.Repeat("y", 40)})
		if err != nil {
			t.Fatal(err)
		}
		line, _ := json.Marshal(e)
		data = append(append(data, line...), '\n')
	}
	tmp := filepath.Join(rt.eventDir, "replacement")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	// Without an explicit reset, the new file identity alone must drop the old marks.
	events, err := rt.journalEventsAfter(300, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertContiguous(t, events, 301, 600)
	if events[0].RunID != "edited-run" {
		t.Fatalf("replay read the replaced journal: run %q", events[0].RunID)
	}
	for _, mark := range rt.journalMarks {
		if mark.seq > 600 {
			t.Fatalf("stale mark %+v survived the replacement", mark)
		}
	}
}

func BenchmarkPublishTextDelta(b *testing.B) {
	rt := journalTestRuntime(b, true)
	b.ReportAllocs()
	b.ResetTimer()
	publishDeltas(b, rt, b.N)
}

func BenchmarkJournalReplayNearTail(b *testing.B) {
	const total = 50000
	rt := journalTestRuntime(b, false)
	publishDeltas(b, rt, total)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		events, err := rt.journalEventsAfter(total-100, maxCachedEvents)
		if err != nil || len(events) != 100 {
			b.Fatalf("events=%d err=%v", len(events), err)
		}
	}
}
