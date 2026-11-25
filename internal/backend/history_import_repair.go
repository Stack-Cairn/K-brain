package backend

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func historyMetadataWithoutCheckpoint(raw json.RawMessage) ([]byte, error) {
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, err
	}
	delete(metadata, "checkpoint")
	if original, ok := metadata["original"]; ok {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(original, &item); err != nil {
			return nil, err
		}
		for _, key := range []string{"checkpoint", "checkpointStatus", "checkpointNativePath", "checkpointReason"} {
			delete(item, key)
		}
		data, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		metadata["original"] = data
	}
	return json.Marshal(metadata)
}

var errHistoryRepairModified = errors.New("checkpoint repair conflicts with existing modified history")

// Only an exact original history request may acquire previously unresolved checkpoints.
func unresolvedHistoryRepair(in legacyHistoryImportRequest, meta session.Meta) bool {
	if in.Checkpoint == nil || in.Checkpoint.Status != "available" || meta.ImportSourceID != in.SourceID {
		return false
	}
	var metadata struct {
		Checkpoint json.RawMessage `json:"checkpoint"`
	}
	if json.Unmarshal([]byte(meta.ImportMetadata), &metadata) != nil {
		return false
	}
	var previous legacyCheckpointImport
	if json.Unmarshal(metadata.Checkpoint, &previous) != nil || previous.Status != "unresolved" || len(previous.Records) > 0 || previous.IndexJSONL != "" {
		return false
	}
	before, err := historyMetadataWithoutCheckpoint(json.RawMessage(meta.ImportMetadata))
	if err != nil {
		return false
	}
	after, err := historyMetadataWithoutCheckpoint(in.SourceMetadata)
	if err != nil || !bytes.Equal(before, after) {
		return false
	}
	candidate := in
	candidate.SourceFingerprint = meta.ImportFingerprint
	candidate.SourceMetadata = json.RawMessage(meta.ImportMetadata)
	candidate.Checkpoint = nil
	if importFingerprint(candidate) == meta.ImportContentFingerprint {
		return true
	}
	candidate.Checkpoint = &previous
	if importFingerprint(candidate) == meta.ImportContentFingerprint {
		return true
	}
	// The frontend's fallback reason is metadata-only, not part of its checkpoint request.
	previous.Reason = ""
	return importFingerprint(candidate) == meta.ImportContentFingerprint
}

func (s *Server) claimUnresolvedHistoryRepair(in legacyHistoryImportRequest, messages []ai.Message) error {
	original, _ := json.Marshal(messages)
	current, _ := json.Marshal(s.store.RawMessages(in.ConversationID))
	if !bytes.Equal(original, current) {
		return errHistoryRepairModified
	}
	// The immutable claim prevents a second export from replacing an accepted repair.
	claim, _ := json.Marshal(map[string]string{"source_id": in.SourceID, "fingerprint": importFingerprint(in)})
	return writeImportFile(filepath.Join(s.eventDir, "history-import-repair", in.ConversationID+".json"), claim)
}
