package backend

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// Background work (a background subagent's report, a wait result, a managed process
// exit) is steered into the agent. While a turn runs the agent folds it in at the
// next tool round; once no turn is running it reaches OnOrphanedSteer or
// Waits().OnWake instead. The TUI answers those by submitting a turn. The backend
// does the same through the canonical run executor, so the desktop sees the new
// turn as ordinary run events (run.accepted, user message, assistant output).
//
// Wake runs never preempt user work: they start only when the session has no
// running turn and nothing queued, and are retried when the blocking run ends.
// A running background subagent does not block them: its own report arrives the
// same way, so a user run may start and finish while it works. Notices are held in memory only; they describe in-process work that
// does not survive a backend restart either.

const (
	// maxWakeBacklog bounds the notices held for one session; older ones are dropped.
	maxWakeBacklog = 32
	// maxWakeStreak bounds consecutive wake runs with no run started by anyone else,
	// so background work that keeps re-arming itself cannot drive the session forever.
	maxWakeStreak = 8
)

type wakeRunKey struct{}

// wakeTemplate carries the request settings of the last run the user (or a goal or
// cron task) started, so a wake run uses the same options, hooks and attendance.
type wakeTemplate struct {
	options     *protocol.RunOptions
	hookPolicy  string
	hookScopeID string
	unattended  bool
}

type wakeState struct {
	mu          sync.Mutex
	texts       []string
	dispatching bool
	// held stops automatic delivery until the next run not started by a wake: set
	// when the streak limit is reached, the user stops a wake run, or the session is
	// closed. Held notices are delivered after that next run ends.
	held bool
	// streak counts wake runs accepted since the last other run.
	streak int
	// current reports whether the current or last accepted run was a wake run.
	current  bool
	template wakeTemplate
}

func isWakeRun(ctx context.Context) bool {
	wake, _ := ctx.Value(wakeRunKey{}).(bool)
	return wake
}

// wireWakes routes notices that arrive while no turn is running into wake runs.
func (s *Server) wireWakes(rt *runtimeSession, ag *agent.Agent) {
	deliver := func(text string) { s.queueWake(rt, text) }
	ag.OnOrphanedSteer = deliver
	ag.Waits().OnWake = deliver
}

// steerRuntime delivers a notice to the session's current agent: into the running
// turn, or through wireWakes when none is running.
func (s *Server) steerRuntime(rt *runtimeSession, text string) {
	rt.mu.Lock()
	ag := rt.agent
	gone := rt.deleted || rt.closing
	rt.mu.Unlock()
	if ag != nil && !gone {
		ag.Notify(text)
	}
}

func (s *Server) queueWake(rt *runtimeSession, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	rt.wake.mu.Lock()
	rt.wake.texts = append(rt.wake.texts, text)
	if extra := len(rt.wake.texts) - maxWakeBacklog; extra > 0 {
		rt.wake.texts = append([]string(nil), rt.wake.texts[extra:]...)
	}
	rt.wake.mu.Unlock()
	// Notices also arrive from the agent's own turn teardown, which still holds the
	// turn; dispatch therefore never runs on the caller's goroutine.
	go s.kickWakes(rt)
}

func (s *Server) kickWakes(rt *runtimeSession) {
	rt.mu.Lock()
	s.dispatchWakesLocked(rt)
	rt.mu.Unlock()
}

// dispatchWakesLocked starts a wake run carrying every pending notice when the
// session is idle. It runs under rt.mu.
func (s *Server) dispatchWakesLocked(rt *runtimeSession) {
	w := &rt.wake
	w.mu.Lock()
	defer w.mu.Unlock()
	if rt.deleted || rt.closing || s.shuttingDown.Load() {
		w.texts = nil
		return
	}
	if w.dispatching || w.held || len(w.texts) == 0 || rt.runtimeErr != nil || rt.agent == nil {
		return
	}
	if rt.activeLocked() || (rt.queue != nil && len(rt.queue.Items)+len(rt.queue.Edits) > 0) {
		return
	}
	if w.streak >= maxWakeStreak {
		w.held = true
		return
	}
	texts, template := w.texts, w.template
	w.texts, w.dispatching = nil, true
	w.streak++
	rt.leases.Add(1)
	go s.startWakeRun(rt, texts, template)
}

func (s *Server) startWakeRun(rt *runtimeSession, texts []string, template wakeTemplate) {
	defer s.releaseRuntime(rt)
	ctx := context.WithValue(context.Background(), wakeRunKey{}, true)
	if template.unattended {
		ctx = withUnattendedRun(ctx)
	}
	in := protocol.PromptRequest{
		ConversationID:  rt.id,
		ClientRequestID: "wake-" + newRunID(),
		Prompt:          strings.Join(texts, "\n\n"),
		Options:         template.options,
		HookPolicy:      template.hookPolicy,
		HookScopeID:     template.hookScopeID,
	}
	_, status, err := s.startCanonicalRun(ctx, rt, in)
	if err != nil && status == http.StatusBadRequest && in.Options != nil {
		// Options remembered from an earlier run can go stale (a removed workspace
		// root or tool); deliver with session defaults rather than lose the notice.
		// A sandboxed session stays sandboxed: if the sandbox itself was refused,
		// the retry is refused too instead of running the commands unconfined.
		in.Options = nil
		if template.options.Sandbox != nil {
			sandbox := *template.options.Sandbox
			in.Options = &protocol.RunOptions{Sandbox: &sandbox}
		}
		_, status, err = s.startCanonicalRun(ctx, rt, in)
	}
	if err == nil {
		// Acceptance already cleared dispatching (noteRunAcceptedLocked), so the
		// run's own completion can dispatch notices that arrive meanwhile.
		return
	}
	rt.wake.mu.Lock()
	defer rt.wake.mu.Unlock()
	rt.wake.dispatching = false
	rt.wake.streak--
	// A conflict means a user run or queued work got there first; the notices wait
	// for that run to end. Anything else (closing, deleted, quarantined) drops them.
	if status == http.StatusConflict {
		rt.wake.texts = append(texts, rt.wake.texts...)
		// The blocking run may have ended before the requeue, while dispatching
		// was still set, and found nothing to deliver; try again once.
		go s.kickWakes(rt)
	}
}

// noteRunAcceptedLocked records a newly accepted run. A run started by anyone but a
// wake resets the wake streak and becomes the template for later wake runs.
func (rt *runtimeSession) noteRunAcceptedLocked(ctx context.Context, in protocol.PromptRequest) {
	w := &rt.wake
	w.mu.Lock()
	defer w.mu.Unlock()
	w.current = isWakeRun(ctx)
	if w.current {
		w.dispatching = false
		return
	}
	w.streak, w.held = 0, false
	template := wakeTemplate{hookPolicy: in.HookPolicy, hookScopeID: in.HookScopeID, unattended: isUnattendedRun(ctx)}
	if in.Options != nil {
		options := *in.Options
		template.options = &options
	}
	w.template = template
}

// holdWakesLocked stops automatic wake runs until the next run started by someone
// else; drop also discards the pending notices.
func (rt *runtimeSession) holdWakesLocked(onlyIfWakeRun, drop bool) {
	w := &rt.wake
	w.mu.Lock()
	defer w.mu.Unlock()
	if onlyIfWakeRun && !w.current {
		return
	}
	w.held = true
	if drop {
		w.texts = nil
	}
}

func (rt *runtimeSession) wakesPendingLocked() bool {
	rt.wake.mu.Lock()
	defer rt.wake.mu.Unlock()
	return len(rt.wake.texts) > 0 || rt.wake.dispatching
}
