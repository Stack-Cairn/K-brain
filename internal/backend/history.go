package backend

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/session/recording"
)

func (rt *runtimeSession) activeLocked() bool {
	if rt.cancel != nil && !rt.runDone {
		return true
	}
	if rt.agent != nil {
		if rt.agent.TurnRunning() {
			return true
		}
		for _, task := range rt.agent.Tasks().List() {
			if task.Status == agent.TaskRunning || task.FollowingUp {
				return true
			}
		}
	}
	return false
}

func mutationError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, session.ErrRevision) {
		status = http.StatusConflict
	}
	if errors.Is(err, session.ErrAnchor) || errors.Is(err, session.ErrNotFound) {
		status = http.StatusNotFound
	}
	writeJSONError(w, status, err.Error())
}

func (s *Server) mutationAllowed(w http.ResponseWriter, rt *runtimeSession) bool {
	if rt.deleted {
		writeJSONError(w, 404, "session not found")
		return false
	}
	if rt.activeLocked() {
		writeJSONError(w, 409, "session has an active turn or child task")
		return false
	}
	if tasks, err := s.store.LoadTasks(rt.id); err == nil {
		for _, task := range tasks {
			if task.Status == string(agent.TaskRunning) {
				writeJSONError(w, 409, "session has an active child task")
				return false
			}
		}
	}
	return true
}

