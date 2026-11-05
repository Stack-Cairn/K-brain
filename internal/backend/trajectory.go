package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

type trajectoryRun struct {
	ID     string           `json:"runId"`
	Events []protocol.Event `json:"-"`
	Usage  protocol.Usage   `json:"usage"`
}

type trajectoryWindow struct {
	Version              string           `json:"version"`
	ConversationID       string           `json:"conversationId"`
	EventsJSON           string           `json:"eventsJson"`
	RawEvents            []protocol.Event `json:"rawEvents"`
	OldestSegmentIndex   int              `json:"oldestSegmentIndex"`
	ReturnedSegmentCount int              `json:"returnedSegmentCount"`
	TotalSegmentCount    int              `json:"totalSegmentCount"`
	HasMoreBefore        bool             `json:"hasMoreBefore"`
	Truncated            bool             `json:"truncated"`
	Redacted             bool             `json:"redacted"`
	LastSeq              int64            `json:"lastSeq"`
}

// Cold reads do not instantiate an agent or require a working provider.
func (s *Server) trajectorySnapshot(id string) ([]protocol.Event, error) {
	s.mu.Lock()
	rt := s.sessions[id]
	if rt != nil {
		rt.leases.Add(1)
		s.mu.Unlock()
		rt.mu.Lock()
		defer s.releaseRuntime(rt)
		defer rt.mu.Unlock()
		if rt.deleted {
			return nil, session.ErrNotFound
		}
		return rt.allEventsLocked()
	}
	// Keep a newly created runtime from publishing while the cold journal is read.
	defer s.mu.Unlock()
	if _, _, err := s.store.Load(id); err != nil {
		return nil, err
	}
	cold := &runtimeSession{id: id, eventDir: s.eventDir}
	return cold.journalEventsAfter(0, 0)
}

func trajectoryRuns(events []protocol.Event) ([]trajectoryRun, bool) {
	runs := []trajectoryRun{}
	indexes := map[string]int{}
	var previous int64
	truncated := false
	for _, e := range events {
		if e.Seq != previous+1 && e.Type != "trajectory.history.rebased" {
			truncated = true
		}
		previous = e.Seq
		i, ok := indexes[e.RunID]
		if !ok {
			i = len(runs)
			indexes[e.RunID] = i
			runs = append(runs, trajectoryRun{ID: e.RunID})
		}
		runs[i].Events = append(runs[i].Events, e)
		if e.Type == protocol.EventUsage {
			var u protocol.Usage
			if json.Unmarshal(e.Payload, &u) == nil {
				addTrajectoryUsage(&runs[i].Usage, u)
			}
		}
	}
	return runs, truncated
}

func addTrajectoryUsage(total *protocol.Usage, u protocol.Usage) {
	total.InputTokens += u.InputTokens
	total.OutputTokens += u.OutputTokens
	total.CachedTokens += u.CachedTokens
	total.CacheWriteTokens += u.CacheWriteTokens
}

func trajectoryInteger(r *http.Request, key string, fallback, min int) (int, error) {
	if !r.URL.Query().Has(key) {
		return fallback, nil
	}
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || n < min {
		return 0, fmt.Errorf("%s must be an integer >= %d", key, min)
	}
	return n, nil
}

