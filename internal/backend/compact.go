package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/session/recording"
)

type compactResponse struct {
	Version        string `json:"version"`
	ConversationID string `json:"conversation_id"`
	RunID          string `json:"run_id"`
	AcceptedSeq    int64  `json:"accepted_seq"`
	Status         string `json:"status"`
	Revision       string `json:"revision,omitempty"`
}

func (s *Server) compactSession(w http.ResponseWriter, r *http.Request, id string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		mutationError(w, err)
		return
	}
	defer s.releaseRuntime(rt)
	var in protocol.CompactRequest
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if strings.TrimSpace(in.ClientRequestID) == "" {
		writeJSONError(w, http.StatusBadRequest, "client_request_id is required")
		return
	}
	if strings.TrimSpace(in.ExpectedRevision) == "" {
		writeJSONError(w, http.StatusBadRequest, "expected_revision is required")
		return
	}
	if in.ConversationID != "" && in.ConversationID != id {
		writeJSONError(w, http.StatusBadRequest, "conversation_id does not match session")
		return
	}
	in.ConversationID = id

	requestHash := hashCompact(in)
	rt.mu.Lock()
	if rt.closing || s.shuttingDown.Load() {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusServiceUnavailable, "backend is closing")
		return
	}
	if old, ok := rt.runs[in.ClientRequestID]; ok {
		rt.mu.Unlock()
		if old.PromptHash != requestHash || old.Kind != "compact" {
			writeJSONError(w, http.StatusConflict, "client_request_id was already used with a different compaction request")
			return
		}
		writeJSON(w, http.StatusOK, compactResponse{Version: protocol.Version, ConversationID: id, RunID: old.RunID, AcceptedSeq: old.AcceptedSeq, Status: compactStatus(old), Revision: revisionOf(s.store, id)})
		return
	}
	if rt.deleted {
		rt.mu.Unlock()
		mutationError(w, session.ErrNotFound)
		return
	}
	if rt.activeLocked() {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "session has an active turn or child task")
		return
	}
	if rt.runtimeErr != nil {
		err := rt.runtimeErr
		rt.mu.Unlock()
		writeJSONError(w, http.StatusInternalServerError, "session runtime is quarantined: "+err.Error())
		return
	}
	if rt.agent == nil || rt.recorder == nil {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusInternalServerError, "session runtime is unavailable")
		return
	}
	persistedSnapshot, revisionErr := s.store.HistorySnapshot(id)
	if revisionErr != nil {
		rt.mu.Unlock()
		mutationError(w, revisionErr)
		return
	}
	currentRevision := persistedSnapshot.Revision
	if in.ExpectedRevision != currentRevision {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "history revision conflict")
		return
	}
	runID := newRunID()
	rt.runID = runID
	rt.runDone = false
	ctx, cancel := context.WithCancel(context.Background())
	rt.cancel = cancel
	agentRef := rt.agent
	recorder := rt.recorder
	transactionSnapshot := recorder.Snapshot()
	rt.runs[in.ClientRequestID] = runRecord{ClientRequestID: in.ClientRequestID, PromptHash: requestHash, Kind: "compact", RunID: runID, AcceptedSeq: rt.nextSeq + 1}
	if err := rt.persistRuns(s.eventDir); err != nil {
		delete(rt.runs, in.ClientRequestID)
		rt.cancel = nil
		rt.runDone = true
		rt.mu.Unlock()
		cancel()
		writeJSONError(w, http.StatusInternalServerError, "persist compaction request: "+err.Error())
		return
	}
	started, err := protocol.NewEvent(rt.nextSeq+1, rt.id, runID, protocol.EventCompactionStarted, protocol.ToolStatus{Tool: "compaction", Status: "running", Compaction: true})
	if err == nil {
		err = rt.persistEvent(s.eventDir, started)
	}
	if err == nil {
		rt.nextSeq = started.Seq
		rt.appendEventLocked(started)
		close(rt.changed)
		rt.changed = make(chan struct{})
		rt.leases.Add(1)
		rt.runWorkers.Add(1)
	}
	rt.mu.Unlock()
	if err != nil {
		cancel()
		s.finishCompaction(rt, runID, in.ClientRequestID, "failed")
		writeJSONError(w, http.StatusInternalServerError, "persist compaction event: "+err.Error())
		return
	}
	acceptedRevision := currentRevision
	go func() {
		defer rt.runWorkers.Done()
		defer s.releaseRuntime(rt)
		s.executeCompaction(rt, ctx, cancel, runID, in.ClientRequestID, acceptedRevision, agentRef, recorder, transactionSnapshot)
	}()
	writeJSON(w, http.StatusAccepted, compactResponse{Version: protocol.Version, ConversationID: id, RunID: runID, AcceptedSeq: started.Seq, Status: "accepted", Revision: currentRevision})
}

