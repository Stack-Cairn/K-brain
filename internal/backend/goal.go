package backend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/goalloop"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

const maxGoalBytes = 16 * 1024

// goalReasonAwaitingInput pauses a goal whose round asked the user for input it cannot
// advance without. Unlike a pause by the user, the user's next plain message resumes it.
const goalReasonAwaitingInput = "awaiting_input"

var errGoalRoundStale = errors.New("goal changed before the round was accepted")

// goalRuntime is the in-memory goal loop of one session. The goal text is durable in
// session metadata; status and round are not, so a reloaded goal comes back paused and
// needs an explicit resume, matching the TUI.
type goalRuntime struct {
	loaded bool
	text   string
	// memoryOff: the goal text was written while memory was off, so its rounds stay out
	// of memory extraction even after memory is turned back on.
	memoryOff bool
	status    string
	round     int
	reason    string
	// nonce is drawn on every set and resume and names its continuation rounds, so their
	// request ids never collide with run records persisted before a reload.
	nonce string
	// revision counts goal text updates; runRevision is the revision the current goal
	// round was started for, so a reply to an older text cannot complete a newer one.
	revision, runRevision int
	// roundRequestID is the client request id of the latest goal round; only the run it
	// accepted belongs to the goal, so pause and clear never stop an unrelated run.
	roundRequestID string
	// started is when the goal was set or last resumed.
	started time.Time
	// next is the round decided for the run that just completed; the run worker starts
	// it once the session is idle again.
	next *goalContinuation
	// cleared is the text of a goal the user cleared, until a new goal is set; plain
	// runs remind the model not to pick it back up. It does not survive a reload.
	cleared string
}

type goalContinuation struct {
	afterRunID      string
	clientRequestID string
	prompt          string
}

// goalBusyRetries and goalBusyDelay bound how long the next goal round waits for a
// session kept busy by background work, such as a background subagent.
var (
	goalBusyRetries = 20
	goalBusyDelay   = 250 * time.Millisecond
)

func newGoalNonce() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// goalRoundLocked reports whether runID is the run accepted for the latest goal round.
func (rt *runtimeSession) goalRoundLocked(runID string) bool {
	if rt.goal.roundRequestID == "" || runID == "" {
		return false
	}
	record, ok := rt.runs[rt.goal.roundRequestID]
	return ok && record.RunID == runID
}

// goalOwnsRunLocked reports whether the session's running turn is a goal round.
func (rt *runtimeSession) goalOwnsRunLocked() bool {
	return rt.cancel != nil && !rt.runDone && rt.goalRoundLocked(rt.runID)
}

func (s *Server) goalMaxRoundsLocked(rt *runtimeSession) int {
	global := 0
	if s.settings != nil {
		if cfg := s.settings.Snapshot(); cfg != nil {
			global = cfg.GoalMaxRounds
		}
	}
	dir := ""
	if rt.agent != nil {
		dir = rt.agent.WorkingDir
	}
	return goalloop.MaxRounds(dir, global)
}

func (s *Server) ensureGoalLoadedLocked(rt *runtimeSession) {
	if rt.goal.loaded {
		return
	}
	rt.goal.loaded = true
	rt.goal.status = "none"
	if meta, _, err := s.store.Load(rt.id); err == nil && strings.TrimSpace(meta.Goal) != "" {
		rt.goal.text, rt.goal.memoryOff = meta.Goal, meta.GoalMemoryOff
		rt.goal.status, rt.goal.reason = "paused", "restored"
	}
}

func (s *Server) goalStateLocked(rt *runtimeSession) protocol.GoalState {
	state := protocol.GoalState{
		Version: protocol.Version, ConversationID: rt.id, Goal: rt.goal.text,
		Status: rt.goal.status, Round: rt.goal.round, MaxRounds: s.goalMaxRoundsLocked(rt), Reason: rt.goal.reason,
	}
	if !rt.goal.started.IsZero() && rt.goal.text != "" {
		state.StartedAt = rt.goal.started.UTC().Format(time.RFC3339)
	}
	if next := rt.goal.next; next != nil {
		state.Status = "continuing"
		state.NextClientRequestID, state.NextPrompt = next.clientRequestID, next.prompt
	}
	return state
}

