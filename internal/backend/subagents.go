package backend

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// Child text and thinking are journaled in chunks: at most one event per
// childFlushInterval per subagent, or sooner once childFlushBytes are waiting.
const (
	childFlushInterval = 100 * time.Millisecond
	childFlushBytes    = 8 << 10
)

// liveSubagent is what a loaded runtime knows about one subagent beyond its
// journal: counters for the status payload, the stop function of a foreground
// child, and the buffer of text not yet journaled.
type liveSubagent struct {
	runID     string
	running   bool
	startedAt time.Time
	toolCount int
	usage     protocol.Usage
	stop      func()

	mu    sync.Mutex
	kind  string
	buf   strings.Builder
	timer *time.Timer
}

func (rt *runtimeSession) subagent(id, runID string) *liveSubagent {
	rt.subagentMu.Lock()
	defer rt.subagentMu.Unlock()
	if rt.subagents == nil {
		rt.subagents = map[string]*liveSubagent{}
	}
	sub := rt.subagents[id]
	if sub == nil {
		sub = &liveSubagent{runID: runID}
		rt.subagents[id] = sub
	}
	if sub.runID == "" {
		sub.runID = runID
	}
	return sub
}

func (rt *runtimeSession) existingSubagent(id string) *liveSubagent {
	rt.subagentMu.Lock()
	defer rt.subagentMu.Unlock()
	return rt.subagents[id]
}

// subagentStats fills the counters of a status payload.
func (rt *runtimeSession) subagentStats(id string, view *protocol.Subagent) {
	rt.subagentMu.Lock()
	defer rt.subagentMu.Unlock()
	sub := rt.subagents[id]
	if sub == nil {
		return
	}
	view.ToolCount = sub.toolCount
	if sub.usage != (protocol.Usage{}) {
		usage := sub.usage
		view.Usage = &usage
	}
}

func (rt *runtimeSession) addSubagentUsage(id string, usage *protocol.Usage) {
	if usage == nil {
		return
	}
	sub := rt.existingSubagent(id)
	if sub == nil {
		return
	}
	rt.subagentMu.Lock()
	addTrajectoryUsage(&sub.usage, *usage)
	rt.subagentMu.Unlock()
}

func (rt *runtimeSession) setSubagentRunning(id string, running bool, stop func()) {
	rt.subagentMu.Lock()
	defer rt.subagentMu.Unlock()
	if sub := rt.subagents[id]; sub != nil {
		if running && !sub.running {
			sub.startedAt = time.Now().UTC()
		}
		sub.running = running
		sub.stop = stop
	}
}

func (rt *runtimeSession) subagentStartedAt(id string) time.Time {
	rt.subagentMu.Lock()
	defer rt.subagentMu.Unlock()
	if sub := rt.subagents[id]; sub != nil {
		return sub.startedAt
	}
	return time.Time{}
}

// stopSubagent ends one running subagent. It reports whether the subagent is
// known to this runtime and whether a stop was sent.
func (rt *runtimeSession) stopSubagent(id string) (known, stopped bool) {
	rt.subagentMu.Lock()
	sub := rt.subagents[id]
	var stop func()
	running := sub != nil && sub.running
	if running {
		stop = sub.stop
	}
	rt.subagentMu.Unlock()
	if stop != nil {
		stop()
		return true, true
	}
	if running && rt.agent != nil && rt.agent.Tasks().Cancel(id) {
		return true, true
	}
	return sub != nil, false
}

// subagentLive reports whether the subagent has started and not yet ended in
// this runtime. It turns false only after the terminal status is journaled.
func (rt *runtimeSession) subagentLive(id string) bool {
	rt.subagentMu.Lock()
	defer rt.subagentMu.Unlock()
	sub := rt.subagents[id]
	return sub != nil && sub.running
}

func (sub *liveSubagent) appendText(rt *runtimeSession, id, kind, text string) {
	if text == "" {
		return
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.kind != kind {
		sub.flushLocked(rt, id)
		sub.kind = kind
	}
	sub.buf.WriteString(text)
	if sub.buf.Len() >= childFlushBytes {
		sub.flushLocked(rt, id)
		return
	}
	if sub.timer == nil {
		sub.timer = time.AfterFunc(childFlushInterval, func() { sub.flush(rt, id) })
	}
}

func (sub *liveSubagent) flush(rt *runtimeSession, id string) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	sub.flushLocked(rt, id)
}

