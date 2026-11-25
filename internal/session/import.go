package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

var ErrHistoryDeleted = errors.New("history was explicitly deleted")

// ErrImportConflict matches every *ImportConflictError.
var ErrImportConflict = errors.New("import conflicts with existing session")

// ErrImportBusy refuses a prefix update while this process runs a turn in the
// session; the turn's save would otherwise be rejected as stale. Another
// process's run is reported as a *RunElsewhereError instead. Both are
// transient: the same import can succeed later.
var ErrImportBusy = errors.New("session has a running turn")

// Reasons an import is refused with an *ImportConflictError.
const (
	// ImportConflictDifferentSource: the session exists but was not imported
	// from this source (another source id, or a session created in K-brain).
	ImportConflictDifferentSource = "different_source"
	// ImportConflictModifiedAfterImport: messages or compactions changed in
	// K-brain after the import (a turn, rewind, edit, compaction, ...).
	ImportConflictModifiedAfterImport = "modified_after_import"
	// ImportConflictDiverged: the stored messages are not a prefix of the
	// incoming ones (a message differs, or the incoming history is shorter).
	ImportConflictDiverged = "diverged"
	// ImportConflictNoNewMessages: the incoming messages equal the stored ones
	// but the source fingerprint or other imported data changed.
	ImportConflictNoNewMessages = "no_new_messages"
	// ImportConflictFingerprintUnchanged: the incoming history extends the
	// stored one under the same source fingerprint. A source that gained
	// messages must report a new fingerprint, so this is refused as tampering.
	ImportConflictFingerprintUnchanged = "fingerprint_unchanged"
)

// ImportConflictError explains why an import cannot be applied to the existing
// session. Its message keeps the historical text that clients may log.
type ImportConflictError struct {
	SourceID string
	Reason   string
}

func (e *ImportConflictError) Error() string {
	return fmt.Sprintf("import source %q conflicts with existing session", e.SourceID)
}

func (e *ImportConflictError) Is(target error) bool { return target == ErrImportConflict }

// ImportOutcome is what ImportOrUpdateHistory did.
type ImportOutcome int

const (
	// ImportCreated wrote a new session.
	ImportCreated ImportOutcome = iota
	// ImportAlready found the same import (same source id and fingerprints).
	ImportAlready
	// ImportUpdated replaced an unmodified earlier import of the same source
	// whose messages are a strict prefix of the incoming ones.
	ImportUpdated
)

// ImportResult reports an import. PreviousMessages is the number of messages
// the session held before an ImportUpdated (0 otherwise).
type ImportResult struct {
	Outcome          ImportOutcome
	PreviousMessages int
}

func filepathForImport(s *Store, id, cwd string) string {
	if !s.projectScoped {
		return filepath.Join(s.filesDir, id)
	}
	return filepath.Join(s.filesDir, projectDir(cwd), id)
}

