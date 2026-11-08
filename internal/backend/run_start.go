package backend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const errSessionBusy = "session already has a running turn"

// replayRunLocked answers a request whose client_request_id was already accepted: the
// original run when the request matches it, an error when it does not. found is false
// for a new request id.
func (s *Server) replayRunLocked(rt *runtimeSession, in protocol.PromptRequest) (protocol.RunAccepted, bool, error) {
	old, ok := rt.runs[in.ClientRequestID]
	if !ok {
		return protocol.RunAccepted{}, false, nil
	}
	matches := old.Kind != "compact" && old.RequestHash != "" && old.RequestHash == hashPrompt(in)
	if !matches && old.Kind != "compact" && rt.agent != nil {
		options, err := normalizeRunOptions(in.Options, rt.agent.WorkingDir, rt.agent.KnownTools())
		if err == nil {
			normalized := in
			normalized.Options = &options
			matches = old.PromptHash == hashPrompt(normalized)
		}
	}
	if !matches {
		return protocol.RunAccepted{}, true, errors.New("client_request_id was already used with a different prompt or options")
	}
	return protocol.RunAccepted{Version: protocol.Version, ConversationID: rt.id, RunID: old.RunID, AcceptedSeq: old.AcceptedSeq}, true, nil
}

// startCanonicalRun serializes acceptance and starts the shared session executor.
// ctx owns the run lifetime; HTTP callers deliberately detach from the connection.
func (s *Server) startCanonicalRun(ctx context.Context, rt *runtimeSession, in protocol.PromptRequest) (protocol.RunAccepted, int, error) {
	return s.startCanonicalRunWith(ctx, rt, in, nil)
}

// runAdmit runs under rt.mu once a new run has passed every check and is about to be
// accepted, so state tied to the run changes atomically with its acceptance. It may add
// request-only text to in.Snapshot. An error rejects the run; the returned rollback, if
// any, undoes the change when persisting the acceptance fails afterwards.
type runAdmit func(in *protocol.PromptRequest) (rollback func(), status int, err error)