func intQuery(r *http.Request, key string, fallback, minimum, maximum int) (int, error) {
	value, exists := r.URL.Query()[key]
	if !exists {
		return fallback, nil
	}
	n, err := strconv.Atoi(value[0])
	if err != nil || n < minimum || n > maximum {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return n, nil
}

func boolQuery(r *http.Request, key string) (*bool, error) {
	values, exists := r.URL.Query()[key]
	if !exists {
		return nil, nil
	}
	b, err := strconv.ParseBool(values[0])
	if err != nil {
		return nil, fmt.Errorf("invalid %s", key)
	}
	return &b, nil
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	page, err := intQuery(r, "page", 1, 1, int(^uint(0)>>1))
	if err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	size, err := intQuery(r, "page_size", 200, 1, 1000)
	if err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	if page-1 > int(^uint(0)>>1)/size {
		writeJSONError(w, 400, "page overflow")
		return
	}
	shared, err := boolQuery(r, "shared")
	if err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	empty, err := boolQuery(r, "cwd_empty")
	if err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	opts := session.PageOptions{Limit: size, Offset: (page - 1) * size, Shared: shared}
	if values, exists := r.URL.Query()["cwd"]; exists {
		cwd := values[0]
		opts.CWD = &cwd
	}
	if empty != nil && *empty {
		if opts.CWD != nil && *opts.CWD != "" {
			writeJSONError(w, 400, "cwd and cwd_empty conflict")
			return
		}
		cwd := ""
		opts.CWD = &cwd
	}
	result, err := s.store.ListPage(r.Context(), opts)
	if err != nil {
		mutationError(w, err)
		return
	}
	out := protocol.SessionPage{Version: protocol.Version, TotalCount: result.Total, Sessions: []protocol.SessionSummary{}}
	for _, meta := range result.Sessions {
		out.Sessions = append(out.Sessions, summary(meta, len(s.store.RawMessages(meta.ID))))
	}
	writeJSON(w, 200, out)
}

func (s *Server) updateSession(w http.ResponseWriter, r *http.Request, id string) {
	var in protocol.UpdateSessionRequest
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if (in.Model == nil && in.Title == nil && in.Pinned == nil && in.Archived == nil && in.CWD == nil) || in.Shared != nil || in.ShareRedactTool != nil {
		writeJSONError(w, 400, "provide title, pinned, archived, model, or cwd; use /share for sharing")
		return
	}
	if in.CWD != nil {
		cwd, err := validSessionCWD(*in.CWD)
		if err != nil {
			writeJSONError(w, 400, err.Error())
			return
		}
		in.CWD = &cwd
	}
	if in.Model != nil && strings.TrimSpace(in.Model.Model) == "" {
		writeJSONError(w, 400, "model.model is required")
		return
	}
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		mutationError(w, err)
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !s.mutationAllowed(w, rt) {
		return
	}
	if in.Model != nil {
		if err := s.switchModelLocked(rt, *in.Model); err != nil {
			writeJSONError(w, 400, "model switch failed: "+err.Error())
			return
		}
	}
	if in.CWD != nil {
		if err := s.switchWorkingDirLocked(rt, *in.CWD); err != nil {
			writeJSONError(w, 400, "workspace switch failed: "+err.Error())
			return
		}
	}
	if in.Title != nil {
		err = s.store.SetTitle(id, *in.Title)
	}
	if err == nil && in.Pinned != nil {
		err = s.store.SetPinned(id, *in.Pinned)
	}
	if err == nil && in.Archived != nil {
		err = s.store.SetArchived(id, *in.Archived)
	}
	if err != nil {
		mutationError(w, err)
		return
	}
	view, err := s.sessionViewLocked(rt)
	if err != nil {
		mutationError(w, err)
		return
	}
	writeJSON(w, 200, view)
}

// validSessionCWD accepts only an absolute path to an existing directory, so a session can
// never be pointed at a relative or missing workspace.
func validSessionCWD(raw string) (string, error) {
	cwd := strings.TrimSpace(raw)
	if cwd == "" || !filepath.IsAbs(cwd) {
		return "", errors.New("cwd must be an absolute directory path")
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return "", errors.New("cwd must be an existing directory")
	}
	return filepath.Clean(cwd), nil
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request, id string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		mutationError(w, err)
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !s.mutationAllowed(w, rt) {
		return
	}
	if err := s.store.Delete(id); err != nil {
		mutationError(w, err)
		return
	}
	rt.deleted = true
	rt.recorder = nil
	terminalManager := rt.terminalManager
	processManager := rt.processManager
	rt.terminalManager = nil
	rt.processManager = nil
	if terminalManager != nil {
		terminalManager.CloseAll()
	}
	if processManager != nil {
		processManager.CloseAll()
	}
	if rt.memoryRuntime != nil {
		_ = rt.memoryRuntime.Close()
		rt.memoryRuntime = nil
	}
	close(rt.changed)
	rt.changed = make(chan struct{})
	for _, suffix := range []string{".jsonl", ".runs.json"} {
		if err := os.Remove(filepath.Join(s.eventDir, id+suffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
			mutationError(w, err)
			return
		}
	}
	if err := os.RemoveAll(filepath.Join(s.eventDir, "sections", id)); err != nil {
		mutationError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "session_id": id})
}

type historyResponse struct {
	MessageOffsets    []int               `json:"message_offsets"`
	Session           protocol.Session    `json:"session"`
	Revision          string              `json:"revision"`
	OldestOffset      int                 `json:"oldest_offset"`
	HasMoreBefore     bool                `json:"has_more_before"`
	TotalMessageCount int                 `json:"total_message_count"`
	ActiveMessages    *[]protocol.Message `json:"active_messages,omitempty"`
}

func (s *Server) history(w http.ResponseWriter, r *http.Request, id string) {
	limit, err := intQuery(r, "max_messages", 200, 1, 10000)
	if err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	before, err := intQuery(r, "before_offset", -1, 0, int(^uint(0)>>1))
	if err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	include, err := boolQuery(r, "include_active")
	if err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		mutationError(w, err)
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	view, err := s.sessionViewLocked(rt)
	if err != nil {
		mutationError(w, err)
		return
	}
	if expected := r.URL.Query().Get("expected_revision"); expected != "" && expected != view.Revision {
		mutationError(w, session.ErrRevision)
		return
	}
	total := len(view.Messages)
	if before < 0 {
		before = int(^uint(0) >> 1)
	}
	start, end := total, total
	if snap, snapErr := s.store.HistorySnapshot(id); snapErr == nil {
		for i, offset := range snap.Offsets {
			if offset < before {
				end = i + 1
			}
		}
		if end == total && before < int(^uint(0)>>1) && (len(snap.Offsets) == 0 || snap.Offsets[0] >= before) {
			end = 0
		}
		start = max(0, end-limit)
		view.Messages = view.Messages[start:end]
		oldest := 0
		if start < len(snap.Offsets) {
			oldest = snap.Offsets[start]
		}
		out := historyResponse{MessageOffsets: append([]int{}, snap.Offsets[start:end]...), Session: view, Revision: view.Revision, OldestOffset: oldest, HasMoreBefore: start > 0, TotalMessageCount: total}
		if include != nil && *include {
			active := []protocol.Message{}
			for _, msg := range snap.ActiveMessages {
				active = append(active, protocol.FromAIMessage(msg))
			}
			out.ActiveMessages = &active
		}
		writeJSON(w, 200, out)
		return
	}
	writeJSONError(w, 500, "history unavailable")
	return

}

func (s *Server) mutateHistory(w http.ResponseWriter, r *http.Request, id string, edit bool) {
	var ref protocol.HistoryMessageRef
	var expected, title string
	var replacement *ai.Message
	if edit {
		var in protocol.EditSessionRequest
		if err := decodeJSON(w, r, &in); err != nil {
			return
		}
		if in.ExpectedRevision == "" {
			writeJSONError(w, 400, "expected_revision is required")
			return
		}
		if in.Replacement.Role != protocol.RoleUser {
			writeJSONError(w, 400, "replacement must be a user message")
			return
		}
		for _, block := range in.Replacement.Content {
			if block.Type != protocol.ContentText && block.Type != protocol.ContentImage {
				writeJSONError(w, 400, "replacement must contain text or images")
				return
			}
		}
		converted, err := in.Replacement.ToAIMessage()
		if err != nil {
			writeJSONError(w, 400, err.Error())
			return
		}
		if strings.TrimSpace(converted.TextContent()) == "" && len(converted.Parts) == 0 {
			writeJSONError(w, 400, "replacement content is required")
			return
		}
		replacement = &converted
		ref, expected = in.MessageRef, in.ExpectedRevision
	} else {
		var in protocol.BranchSessionRequest
		if err := decodeJSON(w, r, &in); err != nil {
			return
		}
		ref, expected, title = in.MessageRef, in.ExpectedRevision, in.Title
	}
	if ref.MessageID == "" || (ref.Role != "" && ref.Role != protocol.RoleUser) {
		writeJSONError(w, 400, "message_ref.message_id must identify a user message")
		return
	}
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		mutationError(w, err)
		return
	}
	rt.mu.Lock()
	if !s.mutationAllowed(w, rt) {
		rt.mu.Unlock()
		return
	}
	trajectoryPrefix, err := s.trajectoryHistoryPrefix(rt, ref.MessageID, edit)
	if err != nil {
		rt.mu.Unlock()
		mutationError(w, err)
		return
	}
	resultID, err := s.store.MutateHistory(id, ref.MessageID, expected, title, replacement)
	if err != nil {
		rt.mu.Unlock()
		mutationError(w, err)
		return
	}
	if err := s.applyTrajectoryHistory(rt, resultID, trajectoryPrefix, edit); err != nil {
		rt.mu.Unlock()
		mutationError(w, err)
		return
	}
	if edit {
		// Discard the recorder's old raw offsets before any subsequent save.
		rt.recorder = nil
		meta, _, loadErr := s.store.Load(id)
		if loadErr == nil {
			ag, factoryErr := s.factory(r.Context(), meta.CWD, protocol.ModelRef{Model: meta.Model, Provider: meta.Provider})
			loadErr = factoryErr
			if loadErr == nil {
				ag.WorkingDir, ag.ModelName, ag.Provider = meta.CWD, meta.Model, meta.Provider
				ag.SetSessionID(id)
				var rec *recording.Recorder
				rec, loadErr = recording.Open(s.store, id, ag)
				if loadErr == nil {
					rt.agent, rt.recorder = ag, rec
					s.wireTasks(rt, ag)
				}
			}
		}
		if loadErr != nil {
			rt.mu.Unlock()
			mutationError(w, loadErr)
			return
		}
		view, viewErr := s.sessionViewLocked(rt)
		rt.mu.Unlock()
		if viewErr != nil {
			mutationError(w, viewErr)
			return
		}
		writeJSON(w, 200, view)
		return
	}
	rt.mu.Unlock()
	branch, err := s.loadRuntimeByID(resultID)
	if err != nil {
		mutationError(w, err)
		return
	}
	writeJSON(w, 201, s.sessionView(branch))
}

