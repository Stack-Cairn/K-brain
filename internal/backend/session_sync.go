package backend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

const (
	sessionSnapshotTTL = 60 * time.Second
	// A backend has one authenticated session scope, so its per-identity limit
	// is also the effective store limit.
	maxSessionSnapshots        = 4
	maxSessionSnapshotBytes    = 64 << 20
	maxSessionSnapshotBuilders = 1
)

type sessionHTTPSnapshot struct {
	epoch     string
	revision  uint64
	expiresAt time.Time
	sessions  []protocol.SessionSummary
	workdirs  []protocol.SessionWorkdir
	bytes     int
	timer     *time.Timer
}

func summaryView(item session.SummaryItem) protocol.SessionSummary {
	out := summary(item.Meta, item.MessageCount)
	out.ExternalRun = externalRunView(item.ExternalRun)
	return out
}

func snapshotWorkdirs(items []session.SummaryItem) []protocol.SessionWorkdir {
	byCWD := map[string]protocol.SessionWorkdir{}
	for _, item := range items {
		cwd := item.Meta.CWD
		if cwd == "" {
			continue
		}
		current := byCWD[cwd]
		current.CWD = cwd
		current.Count++
		if item.Meta.UpdatedAt.After(current.UpdatedAt) {
			current.UpdatedAt = item.Meta.UpdatedAt
		}
		byCWD[cwd] = current
	}
	out := make([]protocol.SessionWorkdir, 0, len(byCWD))
	for _, item := range byCWD {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].CWD < out[j].CWD
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func newSessionSnapshotID() (string, error) {
	data := make([]byte, 24)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func (s *Server) pruneHTTPSnapshotsLocked(now time.Time) {
	for id, snapshot := range s.snapshots {
		if !now.Before(snapshot.expiresAt) {
			if snapshot.timer != nil {
				snapshot.timer.Stop()
			}
			delete(s.snapshots, id)
		}
	}
}

func (s *Server) createSessionSnapshot(w http.ResponseWriter, r *http.Request) {
	archive, err := archiveQuery(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Reserve before copying or encoding a full projection. The retained and
	// in-flight counts share the four-snapshot budget, with only one builder.
	s.snapshotMu.Lock()
	if s.shuttingDown.Load() {
		s.snapshotMu.Unlock()
		writeJSONError(w, http.StatusServiceUnavailable, "backend is closing")
		return
	}
	s.pruneHTTPSnapshotsLocked(time.Now())
	if s.snapshotBuilders >= maxSessionSnapshotBuilders || len(s.snapshots)+s.snapshotBuilders >= maxSessionSnapshots || s.sessionSnapshotBytesLocked() >= maxSessionSnapshotBytes {
		s.snapshotMu.Unlock()
		w.Header().Set("Retry-After", "1")
		writeJSONError(w, http.StatusTooManyRequests, "session snapshot budget exhausted")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	s.snapshotBuilders++
	s.snapshotBuildCancel = cancel
	s.snapshotMu.Unlock()
	defer func() {
		cancel()
		s.snapshotMu.Lock()
		s.snapshotBuilders--
		s.snapshotBuildCancel = nil
		s.snapshotMu.Unlock()
	}()
	if err := r.Context().Err(); err != nil {
		return
	}
	snapshot, err := s.store.SummarySnapshotFiltered(ctx, archive)
	if err != nil {
		if s.shuttingDown.Load() {
			writeJSONError(w, http.StatusServiceUnavailable, "backend is closing")
		} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			mutationError(w, err)
		}
		return
	}
	if ctx.Err() != nil {
		if s.shuttingDown.Load() {
			writeJSONError(w, http.StatusServiceUnavailable, "backend is closing")
		}
		return
	}
	sessions := make([]protocol.SessionSummary, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		sessions = append(sessions, summaryView(item))
	}
	workdirs := snapshotWorkdirs(snapshot.Items)
	encoded, err := json.Marshal(struct {
		Sessions []protocol.SessionSummary `json:"sessions"`
		Workdirs []protocol.SessionWorkdir `json:"workdirs"`
	}{sessions, workdirs})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(encoded) > maxSessionSnapshotBytes {
		writeJSONError(w, http.StatusServiceUnavailable, "session snapshot exceeds memory budget")
		return
	}
	id, err := newSessionSnapshotID()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	s.snapshotMu.Lock()
	if s.shuttingDown.Load() || ctx.Err() != nil {
		s.snapshotMu.Unlock()
		if s.shuttingDown.Load() {
			writeJSONError(w, http.StatusServiceUnavailable, "backend is closing")
		}
		return
	}
	s.pruneHTTPSnapshotsLocked(now)
	totalBytes := s.sessionSnapshotBytesLocked()
	if len(s.snapshots) >= maxSessionSnapshots || totalBytes+len(encoded) > maxSessionSnapshotBytes {
		s.snapshotMu.Unlock()
		w.Header().Set("Retry-After", "1")
		writeJSONError(w, http.StatusTooManyRequests, "session snapshot budget exhausted")
		return
	}
	expiresAt := now.Add(sessionSnapshotTTL)
	stored := sessionHTTPSnapshot{epoch: snapshot.Cursor.Epoch, revision: snapshot.Cursor.Revision, expiresAt: expiresAt, sessions: sessions, workdirs: workdirs, bytes: len(encoded)}
	stored.timer = time.AfterFunc(sessionSnapshotTTL, func() {
		s.snapshotMu.Lock()
		current, ok := s.snapshots[id]
		if ok && current.expiresAt.Equal(expiresAt) {
			delete(s.snapshots, id)
		}
		s.snapshotMu.Unlock()
	})
	s.snapshots[id] = stored
	s.snapshotMu.Unlock()
	writeJSON(w, http.StatusOK, protocol.SessionSnapshotInfo{Epoch: snapshot.Cursor.Epoch, Revision: strconv.FormatUint(snapshot.Cursor.Revision, 10), SnapshotID: id, ExpiresInSeconds: int(sessionSnapshotTTL / time.Second), TotalCount: len(sessions), Workdirs: workdirs})
}

func (s *Server) sessionSnapshotBytesLocked() int {
	total := 0
	for _, snapshot := range s.snapshots {
		total += snapshot.bytes
	}
	return total
}

func (s *Server) sessionSnapshot(w http.ResponseWriter, r *http.Request, id string) {
	s.snapshotMu.Lock()
	s.pruneHTTPSnapshotsLocked(time.Now())
	snapshot, ok := s.snapshots[id]
	if r.Method == http.MethodDelete {
		if ok && snapshot.timer != nil {
			snapshot.timer.Stop()
		}
		delete(s.snapshots, id)
		s.snapshotMu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.snapshotMu.Unlock()
	if !ok {
		writeJSONError(w, http.StatusGone, "session snapshot expired")
		return
	}
	cursor, err := s.store.SummaryCursor(r.Context())
	if err != nil {
		mutationError(w, err)
		return
	}
	if cursor.Epoch != snapshot.epoch || snapshot.revision < cursor.MinSince {
		writeJSONError(w, http.StatusGone, "session snapshot is no longer resumable")
		return
	}
	offset, err := intQuery(r, "offset", 0, 0, int(^uint(0)>>1))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := intQuery(r, "limit", 500, 1, 500)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if offset > len(snapshot.sessions) {
		writeJSONError(w, http.StatusBadRequest, "snapshot offset is out of range")
		return
	}
	end := min(len(snapshot.sessions), offset+limit)
	items := make([]protocol.SessionSummary, end-offset)
	copy(items, snapshot.sessions[offset:end])
	var next *int
	if end < len(snapshot.sessions) {
		value := end
		next = &value
	}
	writeJSON(w, http.StatusOK, protocol.SessionSnapshotPage{Epoch: snapshot.epoch, Revision: strconv.FormatUint(snapshot.revision, 10), SnapshotID: id, TotalCount: len(snapshot.sessions), Sessions: items, NextOffset: next})
}

func uint64Query(r *http.Request, key string) (uint64, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return 0, fmt.Errorf("%s is required", key)
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return n, nil
}

func (s *Server) sessionChanges(w http.ResponseWriter, r *http.Request) {
	since, err := uint64Query(r, "since")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	wait, err := intQuery(r, "wait", 0, 0, 60)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	rescanRaw := r.URL.Query().Get("rescan")
	if rescanRaw != "" && rescanRaw != "0" && rescanRaw != "1" {
		writeJSONError(w, http.StatusBadRequest, "invalid rescan")
		return
	}
	rescan := rescanRaw == "1"
	archive, err := archiveQuery(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.store.SummaryChanges(r.Context(), session.SummaryChangeRequest{Epoch: r.URL.Query().Get("epoch"), Since: since, Wait: time.Duration(wait) * time.Second, Rescan: rescan})
	if err != nil {
		if errors.Is(err, session.ErrTooManySummaryWaiters) {
			w.Header().Set("Retry-After", "1")
			writeJSONError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		if errors.Is(err, session.ErrSummaryRescanLimited) {
			w.Header().Set("Retry-After", "1")
			writeJSONError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		mutationError(w, err)
		return
	}
	out := protocol.SessionChanges{Epoch: result.Cursor.Epoch, HeadRevision: strconv.FormatUint(result.Cursor.Revision, 10), MinSince: strconv.FormatUint(result.Cursor.MinSince, 10), Reset: result.Reset, ResetReason: result.ResetReason, More: result.More, Changes: []protocol.SessionChange{}}
	if !result.Reset {
		next := strconv.FormatUint(result.NextSince, 10)
		out.NextSince = &next
	}
	for _, change := range result.Changes {
		// listed is recomputed for the requested archived filter; an archive or
		// unarchive is an upsert whose session.archived flipped, so a client on
		// the default filter sees listed turn false (or true) and one on
		// archived=include sees the flag change in place.
		listed := change.Kind == "upsert" && archive.Listed(change.Meta, change.MessageCount)
		item := protocol.SessionChange{ID: change.ID, Revision: strconv.FormatUint(change.Revision, 10), Kind: change.Kind, Listed: listed}
		if change.Kind == "upsert" {
			summary := summary(change.Meta, change.MessageCount)
			summary.ExternalRun = externalRunView(change.ExternalRun)
			item.Session = &summary
		}
		out.Changes = append(out.Changes, item)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) sessionWorkdirs(w http.ResponseWriter, r *http.Request) {
	archive, err := archiveQuery(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, err := s.store.SummarySnapshotFiltered(r.Context(), archive)
	if err != nil {
		mutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"epoch": snapshot.Cursor.Epoch, "revision": strconv.FormatUint(snapshot.Cursor.Revision, 10), "workdirs": snapshotWorkdirs(snapshot.Items)})
}

func snapshotPathID(path string) (string, bool) {
	const prefix = "/v1/sessions/snapshots/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	id := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if len(id) != 48 {
		return "", false
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", false
	}
	return id, true
}
