package backend

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type legacyCheckpointImport struct {
	Status       string                         `json:"status"`
	NativePath   string                         `json:"nativePath,omitempty"`
	IndexPath    string                         `json:"indexPath,omitempty"`
	IndexJSONL   string                         `json:"indexJsonl,omitempty"`
	Reason       string                         `json:"reason,omitempty"`
	Records      []legacyCheckpointImportRecord `json:"records,omitempty"`
	InvalidLines []string                       `json:"invalidLines,omitempty"`
}

type legacyCheckpointImportRecord struct {
	Schema        uint32  `json:"schema"`
	TurnSeq       uint64  `json:"turnSeq"`
	TurnID        string  `json:"turnId"`
	Root          string  `json:"root"`
	RelPath       string  `json:"relPath"`
	Kind          string  `json:"kind"`
	ExistedBefore bool    `json:"existedBefore"`
	Blob          *string `json:"blob,omitempty"`
	BlobBase64    *string `json:"blobBase64,omitempty"`
	Size          uint64  `json:"size"`
	MtimeMs       uint64  `json:"mtimeMs"`
	CapturedAt    uint64  `json:"capturedAt"`
	Note          *string `json:"note,omitempty"`
	Mode          *uint32 `json:"mode,omitempty"`
}

type legacyCheckpointMetadata struct {
	SourceID          string                         `json:"source_id"`
	SourceFingerprint string                         `json:"source_fingerprint"`
	ConversationID    string                         `json:"conversation_id"`
	Status            string                         `json:"status"`
	NativePath        string                         `json:"native_path,omitempty"`
	IndexPath         string                         `json:"index_path,omitempty"`
	IndexJSONL        string                         `json:"index_jsonl,omitempty"`
	Reason            string                         `json:"reason,omitempty"`
	Records           []legacyCheckpointImportRecord `json:"records,omitempty"`
	InvalidLines      []string                       `json:"invalid_lines,omitempty"`
}

type checkpointImportResult struct {
	Status string
	Error  string
}

type checkpointImportValidationError struct{ message string }

func (e *checkpointImportValidationError) Error() string { return e.message }

func validateLegacyCheckpointRecord(record legacyCheckpointImportRecord) error {
	if record.Schema != 2 {
		return &checkpointImportValidationError{fmt.Sprintf("unsupported legacy checkpoint schema %d", record.Schema)}
	}
	if record.Kind != "turn" && record.Kind != "file" && record.Kind != "dir" && record.Kind != "error" && record.Kind != "rewind" {
		return &checkpointImportValidationError{fmt.Sprintf("unsupported legacy checkpoint kind %q", record.Kind)}
	}
	if record.TurnSeq > 9007199254740991 || record.CapturedAt > 9007199254740991 || record.MtimeMs > 9007199254740991 || record.Size > 32<<20 {
		return &checkpointImportValidationError{"legacy checkpoint numeric value exceeds its limit"}
	}
	if record.Kind != "rewind" && (record.TurnSeq == 0 || strings.TrimSpace(record.TurnID) == "") {
		return &checkpointImportValidationError{"legacy checkpoint turn identity is missing"}
	}
	if strings.ContainsRune(record.Root+record.RelPath, 0) || (os.PathSeparator != '\\' && strings.ContainsRune(record.Root+record.RelPath, '\\')) {
		return &checkpointImportValidationError{"legacy checkpoint path contains invalid characters"}
	}
	if (record.Kind == "file" || record.Kind == "dir") && (record.Root == "" || record.RelPath == "") {
		return &checkpointImportValidationError{"legacy checkpoint file path is missing"}
	}
	if record.BlobBase64 != nil && (record.Kind != "file" || !record.ExistedBefore || record.Blob == nil) {
		return &checkpointImportValidationError{"legacy checkpoint blob has no file preimage"}
	}
	if record.Root != "" && !filepath.IsAbs(filepath.FromSlash(record.Root)) {
		return &checkpointImportValidationError{"legacy checkpoint root must be absolute"}
	}
	if record.RelPath != "" {
		rel := filepath.FromSlash(record.RelPath)
		clean := filepath.Clean(rel)
		if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return &checkpointImportValidationError{"legacy checkpoint relative path escapes its root"}
		}
		if clean == "" || strings.ContainsRune(clean, 0) {
			return &checkpointImportValidationError{"legacy checkpoint relative path is empty"}
		}
	}
	if record.Blob != nil {
		blob := *record.Blob
		if blob == "" || blob == "." || blob == ".." || strings.ContainsRune(blob, 0) || strings.ContainsAny(blob, "/\\") {
			return &checkpointImportValidationError{"legacy checkpoint blob name is unsafe"}
		}
		if filepath.Base(filepath.FromSlash(blob)) != blob || strings.TrimSpace(blob) != blob {
			return &checkpointImportValidationError{"legacy checkpoint blob name is unsafe"}
		}
	}
	return nil
}
func importedCheckpointID(conversationID, sourceFingerprint string, turnSeq uint64) string {
	sum := sha256.Sum256([]byte(conversationID + "\x00" + sourceFingerprint + "\x00" + fmt.Sprint(turnSeq)))
	return "cp-import-" + hex.EncodeToString(sum[:12])
}