func (s *Server) executeCompaction(rt *runtimeSession, ctx context.Context, cancel context.CancelFunc, runID, clientRequestID, expectedRevision string, ag *agent.Agent, recorder *recording.Recorder, snapshot recording.Snapshot) {
	defer cancel()
	var eventErr error
	var eventMu sync.Mutex
	var compactionUsage *protocol.Usage
	recordEventError := func(err error) {
		if err == nil {
			return
		}
		eventMu.Lock()
		eventErr = errors.Join(eventErr, err)
		eventMu.Unlock()
	}
	ev := agent.Events{
		OnCompactStart: func(took, estimated int) {
			_, err := s.publish(rt, protocol.EventToolStatus, protocol.ToolStatus{Tool: "compaction", Status: "running", Message: formatCompactionProgress(took, estimated), Compaction: true}, "")
			recordEventError(err)
		},
		OnCompacted: func(_ string, _ int, info agent.CompactInfo) {
			eventMu.Lock()
			compactionUsage = protocol.FromAIUsage(&info.Usage)
			eventMu.Unlock()
		},
	}
	compactErr := ag.ManualCompactCommit(ctx, agent.FanIn(recorder.Events(), ev), func() error {
		eventMu.Lock()
		defer eventMu.Unlock()
		if eventErr != nil {
			return eventErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return s.commitCompaction(rt, compactionUsage, func() error {
			return recorder.SaveAtRevision(expectedRevision)
		})
	})
	if compactErr != nil {
		recorder.Restore(snapshot)
		if err := s.restoreCompactionRuntime(rt, ag, recorder); err != nil {
			compactErr = errors.Join(compactErr, fmt.Errorf("restore runtime: %w", err))
			rt.mu.Lock()
			rt.runtimeErr = compactErr
			rt.mu.Unlock()
		}
	}

	if compactErr == nil {
		s.finishCompaction(rt, runID, clientRequestID, "completed")
		return
	}
	state := "failed"
	if errors.Is(compactErr, context.Canceled) || errors.Is(compactErr, context.DeadlineExceeded) {
		state = "cancelled"
	}
	terminal := protocol.RunTerminal{State: state, Error: compactErr.Error()}
	if state == "cancelled" {
		terminal.Message = "compaction cancelled"
	}
	if _, err := s.publish(rt, protocol.EventCompactionCompleted, map[string]any{"status": state, "error": terminal.Error}, ""); err != nil {
		s.markRuntimeError(rt, fmt.Errorf("persist compaction result: %w", err))
	}
	terminalType := protocol.EventRunFailed
	if state == "cancelled" {
		terminalType = protocol.EventRunCancelled
	}
	if _, err := s.publish(rt, terminalType, terminal, ""); err != nil {
		s.markRuntimeError(rt, fmt.Errorf("persist compaction terminal event: %w", err))
	}
	s.finishCompaction(rt, runID, clientRequestID, state)
}

// Stage the success journal before saving history; expose it only after the save.
// A failed save truncates the staged batch while the runtime lock excludes readers.
func (s *Server) commitCompaction(rt *runtimeSession, usage *protocol.Usage, save func() error) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.deleted {
		return session.ErrNotFound
	}
	var events []protocol.Event
	appendEvent := func(typ string, payload any) error {
		event, err := protocol.NewEvent(rt.nextSeq+int64(len(events))+1, rt.id, rt.runID, typ, payload)
		if err == nil {
			events = append(events, event)
		}
		return err
	}
	if usage != nil {
		if err := appendEvent(protocol.EventUsage, usage); err != nil {
			return err
		}
	}
	if err := appendEvent(protocol.EventCompactionCompleted, map[string]any{"status": "completed"}); err != nil {
		return err
	}
	if err := appendEvent(protocol.EventHistoryUpdated, map[string]any{"reason": "compaction"}); err != nil {
		return err
	}
	if err := appendEvent(protocol.EventRunCompleted, protocol.RunTerminal{State: "completed"}); err != nil {
		return err
	}
	var batch []byte
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		batch = append(batch, data...)
		batch = append(batch, '\n')
	}
	file, err := os.OpenFile(filepath.Join(s.eventDir, rt.id+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		restoreErr := file.Truncate(stat.Size())
		if restoreErr == nil {
			restoreErr = file.Sync()
		}
		if restoreErr != nil {
			rt.runtimeErr = errors.Join(rt.runtimeErr, fmt.Errorf("restore compaction journal: %w", restoreErr))
		}
		return errors.Join(cause, restoreErr)
	}
	if _, err := file.Write(batch); err != nil {
		return rollback(err)
	}
	if err := file.Sync(); err != nil {
		return rollback(err)
	}
	if err := save(); err != nil {
		return rollback(err)
	}
	rt.nextSeq = events[len(events)-1].Seq
	for _, event := range events {
		rt.appendEventLocked(event)
	}
	close(rt.changed)
	rt.changed = make(chan struct{})
	return nil
}