func makeImportDir(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

// ImportHistory writes a canonical history under a caller-supplied stable ID
// and reports whether the same import already existed. See
// ImportOrUpdateHistory for how an existing session is treated.
func (s *Store) ImportHistory(id, sourceID, fingerprint, contentFingerprint, metadata, cwd, model, provider, title string, createdAt, updatedAt time.Time, pinned, shared, redact bool, shareToken string, messages []ai.Message, boundary *Compaction) (bool, error) {
	result, err := s.ImportOrUpdateHistory(id, sourceID, fingerprint, contentFingerprint, metadata, cwd, model, provider, title, createdAt, updatedAt, pinned, shared, redact, shareToken, messages, boundary)
	return result.Outcome == ImportAlready, err
}

// ImportOrUpdateHistory writes a canonical history under a caller-supplied
// stable ID. For an existing session:
//   - same source id and both fingerprints: ImportAlready, nothing written;
//   - same source id, a new source fingerprint, the session unchanged in
//     K-brain since its import and its messages a strict prefix of the
//     incoming ones: the messages, active-context boundary and import identity
//     are replaced in place (ImportUpdated), keeping the session id and local
//     state (title, pin, archive, share, tags, goal, model, cwd, usage,
//     snapshots);
//   - anything else: an *ImportConflictError naming the reason.
//
// A deleted history keeps returning ErrHistoryDeleted.
func (s *Store) ImportOrUpdateHistory(id, sourceID, fingerprint, contentFingerprint, metadata, cwd, model, provider, title string, createdAt, updatedAt time.Time, pinned, shared, redact bool, shareToken string, messages []ai.Message, boundary *Compaction) (ImportResult, error) {
	id = strings.TrimSpace(id)
	sourceID = strings.TrimSpace(sourceID)
	fingerprint = strings.TrimSpace(fingerprint)
	contentFingerprint = strings.TrimSpace(contentFingerprint)
	if !validID(id) || sourceID == "" || fingerprint == "" || contentFingerprint == "" {
		return ImportResult{}, errors.New("invalid import identity")
	}
	var result ImportResult
	err := s.withLock(func() error {
		incoming, err := normalizeImportedMessages(messages)
		if err != nil {
			return err
		}
		existing, readErr := s.read(id)
		if readErr == nil {
			if existing.Meta.ImportSourceID == sourceID && existing.Meta.ImportFingerprint == fingerprint && existing.Meta.ImportContentFingerprint == contentFingerprint {
				result.Outcome = ImportAlready
				return nil
			}
			previous := len(existing.Messages)
			if reason := importUpdateConflict(existing, sourceID, fingerprint, incoming); reason != "" {
				return &ImportConflictError{SourceID: sourceID, Reason: reason}
			}
			if s.runLeases[id] != nil {
				return ErrImportBusy
			}
			if err := s.guardExternalRunLocked(id); err != nil {
				return err
			}
			applyImportUpdate(existing, fingerprint, contentFingerprint, metadata, title, updatedAt, incoming, boundary)
			if err := s.write(existing); err != nil {
				return err
			}
			result = ImportResult{Outcome: ImportUpdated, PreviousMessages: previous}
			return nil
		}
		if !errors.Is(readErr, ErrNotFound) {
			return readErr
		}
		if _, err := os.Lstat(filepath.Join(s.filesDir, ".deleted-history", id)); err == nil {
			return ErrHistoryDeleted
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		if updatedAt.IsZero() {
			updatedAt = createdAt
		}
		data := newData(Meta{
			ID: id, Title: title, CWD: cwd, Model: model, Provider: provider,
			CreatedAt: createdAt, UpdatedAt: updatedAt, Pinned: pinned, Shared: shared,
			ShareToken: shareToken, ShareRedactTool: redact,
			ImportSourceID: sourceID, ImportFingerprint: fingerprint, ImportContentFingerprint: contentFingerprint, ImportMetadata: metadata,
		})
		data.CreatedAt = createdAt
		setImportedHistory(data, incoming, boundary)
		dir := filepathForImport(s, id, cwd)
		if err := makeImportDir(dir); err != nil {
			return err
		}
		if err := s.write(data); err != nil {
			return err
		}
		result.Outcome = ImportCreated
		return nil
	})
	return result, err
}

// normalizeImportedMessages gives incoming messages the exact form a reread
// transcript has, so the revision recorded at import time is the one a later
// read computes.
func normalizeImportedMessages(messages []ai.Message) ([]ai.Message, error) {
	out := make([]ai.Message, len(messages))
	for i, message := range messages {
		raw, err := json.Marshal(message)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// setImportedHistory stores messages at seq 0..n-1 and the optional active
// context boundary as compaction 1, then records the resulting revision as
// Meta.ImportRevision: the "unchanged since import" signal.
func setImportedHistory(d *sessionData, messages []ai.Message, boundary *Compaction) {
	clear(d.Messages)
	clear(d.Compactions)
	for index, message := range messages {
		d.Messages[index] = message
	}
	if boundary != nil {
		d.Compactions[1] = *boundary
	}
	d.Meta.ImportRevision = d.revision()
}

func applyImportUpdate(d *sessionData, fingerprint, contentFingerprint, metadata, title string, updatedAt time.Time, messages []ai.Message, boundary *Compaction) {
	setImportedHistory(d, messages, boundary)
	d.Meta.ImportFingerprint = fingerprint
	d.Meta.ImportContentFingerprint = contentFingerprint
	d.Meta.ImportMetadata = metadata
	// The title is local state once set; only an untitled session takes the source's.
	if strings.TrimSpace(d.Meta.Title) == "" {
		d.Meta.Title = title
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	if updatedAt.After(d.Meta.UpdatedAt) {
		d.Meta.UpdatedAt = updatedAt
	}
}

// importUpdateConflict returns "" when existing may be replaced by the
// incoming import of the same source, or the conflict reason.
func importUpdateConflict(existing *sessionData, sourceID, fingerprint string, incoming []ai.Message) string {
	if existing.Meta.ImportSourceID == "" || existing.Meta.ImportSourceID != sourceID {
		return ImportConflictDifferentSource
	}
	if modifiedSinceImport(existing) {
		return ImportConflictModifiedAfterImport
	}
	stored := existing.rawMessages()
	if len(stored) > len(incoming) {
		return ImportConflictDiverged
	}
	for i := range stored {
		if !sameImportedMessage(stored[i], incoming[i]) {
			return ImportConflictDiverged
		}
	}
	if len(stored) == len(incoming) {
		return ImportConflictNoNewMessages
	}
	if existing.Meta.ImportFingerprint == fingerprint {
		return ImportConflictFingerprintUnchanged
	}
	return ""
}

// modifiedSinceImport reports whether messages or compactions changed after
// the import. Imports record the revision (hash of messages and compactions)
// they wrote as Meta.ImportRevision; every history change in K-brain (turns,
// rewinds, edits, truncation, compaction, undoing a compaction) changes it,
// while metadata (title, pin, archive, share, tags, goal, usage) and file
// checkpoints do not take part.
//
// Imports written before ImportRevision existed fall back to the shape an
// import leaves: messages at seq 0..n-1 with none K-brain authored or rewound,
// no subagent tasks, and no compaction other than the imported boundary. That
// fallback cannot see a bare truncation of the newest messages; the strict
// prefix check then treats the copy like an interrupted import.
func modifiedSinceImport(d *sessionData) bool {
	if d.Meta.ImportRevision != "" {
		return d.revision() != d.Meta.ImportRevision
	}
	if len(d.Tasks) > 0 {
		return true
	}
	for seq, c := range d.Compactions {
		if seq != 1 || !c.DropPrior || c.RunID != "" || c.Model != "" || c.Usage != (ai.Usage{}) {
			return true
		}
	}
	for index, seq := range sortedKeys(d.Messages) {
		msg := d.Messages[seq]
		if seq != index || msg.Authored || msg.RewoundFrom != "" {
			return true
		}
	}
	return false
}

// sameImportedMessage compares the identity and content of two messages: id,
// role, text, attachments, reasoning, tool calls and results, and hosted
// search. Messages without an id compare by position and content, which is
// also what their derived canonical id is made of. Display metadata (usage,
// model, timestamps, stop reason) is not compared: the update replaces it
// with the incoming copy anyway.
func sameImportedMessage(a, b ai.Message) bool {
	return bytes.Equal(importedMessageKey(a), importedMessageKey(b))
}

func importedMessageKey(m ai.Message) []byte {
	raw, _ := json.Marshal(struct {
		ID           string            `json:"id"`
		Role         string            `json:"role"`
		Content      string            `json:"content"`
		Parts        []ai.ContentPart  `json:"parts"`
		Reasoning    string            `json:"reasoning"`
		ToolCalls    []ai.ToolCall     `json:"tool_calls"`
		ToolCallID   string            `json:"tool_call_id"`
		Name         string            `json:"name"`
		HostedSearch []ai.HostedSearch `json:"hosted_search"`
	}{m.ID, m.Role, m.Content, m.Parts, m.Reasoning, m.ToolCalls, m.ToolCallID, m.Name, m.HostedSearch})
	return raw
}