// goalRoundPrompt is the visible user text of a goal-check round; the full goal-check
// instructions travel as a request-only snapshot so the transcript stays readable.
func goalRoundPrompt(goal string, round, max int) string {
	return fmt.Sprintf("[goal %d/%d] %s", round, max, goal)
}

type goalRoundKey struct{}

// withGoalRound marks the run of a goal continuation round. The runtime writes its
// prompt, so like a wake run its user message carries ai.OriginBackground and is not
// authored: clients show it as a notice, and prompt recall leaves it out.
func withGoalRound(ctx context.Context) context.Context {
	return context.WithValue(ctx, goalRoundKey{}, true)
}

// runtimePromptRun reports a run whose prompt the runtime wrote: a wake run or a
// goal continuation round.
func runtimePromptRun(ctx context.Context) bool {
	round, _ := ctx.Value(goalRoundKey{}).(bool)
	return round || isWakeRun(ctx)
}

func (s *Server) handleGoal(w http.ResponseWriter, r *http.Request, id string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	defer s.releaseRuntime(rt)
	if r.Method == http.MethodGet {
		rt.mu.Lock()
		s.ensureGoalLoadedLocked(rt)
		state := s.goalStateLocked(rt)
		rt.mu.Unlock()
		writeJSON(w, http.StatusOK, state)
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in protocol.GoalRequest
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	switch in.Action {
	case "clear":
		s.clearGoal(w, rt)
	case "pause":
		s.pauseGoal(w, rt)
	case "update":
		s.updateGoal(w, rt, in.Goal)
	case "set", "resume":
		s.startGoal(w, rt, in)
	default:
		writeJSONError(w, http.StatusBadRequest, "action must be set, resume, pause, update or clear")
	}
}

// pauseGoal stops the running round, if any, and drops any pending continuation; the
// goal text stays durable so resume can pick it up.
func (s *Server) pauseGoal(w http.ResponseWriter, rt *runtimeSession) {
	rt.mu.Lock()
	s.ensureGoalLoadedLocked(rt)
	if rt.goal.text == "" || rt.goal.status != "active" {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "no running goal to pause")
		return
	}
	// Mark paused first so the cancelled round's evaluation does not continue it. Only a
	// running goal round is stopped; any other run of the session is left alone.
	rt.goal.status, rt.goal.reason, rt.goal.next = "paused", "user", nil
	var cancel context.CancelFunc
	if rt.goalOwnsRunLocked() {
		cancel = rt.cancel
	}
	state := s.goalStateLocked(rt)
	rt.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	_, _ = s.publish(rt, protocol.EventGoalUpdated, state, "")
	writeJSON(w, http.StatusOK, state)
}