func (s *Server) trajectory(w http.ResponseWriter, r *http.Request, id string, stats bool) {
	w.Header().Set("Cache-Control", "no-store")
	limit, err := trajectoryInteger(r, "max_segments", 8, 1)
	if err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	if limit > 64 {
		limit = 64
	}
	before, err := trajectoryInteger(r, "before_segment_index", int(^uint(0)>>1), 0)
	if err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	redact := false
	if r.URL.Query().Has("redact_tool_content") {
		redact, err = strconv.ParseBool(r.URL.Query().Get("redact_tool_content"))
		if err != nil {
			writeJSONError(w, 400, "redact_tool_content must be a boolean")
			return
		}
	}
	events, err := s.trajectorySnapshot(id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, session.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeJSONError(w, status, err.Error())
		return
	}
	runs, truncated := trajectoryRuns(events)
	var lastSeq int64
	if len(events) > 0 {
		lastSeq = events[len(events)-1].Seq
	}
	if stats {
		usage := protocol.Usage{}
		tools, failures := 0, 0
		var first, last *time.Time
		if len(events) > 0 {
			first = &events[0].CreatedAt
			last = &events[len(events)-1].CreatedAt
		}
		for _, run := range runs {
			addTrajectoryUsage(&usage, run.Usage)
		}
		for _, e := range events {
			if e.Type == protocol.EventToolCall {
				tools++
			}
			if e.Type == protocol.EventRunFailed {
				failures++
			}
			if e.Type == protocol.EventToolResult {
				var p protocol.ToolResultEvent
				_ = json.Unmarshal(e.Payload, &p)
				if p.ToolResult.Failed {
					failures++
				}
			}
		}
		writeJSON(w, 200, map[string]any{"version": protocol.Version, "conversationId": id, "eventCount": len(events), "runCount": len(runs), "toolCallCount": tools, "errorCount": failures, "usage": usage, "runs": runs, "firstEventAt": first, "lastEventAt": last, "lastSeq": lastSeq, "truncated": truncated})
		return
	}
	end := min(before, len(runs))
	start := max(0, end-limit)
	raw := []protocol.Event{}
	projected := []map[string]any{}
	for i := start; i < end; i++ {
		page := runs[i].Events
		if redact {
			page = redactTrajectoryEvents(page)
		}
		raw = append(raw, page...)
		projected = append(projected, projectTrajectoryRun(page, i+1)...)
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, trajectoryWindow{Version: protocol.Version, ConversationID: id, EventsJSON: string(encoded), RawEvents: raw, OldestSegmentIndex: start, ReturnedSegmentCount: end - start, TotalSegmentCount: len(runs), HasMoreBefore: start > 0, Truncated: truncated, Redacted: redact, LastSeq: lastSeq})
}

func redactTrajectoryEvents(events []protocol.Event) []protocol.Event {
	out := append([]protocol.Event(nil), events...)
	for i, e := range out {
		var p map[string]any
		if json.Unmarshal(e.Payload, &p) != nil {
			continue
		}
		switch e.Type {
		case "trajectory.child.tool_start":
			p["arguments"] = "[redacted]"
		case "trajectory.child.tool_end":
			p["output"] = "[redacted]"
		case protocol.EventChildText, protocol.EventChildThinking, protocol.EventChildSteer:
			p["text"] = "[redacted]"
		case protocol.EventToolCall:
			if call, ok := p["tool_call"].(map[string]any); ok {
				call["arguments"] = map[string]any{"redacted": true}
			}
		case protocol.EventToolResult:
			if result, ok := p["tool_result"].(map[string]any); ok {
				result["output"] = "[redacted]"
				delete(result, "content")
			}
		case protocol.EventToolStatus:
			p["message"] = "[redacted]"
		case protocol.EventPermissionRequest:
			p["command"] = "[redacted]"
			p["description"] = "[redacted]"
			p["rule"] = "[redacted]"
			delete(p, "suggestion")
		case protocol.EventPermissionAutoResolved:
			p["command"] = "[redacted]"
			p["rule"] = "[redacted]"
		case protocol.EventPermissionResult:
			p["reason"] = "[redacted]"
		case protocol.EventAssistantMessage:
			// Assistant messages can embed the same tool arguments as tool.call.
			var msg protocol.Message
			if json.Unmarshal(e.Payload, &msg) == nil {
				for j := range msg.ToolCalls {
					msg.ToolCalls[j].Arguments = json.RawMessage(`{"redacted":true}`)
				}
				for j := range msg.Content {
					if msg.Content[j].ToolCall != nil {
						msg.Content[j].ToolCall.Arguments = json.RawMessage(`{"redacted":true}`)
					}
					if msg.Content[j].ToolResult != nil {
						msg.Content[j].ToolResult.Output = "[redacted]"
						msg.Content[j].ToolResult.Content = nil
					}
				}
				out[i].Payload, _ = json.Marshal(msg)
			}
			continue
		default:
			continue
		}
		out[i].Payload, _ = json.Marshal(p)
	}
	return out
}

