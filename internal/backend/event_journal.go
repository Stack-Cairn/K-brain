package backend

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// The event journal (<eventDir>/<session>.jsonl) is append-only and ordered by seq:
// every append happens under rt.mu with seq nextSeq+1, and the rewrites (history
// edit, compaction rollback) keep that order. Each event is written to the file
// before it is published, so a subscriber never sees an event the journal lacks.

// journalMarkInterval spaces the offset marks: replay from a cursor decodes at most
// this many events before the first one it returns.
const journalMarkInterval = 256

// keepJournalHandle is off on Windows: there a file opened by os.OpenFile cannot be
// removed or renamed while it is open, and the journal may be replaced or removed
// during a run. Windows keeps opening the journal for every event.
const keepJournalHandle = runtime.GOOS != "windows"

// journalMark records that the journal events before offset all have a seq <= seq and
// the events from offset on all have a seq > seq.
type journalMark struct{ seq, offset int64 }

func (rt *runtimeSession) journalFile(dir string) string {
	return filepath.Join(dir, rt.id+".jsonl")
}

// keepJournalOpenLocked reports whether the journal may stay open after a write. It
// does so only while a run streams, so a delta costs one write instead of an open,
// a write and a close; the handle is closed when the run ends.
func (rt *runtimeSession) keepJournalOpenLocked() bool {
	return keepJournalHandle && rt.cancel != nil && !rt.runDone && !rt.closing && !rt.deleted
}

func (rt *runtimeSession) closeJournalLocked() {
	if rt.journal != nil {
		_ = rt.journal.Close()
		rt.journal, rt.journalPath = nil, ""
	}
}

// resetJournalLocked drops the open handle and the offsets once the journal file was
// replaced or removed.
func (rt *runtimeSession) resetJournalLocked() {
	rt.closeJournalLocked()
	rt.journalMarks, rt.journalIdentity = nil, nil
}

// useJournalMarksLocked ties the offset marks to the file f: marks taken from another
// file (the journal was replaced) are dropped. It reports whether f is that file.
func (rt *runtimeSession) useJournalMarksLocked(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		rt.journalMarks, rt.journalIdentity = nil, nil
		return false
	}
	if rt.journalIdentity == nil || !os.SameFile(rt.journalIdentity, info) {
		rt.journalMarks, rt.journalIdentity = nil, info
	}
	return true
}

// openJournalLocked returns the journal handle for path: the kept one while it still
// names the file at path, otherwise a newly opened one, kept when a run streams.
// A path that was removed or replaced is reopened like before, so a write to a
// journal that is gone fails or recreates it rather than reaching a stale file.
func (rt *runtimeSession) openJournalLocked(path string) (f *os.File, temporary bool, err error) {
	if rt.journal != nil && rt.journalPath == path {
		if info, statErr := os.Stat(path); statErr == nil && rt.journalIdentity != nil && os.SameFile(rt.journalIdentity, info) {
			return rt.journal, false, nil
		}
		rt.closeJournalLocked()
	}
	if f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err != nil {
		return nil, false, err
	}
	if path != rt.journalFile(rt.eventDir) {
		return f, true, nil
	}
	if !rt.useJournalMarksLocked(f) || rt.journal != nil || !rt.keepJournalOpenLocked() {
		return f, true, nil
	}
	rt.journal, rt.journalPath = f, path
	return f, false, nil
}

func (rt *runtimeSession) persistEvent(dir string, e protocol.Event) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	path := rt.journalFile(dir)
	f, temporary, err := rt.openJournalLocked(path)
	if err != nil {
		return err
	}
	if temporary {
		defer f.Close()
	}
	// Unbuffered: the event is in the file before the caller publishes it.
	if _, err := f.Write(line); err != nil {
		if f == rt.journal {
			rt.closeJournalLocked()
		}
		return err
	}
	if e.Seq%journalMarkInterval == 0 && path == rt.journalFile(rt.eventDir) && rt.journalIdentity != nil {
		if info, statErr := f.Stat(); statErr == nil && os.SameFile(rt.journalIdentity, info) {
			rt.addJournalMarkLocked(e.Seq, info.Size())
		}
	}
	if f == rt.journal && !rt.keepJournalOpenLocked() {
		rt.closeJournalLocked()
	}
	return nil
}

func (rt *runtimeSession) addJournalMarkLocked(seq, offset int64) {
	i := sort.Search(len(rt.journalMarks), func(i int) bool { return rt.journalMarks[i].seq >= seq })
	if i < len(rt.journalMarks) && rt.journalMarks[i].seq == seq {
		return
	}
	rt.journalMarks = slices.Insert(rt.journalMarks, i, journalMark{seq: seq, offset: offset})
}

// journalStartLocked is the offset where reading the events after the cursor may begin.
func (rt *runtimeSession) journalStartLocked(after int64) int64 {
	i := sort.Search(len(rt.journalMarks), func(i int) bool { return rt.journalMarks[i].seq > after })
	if i == 0 {
		return 0
	}
	return rt.journalMarks[i-1].offset
}

// scanJournal decodes and validates the journal events from offset start on, recording
// offset marks as it goes. visit returns false to stop.
func (rt *runtimeSession) scanJournal(f *os.File, start int64, visit func(protocol.Event) bool) error {
	if start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return err
		}
	}
	decoder := json.NewDecoder(bufio.NewReaderSize(f, 64<<10))
	for {
		var e protocol.Event
		err := decoder.Decode(&e)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := e.Validate(); err != nil {
			return err
		}
		if e.Seq%journalMarkInterval == 0 {
			rt.addJournalMarkLocked(e.Seq, start+decoder.InputOffset())
		}
		if !visit(e) {
			return nil
		}
	}
}

func (rt *runtimeSession) loadJournal(dir string) error {
	rt.eventDir = dir
	f, err := os.Open(rt.journalFile(dir))
	if errors.Is(err, os.ErrNotExist) {
		return rt.loadRuns(dir)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	rt.useJournalMarksLocked(f)
	if err := rt.scanJournal(f, 0, func(e protocol.Event) bool {
		rt.appendEventLocked(e)
		if e.Seq > rt.nextSeq {
			rt.nextSeq = e.Seq
		}
		rt.runID = e.RunID
		return true
	}); err != nil {
		return err
	}
	return rt.loadRuns(dir)
}

func (rt *runtimeSession) journalEventsAfter(after int64, limit int) ([]protocol.Event, error) {
	f, err := os.Open(rt.journalFile(rt.eventDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	start := int64(0)
	if rt.useJournalMarksLocked(f) {
		start = rt.journalStartLocked(after)
	}
	var events []protocol.Event
	bytes := 0
	err = rt.scanJournal(f, start, func(e protocol.Event) bool {
		if e.Seq <= after {
			return true
		}
		events = append(events, e)
		bytes += len(e.Payload) + 256
		return limit <= 0 || (len(events) < limit && bytes < maxCachedEventBytes)
	})
	return events, err
}