func importedCheckpointPath(eventDir, conversationID, sourceFingerprint string, turnSeq uint64) string {
	return filepath.Join(eventDir, "checkpoints", conversationID, importedCheckpointID(conversationID, sourceFingerprint, turnSeq)+".json")
}

func importedCheckpointMetadataPath(eventDir, conversationID, sourceFingerprint string) string {
	sum := sha256.Sum256([]byte(conversationID + "\x00" + sourceFingerprint))
	return filepath.Join(eventDir, "checkpoints", conversationID, "legacy-import", hex.EncodeToString(sum[:16])+".json")
}

// errImportFileConflict: an immutable import file already holds other content.
var errImportFileConflict = errors.New("legacy checkpoint import conflicts with existing data")

func writeImportFile(path string, data []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return errImportFileConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".legacy-checkpoint-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpName, path); errors.Is(err, os.ErrExist) {
		existing, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Equal(existing, data) {
			return nil
		}
		return errImportFileConflict
	} else {
		return err
	}
}

func unixMillis(ms uint64) time.Time {
	if ms > math.MaxInt64 {
		return time.Unix(0, 0).UTC()
	}
	return time.UnixMilli(int64(ms)).UTC()
}

func importedCheckpointRecords(in legacyCheckpointImport, conversationID, sourceFingerprint string) (map[uint64]checkpointRecord, bool, error) {
	partial := in.Status == "partial" || len(in.InvalidLines) > 0
	live := []legacyCheckpointImportRecord{}
	for _, record := range in.Records {
		if err := validateLegacyCheckpointRecord(record); err != nil {
			return nil, false, err
		}
		if record.Kind == "rewind" {
			if record.TurnSeq > 0 {
				kept := live[:0]
				for _, prior := range live {
					if prior.TurnSeq < record.TurnSeq {
						kept = append(kept, prior)
					}
				}
				live = kept
			}
			continue
		}
		live = append(live, record)
	}
	out := map[uint64]checkpointRecord{}
	turns := map[string]uint64{}
	for _, source := range live {
		if prior, exists := turns[source.TurnID]; exists && prior != source.TurnSeq {
			return nil, false, &checkpointImportValidationError{"legacy checkpoint turn id has multiple sequences"}
		}
		turns[source.TurnID] = source.TurnSeq
		record, exists := out[source.TurnSeq]
		if !exists {
			record = checkpointRecord{ID: importedCheckpointID(conversationID, sourceFingerprint, source.TurnSeq), SessionID: conversationID, TurnID: source.TurnID, FirstCapturedAt: unixMillis(source.CapturedAt), Entries: []checkpointEntry{}, Incomplete: partial}
		} else if record.TurnID != source.TurnID {
			return nil, false, &checkpointImportValidationError{"legacy checkpoint turn identity is inconsistent"}
		}
		if unixMillis(source.CapturedAt).Before(record.FirstCapturedAt) {
			record.FirstCapturedAt = unixMillis(source.CapturedAt)
		}
		out[source.TurnSeq] = record
	}
	// Native rewind selects the earliest preimage for each path at or after the target turn.
	for seq, record := range out {
		seen := map[string]bool{}
		for _, source := range live {
			if source.TurnSeq < seq || source.Kind == "turn" {
				continue
			}
			path := filepath.Join(filepath.FromSlash(source.Root), filepath.FromSlash(source.RelPath))
			if source.Kind != "error" {
				if seen[path] {
					continue
				}
				seen[path] = true
			}
			entry := checkpointEntry{Path: path, Mode: dereferenceMode(source.Mode)}
			switch source.Kind {
			case "file":
				entry.Missing = !source.ExistedBefore
				if source.ExistedBefore {
					if source.BlobBase64 == nil {
						entry.Error = "legacy checkpoint blob is missing"
					} else {
						data, err := base64.StdEncoding.Strict().DecodeString(*source.BlobBase64)
						if err != nil {
							return nil, false, &checkpointImportValidationError{"legacy checkpoint blob is not valid base64"}
						}
						if uint64(len(data)) != source.Size {
							return nil, false, &checkpointImportValidationError{"legacy checkpoint blob size does not match its ledger"}
						}
						entry.Data = data
					}
				}
			case "dir":
				entry.Error = "legacy checkpoint directory marker is not restorable"
			case "error":
				entry.Error = dereferenceString(source.Note, "legacy checkpoint capture error")
			}
			if entry.Error != "" {
				record.Incomplete, partial = true, true
			}
			record.Entries = append(record.Entries, entry)
		}
		sort.SliceStable(record.Entries, func(i, j int) bool { return record.Entries[i].Path < record.Entries[j].Path })
		out[seq] = record
	}
	return out, partial, nil
}