// updateGoal replaces the goal text. A running goal keeps going and later rounds use
// the new text; a paused goal stays paused until resumed.
func (s *Server) updateGoal(w http.ResponseWriter, rt *runtimeSession, raw string) {
	goal := strings.TrimSpace(raw)
	if goal == "" || len(goal) > maxGoalBytes {
		writeJSONError(w, http.StatusBadRequest, "goal is required and must be within the size limit")
		return
	}
	rt.mu.Lock()
	s.ensureGoalLoadedLocked(rt)
	if rt.goal.text == "" {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "no goal to update")
		return
	}
	memoryOff := rt.goal.memoryOff || !s.memoryEnabledNow(rt)
	if err := s.store.SetGoalMemory(rt.id, goal, memoryOff); err != nil {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rt.goal.text, rt.goal.memoryOff = goal, memoryOff
	rt.goal.revision++
	state := s.goalStateLocked(rt)
	rt.mu.Unlock()
	_, _ = s.publish(rt, protocol.EventGoalUpdated, state, "")
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) clearGoal(w http.ResponseWriter, rt *runtimeSession) {
	rt.mu.Lock()
	s.ensureGoalLoadedLocked(rt)
	if err := s.store.SetGoal(rt.id, ""); err != nil {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A goal round still running is stopped too; a plain user run is left alone.
	var cancel context.CancelFunc
	if rt.goal.status == "active" && rt.goalOwnsRunLocked() {
		cancel = rt.cancel
	}
	rt.goal = goalRuntime{loaded: true, status: "cleared", cleared: rt.goal.text}
	state := s.goalStateLocked(rt)
	rt.goal.status = "none"
	rt.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	_, _ = s.publish(rt, protocol.EventGoalUpdated, state, "")
	writeJSON(w, http.StatusOK, state)
}
func (s *Server) startGoal(w http.ResponseWriter, rt *runtimeSession, in protocol.GoalRequest) {
	if in.Run == nil || strings.TrimSpace(in.Run.ClientRequestID) == "" {
		writeJSONError(w, http.StatusBadRequest, "run.client_request_id is required")
		return
	}
	goal := strings.TrimSpace(in.Goal)
	if in.Action == "set" && goal == "" {
		writeJSONError(w, http.StatusBadRequest, "goal is required")
		return
	}
	if len(goal) > maxGoalBytes {
		writeJSONError(w, http.StatusBadRequest, "goal is too long")
		return
	}
	run := *in.Run
	run.ConversationID, run.PromptCommand, run.Content = rt.id, false, nil
	run.Prompt = "/goal resume"
	if in.Action == "set" {
		run.Prompt = "/goal " + goal
	}
	rt.mu.Lock()
	s.ensureGoalLoadedLocked(rt)
	// A retried request replays its original run and leaves the goal untouched, even
	// after that run finished or while it is still running.
	if accepted, found, err := s.replayRunLocked(rt, run); found {
		state := s.goalStateLocked(rt)
		rt.mu.Unlock()
		if err != nil {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		state.Run = &accepted
		writeJSON(w, http.StatusOK, state)
		return
	}
	if rt.activeLocked() {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusConflict, errSessionBusy)
		return
	}
	if in.Action == "resume" {
		goal = rt.goal.text
		if goal == "" {
			rt.mu.Unlock()
			writeJSONError(w, http.StatusConflict, "no goal to resume")
			return
		}
	}
	rt.mu.Unlock()

	// History keeps what the user typed; the model gets the goal itself (first round, as
	// in the TUI) or the goal check (resume) as request-only text.
	if in.Action == "set" {
		run.Snapshot = goalloop.StartPrompt(goal)
	} else {
		run.Snapshot = goalloop.ContinuePrompt(goal)
	}
	// The goal changes inside run acceptance, under the same lock, so of concurrent
	// requests only the accepted one ever touches it and a rejected one has nothing to
	// restore. The event is published there too, ahead of the run's first decision.
	var state protocol.GoalState
	admit := func(*protocol.PromptRequest) (func(), int, error) {
		if in.Action == "resume" && rt.goal.text != goal {
			return nil, http.StatusConflict, errors.New("goal changed before resume was accepted")
		}
		memoryOff := rt.goal.memoryOff
		if in.Action == "set" {
			// Admission runs after this run's settings refresh, so the agent's switch is the
			// one that marks this round's History message.
			memoryOff = !rt.agent.MemoryEnabled()
			if err := s.store.SetGoalMemory(rt.id, goal, memoryOff); err != nil {
				return nil, http.StatusInternalServerError, err
			}
		}
		previous := rt.goal
		rt.goal = goalRuntime{loaded: true, text: goal, memoryOff: memoryOff, status: "active", nonce: newGoalNonce(), roundRequestID: run.ClientRequestID, started: time.Now()}
		state = s.goalStateLocked(rt)
		if rt.runID == "" {
			rt.runID = "recovery"
		}
		_ = rt.publishEventLocked(rt.runID, protocol.EventGoalUpdated, state)
		return func() {
			if in.Action == "set" {
				_ = s.store.SetGoalMemory(rt.id, previous.text, previous.memoryOff)
			}
			rt.goal = previous
		}, 0, nil
	}
	accepted, status, err := s.startCanonicalRunWith(context.Background(), rt, run, admit)
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	if status != http.StatusAccepted {
		// A concurrent retry of this request won acceptance first; report the goal as is.
		rt.mu.Lock()
		state = s.goalStateLocked(rt)
		rt.mu.Unlock()
	}
	state.Run = &accepted
	writeJSON(w, http.StatusOK, state)
}

// evaluateGoal runs inside executeRun before the terminal event, so a client streaming
// the run sees the decision first. outcome is completed, cancelled or failed.
func (s *Server) evaluateGoal(rt *runtimeSession, runID, outcome string) {
	rt.mu.Lock()
	s.ensureGoalLoadedLocked(rt)
	// Only the goal's own round decides it; a cron or user run that took the idle
	// session between rounds is not a goal round.
	if rt.goal.status != "active" || !rt.goalRoundLocked(runID) {
		rt.mu.Unlock()
		return
	}
	// A reply written for an older goal text cannot complete the updated goal; the
	// next round checks the new text instead.
	current := rt.goal.runRevision == rt.goal.revision
	switch {
	case outcome != "completed":
		// A stopped or failed round pauses the goal; only an explicit resume restarts it.
		rt.goal.status, rt.goal.reason = "paused", outcome
	case current && goalReplyLeads(rt, goalloop.Met):
		_ = s.store.SetGoal(rt.id, "")
		rt.goal.status, rt.goal.reason = "met", ""
	case current && goalReplyLeads(rt, goalloop.NeedsInput):
		// Another round cannot get the user's answer; wait for their next message.
		rt.goal.status, rt.goal.reason = "paused", goalReasonAwaitingInput
	case rt.goal.round >= s.goalMaxRoundsLocked(rt):
		rt.goal.status, rt.goal.reason = "paused", "max_rounds"
	default:
		rt.goal.round++
		rt.goal.next = &goalContinuation{
			afterRunID:      runID,
			clientRequestID: fmt.Sprintf("goal-%s-%s-%d", rt.id, rt.goal.nonce, rt.goal.round),
			prompt:          goalRoundPrompt(rt.goal.text, rt.goal.round, s.goalMaxRoundsLocked(rt)),
		}
	}
	state := s.goalStateLocked(rt)
	if rt.goal.status == "met" {
		// Met clears the durable goal; the event still carries the text for the notice.
		rt.goal.text = ""
	}
	rt.mu.Unlock()
	_, _ = rt.publish(protocol.EventGoalUpdated, state, "")
}

// goalReplyLeads applies a leading-token check to the round's final reply. A reply cut
// off at the length limit never counts.
func goalReplyLeads(rt *runtimeSession, leads func(string) bool) bool {
	if rt.agent == nil {
		return false
	}
	messages := rt.agent.MessagesSnapshot()
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != protocol.RoleAssistant {
			continue
		}
		return messages[i].StopReason != ai.StopReasonLength && leads(messages[i].TextContent())
	}
	return false
}

