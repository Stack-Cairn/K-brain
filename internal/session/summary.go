package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

const (
	maxSummaryChanges    = 500
	maxSummaryWaiters    = 4
	maxSummaryTombstones = 10_000
	summaryTombstoneTTL  = 24 * time.Hour
)

var ErrTooManySummaryWaiters = errors.New("too many session change waiters")
var ErrSummaryRescanLimited = errors.New("session summary rescan is rate limited")

type summaryEntry struct {
	path    string
	size    int64
	modTime time.Time
	meta    Meta
	count   int
}

type summaryChangeEntry struct {
	revision uint64
	kind     string
	listed   bool
	entry    summaryEntry
	created  time.Time
}

type summaryState struct {
	epoch        string
	revision     uint64
	minSince     uint64
	entries      map[string]summaryEntry
	changes      map[string]summaryChangeEntry
	notify       chan struct{}
	nextPublish  time.Time
	publishTimer *time.Timer
	waiters      int
	leaseUntil   time.Time
	scannerStop  chan struct{}
	scannerDone  chan struct{}
	scanErr      error
	dirty        bool
	lastRescan   time.Time
	journalUntil time.Time
	journalTimer *time.Timer
}

type SummaryCursor struct {
	Epoch    string
	Revision uint64
	MinSince uint64
}

type SummaryItem struct {
	Meta         Meta
	MessageCount int
}

type SummarySnapshot struct {
	Cursor SummaryCursor
	Items  []SummaryItem
}

type SummaryChange struct {
	ID           string
	Revision     uint64
	Kind         string
	Listed       bool
	Meta         Meta
	MessageCount int
}

type SummaryChangeRequest struct {
	Epoch  string
	Since  uint64
	Wait   time.Duration
	Rescan bool
}

type SummaryChangeResult struct {
	Cursor      SummaryCursor
	NextSince   uint64
	Reset       bool
	ResetReason string
	More        bool
	Changes     []SummaryChange
}

type sessionFile struct {
	id      string
	path    string
	size    int64
	modTime time.Time
}

func cloneSummaryUsage(usage ai.Usage) ai.Usage {
	if usage.PromptTokensDetails != nil {
		details := *usage.PromptTokensDetails
		usage.PromptTokensDetails = &details
	}
	return usage
}

func cloneMeta(meta Meta) Meta {
	meta.Tags = append([]string(nil), meta.Tags...)
	if meta.SubUsage != nil {
		source := meta.SubUsage
		meta.SubUsage = make(map[string]ai.Usage, len(source))
		for key, usage := range source {
			meta.SubUsage[key] = cloneSummaryUsage(usage)
		}
	}
	if meta.ModelUsage != nil {
		source := meta.ModelUsage
		meta.ModelUsage = make(map[string]ai.Usage, len(source))
		for key, usage := range source {
			meta.ModelUsage[key] = cloneSummaryUsage(usage)
		}
	}
	return meta
}

func summaryListed(entry summaryEntry) bool {
	return ArchiveExclude.Listed(entry.meta, entry.count)
}

func summaryProjectionEqual(left, right summaryEntry) bool {
	return left.meta.ID == right.meta.ID &&
		left.meta.Title == right.meta.Title &&
		left.meta.CWD == right.meta.CWD &&
		left.meta.Model == right.meta.Model &&
		left.meta.Provider == right.meta.Provider &&
		left.meta.CreatedAt.Equal(right.meta.CreatedAt) &&
		left.meta.UpdatedAt.Equal(right.meta.UpdatedAt) &&
		left.meta.Pinned == right.meta.Pinned &&
		left.meta.Archived == right.meta.Archived &&
		left.meta.Shared == right.meta.Shared &&
		left.meta.ForkedFrom == right.meta.ForkedFrom &&
		left.meta.ForkSeq == right.meta.ForkSeq &&
		left.meta.TaskID == right.meta.TaskID &&
		left.count == right.count
}