func (s *Server) startCanonicalRunWith(ctx context.Context, rt *runtimeSession, in protocol.PromptRequest, admit runAdmit) (protocol.RunAccepted, int, error) {
	fail := func(status int, message string) (protocol.RunAccepted, int, error) {
		return protocol.RunAccepted{}, status, errors.New(message)
	}
	if err := ctx.Err(); err != nil {
		return protocol.RunAccepted{}, http.StatusConflict, err
	}
	if strings.TrimSpace(in.ClientRequestID) == "" {
		return fail(400, "client_request_id is required")
	}
	if in.ConversationID != "" && in.ConversationID != rt.id {
		return fail(400, "conversation_id does not match session")
	}
	if in.HookPolicy != "" && in.HookPolicy != "backend" {
		return fail(400, "hook_policy must be backend")
	}
	if len(in.HookScopeID) > 256 {
		return fail(400, "hook_scope_id is too long")
	}
	if in.StopRequested {
		return fail(409, "run was stopped before acceptance")
	}
	if strings.TrimSpace(in.Prompt) == "" && len(in.Content) == 0 {
		return fail(400, "prompt or content is required")
	}
	user := protocol.Message{Role: protocol.RoleUser}
	if in.Prompt != "" {
		user.Content = append(user.Content, protocol.ContentBlock{Type: protocol.ContentText, Text: in.Prompt})
	}
	user.Content = append(user.Content, in.Content...)
	if err := user.Validate(); err != nil {
		return fail(400, err.Error())
	}
	for _, block := range user.Content {
		if block.Type == protocol.ContentThinking {
			return fail(400, "user content must be text or image")
		}
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closing || s.shuttingDown.Load() {
		return fail(http.StatusServiceUnavailable, "backend is closing")
	}
	if rt.deleted {
		return fail(404, "session not found")
	}
	if rt.runtimeErr != nil {
		return fail(500, "session runtime is quarantined: "+rt.runtimeErr.Error())
	}
	requestHash := hashPrompt(in)
	if accepted, found, err := s.replayRunLocked(rt, in); found {
		if err != nil {
			return fail(409, err.Error())
		}
		return accepted, http.StatusOK, nil
	}
	if rt.queue != nil && len(rt.queue.Items)+len(rt.queue.Edits) > 0 && ctx.Value(queueDispatchKey{}) != rt {
		return fail(409, "session has queued work; submit through /queue")
	}
	if rt.queue != nil && ctx.Value(queueDispatchKey{}) != rt {
		if _, exists := rt.queue.Receipts[in.ClientRequestID]; exists {
			return fail(409, "client_request_id belongs to a queue submission")
		}
	}
	if rt.activeLocked() {
		return fail(409, errSessionBusy)
	}
	// Expand after the idempotency check so a retried request replays even if its
	// template changed or disappeared in between.
	if in.PromptCommand {
		expanded, err := s.expandPromptCommand(in.Prompt, rt.workingDir())
		if err != nil {
			return fail(422, err.Error())
		}
		in.Snapshot = expanded
	}
	if in.Model != nil && (in.Model.Model != "" || in.Model.Provider != "") {
		if err := s.switchModelLocked(rt, *in.Model); err != nil {
			return fail(rebuildErrorStatus(err), "model switch failed: "+err.Error())
		}
	}
	if err := s.refreshSettingsLocked(rt); err != nil {
		return fail(400, err.Error())
	}
	// A desktop bridge may have connected or gone since the session loaded.
	s.syncHostTools(rt.agent)
	// Normalize after model selection so model-specific tools and MCP filters are current.
	options, err := normalizeRunOptions(in.Options, rt.agent.WorkingDir, rt.agent.KnownTools())
	if err != nil {
		return fail(400, err.Error())
	}
	in.Options = &options
	runPolicy, err := s.runSandboxPolicy(rt.agent.SandboxPolicy, rt.agent.WorkingDir, options)
	if err != nil {
		return fail(400, "options.sandbox: "+err.Error())
	}
	hash := hashPrompt(in)
	if s.mcp != nil {
		mcpTools, filter := s.mcp.ToolsForTurn(ctx, rt.agent.WorkingDir, options.MCPServerIDs, rt.mcpActivation)
		rt.agent.SetMCPTools(mcpTools)
		rt.agent.RequestToolFilter = filter
	}
	if in.ResumeMessageID != "" {
		messages := rt.agent.MessagesSnapshot()
		submitted, _ := user.ToAIMessage()
		if len(messages) == 0 || messages[len(messages)-1].Role != "user" || messages[len(messages)-1].ID != in.ResumeMessageID || !sameUserContent(messages[len(messages)-1], submitted) {
			return fail(409, "resume_message_id and content must match the tail user message")
		}
		user = protocol.FromAIMessage(messages[len(messages)-1])
	}
	if in.TurnID != "" {
		user.ID = in.TurnID
	}
	if runtimePromptRun(ctx) && in.ResumeMessageID == "" {
		user.Origin = ai.OriginBackground
	}
	rollback := func() {}
	if admit != nil {
		undo, status, err := admit(&in)
		if err != nil {
			return fail(status, err.Error())
		}
		if undo != nil {
			rollback = undo
		}
	}
	runID := newRunID()
	// Stopping the run cancels with context.Canceled; the run's own end cancels
	// with tools.ErrTurnEnded, which background work the turn started outlives.
	runCtx, cancelRun := context.WithCancelCause(ctx)
	cancel := func() { cancelRun(context.Canceled) }
	rt.runID, rt.runDone, rt.cancel = runID, false, cancel
	rt.checkpoint = newCheckpointCapture(s.eventDir, rt.id, runID, in.TurnID)
	abort := func(err error) (protocol.RunAccepted, int, error) {
		cancel()
		rollback()
		rt.cancel, rt.checkpoint, rt.runDone = nil, nil, true
		rt.runtimeErr = fmt.Errorf("persist run acceptance: %w", err)
		return protocol.RunAccepted{}, 500, rt.runtimeErr
	}
	if err := rt.publishEventLocked(runID, protocol.EventRunAccepted, map[string]any{"client_request_id": in.ClientRequestID}); err != nil {
		return abort(err)
	}
	acceptedSeq := rt.nextSeq
	if in.ResumeMessageID == "" {
		if err := rt.publishEventLocked(runID, protocol.EventUserMessage, user); err != nil {
			return abort(err)
		}
	}
	rt.runs[in.ClientRequestID] = runRecord{ClientRequestID: in.ClientRequestID, PromptHash: hash, RequestHash: requestHash, RunID: runID, AcceptedSeq: acceptedSeq}
	if err := rt.persistRuns(s.eventDir); err != nil {
		return abort(err)
	}
	rt.noteRunAcceptedLocked(ctx, in)
	rt.leases.Add(1)
	rt.runWorkers.Add(1)
	go func() {
		defer rt.runWorkers.Done()
		defer s.releaseRuntime(rt)
		s.executeRun(rt, runCtx, func() { cancelRun(tools.ErrTurnEnded) }, runID, in, options, runPolicy)
		// The next goal round starts only after executeRun has marked this run done.
		s.continueGoal(rt, runID, in)
		rt.mu.Lock()
		if rt.queue != nil {
			s.startQueueWorkerLocked(rt)
		}
		// Background notices that arrived during the run (or at its end) start the
		// next turn once no user work is waiting.
		s.dispatchWakesLocked(rt)
		rt.mu.Unlock()
	}()
	return protocol.RunAccepted{Version: protocol.Version, ConversationID: rt.id, RunID: runID, AcceptedSeq: acceptedSeq}, http.StatusAccepted, nil
}
