package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

// ErrNotBackgroundable reports that a tool call is not running a command that
// can be moved to the background: it is unknown, already finished, interactive,
// or was already moved.
var ErrNotBackgroundable = errors.New("tool call is not running a command that can be moved to the background")

// backgroundHandoverWait bounds how long Background waits for a command to be
// handed over to the process manager.
const backgroundHandoverWait = 10 * time.Second

// backgroundOutputTail bounds the output so far that the tool result carries.
const backgroundOutputTail = 8 << 10

// Foreground tracks the foreground shell commands that the user can move to
// the background while they run, by tool call id.
type Foreground struct {
	mu       sync.Mutex
	calls    map[string]*foregroundCall
	onChange func(toolCallID string, backgroundable bool)
}

type foregroundCall struct {
	detach    chan struct{}
	requested bool
	reply     chan backgroundReply
}

type backgroundReply struct {
	processID string
	err       error
}

func NewForeground() *Foreground {
	return &Foreground{calls: map[string]*foregroundCall{}}
}

// SetOnChange registers fn to learn when a tool call becomes backgroundable and
// when it stops being so. fn runs on the tool's goroutine and must not block
// for long.
func (f *Foreground) SetOnChange(fn func(toolCallID string, backgroundable bool)) {
	f.mu.Lock()
	f.onChange = fn
	f.mu.Unlock()
}