func (sub *liveSubagent) flushLocked(rt *runtimeSession, id string) {
	if sub.timer != nil {
		sub.timer.Stop()
		sub.timer = nil
	}
	if sub.buf.Len() == 0 {
		return
	}
	text := sub.buf.String()
	sub.buf.Reset()
	_, _ = rt.publishTrajectory(sub.runID, sub.kind, protocol.ChildTextEvent{TaskID: id, Text: text}, "")
}

// publishChild journals a child tool or steer event after any text it follows.
func (sub *liveSubagent) publishChild(rt *runtimeSession, id, kind string, payload any) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	sub.flushLocked(rt, id)
	_, _ = rt.publishTrajectory(sub.runID, kind, payload, "")
}

func (rt *runtimeSession) flushSubagent(id string) {
	if sub := rt.existingSubagent(id); sub != nil {
		sub.flush(rt, id)
	}
}

func (rt *runtimeSession) trajectoryChildEventsForRun(id, runID string) agent.Events {
	sub := rt.subagent(id, runID)
	return agent.Events{
		OnText:  func(s string) { sub.appendText(rt, id, protocol.EventChildText, s) },
		OnThink: func(s string) { sub.appendText(rt, id, protocol.EventChildThinking, s) },
		OnSteer: func(s string) {
			sub.publishChild(rt, id, protocol.EventChildSteer, protocol.ChildTextEvent{TaskID: id, Text: s})
		},
		OnToolStart: func(call, name, args string) {
			rt.subagentMu.Lock()
			sub.toolCount++
			rt.subagentMu.Unlock()
			sub.publishChild(rt, id, protocol.EventChildToolStart, map[string]any{"task_id": id, "id": call, "name": name, "arguments": args})
		},
		OnToolResult: func(call, name string, result tools.Result) {
			payload := map[string]any{"task_id": id, "id": call, "name": name, "output": result.Text, "failed": result.Failed, "cancelled": result.Cancelled}
			if result.ExitCode != nil {
				payload["exit_code"] = *result.ExitCode
			}
			if result.TimedOut {
				payload["timed_out"] = true
			}
			if ms := result.DurationMs(); ms > 0 {
				payload["duration_ms"] = ms
			}
			sub.publishChild(rt, id, protocol.EventChildToolEnd, payload)
		},
	}
}

// subagentRecord folds what the journal says about one subagent.
type subagentRecord struct {
	view     protocol.Subagent
	usage    protocol.Usage
	tools    int
	seen     bool
	terminal bool
}

func isSubagentStatusEvent(typ string) bool {
	switch typ {
	case protocol.EventSubagentStarted, protocol.EventSubagentUpdate, protocol.EventSubagentCompleted, protocol.EventSubagentFailed:
		return true
	}
	return false
}

func isChildEvent(typ string) bool {
	switch typ {
	case protocol.EventChildText, protocol.EventChildThinking, protocol.EventChildSteer, protocol.EventChildToolStart, protocol.EventChildToolEnd:
		return true
	}
	return false
}

// subagentEventID returns the subagent an event belongs to, or "".
func subagentEventID(e protocol.Event) string {
	if isSubagentStatusEvent(e.Type) {
		var payload protocol.SubagentEvent
		if json.Unmarshal(e.Payload, &payload) == nil {
			return payload.Subagent.ID
		}
		return ""
	}
	if isChildEvent(e.Type) || e.Type == "trajectory.request.completed" {
		var payload struct {
			TaskID string `json:"task_id"`
		}
		if json.Unmarshal(e.Payload, &payload) == nil {
			return payload.TaskID
		}
	}
	return ""
}

func mergeSubagent(dst *protocol.Subagent, src protocol.Subagent) {
	dst.ID = src.ID
	if src.ParentID != "" {
		dst.ParentID = src.ParentID
	}
	if src.Description != "" {
		dst.Description = src.Description
	}
	if src.Status != "" {
		dst.Status = src.Status
	}
	if src.Model.Model != "" {
		dst.Model = src.Model
	}
	if src.Attempt != 0 {
		dst.Attempt = src.Attempt
	}
	if src.Report != "" {
		dst.Report = src.Report
	}
	if src.Error != "" {
		dst.Error = src.Error
	}
	if src.ParentToolCallID != "" {
		dst.ParentToolCallID = src.ParentToolCallID
	}
	if src.Background {
		dst.Background = true
	}
	if src.DurationMS != 0 {
		dst.DurationMS = src.DurationMS
	}
	if src.StartedAt != nil {
		dst.StartedAt = src.StartedAt
	}
	if src.UpdatedAt != nil {
		dst.UpdatedAt = src.UpdatedAt
	}
	if src.EndedAt != nil {
		dst.EndedAt = src.EndedAt
	}
}