func projectTrajectoryRun(events []protocol.Event, turn int) []map[string]any {
	out := []map[string]any{}
	step, completedStep := 1, 0
	firstToken := false
	pending := false
	toolSteps := map[string]int{}
	requestSteps := map[string]int{}
	toolRuns := map[string][]string{}
	linked := map[string]bool{}
	for _, event := range events {
		if event.Type != "trajectory.request.started" {
			continue
		}
		p := trajectoryRequestPayload(event)
		child, call := trajectoryString(p, "task_id"), trajectoryString(p, "parent_tool_call_id")
		if child != "" && call != "" && !linked[child] {
			toolRuns[call] = append(toolRuns[call], child)
			linked[child] = true
		}
	}
	observed := false
	previousHeader := ""
	emit := func(e protocol.Event, k string, fields map[string]any) {
		row := map[string]any{"k": k, "t": turn, "at": e.CreatedAt.UnixMilli()}
		for k, v := range fields {
			row[k] = v
		}
		out = append(out, row)
	}
	for _, e := range events {
		if e.Type == "trajectory.user.identity" {
			p := trajectoryRequestPayload(e)
			for i := range out {
				if out[i]["k"] == "user" {
					out[i]["id"] = p["message_id"]
				}
			}
			continue
		}
		switch e.Type {
		case "trajectory.request.started":
			p := trajectoryRequestPayload(e)
			if trajectoryString(p, "task_id") != "" {
				continue
			}
			observed = true
			if userID := trajectoryString(p, "user_message_id"); userID != "" {
				for i := range out {
					if out[i]["k"] == "user" {
						out[i]["id"] = userID
					}
				}
			}
			requestID := trajectoryString(p, "request_id")
			requestSteps[requestID] = step
			refs, _ := p["sections"].([]any)
			encoded, _ := json.Marshal(refs)
			header := sectionDigest(string(encoded))
			change := "initial"
			if previousHeader != "" {
				change = "system-and-tools"
				if previousHeader == header {
					change = "none"
				}
			}
			emit(e, "header", map[string]any{"v": 2, "hid": header, "sec": refs, "ch": change, "prev": previousHeader})
			previousHeader = header
			emit(e, "step_start", map[string]any{"s": step, "hid": header})
		case "trajectory.transport", "trajectory.retry", "trajectory.failover":
			p := trajectoryRequestPayload(e)
			if trajectoryString(p, "task_id") != "" {
				continue
			}
			owner := requestSteps[trajectoryString(p, "request_id")]
			if owner == 0 {
				continue
			}
			if e.Type == "trajectory.failover" {
				emit(e, "failover", map[string]any{"s": owner, "n": p["attempt"], "from": p["from"], "to": p["to"], "ti": p["target_index"], "err": p["error"]})
			} else if e.Type == "trajectory.transport" {
				emit(e, "transport", map[string]any{"s": owner, "p": p["provider"], "o": p["origin"], "hn": p["header_names"]})
			} else {
				emit(e, "retry", map[string]any{"s": owner, "p": p["provider"], "n": p["attempt"], "max": p["max"], "delay": p["delay_ms"], "err": p["error"]})
			}
		case "trajectory.request.completed":
			p := trajectoryRequestPayload(e)
			if trajectoryString(p, "task_id") != "" {
				continue
			}
			owner := requestSteps[trajectoryString(p, "request_id")]
			if owner == 0 {
				continue
			}
			var u protocol.Usage
			data, _ := json.Marshal(p["usage"])
			_ = json.Unmarshal(data, &u)
			emit(e, "step_end", map[string]any{"s": owner, "st": p["status"], "err": p["error"], "p": p["provider"], "m": p["model"], "sr": p["stop_reason"], "u": map[string]int{"input": max(0, u.InputTokens-u.CachedTokens-u.CacheWriteTokens), "output": u.OutputTokens, "cacheRead": u.CachedTokens, "cacheWrite": u.CacheWriteTokens, "totalTokens": u.InputTokens + u.OutputTokens}})
			completedStep = owner
			step = owner + 1
			firstToken = false
			pending = false
		case "compaction.started":
			emit(e, "compaction_start", map[string]any{"t": nil})
		case "compaction.completed":
			var p struct {
				Status string `json:"status"`
				Error  string `json:"error"`
			}
			_ = json.Unmarshal(e.Payload, &p)
			status := "complete"
			if p.Status == "failed" {
				status = "error"
			}
			emit(e, "compaction_end", map[string]any{"t": nil, "st": status, "err": p.Error})
		case protocol.EventUserMessage:
			var p protocol.Message
			_ = json.Unmarshal(e.Payload, &p)
			text := ""
			for _, b := range p.Content {
				if b.Type == protocol.ContentText {
					text += b.Text
				}
			}
			fields := map[string]any{"tx": text}
			if p.ID != "" {
				fields["id"] = p.ID
			}
			emit(e, "user", fields)
		case protocol.EventTextDelta, protocol.EventThinkingDelta:
			pending = true
			if !firstToken {
				emit(e, "first_token", map[string]any{"s": step})
				firstToken = true
			}
		case protocol.EventUsage:
			if observed {
				continue
			}
			var u protocol.Usage
			_ = json.Unmarshal(e.Payload, &u)
			// LiveAgent input excludes cache reads/writes; protocol input includes them.
			emit(e, "step_end", map[string]any{"s": step, "st": "complete", "u": map[string]int{"input": max(0, u.InputTokens-u.CachedTokens-u.CacheWriteTokens), "output": u.OutputTokens, "cacheRead": u.CachedTokens, "cacheWrite": u.CacheWriteTokens, "totalTokens": u.InputTokens + u.OutputTokens}})
			completedStep = step
			step++
			firstToken = false
			pending = false
		case protocol.EventAssistantMessage:
			var p protocol.Message
			_ = json.Unmarshal(e.Payload, &p)
			for i := len(out) - 1; i >= 0; i-- {
				if out[i]["k"] == "step_end" {
					if p.Provider != "" {
						out[i]["p"] = p.Provider
					}
					if p.Model != "" {
						out[i]["m"] = p.Model
					}
					if p.StopReason != "" {
						out[i]["sr"] = p.StopReason
					}
					break
				}
			}
		case protocol.EventToolCall:
			var p protocol.ToolCallEvent
			_ = json.Unmarshal(e.Payload, &p)
			owner := max(1, completedStep)
			toolSteps[p.ToolCall.ID] = owner

			emit(e, "tool_start", map[string]any{"s": owner, "id": p.ToolCall.ID, "n": p.ToolCall.Name, "a": string(p.ToolCall.Arguments)})
		case protocol.EventToolResult:
			var p protocol.ToolResultEvent
			_ = json.Unmarshal(e.Payload, &p)
			emit(e, "tool_end", map[string]any{"s": max(1, toolSteps[p.ToolResult.ID]), "id": p.ToolResult.ID, "err": p.ToolResult.Failed || p.ToolResult.Cancelled, "sum": p.ToolResult.Output, "run": toolRuns[p.ToolResult.ID]})

		case protocol.EventRunCompleted, protocol.EventRunFailed, protocol.EventRunCancelled:
			var p protocol.RunTerminal
			_ = json.Unmarshal(e.Payload, &p)
			status := "complete"
			if e.Type == protocol.EventRunFailed {
				status = "error"
			}
			if e.Type == protocol.EventRunCancelled {
				status = "aborted"
			}
			if pending || (completedStep == 0 && status != "complete") {
				emit(e, "step_end", map[string]any{"s": step, "st": status, "err": p.Error})
			} else if status != "complete" {
				// Usage callbacks also fire when the provider request fails.
				for i := len(out) - 1; i >= 0; i-- {
					if out[i]["k"] == "step_end" {
						out[i]["st"], out[i]["err"] = status, p.Error
						break
					}
				}
			}
			emit(e, "turn_end", map[string]any{"st": status, "err": p.Error})
		}
	}
	return out
}