func dereferenceMode(mode *uint32) uint32 {
	if mode == nil {
		return 0o600
	}
	return *mode & 0o777
}

func dereferenceString(value *string, fallback string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	return *value
}

func prepareLegacyCheckpoints(in legacyCheckpointImport, conversationID, sourceFingerprint string) (map[uint64]checkpointRecord, bool, error) {
	if !validID(conversationID) {
		return nil, false, &checkpointImportValidationError{"invalid checkpoint conversation id"}
	}
	switch in.Status {
	case "unresolved", "not_found":
		if len(in.Records) > 0 || len(in.InvalidLines) > 0 || in.IndexJSONL != "" {
			return nil, false, &checkpointImportValidationError{"checkpoint status contradicts its artifacts"}
		}
		return nil, false, nil
	case "available", "partial":
		return importedCheckpointRecords(in, conversationID, sourceFingerprint)
	default:
		return nil, false, &checkpointImportValidationError{"invalid legacy checkpoint status"}
	}
}

// importLegacyCheckpoints attaches imported checkpoints to their user turns.
// Turns at a message sequence below keepBefore are skipped: after an in-place
// prefix update they were imported earlier and keep their snapshots as they
// are now.
func (s *Server) importLegacyCheckpoints(in legacyCheckpointImport, conversationID, sourceID, sourceFingerprint string, records map[uint64]checkpointRecord, partial bool, keepBefore int) (checkpointImportResult, error) {
	if in.Status == "unresolved" || in.Status == "not_found" {
		return checkpointImportResult{Status: in.Status, Error: in.Reason}, nil
	}
	metadata := legacyCheckpointMetadata{SourceID: sourceID, SourceFingerprint: sourceFingerprint, ConversationID: conversationID, Status: in.Status, NativePath: in.NativePath, IndexPath: in.IndexPath, IndexJSONL: in.IndexJSONL, Reason: in.Reason, Records: in.Records, InvalidLines: in.InvalidLines}
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return checkpointImportResult{}, err
	}
	marker := importedCheckpointMetadataPath(s.eventDir, conversationID, sourceFingerprint)
	if previous, err := os.ReadFile(marker); err == nil {
		if !bytes.Equal(previous, metadataBytes) {
			return checkpointImportResult{}, errImportFileConflict
		}
		// A completed import must not resurrect snapshots consumed by a later rewind.
		for _, record := range records {
			if _, err := s.store.MessageSequence(conversationID, record.TurnID); err != nil {
				partial = true
			}
		}
		return checkpointImportSummary(partial), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return checkpointImportResult{}, err
	}
	for seq, record := range records {
		messageSeq, err := s.store.MessageSequence(conversationID, record.TurnID)
		if err != nil {
			partial = true
			continue
		}
		if messageSeq < keepBefore {
			continue
		}
		record.TurnSeq = messageSeq
		path := importedCheckpointPath(s.eventDir, conversationID, sourceFingerprint, seq)
		data, err := json.Marshal(record)
		if err != nil {
			return checkpointImportResult{}, err
		}
		if err := writeImportFile(path, data); err != nil {
			return checkpointImportResult{}, err
		}
		if err := s.store.SetSnapshot(conversationID, messageSeq, record.ID); err != nil {
			return checkpointImportResult{}, err
		}
	}
	if err := writeImportFile(marker, metadataBytes); err != nil {
		return checkpointImportResult{}, err
	}
	return checkpointImportSummary(partial), nil
}

func checkpointImportSummary(partial bool) checkpointImportResult {
	if partial {
		return checkpointImportResult{Status: "partial", Error: "legacy checkpoint artifacts include missing, unmapped, or non-restorable records"}
	}
	return checkpointImportResult{Status: "available"}
}