// foldSubagents reads every subagent out of a session journal plus the
// stored background tasks, in start order.
func foldSubagents(events []protocol.Event, stored []session.Task) []protocol.Subagent {
	records := map[string]*subagentRecord{}
	var order []string
	get := func(id string) *subagentRecord {
		rec := records[id]
		if rec == nil {
			rec = &subagentRecord{view: protocol.Subagent{ID: id}}
			records[id] = rec
			order = append(order, id)
		}
		return rec
	}
	for _, e := range events {
		switch {
		case isSubagentStatusEvent(e.Type):
			var payload protocol.SubagentEvent
			if json.Unmarshal(e.Payload, &payload) != nil || payload.Subagent.ID == "" {
				continue
			}
			rec := get(payload.Subagent.ID)
			rec.seen = true
			mergeSubagent(&rec.view, payload.Subagent)
			if payload.Subagent.Status == string(agent.TaskRunning) {
				rec.view.EndedAt, rec.view.DurationMS, rec.terminal = nil, 0, false
				rec.view.Error, rec.view.Report = "", ""
			} else if e.Type == protocol.EventSubagentCompleted || e.Type == protocol.EventSubagentFailed {
				rec.terminal = true
			}
		case e.Type == protocol.EventChildToolStart:
			if id := subagentEventID(e); id != "" && records[id] != nil {
				records[id].tools++
			}
		case e.Type == "trajectory.request.completed":
			p := trajectoryRequestPayload(e)
			id := trajectoryString(p, "task_id")
			if id == "" || records[id] == nil {
				continue
			}
			var usage struct {
				Usage *protocol.Usage `json:"usage"`
			}
			if json.Unmarshal(e.Payload, &usage) == nil && usage.Usage != nil {
				addTrajectoryUsage(&records[id].usage, *usage.Usage)
			}
		}
	}
	for _, task := range stored {
		rec := get(task.ID)
		view := storedSubagentView(task)
		view.Background = true
		if rec.seen {
			// The journal knows the live details; the store adds what it alone keeps.
			if rec.view.Description == "" {
				rec.view.Description = view.Description
			}
			if rec.view.ParentToolCallID == "" {
				rec.view.ParentToolCallID = view.ParentToolCallID
			}
			rec.view.Background = true
			continue
		}
		rec.view = view
		rec.seen = true
	}
	out := make([]protocol.Subagent, 0, len(order))
	for _, id := range order {
		rec := records[id]
		if !rec.seen {
			continue
		}
		view := rec.view
		if rec.tools > view.ToolCount {
			view.ToolCount = rec.tools
		}
		if rec.usage != (protocol.Usage{}) {
			usage := rec.usage
			view.Usage = &usage
		}
		if view.DurationMS == 0 && view.StartedAt != nil && view.EndedAt != nil {
			view.DurationMS = view.EndedAt.Sub(*view.StartedAt).Milliseconds()
		}
		out = append(out, view)
	}
	started := func(v protocol.Subagent) time.Time {
		if v.StartedAt == nil {
			return time.Time{}
		}
		return *v.StartedAt
	}
	sort.SliceStable(out, func(i, j int) bool { return started(out[i]).Before(started(out[j])) })
	return out
}

