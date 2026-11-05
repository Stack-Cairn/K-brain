package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

type checkpointEntry struct {
	Path    string `json:"path"`
	Data    []byte `json:"data,omitempty"`
	Mode    uint32 `json:"mode,omitempty"`
	Missing bool   `json:"missing,omitempty"`
	Error   string `json:"error,omitempty"`
}

type checkpointRecord struct {
	ID              string            `json:"id"`
	SessionID       string            `json:"session_id"`
	RunID           string            `json:"run_id"`
	TurnID          string            `json:"turn_id"`
	TurnSeq         int               `json:"turn_seq"`
	FirstCapturedAt time.Time         `json:"first_captured_at"`
	Incomplete      bool              `json:"incomplete"`
	Entries         []checkpointEntry `json:"entries"`
}

type checkpointCapture struct {
	mu                                 sync.Mutex
	eventDir, sessionID, runID, turnID string
	entries                            map[string]checkpointEntry
	captureErrors                      int
	firstCapturedAt                    time.Time
}

func newCheckpointCapture(eventDir, sessionID, runID, turnID string) *checkpointCapture {
	return &checkpointCapture{eventDir: eventDir, sessionID: sessionID, runID: runID, turnID: turnID, entries: map[string]checkpointEntry{}}
}

func (c *checkpointCapture) capture(path string) {
	path, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return
	}
	c.mu.Lock()
	if _, ok := c.entries[path]; ok {
		c.mu.Unlock()
		return
	}
	if c.firstCapturedAt.IsZero() {
		c.firstCapturedAt = time.Now().UTC()
	}
	c.mu.Unlock()

	entry := checkpointEntry{Path: path}
	info, statErr := os.Lstat(path)
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		entry.Missing = true
	case statErr != nil:
		entry.Error = statErr.Error()
	case info.Mode()&os.ModeSymlink != 0:
		entry.Error = "symbolic links are not checkpointed"
	case !info.Mode().IsRegular():
		entry.Error = "only regular files are checkpointed"
	default:
		entry.Mode = uint32(info.Mode().Perm())
		entry.Data, statErr = os.ReadFile(path)
		if statErr != nil {
			entry.Error = statErr.Error()
		}
	}
	c.mu.Lock()
	if _, exists := c.entries[path]; !exists {
		c.entries[path] = entry
		if entry.Error != "" {
			c.captureErrors++
		}
	}
	c.mu.Unlock()
}

func (c *checkpointCapture) commit(turnSeq int) (checkpointRecord, error) {
	c.mu.Lock()
	entries := make([]checkpointEntry, 0, len(c.entries))
	for _, entry := range c.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	record := checkpointRecord{ID: fmt.Sprintf("cp-%d", time.Now().UnixNano()), SessionID: c.sessionID, RunID: c.runID, TurnID: c.turnID, TurnSeq: turnSeq, FirstCapturedAt: c.firstCapturedAt, Incomplete: c.captureErrors > 0, Entries: entries}
	c.mu.Unlock()
	if record.FirstCapturedAt.IsZero() {
		record.FirstCapturedAt = time.Now().UTC()
	}
	if err := os.MkdirAll(filepath.Join(c.eventDir, "checkpoints", c.sessionID), 0o700); err != nil {
		return checkpointRecord{}, err
	}
	path := filepath.Join(c.eventDir, "checkpoints", c.sessionID, record.ID+".json")
	data, err := json.Marshal(record)
	if err != nil {
		return checkpointRecord{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-*.tmp")
	if err != nil {
		return checkpointRecord{}, err
	}
	tmpName := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(tmpName) }()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return checkpointRecord{}, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return checkpointRecord{}, err
	}
	return record, nil
}

func (s *Server) checkpointPath(id, checkpointID string) string {
	if !validID(id) || !strings.HasPrefix(checkpointID, "cp-") || strings.ContainsAny(checkpointID, `/\\`) {
		return ""
	}
	return filepath.Join(s.eventDir, "checkpoints", id, checkpointID+".json")
}

func (s *Server) loadCheckpoints(id string) ([]checkpointRecord, error) {
	dir := filepath.Join(s.eventDir, "checkpoints", id)
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []checkpointRecord
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil {
			return nil, err
		}
		var record checkpointRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		if record.SessionID != id || record.TurnSeq < 0 {
			return nil, errors.New("invalid checkpoint manifest")
		}
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TurnSeq < out[j].TurnSeq })
	return out, nil
}