// Running returns the ids of the tool calls that can be moved to the
// background now.
func (f *Foreground) Running() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.calls))
	for id, c := range f.calls {
		if !c.requested {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// Background moves the running command of tool call id to the background and
// returns the managed process id it continues under. The tool call then
// returns at once. A call that is unknown, finished or already moved returns
// ErrNotBackgroundable.
func (f *Foreground) Background(ctx context.Context, toolCallID string) (string, error) {
	f.mu.Lock()
	c := f.calls[toolCallID]
	if c == nil || c.requested {
		f.mu.Unlock()
		return "", ErrNotBackgroundable
	}
	c.requested = true
	close(c.detach)
	notify := f.onChange
	f.mu.Unlock()
	if notify != nil {
		notify(toolCallID, false)
	}
	timer := time.NewTimer(backgroundHandoverWait)
	defer timer.Stop()
	select {
	case r := <-c.reply:
		return r.processID, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return "", errors.New("the command was not handed over in time")
	}
}

func (f *Foreground) register(toolCallID string) *foregroundCall {
	c := &foregroundCall{detach: make(chan struct{}), reply: make(chan backgroundReply, 1)}
	f.mu.Lock()
	if f.calls[toolCallID] != nil {
		// Tool call ids are unique; a repeat is not offered for backgrounding.
		f.mu.Unlock()
		return nil
	}
	f.calls[toolCallID] = c
	notify := f.onChange
	f.mu.Unlock()
	if notify != nil {
		notify(toolCallID, true)
	}
	return c
}

// unregister ends the call's backgrounding window and answers a pending
// Background request with the outcome.
func (f *Foreground) unregister(toolCallID string, c *foregroundCall, processID string, err error) {
	f.mu.Lock()
	if f.calls[toolCallID] == c {
		delete(f.calls, toolCallID)
	}
	requested := c.requested
	notify := f.onChange
	f.mu.Unlock()
	if requested {
		c.reply <- backgroundReply{processID: processID, err: err}
		return
	}
	if notify != nil {
		notify(toolCallID, false)
	}
}

type backgroundingKey struct{}

type backgrounding struct {
	foreground *Foreground
	processes  func() *ManagedProcessManager
}

// WithBackgrounding lets the shell tools of a call register their foreground
// command with fg, so the user can move it to the background. processes
// returns the manager that takes the command over; it is called only then.
func WithBackgrounding(ctx context.Context, fg *Foreground, processes func() *ManagedProcessManager) context.Context {
	if fg == nil || processes == nil {
		return ctx
	}
	return context.WithValue(ctx, backgroundingKey{}, backgrounding{foreground: fg, processes: processes})
}

// foregroundRun is the outcome of a foreground shell command: its result, or
// the text the tool returns when the user moved it to the background.
type foregroundRun struct {
	bashrun.Result
	backgrounded string
}

// runForeground runs opts as a foreground command that the user can move to
// the background while it runs, when the call allows it. processes overrides
// the manager the call's context provides.
func runForeground(ctx context.Context, opts bashrun.Options, processes *ManagedProcessManager) (foregroundRun, error) {
	b, _ := ctx.Value(backgroundingKey{}).(backgrounding)
	id := ToolCallID(ctx)
	if b.foreground == nil || id == "" || opts.Interactive {
		return foregroundRun{Result: bashrun.Run(ctx, opts)}, nil
	}
	call := b.foreground.register(id)
	if call == nil {
		return foregroundRun{Result: bashrun.Run(ctx, opts)}, nil
	}
	opts.Detach = call.detach
	res := bashrun.Run(ctx, opts)
	if res.Detached == nil {
		b.foreground.unregister(id, call, "", ErrNotBackgroundable)
		return foregroundRun{Result: res}, nil
	}
	if processes == nil {
		processes = b.processes()
	}
	dir := opts.Dir
	if dir == "" {
		dir = bashrun.WorkingDir(ctx)
	}
	text, processID, err := adoptForeground(ctx, processes, res.Detached, opts.Command, dir)
	b.foreground.unregister(id, call, processID, err)
	if err != nil {
		return foregroundRun{}, err
	}
	reportProcessStarted(ctx, processID)
	return foregroundRun{Result: res, backgrounded: text}, nil
}

func adoptForeground(ctx context.Context, processes *ManagedProcessManager, d *bashrun.Detached, command, dir string) (string, string, error) {
	var p *liveagentProcess
	err := errors.New("no process manager")
	if processes != nil {
		p, err = processes.adopt(ctx, d, command, dir)
	}
	if err != nil {
		d.Kill()
		_ = d.Wait()
		return "", "", fmt.Errorf("move the command to the background: %w", err)
	}
	p.mu.Lock()
	cursor := p.bytes
	p.mu.Unlock()
	// The tail ends exactly at cursor, so a wait from cursor repeats nothing.
	start := max(0, cursor-backgroundOutputTail)
	var tail string
	if cursor > start {
		tail, _, _, _, _ = processes.read(p, start, cursor-start)
	}
	if start > 0 {
		if i := strings.IndexByte(tail, '\n'); i >= 0 {
			tail = tail[i+1:]
		}
		tail = "[…]\n" + tail
	}
	snap := p.snapshot()
	return backgroundedText(snap, cursor, tail), snap.ID, nil
}

func backgroundedText(snap liveagentProcessSnapshot, cursor int64, tail string) string {
	var b strings.Builder
	b.WriteString("The user moved this command to the background. It is still running; its output keeps being captured.\n")
	fmt.Fprintf(&b, "process_id=%s\npid=%d\ncwd=%s\nlog=%s\ncommand=%s\ncursor=%d\n", snap.ID, snap.PID, snap.CWD, snap.LogPath, snap.Command, cursor)
	if tail = strings.TrimRight(tail, "\n"); strings.TrimSpace(tail) != "" {
		b.WriteString("\nOutput so far:\n" + tail + "\n")
	} else {
		b.WriteString("\nNo output so far.\n")
	}
	fmt.Fprintf(&b, "\nRead further output with ManagedProcess(action=\"wait\", process_id=%q, cursor=%d) or ManagedProcess(action=\"read_log\", process_id=%q); stop it with ManagedProcess(action=\"stop\", process_id=%q). You will be notified when it exits.", snap.ID, cursor, snap.ID, snap.ID)
	return b.String()
}

// BackgroundProcessTool is the ManagedProcess tool for a catalog that has
// none: it reads, waits for and stops the commands the user moved to the
// background. It cannot start processes.
func BackgroundProcessTool(manager *ManagedProcessManager) Tool {
	def := ai.NewTool("ManagedProcess",
		"Inspect, wait for, read the log of, or stop a command the user moved to the background. A background command that exits while no wait is watching it is reported to you as a message, so do not poll only to learn that it finished.",
		`{"type":"object","required":["action"],"properties":{"action":{"type":"string","description":"Process action: status, read_log, wait, or stop.","enum":["status","read_log","wait","stop"]},"process_id":{"type":"string","description":"Process identifier from the background notice; optional for status."},"cursor":{"type":"number","minimum":0,"description":"Absolute log-byte cursor already consumed."},"yield_time_ms":{"type":"number","minimum":1,"maximum":300000,"description":"Maximum wait duration in milliseconds."},"max_bytes":{"type":"number","minimum":1,"maximum":524288,"description":"Maximum output bytes for read_log and wait."}},"additionalProperties":false}`)
	return Tool{Def: def, Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
		a, err := liveagentArguments(def, raw)
		if err != nil {
			return "", err
		}
		if strings.EqualFold(strings.TrimSpace(laString(a, "action")), "start") {
			return "", errors.New("ManagedProcess cannot start processes here; run the command with bash")
		}
		return liveagentManagedProcess(ctx, manager, a)
	}}
}
