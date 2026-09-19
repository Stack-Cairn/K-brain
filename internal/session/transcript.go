package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

const transcriptVersion = 1

type record struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type metadataRecord struct {
	Version int `json:"version"`
	Meta
	CreatedAt time.Time `json:"created_at"`
	Todos     string    `json:"todos,omitempty"`
}

type messageRecord struct {
	Seq     int        `json:"seq"`
	Message ai.Message `json:"message"`
}

type snapshotRecord struct {
	Seq int    `json:"seq"`
	Ref string `json:"ref"`
}

func (s *Store) read(id string) (*sessionData, error) {
	path := s.TranscriptPath(id)
	if path == "" {
		return nil, fmt.Errorf("invalid session id %q", id)
	}
	info, err := os.Lstat(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("invalid session directory %s", id)
	}
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("invalid session file %s", id)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	var d *sessionData
	for line := 1; ; line++ {
		data, readErr := reader.ReadBytes('\n')
		if len(data) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		fail := func(err error) (*sessionData, error) { return nil, fmt.Errorf("session %s line %d: %w", id, line, err) }
		var r record
		if err := json.Unmarshal(data, &r); err != nil {
			return fail(err)
		}
		if len(r.Payload) == 0 || bytes.Equal(bytes.TrimSpace(r.Payload), []byte("null")) {
			return fail(errors.New("missing record payload"))
		}
		if d == nil && r.Type != "session_meta" {
			return fail(errors.New("metadata must be first"))
		}
		switch r.Type {
		case "session_meta":
			if d != nil {
				return fail(errors.New("duplicate metadata"))
			}
			var m metadataRecord
			if err := json.Unmarshal(r.Payload, &m); err != nil {
				return fail(err)
			}
			if m.Version != transcriptVersion || m.ID != id || m.CreatedAt.IsZero() || m.UpdatedAt.IsZero() {
				return fail(errors.New("invalid metadata or unsupported version"))
			}
			d = newData(m.Meta)
			d.CreatedAt, d.Todos = m.CreatedAt, m.Todos
		case "message":
			var m messageRecord
			if err := json.Unmarshal(r.Payload, &m); err != nil {
				return fail(err)
			}
			if _, exists := d.Messages[m.Seq]; m.Seq < 0 || m.Message.Role == "" || exists {
				return fail(errors.New("invalid or duplicate message"))
			}
			d.Messages[m.Seq] = m.Message
		case "task":
			var t Task
			if err := json.Unmarshal(r.Payload, &t); err != nil {
				return fail(err)
			}
			if _, exists := d.Tasks[t.ID]; exists {
				return fail(errors.New("duplicate task"))
			}
			d.Tasks[t.ID] = t
		case "snapshot":
			var snap snapshotRecord
			if err := json.Unmarshal(r.Payload, &snap); err != nil {
				return fail(err)
			}
			if _, exists := d.Snapshots[snap.Seq]; snap.Seq < 0 || snap.Ref == "" || exists {
				return fail(errors.New("invalid or duplicate snapshot"))
			}
			d.Snapshots[snap.Seq] = snap.Ref
		case "schedule":
			var sc Schedule
			if err := json.Unmarshal(r.Payload, &sc); err != nil {
				return fail(err)
			}
			if _, exists := d.Schedules[sc.ID]; sc.ID <= 0 || exists {
				return fail(errors.New("invalid or duplicate schedule"))
			}
			d.Schedules[sc.ID] = sc
		case "compaction":
			var c Compaction
			if err := json.Unmarshal(r.Payload, &c); err != nil {
				return fail(err)
			}
			if _, exists := d.Compactions[c.Seq]; c.Seq <= 0 || exists {
				return fail(errors.New("invalid or duplicate compaction"))
			}
			d.Compactions[c.Seq] = c
		default:
			return fail(fmt.Errorf("unknown record type %q", r.Type))
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if d == nil {
		return nil, fmt.Errorf("session %s: missing metadata", id)
	}
	return d, nil
}

func (s *Store) write(d *sessionData) error {
	path := s.TranscriptPath(d.Meta.ID)
	if s.projectScoped {
		projectPath := filepath.Join(s.filesDir, projectDir(d.Meta.CWD), d.Meta.ID, "session.jsonl")
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			path = projectPath
		}
	}
	if path == "" {
		return fmt.Errorf("invalid session id %q", d.Meta.ID)
	}
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid session directory %s", dir)
	}
	f, err := os.CreateTemp(dir, ".session-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	writer := bufio.NewWriter(f)
	enc := json.NewEncoder(writer)
	enc.SetEscapeHTML(false)
	emit := func(kind string, value any) error {
		return enc.Encode(struct {
			Type    string `json:"type"`
			Payload any    `json:"payload"`
		}{kind, value})
	}
	if err := emit("session_meta", metadataRecord{Version: transcriptVersion, Meta: d.Meta, CreatedAt: d.CreatedAt, Todos: d.Todos}); err != nil {
		return err
	}
	for _, seq := range sortedKeys(d.Messages) {
		if err := emit("message", messageRecord{seq, d.Messages[seq]}); err != nil {
			return err
		}
	}
	for _, seq := range sortedKeys(d.Compactions) {
		if err := emit("compaction", d.Compactions[seq]); err != nil {
			return err
		}
	}
	taskIDs := make([]string, 0, len(d.Tasks))
	for id := range d.Tasks {
		taskIDs = append(taskIDs, id)
	}
	sort.Strings(taskIDs)
	for _, id := range taskIDs {
		if err := emit("task", d.Tasks[id]); err != nil {
			return err
		}
	}
	for _, seq := range sortedKeys(d.Snapshots) {
		if err := emit("snapshot", snapshotRecord{seq, d.Snapshots[seq]}); err != nil {
			return err
		}
	}
	for _, id := range sortedKeys(d.Schedules) {
		if err := emit("schedule", d.Schedules[id]); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