func newSummaryEpoch() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func (s *Store) scanSessionFiles() ([]sessionFile, error) {
	entries, err := os.ReadDir(s.filesDir)
	if err != nil {
		return nil, err
	}
	files := make([]sessionFile, 0, len(entries))
	seen := map[string]bool{}
	appendFile := func(id, path string) error {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("invalid session file for %s", id)
		}
		if seen[id] {
			return fmt.Errorf("session id %q is ambiguous", id)
		}
		seen[id] = true
		files = append(files, sessionFile{id: id, path: path, size: info.Size(), modTime: info.ModTime()})
		return nil
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if validID(entry.Name()) {
			if err := appendFile(entry.Name(), filepath.Join(s.filesDir, entry.Name(), "session.jsonl")); err != nil {
				return nil, err
			}
		}
		if s.projectScoped && validProjectDir(entry.Name()) {
			children, err := os.ReadDir(filepath.Join(s.filesDir, entry.Name()))
			if err != nil {
				return nil, err
			}
			for _, child := range children {
				if child.IsDir() && validID(child.Name()) {
					if err := appendFile(child.Name(), filepath.Join(s.filesDir, entry.Name(), child.Name(), "session.jsonl")); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	return files, nil
}

func (s *Store) readStableSummary(ctx context.Context, file sessionFile) (summaryEntry, error) {
	current := file
	for range 3 {
		if err := ctx.Err(); err != nil {
			return summaryEntry{}, err
		}
		data, err := s.readPath(current.id, current.path)
		if err != nil {
			return summaryEntry{}, err
		}
		info, err := os.Lstat(current.path)
		if err != nil {
			return summaryEntry{}, err
		}
		if info.Mode().IsRegular() && info.Size() == current.size && info.ModTime().Equal(current.modTime) {
			return summaryEntry{path: current.path, size: current.size, modTime: current.modTime, meta: cloneMeta(data.Meta), count: len(data.Messages)}, nil
		}
		current.size, current.modTime = info.Size(), info.ModTime()
	}
	return summaryEntry{}, fmt.Errorf("session %s changed while reading", file.id)
}

func (s *Store) ensureSummariesLocked(ctx context.Context) error {
	if s.summaries != nil {
		return nil
	}
	files, err := s.scanSessionFiles()
	if err != nil {
		return err
	}
	entries := make(map[string]summaryEntry, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, err := s.readStableSummary(ctx, file)
		if err != nil {
			return err
		}
		entries[file.id] = entry
	}
	epoch, err := newSummaryEpoch()
	if err != nil {
		return err
	}
	s.summaries = &summaryState{epoch: epoch, entries: entries, changes: map[string]summaryChangeEntry{}, notify: make(chan struct{})}
	return nil
}

func (s *Store) syncSummariesLocked(ctx context.Context) error {
	if err := s.ensureSummariesLocked(ctx); err != nil {
		return err
	}
	files, err := s.scanSessionFiles()
	if err != nil {
		return err
	}
	next := make(map[string]summaryEntry, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if cached, ok := s.summaries.entries[file.id]; ok && cached.path == file.path && cached.size == file.size && cached.modTime.Equal(file.modTime) {
			next[file.id] = cached
			continue
		}
		entry, err := s.readStableSummary(ctx, file)
		if err != nil {
			return err
		}
		next[file.id] = entry
	}
	old := s.summaries.entries
	ids := make([]string, 0, len(old)+len(next))
	seen := make(map[string]bool, len(old)+len(next))
	for id := range old {
		seen[id] = true
		ids = append(ids, id)
	}
	for id := range next {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		before, hadBefore := old[id]
		after, hasAfter := next[id]
		s.recordSummaryChangeLocked(id, before, hadBefore, after, hasAfter)
	}
	s.summaries.entries = next
	s.summaries.scanErr = nil
	s.summaries.dirty = false
	return nil
}

func (s *Store) recordSummaryChangeLocked(id string, before summaryEntry, hadBefore bool, after summaryEntry, hasAfter bool) {
	state := s.summaries
	if state == nil || (!hadBefore && !hasAfter) {
		return
	}
	if hadBefore && hasAfter && summaryProjectionEqual(before, after) {
		return
	}
	if state.revision == math.MaxUint64 {
		epoch, err := newSummaryEpoch()
		if err != nil {
			fallback := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", state.epoch, time.Now().UnixNano())))
			epoch = hex.EncodeToString(fallback[:16])
		}
		if state.publishTimer != nil {
			state.publishTimer.Stop()
		}
		if state.journalTimer != nil {
			state.journalTimer.Stop()
		}
		state.epoch = epoch
		state.revision = 0
		state.minSince = 0
		state.changes = map[string]summaryChangeEntry{}
		state.nextPublish = time.Time{}
		state.publishTimer = nil
		state.journalUntil = time.Time{}
		state.journalTimer = nil
		close(state.notify)
		state.notify = make(chan struct{})
	}
	state.revision++
	now := time.Now()
	if now.After(state.journalUntil) {
		state.changes = map[string]summaryChangeEntry{}
		state.minSince = state.revision
		return
	}
	change := summaryChangeEntry{revision: state.revision, kind: "upsert", listed: hasAfter && summaryListed(after), entry: after, created: time.Now()}
	if !hasAfter {
		change.kind = "delete"
	}
	state.changes[id] = change
	s.pruneSummaryChangesLocked(time.Now())
	if state.scannerStop == nil && state.waiters == 0 {
		return
	}
	newWindow := state.nextPublish.IsZero() || !now.Before(state.nextPublish)
	if newWindow {
		state.nextPublish = now.Add(s.summaryCoalesce)
		epoch, publishAt := state.epoch, state.nextPublish
		state.publishTimer = time.AfterFunc(s.summaryCoalesce, func() {
			s.publishSummaryWindow(epoch, publishAt)
		})
	}
}

func (s *Store) publishSummaryWindow(epoch string, publishAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.summaries == nil || s.summaries.epoch != epoch || !s.summaries.nextPublish.Equal(publishAt) {
		return
	}
	s.summaries.nextPublish = time.Time{}
	s.summaries.publishTimer = nil
	close(s.summaries.notify)
	s.summaries.notify = make(chan struct{})
}

func (s *Store) extendSummaryJournalLocked(until time.Time) {
	state := s.summaries
	if state == nil || !until.After(state.journalUntil) {
		return
	}
	state.journalUntil = until
	if state.journalTimer != nil {
		state.journalTimer.Stop()
	}
	epoch := state.epoch
	state.journalTimer = time.AfterFunc(max(time.Millisecond, time.Until(until)), func() {
		s.expireSummaryJournal(epoch)
	})
}

func (s *Store) expireSummaryJournal(epoch string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.summaries
	if s.closed || state == nil || state.epoch != epoch {
		return
	}
	if time.Now().Before(state.journalUntil) {
		state.journalTimer = time.AfterFunc(time.Until(state.journalUntil), func() {
			s.expireSummaryJournal(epoch)
		})
		return
	}
	state.changes = map[string]summaryChangeEntry{}
	state.minSince = state.revision
	state.journalTimer = nil
}

func (s *Store) updateSummaryAfterWriteLocked(data *sessionData, path string) error {
	if s.summaries == nil {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		s.summaries.dirty = true
		s.summaries.scanErr = err
		close(s.summaries.notify)
		s.summaries.notify = make(chan struct{})
		return nil
	}
	before, hadBefore := s.summaries.entries[data.Meta.ID]
	after := summaryEntry{path: path, size: info.Size(), modTime: info.ModTime(), meta: cloneMeta(data.Meta), count: len(data.Messages)}
	s.recordSummaryChangeLocked(data.Meta.ID, before, hadBefore, after, true)
	s.summaries.entries[data.Meta.ID] = after
	return nil
}

func (s *Store) removeSummaryLocked(id string) {
	if s.summaries == nil {
		return
	}
	before, ok := s.summaries.entries[id]
	if !ok {
		return
	}
	s.recordSummaryChangeLocked(id, before, true, summaryEntry{}, false)
	delete(s.summaries.entries, id)
}

func summaryCursor(state *summaryState) SummaryCursor {
	return SummaryCursor{Epoch: state.epoch, Revision: state.revision, MinSince: state.minSince}
}

func (s *Store) SummaryCursor(ctx context.Context) (cursor SummaryCursor, err error) {
	err = s.withLock(func() error {
		if err := s.ensureSummariesLocked(ctx); err != nil {
			return err
		}
		if s.summaries.dirty {
			if err := s.syncSummariesLocked(ctx); err != nil {
				return err
			}
		}
		s.pruneSummaryChangesLocked(time.Now())
		cursor = summaryCursor(s.summaries)
		return nil
	})
	return
}

func (s *Store) SummarySnapshot(ctx context.Context) (snapshot SummarySnapshot, err error) {
	return s.SummarySnapshotFiltered(ctx, ArchiveExclude)
}

// SummarySnapshotFiltered is SummarySnapshot with an explicit archived filter.
func (s *Store) SummarySnapshotFiltered(ctx context.Context, archive ArchiveFilter) (snapshot SummarySnapshot, err error) {
	err = s.withLock(func() error {
		if err := s.syncSummariesLocked(ctx); err != nil {
			return err
		}
		s.pruneSummaryChangesLocked(time.Now())
		s.extendSummaryJournalLocked(time.Now().Add(s.summarySnapshotLease))
		snapshot.Cursor = summaryCursor(s.summaries)
		for _, entry := range s.summaries.entries {
			if archive.Listed(entry.meta, entry.count) {
				snapshot.Items = append(snapshot.Items, SummaryItem{Meta: cloneMeta(entry.meta), MessageCount: entry.count})
			}
		}
		return nil
	})
	if err == nil {
		sort.Slice(snapshot.Items, func(i, j int) bool { return metaBefore(snapshot.Items[i].Meta, snapshot.Items[j].Meta) })
	}
	return
}

func (s *Store) pruneSummaryChangesLocked(now time.Time) {
	state := s.summaries
	if state == nil {
		return
	}
	tombstones := make([]struct {
		id     string
		change summaryChangeEntry
	}, 0)
	for id, change := range state.changes {
		if change.kind == "delete" {
			tombstones = append(tombstones, struct {
				id     string
				change summaryChangeEntry
			}{id: id, change: change})
		}
	}
	sort.Slice(tombstones, func(i, j int) bool { return tombstones[i].change.revision < tombstones[j].change.revision })
	removeCount := max(0, len(tombstones)-maxSummaryTombstones)
	for index, item := range tombstones {
		if index >= removeCount && now.Sub(item.change.created) <= summaryTombstoneTTL {
			continue
		}
		delete(state.changes, item.id)
		state.minSince = max(state.minSince, item.change.revision)
	}
}

func (s *Store) summaryChangesLocked(request SummaryChangeRequest, force bool) (SummaryChangeResult, bool) {
	state := s.summaries
	now := time.Now()
	s.pruneSummaryChangesLocked(now)
	result := SummaryChangeResult{Cursor: summaryCursor(state), NextSince: request.Since, Changes: []SummaryChange{}}
	if request.Epoch == "" || request.Epoch != state.epoch {
		result.Reset, result.ResetReason = true, "epoch"
		return result, true
	}
	if request.Since < state.minSince {
		result.Reset, result.ResetReason = true, "expired"
		return result, true
	}
	if request.Since > state.revision {
		result.Reset, result.ResetReason = true, "future"
		return result, true
	}
	candidates := make([]struct {
		id     string
		change summaryChangeEntry
	}, 0)
	for id, change := range state.changes {
		if change.revision > request.Since {
			candidates = append(candidates, struct {
				id     string
				change summaryChangeEntry
			}{id: id, change: change})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].change.revision < candidates[j].change.revision })
	if len(candidates) == 0 {
		return result, force || request.Wait <= 0
	}
	if !force && request.Wait > 0 && !state.nextPublish.IsZero() && time.Now().Before(state.nextPublish) {
		return result, false
	}
	result.More = len(candidates) > maxSummaryChanges
	if result.More {
		candidates = candidates[:maxSummaryChanges]
	}
	for _, candidate := range candidates {
		change := candidate.change
		result.Changes = append(result.Changes, SummaryChange{ID: candidate.id, Revision: change.revision, Kind: change.kind, Listed: change.listed, Meta: cloneMeta(change.entry.meta), MessageCount: change.entry.count})
	}
	result.NextSince = result.Changes[len(result.Changes)-1].Revision
	return result, true
}

func (s *Store) startSummaryScannerLocked() {
	state := s.summaries
	state.leaseUntil = time.Now().Add(s.summaryScanLease)
	s.extendSummaryJournalLocked(state.leaseUntil)
	if state.scannerStop != nil {
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	state.scannerStop, state.scannerDone = stop, done
	go s.summaryScanner(stop, done)
}

func (s *Store) summaryScanner(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	timer := time.NewTimer(s.summaryScanInterval)
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		state := s.summaries
		if state == nil || state.waiters == 0 && time.Now().After(state.leaseUntil) {
			if state != nil {
				state.scannerStop = nil
			}
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		_ = s.withLock(func() error {
			err := s.syncSummariesLocked(context.Background())
			if err != nil && s.summaries != nil {
				s.summaries.scanErr = err
				close(s.summaries.notify)
				s.summaries.notify = make(chan struct{})
			}
			return nil
		})
		timer.Reset(s.summaryScanInterval)
	}
}

func (s *Store) releaseSummaryWaiter() {
	s.mu.Lock()
	if s.summaries != nil && s.summaries.waiters > 0 {
		s.summaries.waiters--
	}
	s.mu.Unlock()
}

func (s *Store) SummaryChanges(ctx context.Context, request SummaryChangeRequest) (SummaryChangeResult, error) {
	if request.Wait < 0 || request.Wait > 60*time.Second {
		return SummaryChangeResult{}, errors.New("change wait must be between 0 and 60 seconds")
	}
	err := s.withLock(func() error {
		if err := s.ensureSummariesLocked(ctx); err != nil {
			return err
		}
		if request.Rescan {
			if !s.summaries.lastRescan.IsZero() && time.Since(s.summaries.lastRescan) < time.Second {
				return ErrSummaryRescanLimited
			}
			if err := s.syncSummariesLocked(ctx); err != nil {
				return err
			}
			s.summaries.lastRescan = time.Now()
		}
		if s.summaries.waiters >= maxSummaryWaiters {
			return ErrTooManySummaryWaiters
		}
		s.summaries.waiters++
		s.startSummaryScannerLocked()
		return nil
	})
	if err != nil {
		return SummaryChangeResult{}, err
	}
	defer s.releaseSummaryWaiter()
	deadline := time.Now().Add(request.Wait)
	for {
		if s.summaryWaitObserver != nil {
			s.summaryWaitObserver()
		}
		var result SummaryChangeResult
		var ready bool
		var notify <-chan struct{}
		var delay time.Duration
		err := s.withLock(func() error {
			if s.summaries.scanErr != nil {
				return s.summaries.scanErr
			}
			force := request.Wait <= 0 || !time.Now().Before(deadline)
			result, ready = s.summaryChangesLocked(request, force)
			if ready {
				return nil
			}
			notify = s.summaries.notify
			delay = time.Until(deadline)
			return nil
		})
		if err != nil {
			return SummaryChangeResult{}, err
		}
		if ready {
			return result, nil
		}
		if delay < 0 {
			delay = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return SummaryChangeResult{}, ctx.Err()
		case <-notify:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}