func sameUserContent(a, b ai.Message) bool {
	return reflect.DeepEqual(a.ContentParts(), b.ContentParts())
}

func shareStatus(meta session.Meta) protocol.ShareStatus {
	return protocol.ShareStatus{ConversationID: meta.ID, Enabled: meta.Shared, Token: meta.ShareToken, CreatedAt: meta.ShareCreatedAt, UpdatedAt: meta.ShareUpdatedAt, RedactToolContent: !meta.Shared || meta.ShareRedactTool}
}

func (s *Server) share(w http.ResponseWriter, r *http.Request, id string) {
	var in protocol.ShareUpdateRequest
	if r.Method == http.MethodPost {
		if err := decodeJSON(w, r, &in); err != nil {
			return
		}
	}
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		mutationError(w, err)
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.deleted {
		mutationError(w, session.ErrNotFound)
		return
	}
	meta, _, err := s.store.Load(id)
	if err != nil {
		mutationError(w, err)
		return
	}
	if r.Method == http.MethodPost {
		token, redact := meta.ShareToken, meta.ShareRedactTool
		if !meta.Shared {
			redact = true
		}
		if in.RedactToolContent != nil {
			redact = *in.RedactToolContent
		}
		if !in.Enabled {
			token = ""
		}
		if in.Enabled && token == "" {
			var bytes [32]byte
			if _, err := rand.Read(bytes[:]); err != nil {
				mutationError(w, err)
				return
			}
			token = hex.EncodeToString(bytes[:])
		}
		if err := s.store.SetShared(id, token, in.Enabled, redact); err != nil {
			mutationError(w, err)
			return
		}
		meta, _, err = s.store.Load(id)
		if err != nil {
			mutationError(w, err)
			return
		}
		if !in.Enabled {
			meta.ShareToken = ""
		}
	}
	writeJSON(w, 200, shareStatus(meta))
}