// childTranscript rebuilds a subagent's transcript from its child events:
// its prompt, then assistant messages with text, thinking and tool calls,
// each tool call followed by its result.
func childTranscript(events []protocol.Event, id, prompt string, model protocol.ModelRef) []protocol.Message {
	messages := []protocol.Message{}
	if prompt != "" {
		messages = append(messages, protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: prompt}}})
	}
	current := -1
	assistant := func(at time.Time) *protocol.Message {
		if current < 0 {
			created := at
			messages = append(messages, protocol.Message{ID: id + "-" + strconv.Itoa(len(messages)), Role: protocol.RoleAssistant, Model: model.Model, Provider: model.Provider, CreatedAt: &created})
			current = len(messages) - 1
		}
		return &messages[current]
	}
	appendBlock := func(msg *protocol.Message, typ, text string) {
		if n := len(msg.Content); n > 0 && msg.Content[n-1].Type == typ {
			msg.Content[n-1].Text += text
			return
		}
		msg.Content = append(msg.Content, protocol.ContentBlock{Type: typ, Text: text})
	}
	for _, e := range events {
		if !isChildEvent(e.Type) || subagentEventID(e) != id {
			continue
		}
		p := trajectoryRequestPayload(e)
		switch e.Type {
		case protocol.EventChildText:
			appendBlock(assistant(e.CreatedAt), protocol.ContentText, trajectoryString(p, "text"))
		case protocol.EventChildThinking:
			msg := assistant(e.CreatedAt)
			// Thinking that follows text starts the next model reply.
			if n := len(msg.Content); n > 0 && msg.Content[n-1].Type == protocol.ContentText || len(msg.ToolCalls) > 0 {
				current = -1
				msg = assistant(e.CreatedAt)
			}
			appendBlock(msg, protocol.ContentThinking, trajectoryString(p, "text"))
		case protocol.EventChildSteer:
			current = -1
			messages = append(messages, protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: trajectoryString(p, "text")}}})
		case protocol.EventChildToolStart:
			msg := assistant(e.CreatedAt)
			args := json.RawMessage(trajectoryString(p, "arguments"))
			if !json.Valid(args) {
				args = json.RawMessage("{}")
			}
			msg.ToolCalls = append(msg.ToolCalls, protocol.ToolCall{ID: trajectoryString(p, "id"), Name: trajectoryString(p, "name"), Arguments: args})
		case protocol.EventChildToolEnd:
			stop := ""
			if p["cancelled"] == true {
				stop = string(ai.StopReasonCancelled)
			} else if p["failed"] == true {
				stop = string(ai.StopReasonError)
			}
			created := e.CreatedAt
			messages = append(messages, protocol.Message{Role: protocol.RoleTool, ToolCallID: trajectoryString(p, "id"), Name: trajectoryString(p, "name"), Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: trajectoryString(p, "output")}}, StopReason: stop, CreatedAt: &created})
			current = -1
		}
	}
	return messages
}

// subagentPrompt finds the prompt the parent gave a subagent in its tool call.
func subagentPromptFromCall(events []protocol.Event, callID string) string {
	if callID == "" {
		return ""
	}
	for _, e := range events {
		if e.Type != protocol.EventToolCall {
			continue
		}
		var payload protocol.ToolCallEvent
		if json.Unmarshal(e.Payload, &payload) != nil || payload.ToolCall.ID != callID {
			continue
		}
		var args struct {
			Prompt string `json:"prompt"`
		}
		_ = json.Unmarshal(payload.ToolCall.Arguments, &args)
		return args.Prompt
	}
	return ""
}

func storedTranscriptMessages(messages []ai.Message) []protocol.Message {
	out := []protocol.Message{}
	for _, message := range messages {
		if message.Role == protocol.RoleSystem || message.Role == protocol.RoleDeveloper {
			continue
		}
		out = append(out, protocol.FromAIMessage(message))
	}
	return out
}

// loadedRuntime returns the session's runtime only when it is already loaded.
func (s *Server) loadedRuntime(id string) *runtimeSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt := s.sessions[id]
	if rt != nil {
		rt.leases.Add(1)
	}
	return rt
}

func (s *Server) sessionSubagents(id string) ([]protocol.Event, []protocol.Subagent, *runtimeSession, error) {
	events, err := s.trajectorySnapshot(id)
	if err != nil {
		return nil, nil, nil, err
	}
	stored, err := s.store.LoadTasks(id)
	if err != nil {
		return nil, nil, nil, err
	}
	list := foldSubagents(events, stored)
	rt := s.loadedRuntime(id)
	for i := range list {
		// A subagent only runs inside a loaded runtime; one the journal still
		// shows running was cut off by a restart.
		if list[i].Status == string(agent.TaskRunning) && (rt == nil || !rt.subagentLive(list[i].ID)) {
			list[i].Status = string(agent.TaskCancelled)
		}
	}
	return events, list, rt, nil
}

func writeSubagentError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, session.ErrNotFound) {
		status = http.StatusNotFound
	}
	writeJSONError(w, status, err.Error())
}