// goalUserRunAdmit admits a plain user run against the session goal.
//
// When the goal paused for the user's input, this run carries their answer: it becomes
// the goal's round, with the reply rules as request-only text, so the round's outcome
// decides the goal like any other round — met, waiting again, or continuing.
//
// When the user paused (or a reload restored) the goal, or cleared it, the run stays a
// plain run and only gets a request-only notice. History still shows the /goal command,
// and without the notice the model picks the goal back up on its own. The notice never
// enters history, so it cannot land between a tool call and its result.
func (s *Server) goalUserRunAdmit(rt *runtimeSession) runAdmit {
	return func(in *protocol.PromptRequest) (func(), int, error) {
		s.ensureGoalLoadedLocked(rt)
		switch {
		case rt.goal.text != "" && rt.goal.status == "paused" && rt.goal.reason == goalReasonAwaitingInput:
		case rt.goal.text != "" && rt.goal.status == "paused":
			appendSnapshot(in, goalloop.PausedNotice(rt.goal.text))
			return nil, 0, nil
		case rt.goal.text == "" && rt.goal.cleared != "":
			appendSnapshot(in, goalloop.ClearedNotice(rt.goal.cleared))
			return nil, 0, nil
		default:
			return nil, 0, nil
		}
		previous := rt.goal
		rt.goal.status, rt.goal.reason = "active", ""
		rt.goal.roundRequestID, rt.goal.runRevision = in.ClientRequestID, rt.goal.revision
		appendSnapshot(in, goalloop.ReplyPrompt(rt.goal.text))
		if rt.runID == "" {
			rt.runID = "recovery"
		}
		_ = rt.publishEventLocked(rt.runID, protocol.EventGoalUpdated, s.goalStateLocked(rt))
		return func() { rt.goal = previous }, 0, nil
	}
}