func (s *Server) publicShare(w http.ResponseWriter, r *http.Request) {
	html := strings.HasPrefix(r.URL.Path, "/share/")
	prefix := "/v1/shares/"
	if html {
		prefix = "/share/"
	}
	token := strings.TrimPrefix(r.URL.Path, prefix)
	if len(token) != 64 || !validID(token) {
		writeJSONError(w, 404, "share not found")
		return
	}
	meta, messages, err := s.store.SharedByToken(token)
	if err != nil {
		writeJSONError(w, 404, "share not found")
		return
	}
	// Use an allowlist projection: no CWD, provider, usage, tasks, or internal metadata.
	out := struct {
		ConversationID string             `json:"conversation_id"`
		Title          string             `json:"title"`
		Messages       []protocol.Message `json:"messages"`
	}{ConversationID: meta.ID, Title: meta.Title, Messages: []protocol.Message{}}
	for _, msg := range messages {
		if msg.Role != "user" && msg.Role != "assistant" && msg.Role != "tool" {
			continue
		}
		if meta.ShareRedactTool && (msg.Role == "tool" || (msg.Role == "user" && !msg.Authored && strings.Contains(msg.Content, "task"))) {
			continue
		}
		full := protocol.FromAIMessage(msg)
		public := protocol.Message{ID: full.ID, Role: full.Role}
		for _, block := range full.Content {
			if block.Type == protocol.ContentText || block.Type == protocol.ContentImage {
				public.Content = append(public.Content, block)
			}
		}
		if !meta.ShareRedactTool {
			public.ToolCalls, public.ToolCallID, public.Name = full.ToolCalls, full.ToolCallID, full.Name
		}
		if len(public.Content) > 0 || len(public.ToolCalls) > 0 {
			out.Messages = append(out.Messages, public)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if html {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		page := template.Must(template.New("share").Parse(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}}</title><style>body{max-width:850px;margin:3rem auto;font:16px system-ui;padding:1rem}pre{white-space:pre-wrap;overflow-wrap:anywhere}article{border-top:1px solid #ddd;padding:1rem 0}</style></head><body><h1>{{.Title}}</h1>{{range .Messages}}<article><h2>{{.Role}}</h2>{{range .Content}}{{if .Text}}<pre>{{.Text}}</pre>{{end}}{{end}}</article>{{end}}</body></html>`))
		_ = page.Execute(w, out)
		return
	}
	writeJSON(w, 200, out)
}

var _ = json.Valid