func lastEventSeq(events []protocol.Event) int64 {
	if len(events) == 0 {
		return 0
	}
	return events[len(events)-1].Seq
}

// handleSubagents serves /v1/sessions/{id}/subagents[/{task}[/events|/cancel]].
func (s *Server) handleSubagents(w http.ResponseWriter, r *http.Request, id string, rest []string) {
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		events, list, rt, err := s.sessionSubagents(id)
		if rt != nil {
			defer s.releaseRuntime(rt)
		}
		if err != nil {
			writeSubagentError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, protocol.SubagentList{Subagents: list, LastSeq: lastEventSeq(events)})
	case len(rest) == 1 && rest[0] != "" && r.Method == http.MethodGet:
		s.subagentTranscript(w, id, rest[0])
	case len(rest) == 2 && rest[0] != "" && rest[1] == "events" && r.Method == http.MethodGet:
		s.subagentEvents(w, r, id, rest[0])
	case len(rest) == 2 && rest[0] != "" && rest[1] == "cancel" && r.Method == http.MethodPost:
		s.cancelSubagent(w, id, rest[0])
	default:
		writeJSONError(w, http.StatusNotFound, "route not found")
	}
}

func (s *Server) subagentTranscript(w http.ResponseWriter, id, task string) {
	events, list, rt, err := s.sessionSubagents(id)
	if rt != nil {
		defer s.releaseRuntime(rt)
	}
	if err != nil {
		writeSubagentError(w, err)
		return
	}
	var view *protocol.Subagent
	for i := range list {
		if list[i].ID == task {
			view = &list[i]
		}
	}
	if view == nil {
		writeJSONError(w, http.StatusNotFound, "subagent not found")
		return
	}
	live := rt != nil && rt.subagentLive(task)
	out := protocol.SubagentTranscript{Subagent: *view, LastSeq: lastEventSeq(events), Live: live}
	if !live {
		// A finished background subagent keeps its exact transcript.
		stored, loadErr := s.store.SubagentTranscript(id, task)
		if loadErr != nil {
			writeJSONError(w, http.StatusInternalServerError, loadErr.Error())
			return
		}
		if len(stored) > 0 {
			out.Messages = storedTranscriptMessages(stored)
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	prompt := subagentPromptFromCall(events, view.ParentToolCallID)
	if prompt == "" && view.Background {
		if tasks, loadErr := s.store.LoadTasks(id); loadErr == nil {
			for _, t := range tasks {
				if t.ID == task {
					prompt = t.Prompt
				}
			}
		}
	}
	out.Messages = childTranscript(events, task, prompt, view.Model)
	writeJSON(w, http.StatusOK, out)
}

// subagentEvents streams one subagent's status and child events after
// after_seq, and ends once the subagent is no longer running.
func (s *Server) subagentEvents(w http.ResponseWriter, r *http.Request, id, task string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	defer s.releaseRuntime(rt)
	after, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		// Read liveness first so events published before it settled are replayed.
		live := rt.subagentLive(task)
		rt.mu.Lock()
		if rt.deleted {
			rt.mu.Unlock()
			return
		}
		replay, err := rt.eventsAfterLocked(after)
		changed := rt.changed
		rt.mu.Unlock()
		if err != nil {
			return
		}
		for _, e := range replay {
			after = e.Seq
			if subagentEventID(e) != task || e.Type == "trajectory.request.completed" {
				continue
			}
			if err := writeSSE(w, e); err != nil {
				return
			}
		}
		flusher.Flush()
		if len(replay) > 0 {
			continue
		}
		if !live {
			return
		}
		select {
		case <-changed:
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) cancelSubagent(w http.ResponseWriter, id, task string) {
	rt := s.loadedRuntime(id)
	if rt == nil {
		if _, _, err := s.store.Load(id); err != nil {
			writeSubagentError(w, err)
			return
		}
		writeJSONError(w, http.StatusConflict, "subagent is not running")
		return
	}
	defer s.releaseRuntime(rt)
	known, stopped := rt.stopSubagent(task)
	switch {
	case stopped:
		writeJSON(w, http.StatusAccepted, map[string]any{"id": task, "cancelled": true})
	case known:
		writeJSONError(w, http.StatusConflict, "subagent is not running")
	default:
		writeJSONError(w, http.StatusNotFound, "subagent not found")
	}
}