func (s *Server) restoreCompactionRuntime(rt *runtimeSession, failed *agent.Agent, recorder *recording.Recorder) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.agent != failed {
		return errors.New("compaction runtime changed before rollback")
	}
	if recorder == nil {
		return errors.New("compaction recorder is unavailable")
	}
	rt.recorder = recorder
	return nil
}

func (s *Server) markRuntimeError(rt *runtimeSession, err error) {
	if err == nil {
		return
	}
	rt.mu.Lock()
	rt.runtimeErr = errors.Join(rt.runtimeErr, err)
	rt.mu.Unlock()
}

func (s *Server) finishCompaction(rt *runtimeSession, runID, clientRequestID, state string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.runID != runID {
		return
	}
	rt.runDone = true
	rt.cancel = nil
	rt.closeJournalLocked()
	if record, ok := rt.runs[clientRequestID]; ok {
		record.Terminal = true
		record.State = state
		rt.runs[clientRequestID] = record
		if err := rt.persistRuns(s.eventDir); err != nil {
			rt.runtimeErr = errors.Join(rt.runtimeErr, fmt.Errorf("persist compaction terminal state: %w", err))
		}
	}
	old := rt.changed
	rt.changed = make(chan struct{})
	close(old)
	s.dispatchWakesLocked(rt)
}

func hashCompact(in protocol.CompactRequest) string {
	b, _ := json.Marshal(struct {
		ClientRequestID  string `json:"client_request_id"`
		ConversationID   string `json:"conversation_id"`
		ExpectedRevision string `json:"expected_revision"`
	}{in.ClientRequestID, in.ConversationID, in.ExpectedRevision})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func compactStatus(record runRecord) string {
	if !record.Terminal {
		return "accepted"
	}
	if record.State == "" {
		return "completed"
	}
	return record.State
}

func revisionOf(store *session.Store, id string) string {
	snapshot, err := store.HistorySnapshot(id)
	if err != nil {
		return ""
	}
	return snapshot.Revision
}

func formatCompactionProgress(took, estimated int) string {
	return fmt.Sprintf("compacting history (%d messages, %d estimated tokens)", took, estimated)
}
