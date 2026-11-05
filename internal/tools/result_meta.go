package tools

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

// callMeta collects what a running tool reports about itself beside its text:
// the exit status of a command it ran, and the time it spent waiting for the
// permission gate, which its duration leaves out.
type callMeta struct {
	mu       sync.Mutex
	exitCode *int
	timedOut bool
	gateWait time.Duration
	denial   *sandbox.Denial
}

type callMetaKey struct{}

func withCallMeta(ctx context.Context) (context.Context, *callMeta) {
	m := &callMeta{}
	return context.WithValue(ctx, callMetaKey{}, m), m
}

func callMetaFrom(ctx context.Context) *callMeta {
	m, _ := ctx.Value(callMetaKey{}).(*callMeta)
	return m
}

// reportExit records the exit status of the command the current tool call ran.
// A nil code (the command did not exit on its own) records nothing.
func reportExit(ctx context.Context, code *int) {
	m := callMetaFrom(ctx)
	if m == nil || code == nil {
		return
	}
	v := *code
	m.mu.Lock()
	m.exitCode = &v
	m.mu.Unlock()
}

// reportTimedOut records that the command the current tool call ran hit its timeout.
func reportTimedOut(ctx context.Context) {
	if m := callMetaFrom(ctx); m != nil {
		m.mu.Lock()
		m.timedOut = true
		m.mu.Unlock()
	}
}

// reportShellRun records the outcome of a command run through bashrun.
func reportShellRun(ctx context.Context, exitCode *int, timedOut bool) {
	if timedOut {
		reportTimedOut(ctx)
		return
	}
	reportExit(ctx, exitCode)
}

// reportProcess records how a managed process the current tool call observed
// has ended; a running or cancelled process records nothing, nor does a status
// the process did not exit with (a signal is recorded as -1).
func reportProcess(ctx context.Context, snap liveagentProcessSnapshot) {
	switch snap.Status {
	case liveagentProcessTimedOut:
		reportTimedOut(ctx)
	case liveagentProcessCompleted, liveagentProcessFailed:
		if snap.ExitCode != nil && *snap.ExitCode >= 0 {
			reportExit(ctx, snap.ExitCode)
		}
	}
}

// explainSandboxDenial recognizes a command the OS sandbox most likely
// stopped, records the denial for the tool result and returns text with the
// hint line appended for the model; text is unchanged when the sandbox is off
// or does not explain the failure.
func explainSandboxDenial(ctx context.Context, text, command, dir string, res bashrun.Result) string {
	d := sandbox.FromContext(ctx).ExplainFailure(command, dir, res.Output, res.ExitCode)
	if d == nil {
		return text
	}
	if m := callMetaFrom(ctx); m != nil {
		m.mu.Lock()
		m.denial = d
		m.mu.Unlock()
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + d.Hint()
}

func (m *callMeta) addGateWait(d time.Duration) {
	if m == nil || d <= 0 {
		return
	}
	m.mu.Lock()
	m.gateWait += d
	m.mu.Unlock()
}

// apply copies the collected fields onto r, with the call's duration: the time
// since start minus the time spent waiting for the user's permission.
func (m *callMeta) apply(r *Result, start time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.ExitCode = m.exitCode
	r.TimedOut = m.timedOut
	r.SandboxDenial = m.denial
	r.Duration = max(time.Since(start)-m.gateWait, time.Nanosecond)
}

// DurationMs is Duration rounded up to whole milliseconds, so a call that ran
// never reads as taking none; 0 when the tool did not run.
func (r Result) DurationMs() int64 {
	if r.Duration <= 0 {
		return 0
	}
	return int64((r.Duration + time.Millisecond - 1) / time.Millisecond)
}
