package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

type legacyActiveContext struct {
	Cutoff  int    `json:"cutoff"`
	Summary string `json:"summary"`
}

type legacyHistoryImportRequest struct {
	ActiveContext     *legacyActiveContext    `json:"active_context,omitempty"`
	Checkpoint        *legacyCheckpointImport `json:"checkpoint,omitempty"`
	SourceID          string                  `json:"source_id"`
	SourceFingerprint string                  `json:"source_fingerprint"`
	ConversationID    string                  `json:"conversation_id"`
	Title             string                  `json:"title"`
	CWD               string                  `json:"cwd"`
	Model             protocol.ModelRef       `json:"model"`
	CreatedAt         time.Time               `json:"created_at"`
	UpdatedAt         time.Time               `json:"updated_at"`
	Pinned            bool                    `json:"pinned"`
	Shared            bool                    `json:"shared"`
	ShareToken        string                  `json:"share_token"`
	ShareRedactTool   bool                    `json:"share_redact_tool"`
	Messages          []protocol.Message      `json:"messages"`
	SourceMetadata    json.RawMessage         `json:"source_metadata"`
}

// maxLegacyHistoryBodyBytes caps one-shot conversation imports. Legacy clients
// send the whole conversation and their source metadata repeats it twice, so a
// conversation with inline images can exceed 64 MiB (reported in the field).
// Import is a one-time desktop migration, so the cap only guards against
// runaway bodies; it stays below maxOversizedBodyDrainBytes so a rejected body
// still gets a readable 413.
const maxLegacyHistoryBodyBytes = 512 << 20

func importFingerprint(in legacyHistoryImportRequest) string {
	raw, _ := json.Marshal(in)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func (s *Server) importLegacyHistory(w http.ResponseWriter, r *http.Request) {
	var in legacyHistoryImportRequest
	if err := decodeJSONLimit(w, r, &in, maxLegacyHistoryBodyBytes); err != nil {
		return
	}
	in.SourceID, in.SourceFingerprint, in.ConversationID = strings.TrimSpace(in.SourceID), strings.TrimSpace(in.SourceFingerprint), strings.TrimSpace(in.ConversationID)
	if !validID(in.SourceID) || in.SourceFingerprint == "" || in.ConversationID == "" || in.SourceID != in.ConversationID || in.Model.Model == "" {
		writeJSONError(w, http.StatusBadRequest, "source_id, conversation_id, matching fingerprint, and model.model are required")
		return
	}
	messages := make([]ai.Message, 0, len(in.Messages))
	seen := map[string]bool{}
	for _, message := range in.Messages {
		if message.ID != "" && seen[message.ID] {
			writeJSONError(w, http.StatusBadRequest, "duplicate history message id")
			return
		}
		seen[message.ID] = true
		converted, err := message.ToAIMessage()
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid history message: "+err.Error())
			return
		}
		messages = append(messages, converted)
	}
	metadata := strings.TrimSpace(string(in.SourceMetadata))
	if metadata == "" || metadata == "null" {
		metadata = "{}"
	}
	var boundary *session.Compaction
	if in.ActiveContext != nil {
		c := in.ActiveContext
		minimum := 0
		if len(messages) > 0 && messages[0].Role == "system" {
			minimum = 1
		}
		if c.Cutoff < minimum || c.Cutoff > len(messages) {
			writeJSONError(w, http.StatusBadRequest, "invalid active context cutoff")
			return
		}
		boundary = &session.Compaction{Seq: 1, Cutoff: c.Cutoff, Summary: c.Summary, DropPrior: true, Fresh: c.Summary == ""}
	}
	var checkpointRecords map[uint64]checkpointRecord
	var checkpointPartial bool
	if in.Checkpoint != nil {
		var err error
		checkpointRecords, checkpointPartial, err = prepareLegacyCheckpoints(*in.Checkpoint, in.ConversationID, in.SourceFingerprint)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	for _, record := range checkpointRecords {
		for _, message := range in.Messages {
			if message.ID == record.TurnID && message.Role != protocol.RoleUser {
				writeJSONError(w, http.StatusBadRequest, "legacy checkpoint turn must refer to a user message")
				return
			}
		}
	}
	contentFingerprint := importFingerprint(in)
	already, err := s.store.ImportHistory(in.ConversationID, in.SourceID, in.SourceFingerprint, contentFingerprint, metadata, in.CWD, in.Model.Model, in.Model.Provider, in.Title, in.CreatedAt, in.UpdatedAt, in.Pinned, in.Shared, in.ShareRedactTool, in.ShareToken, messages, boundary)
	if errors.Is(err, session.ErrHistoryDeleted) {
		writeJSONError(w, http.StatusGone, err.Error())
		return
	}
	if err != nil && strings.Contains(err.Error(), "conflicts with existing") && !checkpointPartial {
		meta, _, loadErr := s.store.Load(in.ConversationID)
		if loadErr == nil && unresolvedHistoryRepair(in, meta) {
			rt, runtimeErr := s.loadRuntimeByID(in.ConversationID)
			if runtimeErr != nil {
				writeJSONError(w, http.StatusInternalServerError, runtimeErr.Error())
				return
			}
			defer s.releaseRuntime(rt)
			rt.mu.Lock()
			defer rt.mu.Unlock()
			if !s.mutationAllowed(w, rt) {
				return
			}
			err = s.claimUnresolvedHistoryRepair(in, messages)
			already = err == nil
		}
	}
	if err != nil {
		if strings.Contains(err.Error(), "conflicts with existing") {
			writeJSONError(w, http.StatusConflict, err.Error())
		} else {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	checkpointStatus := "unresolved"
	checkpointReason := "checkpoint export was not supplied"
	if in.Checkpoint != nil {
		checkpoint, checkpointErr := s.importLegacyCheckpoints(*in.Checkpoint, in.ConversationID, in.SourceID, in.SourceFingerprint, checkpointRecords, checkpointPartial)
		if checkpointErr != nil {
			if _, ok := checkpointErr.(*checkpointImportValidationError); ok {
				writeJSONError(w, http.StatusBadRequest, checkpointErr.Error())
			} else {
				writeJSONError(w, http.StatusInternalServerError, checkpointErr.Error())
			}
			return
		}
		checkpointStatus = checkpoint.Status
		checkpointReason = checkpoint.Error
	}
	response := map[string]any{"source_id": in.SourceID, "backend_id": in.ConversationID, "status": map[bool]string{true: "already_imported", false: "imported"}[already], "checkpoint": checkpointStatus, "fingerprint": importFingerprint(in)}
	if checkpointReason != "" {
		response["checkpoint_reason"] = checkpointReason
	}
	writeJSON(w, http.StatusOK, response)
}
