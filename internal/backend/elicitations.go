package backend

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/elicit"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// MCP elicitation over /v1.
//
// When an MCP server asks for input during one of its tools in a run started
// with options.mcp_elicitation, the run publishes elicitation.requested and
// waits, like a question, until the client answers through
// POST /v1/sessions/{id}/elicitations/{elicitation_id}, the deadline passes
// (cancel, reason timeout) or the run stops (cancel, reason cancelled); then
// it publishes elicitation.resolved. Runs without the option, and unattended
// runs, decline at once without publishing anything; the decline still shows
// in the tool call's elicitations.
//
// The journal never holds the values of sensitive fields (elicit.Redact):
// only the MCP server receives them, from memory.
//
// SSHManager reuses this flow for the keyboard-interactive prompts of an SSH
// login (source "ssh", server "ssh:<host name>"); the journal then keeps none
// of the answers (elicit.RedactFor), sensitive or not.

type elicitationWaiter struct {
	request    protocol.ElicitationRequest
	ctx        context.Context
	done       chan struct{}
	resolution *protocol.ElicitationResolution
	// content is the accepted answer in full, for the MCP server.
	content map[string]any
}

func newElicitationID() string {
	return "elicit-" + strings.TrimPrefix(newRunID(), "run-")
}

func (s *Server) waitElicitation(ctx context.Context, rt *runtimeSession, runID, toolCallID string, req elicit.Request) elicit.Response {
	cancelled := elicit.Response{Action: elicit.Cancel, Reason: elicit.ReasonCancelled}
	if ctx.Err() != nil {
		return cancelled
	}
	timeout := s.questionWait
	if timeout <= 0 {
		timeout = defaultQuestionTimeout
	}
	fields := req.Fields
	if fields == nil {
		fields = []protocol.ElicitField{}
	}
	waiter := &elicitationWaiter{ctx: ctx, done: make(chan struct{}), request: protocol.ElicitationRequest{
		ElicitationID:   newElicitationID(),
		ToolCallID:      toolCallID,
		RunID:           runID,
		DeadlineAt:      time.Now().Add(timeout).UnixMilli(),
		Source:          req.Source,
		Server:          req.Server,
		Tool:            req.Tool,
		Message:         req.Message,
		Fields:          fields,
		RequestedSchema: req.Schema,
	}}
	id := waiter.request.ElicitationID
	rt.mu.Lock()
	if rt.runID != runID || rt.runDone || rt.deleted {
		rt.mu.Unlock()
		return cancelled
	}
	if err := rt.publishEventLocked(runID, protocol.EventElicitationRequested, waiter.request); err != nil {
		rt.mu.Unlock()
		return cancelled
	}
	if rt.elicitations == nil {
		rt.elicitations = make(map[string]*elicitationWaiter)
	}
	rt.elicitations[id] = waiter
	rt.mu.Unlock()

	timer := time.NewTimer(time.Until(time.UnixMilli(waiter.request.DeadlineAt)))
	defer timer.Stop()
	select {
	case <-waiter.done:
	case <-timer.C:
	case <-ctx.Done():
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	defer delete(rt.elicitations, id)
	if waiter.resolution == nil {
		reason := elicit.ReasonTimeout
		if ctx.Err() != nil {
			reason = elicit.ReasonCancelled
		}
		if err := rt.settleElicitationLocked(waiter, elicit.Cancel, reason, nil); err != nil {
			return elicit.Response{Action: elicit.Cancel, Reason: reason, ID: id}
		}
	}
	return elicit.Response{Action: waiter.resolution.Action, Reason: waiter.resolution.Reason, Content: waiter.content, ID: id}
}

func (rt *runtimeSession) settleElicitationLocked(waiter *elicitationWaiter, action, reason string, content map[string]any) error {
	if waiter.resolution != nil {
		return nil
	}
	req := waiter.request
	result := protocol.ElicitationResolution{ElicitationID: req.ElicitationID, ToolCallID: req.ToolCallID, RunID: req.RunID, Source: req.Source, Server: req.Server, Tool: req.Tool, Message: req.Message, Action: action, Reason: reason}
	result.Content, result.Redacted = elicit.RedactFor(req.Source, req.Fields, content)
	if err := rt.publishEventLocked(req.RunID, protocol.EventElicitationResolved, result); err != nil {
		return err
	}
	waiter.resolution = &result
	waiter.content = content
	close(waiter.done)
	return nil
}

func writeElicitationError(w http.ResponseWriter, status int, msg string, fieldErrors elicit.Errors) {
	body := map[string]any{"version": protocol.Version, "error": msg}
	if named := maps.Clone(fieldErrors); len(named) > 0 {
		delete(named, "")
		if len(named) > 0 {
			body["field_errors"] = named
		}
	}
	writeJSON(w, status, body)
}

func (s *Server) answerElicitation(w http.ResponseWriter, r *http.Request, id, elicitationID string) {
	var in protocol.ElicitationAnswerRequest
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if in.ConversationID != id || in.ElicitationID != elicitationID || in.RunID == "" {
		writeJSONError(w, http.StatusConflict, "elicitation identity mismatch")
		return
	}
	switch in.Action {
	case elicit.Accept:
	case elicit.Decline, elicit.Cancel:
		if in.Content != nil {
			writeJSONError(w, http.StatusBadRequest, "content is only sent with action accept")
			return
		}
	default:
		writeJSONError(w, http.StatusBadRequest, "action must be accept, decline or cancel")
		return
	}
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	defer s.releaseRuntime(rt)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	events, err := rt.allEventsLocked()
	if err != nil {
		mutationError(w, err)
		return
	}
	var request *protocol.ElicitationRequest
	var resolved *protocol.ElicitationResolution
	for i := range events {
		event := &events[i]
		if event.RunID != in.RunID {
			continue
		}
		switch event.Type {
		case protocol.EventElicitationRequested:
			var req protocol.ElicitationRequest
			if json.Unmarshal(event.Payload, &req) == nil && req.ElicitationID == elicitationID {
				request = &req
			}
		case protocol.EventElicitationResolved:
			var res protocol.ElicitationResolution
			if json.Unmarshal(event.Payload, &res) == nil && res.ElicitationID == elicitationID {
				resolved = &res
			}
		}
	}
	if request == nil {
		writeJSONError(w, http.StatusNotFound, "elicitation request not found for this run")
		return
	}
	var content map[string]any
	if in.Action == elicit.Accept {
		answer := in.Content
		if answer == nil {
			answer = map[string]any{}
		}
		var errs elicit.Errors
		if content, errs = elicit.Validate(request.Fields, answer); errs != nil {
			writeElicitationError(w, http.StatusBadRequest, "invalid content: "+errs.Error(), errs)
			return
		}
	}
	if resolved != nil {
		kept, _ := elicit.RedactFor(request.Source, request.Fields, content)
		if resolved.Reason == elicit.ReasonUser && resolved.Action == in.Action && sameContent(resolved.Content, kept) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true})
			return
		}
		writeJSONError(w, http.StatusConflict, "elicitation is already resolved")
		return
	}
	waiter := rt.elicitations[elicitationID]
	if waiter == nil || rt.runID != in.RunID || rt.runDone || rt.deleted {
		writeJSONError(w, http.StatusConflict, "elicitation is no longer pending")
		return
	}
	if waiter.ctx.Err() != nil || time.Now().UnixMilli() >= request.DeadlineAt {
		reason := elicit.ReasonTimeout
		if waiter.ctx.Err() != nil {
			reason = elicit.ReasonCancelled
		}
		if err := rt.settleElicitationLocked(waiter, elicit.Cancel, reason, nil); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSONError(w, http.StatusConflict, "elicitation is no longer pending")
		return
	}
	if err := rt.settleElicitationLocked(waiter, in.Action, elicit.ReasonUser, content); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sameContent compares answers as the journal holds them, where an empty
// answer is absent.
func sameContent(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
