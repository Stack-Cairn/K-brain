package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

const maxQueueItems = 128
const maxQueueReceipts = 4096
const maxQueueBytes = 16 << 20

type queueDispatchKey struct{}

func (rt *runtimeSession) loadQueueLocked() error {
	if rt.queue != nil {
		return nil
	}
	doc := &queueDocument{Version: 1, Items: []queueEntry{}, Edits: map[string]queueEdit{}, Receipts: map[string]queueReceipt{}}
	file, err := os.Open(filepath.Join(rt.eventDir, rt.id+".queue.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, maxQueueBytes+1))
		if err != nil {
			return err
		}
		if len(data) > maxQueueBytes {
			return errors.New("queue document exceeds limit")
		}
		if err := json.Unmarshal(data, doc); err != nil {
			return fmt.Errorf("decode queue: %w", err)
		}
		if doc.Version != 1 || doc.Receipts == nil || doc.Edits == nil {
			return errors.New("invalid queue document")
		}
		if len(doc.Items)+len(doc.Edits) > maxQueueItems || len(doc.Receipts) > maxQueueReceipts {
			return errors.New("invalid queue size")
		}
		seen := map[string]bool{}
		check := func(item queueEntry) bool {
			if item.ID == "" || item.Request.ClientRequestID == "" || seen[item.ID] {
				return false
			}
			seen[item.ID] = true
			receipt, ok := doc.Receipts[item.Request.ClientRequestID]
			return ok && receipt.ID == item.ID
		}
		for _, item := range doc.Items {
			if !check(item) {
				return errors.New("invalid queue item identity")
			}
		}
		for key, edit := range doc.Edits {
			if key != edit.Entry.ID || edit.Index < 0 || !check(edit.Entry) {
				return errors.New("invalid queue edit identity")
			}
		}
		// Restoring work never implicitly replays tool operations.
		doc.Paused = true
		if err := rt.recoverQueueAcceptancesLocked(doc); err != nil {
			return err
		}
	}
	rt.queue = doc
	return nil
}

func (rt *runtimeSession) recoverQueueAcceptancesLocked(doc *queueDocument) error {
	file, err := os.Open(filepath.Join(rt.eventDir, rt.id+".jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	byRun := map[string]string{}
	for requestID, rec := range rt.runs {
		byRun[rec.RunID] = requestID
	}
	decoder := json.NewDecoder(file)
	for {
		var event protocol.Event
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return err
		}
		if event.Type == protocol.EventRunAccepted {
			var payload struct {
				ClientRequestID string `json:"client_request_id"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return err
			}
			if _, queued := doc.Receipts[payload.ClientRequestID]; queued {
				if _, exists := rt.runs[payload.ClientRequestID]; !exists {
					rt.runs[payload.ClientRequestID] = runRecord{ClientRequestID: payload.ClientRequestID, RunID: event.RunID, AcceptedSeq: event.Seq}
					byRun[event.RunID] = payload.ClientRequestID
				}
			}
		}
		if event.Type == protocol.EventRunCompleted || event.Type == protocol.EventRunFailed || event.Type == protocol.EventRunCancelled {
			if requestID, ok := byRun[event.RunID]; ok {
				var terminal protocol.RunTerminal
				if err := json.Unmarshal(event.Payload, &terminal); err != nil {
					return err
				}
				rec := rt.runs[requestID]
				rec.State = terminal.State
				rec.Terminal = true
				rt.runs[requestID] = rec
			}
		}
	}
	return nil
}

func cloneQueue(doc *queueDocument) *queueDocument {
	data, _ := json.Marshal(doc)
	var copy queueDocument
	_ = json.Unmarshal(data, &copy)
	return &copy
}

func (rt *runtimeSession) saveQueueLocked(next *queueDocument) error {
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(data) > maxQueueBytes {
		return errors.New("queue document exceeds 16 MiB limit")
	}
	file, err := os.CreateTemp(rt.eventDir, ".session-queue-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), filepath.Join(rt.eventDir, rt.id+".queue.json"))
	}
	if err != nil {
		return err
	}
	rt.queue = next
	old := rt.changed
	rt.changed = make(chan struct{})
	close(old)
	return nil
}

func queueItemView(entry queueEntry, detail bool) queueItem {
	count := 0
	for _, block := range entry.Request.Content {
		if block.Type == protocol.ContentImage || block.Type == protocol.ContentFile {
			count++
		}
	}
	item := queueItem{ID: entry.ID, ClientRequestID: entry.Request.ClientRequestID, PreviewText: entry.Request.Prompt, FileCount: count, CreatedAt: entry.CreatedAt, Source: entry.Source, Editable: true}
	if detail {
		req := entry.Request
		item.Request = &req
		item.DraftJSON = entry.DraftJSON
		item.UploadedFilesJSON = entry.UploadedFilesJSON
	}
	return item
}

func defaultQueueDraft(prompt string) string {
	data, _ := json.Marshal(map[string]any{"text": prompt, "segments": []map[string]string{{"type": "text", "text": prompt}}})
	return string(data)
}

func (rt *runtimeSession) queueReceiptLocked(requestID string) (queueReceipt, bool) {
	receipt, ok := rt.queue.Receipts[requestID]
	if rec, exists := rt.runs[requestID]; ok && exists {
		receipt.Run = &protocol.RunAccepted{Version: protocol.Version, ConversationID: rt.id, RunID: rec.RunID, AcceptedSeq: rec.AcceptedSeq}
		receipt.State = "running"
		if rec.Terminal {
			receipt.State = rec.State
		} else if rt.cancel == nil || rt.runDone || rec.RunID != rt.runID {
			receipt.State = "interrupted"
			receipt.Error = "run interrupted by backend restart; it will not be replayed"
		}
	}
	return receipt, ok
}

func (rt *runtimeSession) queueResponseLocked() queueResponse {
	snapshot := queueSnapshot{ConversationID: rt.id, Revision: rt.queue.Revision, Paused: rt.queue.Paused, Items: []queueItem{}}
	for _, entry := range rt.queue.Items {
		snapshot.Items = append(snapshot.Items, queueItemView(entry, false))
	}
	return queueResponse{Version: protocol.Version, Accepted: true, Snapshot: snapshot}
}

func (rt *runtimeSession) publishQueueLocked() {
	runID := rt.runID
	if runID == "" {
		runID = "queue"
	}
	if err := rt.publishEventLocked(runID, "queue.updated", rt.queueResponseLocked().Snapshot); err != nil {
		rt.runtimeErr = fmt.Errorf("persist queue event: %w", err)
	}
}

func validateQueuedPrompt(in protocol.PromptRequest, id string) error {
	if strings.TrimSpace(in.ClientRequestID) == "" || len(in.ClientRequestID) > 256 {
		return errors.New("client_request_id must contain 1 to 256 characters")
	}
	if in.ConversationID != "" && in.ConversationID != id {
		return errors.New("conversation_id does not match session")
	}
	if in.StopRequested {
		return errors.New("run was stopped before acceptance")
	}
	if in.ResumeMessageID != "" {
		return errors.New("resume_message_id is only supported by immediate runs")
	}
	if in.HookPolicy != "" && in.HookPolicy != "backend" {
		return errors.New("hook_policy must be backend")
	}
	if len(in.HookScopeID) > 256 {
		return errors.New("hook_scope_id is too long")
	}
	msg := protocol.Message{Role: protocol.RoleUser, Content: append([]protocol.ContentBlock{{Type: protocol.ContentText, Text: in.Prompt}}, in.Content...)}
	if strings.TrimSpace(in.Prompt) == "" && len(in.Content) == 0 {
		return errors.New("prompt or content is required")
	}
	for _, block := range msg.Content {
		if block.Type == protocol.ContentThinking {
			return errors.New("user content must be text, image or file")
		}
	}
	return msg.Validate()
}

func validateQueueDraft(raw, uploads string) error {
	if raw != "" {
		var draft struct {
			Text *string `json:"text"`
		}
		if err := json.Unmarshal([]byte(raw), &draft); err != nil || draft.Text == nil {
			return errors.New("draft_json must be a draft object with text")
		}
	}
	if uploads != "" {
		var files []json.RawMessage
		if json.Unmarshal([]byte(uploads), &files) != nil || files == nil {
			return errors.New("uploaded_files_json must be an array")
		}
		// Uploaded file resolution belongs at submission, not at dispatch.
		if len(files) > 0 {
			return errors.New("resolve uploads into canonical request content before queue submission")
		}
	}
	return nil
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request, id string, actions bool) {
	var submit queueSubmit
	var action queueAction
	if r.Method == http.MethodPost {
		if actions {
			if decodeJSON(w, r, &action) != nil {
				return
			}
		} else {
			if decodeJSON(w, r, &submit) != nil {
				return
			}
		}
	} else if r.Method != http.MethodGet || actions {
		writeJSONError(w, 405, "method not allowed")
		return
	}
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		mutationError(w, err)
		return
	}
	defer s.releaseRuntime(rt)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closing || rt.deleted || s.shuttingDown.Load() {
		writeJSONError(w, 409, "session is unavailable")
		return
	}
	if rt.runtimeErr != nil {
		writeJSONError(w, 500, "session runtime is quarantined")
		return
	}
	if err = rt.loadQueueLocked(); err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		if r.URL.Query().Get("wait") == "1" {
			revision, parseErr := strconv.ParseUint(r.URL.Query().Get("after_revision"), 10, 64)
			if parseErr != nil {
				writeJSONError(w, 400, "after_revision is required for wait")
				return
			}
			deadline := time.NewTimer(20 * time.Second)
			defer deadline.Stop()
			waiting := true
			for waiting && rt.queue.Revision == revision && !rt.closing && !s.shuttingDown.Load() {
				changed := rt.changed
				rt.mu.Unlock()
				select {
				case <-changed:
				case <-deadline.C:
					waiting = false
				case <-r.Context().Done():
					rt.mu.Lock()
					return
				}
				rt.mu.Lock()
			}
		}
		out := rt.queueResponseLocked()
		if requestID := r.URL.Query().Get("client_request_id"); requestID != "" {
			receipt, ok := rt.queueReceiptLocked(requestID)
			if !ok {
				writeJSONError(w, 404, "queue receipt not found")
				return
			}
			out.Receipt = &receipt
		}
		writeJSON(w, 200, out)
		return
	}
	if rt.queueDispatchID != "" {
		writeJSONError(w, 409, "queue dispatch is in progress; reload queue before retrying")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	if actions {
		s.queueActionLocked(w, rt, action)
		return
	}
	policy := submit.Policy
	if policy == "" {
		policy = "append"
	}
	if policy != "append" && policy != "auto" && policy != "interrupt" {
		writeJSONError(w, 400, "invalid queue policy")
		return
	}
	if err = validateQueuedPrompt(submit.Request, id); err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	if err = validateQueueDraft(submit.DraftJSON, submit.UploadedFilesJSON); err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	if submit.Source == "" {
		submit.Source = "gui"
	}
	if submit.Source != "gui" && submit.Source != "webui" {
		writeJSONError(w, 400, "invalid queue source")
		return
	}
	submit.Request.ConversationID = id
	hashBytes, _ := json.Marshal(submit)
	hash := hashPrompt(protocol.PromptRequest{Prompt: string(hashBytes)})
	if receipt, exists := rt.queueReceiptLocked(submit.Request.ClientRequestID); exists {
		out := rt.queueResponseLocked()
		out.Receipt = &receipt
		if receipt.Hash != hash {
			out.Accepted = false
			out.ErrorCode = "conflict"
			out.Message = "client_request_id was already used with a different queue submission"
			writeJSON(w, 409, out)
			return
		}
		writeJSON(w, 200, out)
		return
	}
	if _, exists := rt.runs[submit.Request.ClientRequestID]; exists {
		writeJSONError(w, 409, "client_request_id belongs to an immediate run")
		return
	}
	if len(rt.queue.Items)+len(rt.queue.Edits) >= maxQueueItems || len(rt.queue.Receipts) >= maxQueueReceipts {
		writeJSONError(w, 429, "session queue capacity reached")
		return
	}
	if options, err := normalizeRunOptions(submit.Request.Options, rt.workingDir(), rt.agent.KnownTools()); err != nil {
		writeJSONError(w, 400, err.Error())
		return
	} else if _, err := s.runSandboxPolicy(rt.agent.SandboxPolicy, rt.workingDir(), options); err != nil {
		writeJSONError(w, 400, "options.sandbox: "+err.Error())
		return
	}
	entry := queueEntry{ID: newRunID(), Request: submit.Request, Source: submit.Source, CreatedAt: time.Now().UnixMilli(), DraftJSON: submit.DraftJSON, UploadedFilesJSON: submit.UploadedFilesJSON, MemoryOff: !s.memoryEnabledNow(rt)}
	if entry.DraftJSON == "" {
		entry.DraftJSON = defaultQueueDraft(entry.Request.Prompt)
	}
	if entry.UploadedFilesJSON == "" {
		entry.UploadedFilesJSON = "[]"
	}
	next := cloneQueue(rt.queue)
	if policy == "interrupt" {
		next.Items = append([]queueEntry{entry}, next.Items...)
	} else {
		next.Items = append(next.Items, entry)
	}
	next.Revision++
	receipt := queueReceipt{ID: entry.ID, Hash: hash, State: "queued"}
	next.Receipts[entry.Request.ClientRequestID] = receipt
	if err = rt.saveQueueLocked(next); err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	rt.publishQueueLocked()
	if policy == "interrupt" && !next.Paused && rt.cancel != nil {
		rt.cancel()
	}
	s.startQueueWorkerLocked(rt)
	out := rt.queueResponseLocked()
	out.Receipt = &receipt
	writeJSON(w, 202, out)
}

func (s *Server) queueActionLocked(w http.ResponseWriter, rt *runtimeSession, in queueAction) {
	out := rt.queueResponseLocked()
	fail := func(status int, code, message string) {
		out.Accepted = false
		out.ErrorCode = code
		out.Message = message
		writeJSON(w, status, out)
	}
	if in.Action == "get_item" {
		for _, entry := range rt.queue.Items {
			if entry.ID == in.ItemID {
				item := queueItemView(entry, true)
				out.Item = &item
				writeJSON(w, 200, out)
				return
			}
		}
		if edit, ok := rt.queue.Edits[in.ItemID]; ok {
			item := queueItemView(edit.Entry, true)
			out.Item = &item
			writeJSON(w, 200, out)
			return
		}
		fail(404, "not_found", "queued item not found")
		return
	}
	if in.Revision != rt.queue.Revision {
		fail(409, "conflict", "queue revision conflict")
		return
	}
	next := cloneQueue(rt.queue)
	index := -1
	for i, entry := range next.Items {
		if entry.ID == in.ItemID {
			index = i
			break
		}
	}
	interrupt := false
	switch in.Action {
	case "pause":
		next.Paused = true
	case "resume":
		next.Paused = false
	case "edit_cancel", "edit_commit":
		edit, ok := next.Edits[in.ItemID]
		if !ok {
			fail(404, "not_found", "queue edit not found")
			return
		}
		if in.Action == "edit_commit" {
			if in.Request == nil {
				fail(400, "invalid_payload", "request is required")
				return
			}
			request := *in.Request
			if request.ClientRequestID != edit.Entry.Request.ClientRequestID {
				fail(400, "invalid_payload", "edit must preserve client_request_id")
				return
			}
			if err := validateQueuedPrompt(request, rt.id); err != nil {
				fail(400, "invalid_payload", err.Error())
				return
			}
			if err := validateQueueDraft(in.DraftJSON, in.UploadedFilesJSON); err != nil {
				fail(400, "invalid_payload", err.Error())
				return
			}
			if options, err := normalizeRunOptions(request.Options, rt.workingDir(), rt.agent.KnownTools()); err != nil {
				fail(400, "invalid_payload", err.Error())
				return
			} else if _, err := s.runSandboxPolicy(rt.agent.SandboxPolicy, rt.workingDir(), options); err != nil {
				fail(400, "invalid_payload", "options.sandbox: "+err.Error())
				return
			}
			request.ConversationID = rt.id
			edit.Entry.Request = request
			// Text edited while memory is off is off-period input; the mark is never cleared.
			edit.Entry.MemoryOff = edit.Entry.MemoryOff || !s.memoryEnabledNow(rt)
			edit.Entry.DraftJSON = in.DraftJSON
			if edit.Entry.DraftJSON == "" {
				edit.Entry.DraftJSON = defaultQueueDraft(request.Prompt)
			}
			edit.Entry.UploadedFilesJSON = in.UploadedFilesJSON
			if edit.Entry.UploadedFilesJSON == "" {
				edit.Entry.UploadedFilesJSON = "[]"
			}
		}
		pos := min(edit.Index, len(next.Items))
		next.Items = append(next.Items, queueEntry{})
		copy(next.Items[pos+1:], next.Items[pos:])
		next.Items[pos] = edit.Entry
		delete(next.Edits, in.ItemID)
	default:
		if index < 0 {
			fail(404, "not_found", "queued item not found")
			return
		}
		entry := next.Items[index]
		switch in.Action {
		case "move":
			swap := index - 1
			if in.Direction == "down" {
				swap = index + 1
			} else if in.Direction != "up" {
				fail(400, "invalid_request", "direction must be up or down")
				return
			}
			if swap < 0 || swap >= len(next.Items) {
				writeJSON(w, 200, out)
				return
			}
			next.Items[index], next.Items[swap] = next.Items[swap], next.Items[index]
		case "remove", "edit_begin", "run_now":
			next.Items = append(next.Items[:index], next.Items[index+1:]...)
			if in.Action == "remove" {
				receipt := next.Receipts[entry.Request.ClientRequestID]
				receipt.State = "cancelled"
				next.Receipts[entry.Request.ClientRequestID] = receipt
			}
			if in.Action == "edit_begin" {
				next.Edits[entry.ID] = queueEdit{Entry: entry, Index: index}
				item := queueItemView(entry, true)
				out.Item = &item
			}
			if in.Action == "run_now" {
				next.Items = append([]queueEntry{entry}, next.Items...)
				next.Paused = false
				interrupt = true
			}
		default:
			fail(400, "unsupported_action", "unsupported queue action")
			return
		}
	}
	next.Revision++
	if err := rt.saveQueueLocked(next); err != nil {
		fail(500, "persistence_error", err.Error())
		return
	}
	rt.publishQueueLocked()
	if interrupt && rt.cancel != nil {
		rt.cancel()
	}
	s.startQueueWorkerLocked(rt)
	result := rt.queueResponseLocked()
	result.Item = out.Item
	writeJSON(w, 200, result)
}

func (s *Server) startQueueWorkerLocked(rt *runtimeSession) {
	if rt.queueWorker || rt.queue.Paused || len(rt.queue.Items) == 0 || rt.closing || s.shuttingDown.Load() || rt.runtimeErr != nil {
		return
	}
	rt.queueWorker = true
	rt.leases.Add(1)
	rt.runWorkers.Add(1)
	go func() {
		defer rt.runWorkers.Done()
		defer s.releaseRuntime(rt)
		defer func() {
			rt.mu.Lock()
			rt.queueWorker = false
			rt.queueDispatchID = ""
			s.startQueueWorkerLocked(rt)
			rt.mu.Unlock()
		}()
		for {
			rt.mu.Lock()
			if rt.closing || rt.deleted || s.shuttingDown.Load() || rt.runtimeErr != nil || rt.queue.Paused || len(rt.queue.Items) == 0 {
				rt.mu.Unlock()
				return
			}
			if rt.activeLocked() {
				changed := rt.changed
				rt.mu.Unlock()
				<-changed
				continue
			}
			entry := rt.queue.Items[0]
			// An acceptance persisted before a crash is attached, never resubmitted.
			record, already := rt.runs[entry.Request.ClientRequestID]
			rt.queueDispatchID = entry.Request.ClientRequestID
			rt.mu.Unlock()
			accepted := protocol.RunAccepted{Version: protocol.Version, ConversationID: rt.id, RunID: record.RunID, AcceptedSeq: record.AcceptedSeq}
			status := http.StatusOK
			var err error
			if !already {
				dispatchCtx := context.WithValue(context.Background(), queueDispatchKey{}, rt)
				if entry.MemoryOff {
					dispatchCtx = agent.WithMemoryOffAuthored(dispatchCtx)
				}
				accepted, status, err = s.startCanonicalRun(dispatchCtx, rt, entry.Request)
			}
			rt.mu.Lock()
			rt.queueDispatchID = ""
			if err != nil && status == http.StatusConflict && err.Error() == errSessionBusy {
				rt.mu.Unlock()
				continue
			}
			if s.shuttingDown.Load() || rt.closing || rt.runtimeErr != nil {
				rt.mu.Unlock()
				return
			}
			next := cloneQueue(rt.queue)
			// Actions cannot move or remove the dispatching item.
			if len(next.Items) == 0 || next.Items[0].ID != entry.ID {
				rt.runtimeErr = errors.New("queue dispatch identity changed")
				rt.mu.Unlock()
				return
			}
			next.Items = next.Items[1:]
			next.Revision++
			receipt := next.Receipts[entry.Request.ClientRequestID]
			if err != nil {
				receipt.State = "failed"
				receipt.Error = err.Error()
			} else {
				receipt.State = "accepted"
				receipt.Run = &accepted
			}
			next.Receipts[entry.Request.ClientRequestID] = receipt
			if persistErr := rt.saveQueueLocked(next); persistErr != nil {
				rt.runtimeErr = fmt.Errorf("persist queue dispatch: %w", persistErr)
				rt.mu.Unlock()
				return
			}
			rt.publishQueueLocked()
			rt.mu.Unlock()
		}
	}()
}