func (s *Server) loadCheckpoint(id string, seq int) (checkpointRecord, error) {
	records, err := s.loadCheckpoints(id)
	if err != nil {
		return checkpointRecord{}, err
	}
	for _, record := range records {
		if record.TurnSeq == seq {
			return record, nil
		}
	}
	return checkpointRecord{}, fmt.Errorf("checkpoint turn %d not found", seq)
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func authorizedFile(path string, roots []string) bool {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false
	}
	for _, raw := range roots {
		root, err := filepath.Abs(filepath.Clean(strings.TrimSpace(raw)))
		if err != nil || root == "" {
			continue
		}
		rel, err := filepath.Rel(root, abs)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			if safePath(root, rel) {
				return true
			}
		}
	}
	return false
}

func safePath(root, rel string) bool {
	if info, err := os.Lstat(root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	current := root
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	for i, part := range parts {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return i == len(parts)-1
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

type checkpointDiffEntry struct {
	Path        string `json:"path"`
	Key         string `json:"key"`
	Action      string `json:"action"`
	CurrentHash string `json:"current_hash,omitempty"`
}
type checkpointDiff struct {
	TurnSeq           int                   `json:"turn_seq"`
	RestoreFiles      int                   `json:"restore_files"`
	DeleteFiles       int                   `json:"delete_files"`
	CleanFiles        int                   `json:"clean_files"`
	SkippedDirs       int                   `json:"skipped_dirs"`
	MissingBlobs      int                   `json:"missing_blobs"`
	UnresolvableFiles int                   `json:"unresolvable_files"`
	CaptureErrors     int                   `json:"capture_errors"`
	Entries           []checkpointDiffEntry `json:"entries"`
}
type checkpointExpected struct {
	Key         string `json:"key"`
	CurrentHash string `json:"current_hash"`
}
type checkpointRootsRequest struct {
	AuthorizedRoots []string `json:"authorized_roots"`
}
type checkpointRewindRequest struct {
	AuthorizedRoots []string             `json:"authorized_roots"`
	Expected        []checkpointExpected `json:"expected"`
}
type checkpointRewindResult struct {
	TurnSeq       int      `json:"turn_seq"`
	RestoredFiles int      `json:"restored_files"`
	DeletedFiles  int      `json:"deleted_files"`
	CleanFiles    int      `json:"clean_files"`
	SkippedDirs   int      `json:"skipped_dirs"`
	CaptureErrors int      `json:"capture_errors"`
	Conflicts     []string `json:"conflicts"`
	Failed        []string `json:"failed"`
	Revision      string   `json:"revision,omitempty"`
}

func (s *Server) checkpointDiff(record checkpointRecord, roots []string) checkpointDiff {
	out := checkpointDiff{TurnSeq: record.TurnSeq, Entries: []checkpointDiffEntry{}}
	for _, entry := range record.Entries {
		if entry.Error != "" {
			continue
		}
		if !authorizedFile(entry.Path, roots) {
			out.UnresolvableFiles++
			continue
		}
		hash, err := hashFile(entry.Path)
		if err != nil {
			out.UnresolvableFiles++
			continue
		}
		action := "clean"
		if entry.Missing && hash != "missing" {
			action = "delete"
			out.DeleteFiles++
		} else if !entry.Missing {
			want := sha256.Sum256(entry.Data)
			expected := hex.EncodeToString(want[:])
			if hash != expected {
				action = "restore"
				out.RestoreFiles++
			} else {
				out.CleanFiles++
			}
		}
		out.Entries = append(out.Entries, checkpointDiffEntry{Path: entry.Path, Key: entry.Path, Action: action, CurrentHash: hash})
	}
	out.CaptureErrors += recordCaptureErrors(record)
	return out
}
func recordCaptureErrors(record checkpointRecord) int {
	n := 0
	for _, e := range record.Entries {
		if e.Error != "" {
			n++
		}
	}
	return n
}

func (s *Server) checkpointList(w http.ResponseWriter, r *http.Request, id string) {
	records, err := s.loadCheckpoints(id)
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		files, dirs := 0, 0
		for _, entry := range record.Entries {
			if entry.Error == "" {
				files++
			} else {
				dirs++
			}
		}
		out = append(out, map[string]any{"turn_seq": record.TurnSeq, "turn_id": record.TurnID, "file_count": files, "dir_count": dirs, "incomplete": record.Incomplete, "first_captured_at": record.FirstCapturedAt.UnixMilli()})
	}
	writeJSON(w, 200, out)
}

func (s *Server) checkpointPreview(w http.ResponseWriter, r *http.Request, id string, seq int) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	defer s.releaseRuntime(rt)
	rt.mu.Lock()
	active := rt.activeLocked()
	rt.mu.Unlock()
	if active {
		// Preview only reads; rewind re-hashes every file and refuses on a change.
		writeJSONError(w, http.StatusConflict, "session has an active turn")
		return
	}
	var in checkpointRootsRequest
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	record, err := s.loadCheckpoint(id, seq)
	if err != nil {
		writeJSONError(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, s.checkpointDiff(record, in.AuthorizedRoots))
}

func (s *Server) checkpointRewind(w http.ResponseWriter, r *http.Request, id string, seq int) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, 404, err.Error())
		return
	}
	defer s.releaseRuntime(rt)
	var in checkpointRewindRequest
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !s.mutationAllowed(w, rt) {
		return
	}
	// A background subagent may still be writing the files the rewind restores.
	if !rt.backgroundSettledLocked() {
		writeJSONError(w, http.StatusConflict, errBackgroundRunning.Error())
		return
	}
	record, err := s.loadCheckpoint(id, seq)
	if err != nil {
		writeJSONError(w, 404, err.Error())
		return
	}
	diff := s.checkpointDiff(record, in.AuthorizedRoots)
	expected := map[string]string{}
	for _, item := range in.Expected {
		expected[item.Key] = item.CurrentHash
	}
	result := checkpointRewindResult{TurnSeq: seq, CaptureErrors: diff.CaptureErrors, SkippedDirs: diff.SkippedDirs, Conflicts: []string{}, Failed: []string{}}
	for _, item := range diff.Entries {
		want, ok := expected[item.Key]
		if !ok || want != item.CurrentHash {
			result.Conflicts = append(result.Conflicts, item.Path)
		}
	}
	if len(result.Conflicts) > 0 {
		writeJSON(w, 409, result)
		return
	}
	backups := map[string][]byte{}
	missing := map[string]bool{}
	for _, item := range diff.Entries {
		if item.Action != "restore" && item.Action != "delete" {
			result.CleanFiles++
			continue
		}
		data, readErr := os.ReadFile(item.Path)
		if errors.Is(readErr, os.ErrNotExist) {
			missing[item.Path] = true
		} else if readErr != nil {
			result.Failed = append(result.Failed, item.Path)
			continue
		} else {
			backups[item.Path] = data
		}
	}
	if len(result.Failed) > 0 {
		writeJSON(w, 500, result)
		return
	}
	rollback := func() {
		for path, data := range backups {
			_ = os.WriteFile(path, data, 0o600)
		}
		for path := range missing {
			_ = os.Remove(path)
		}
	}
	for _, entry := range record.Entries {
		if !authorizedFile(entry.Path, in.AuthorizedRoots) || entry.Error != "" {
			continue
		}
		itemAction := "clean"
		current, _ := hashFile(entry.Path)
		if entry.Missing && current != "missing" {
			itemAction = "delete"
		} else if !entry.Missing {
			sum := sha256.Sum256(entry.Data)
			if current != hex.EncodeToString(sum[:]) {
				itemAction = "restore"
			}
		}
		if itemAction == "delete" {
			if err := os.Remove(entry.Path); err != nil {
				rollback()
				result.Failed = append(result.Failed, entry.Path)
				break
			}
			result.DeletedFiles++
		}
		if itemAction == "restore" {
			if err := os.WriteFile(entry.Path, entry.Data, os.FileMode(entry.Mode)); err != nil {
				rollback()
				result.Failed = append(result.Failed, entry.Path)
				break
			}
			result.RestoredFiles++
		}
	}
	if len(result.Failed) > 0 {
		writeJSON(w, 500, result)
		return
	}
	if _, err := s.store.MessageSequence(id, record.TurnID); err != nil {
		rollback()
		writeJSONError(w, 500, err.Error())
		return
	}
	if err := s.store.ClearSnapshotsFrom(id, seq); err != nil {
		rollback()
		writeJSONError(w, 500, err.Error())
		return
	}
	if snap, snapErr := s.store.HistorySnapshot(id); snapErr == nil {
		result.Revision = snap.Revision
	}
	go func(revision string) {
		_, _ = s.publish(rt, protocol.EventHistoryUpdated, map[string]any{"revision": revision, "turn_seq": seq}, "")
	}(result.Revision)
	writeJSON(w, 200, result)
}