// appendSnapshot adds request-only text after any the run already carries, such as an
// expanded prompt command.
func appendSnapshot(in *protocol.PromptRequest, text string) {
	if in.Snapshot != "" {
		text = in.Snapshot + "\n\n" + text
	}
	in.Snapshot = text
}

// continueGoal starts the round decided by evaluateGoal once the finished run has
// released the session. evaluateGoal only decides rounds for goal runs, so previous is
// always the goal's own last round and the next round inherits its options. A clear, pause or new goal in between drops it. A session kept
// busy by background work is retried briefly; a round that still cannot start pauses
// the goal.
func (s *Server) continueGoal(rt *runtimeSession, runID string, previous protocol.PromptRequest) {
	rt.mu.Lock()
	next := rt.goal.next
	if next == nil || next.afterRunID != runID {
		rt.mu.Unlock()
		return
	}
	rt.goal.next = nil
	nonce, round := rt.goal.nonce, rt.goal.round
	rt.mu.Unlock()

	reason := "start_failed"
	for attempt := 0; ; attempt++ {
		rt.mu.Lock()
		if rt.goal.status != "active" || rt.goal.nonce != nonce || rt.goal.round != round {
			rt.mu.Unlock()
			return
		}
		goal, revision, memoryOff := rt.goal.text, rt.goal.revision, rt.goal.memoryOff
		rt.mu.Unlock()
		run := protocol.PromptRequest{
			ConversationID: rt.id, ClientRequestID: next.clientRequestID, Prompt: next.prompt,
			Options: previous.Options, HookPolicy: previous.HookPolicy, HookScopeID: previous.HookScopeID,
			Snapshot: goalloop.ContinuePrompt(goal),
		}
		// The round claims the goal only when it is accepted, and only if no pause, clear,
		// new goal or update landed since the snapshot above was built.
		stale := false
		admit := func(*protocol.PromptRequest) (func(), int, error) {
			if rt.goal.status != "active" || rt.goal.nonce != nonce || rt.goal.round != round || rt.goal.revision != revision {
				stale = true
				return nil, http.StatusConflict, errGoalRoundStale
			}
			prevRevision, prevRequest := rt.goal.runRevision, rt.goal.roundRequestID
			rt.goal.runRevision, rt.goal.roundRequestID = revision, next.clientRequestID
			return func() { rt.goal.runRevision, rt.goal.roundRequestID = prevRevision, prevRequest }, 0, nil
		}
		// Rounds repeat the goal text as the user message; text written while memory was
		// off stays out of extraction.
		roundCtx := withGoalRound(context.Background())
		if memoryOff {
			roundCtx = agent.WithMemoryOffAuthored(roundCtx)
		}
		_, status, err := s.startCanonicalRunWith(roundCtx, rt, run, admit)
		if err == nil && status == http.StatusAccepted {
			return
		}
		if stale {
			rt.mu.Lock()
			moved := rt.goal.status != "active" || rt.goal.nonce != nonce || rt.goal.round != round
			rt.mu.Unlock()
			if moved {
				return
			}
			// Only the text changed: rebuild the round prompt from the new text.
			continue
		}
		// A replayed (200) request id means no new round started.
		if err != nil && status == http.StatusConflict && err.Error() == errSessionBusy {
			reason = "busy"
			if attempt < goalBusyRetries {
				time.Sleep(goalBusyDelay)
				continue
			}
		}
		break
	}
	rt.mu.Lock()
	if rt.goal.status != "active" || rt.goal.nonce != nonce || rt.goal.round != round {
		rt.mu.Unlock()
		return
	}
	rt.goal.status, rt.goal.reason = "paused", reason
	state := s.goalStateLocked(rt)
	rt.mu.Unlock()
	_, _ = s.publish(rt, protocol.EventGoalUpdated, state, "")
}
