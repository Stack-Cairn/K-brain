package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

type WaitStatus string

const (
	WaitRunning WaitStatus = "running"
	WaitMet     WaitStatus = "condition met"
	WaitTimeout WaitStatus = "timed out"
	WaitFailed  WaitStatus = "command failing"
	WaitKilled  WaitStatus = "cancelled"
)

type waitTask struct {
	ID        string
	Command   string
	Until     string
	Interval  time.Duration
	Timeout   time.Duration
	Started   time.Time
	status    atomic.Value
	Detail    string
	Done      chan struct{}
	cancel    context.CancelFunc
	delivered atomic.Bool
}

func (w *waitTask) Status() WaitStatus {
	if v := w.status.Load(); v != nil {
		return v.(WaitStatus)
	}
	return WaitRunning
}

func (w *waitTask) setStatus(s WaitStatus) { w.status.Store(s) }

type waitRegistry struct {
	mu     sync.Mutex
	waits  map[string]*waitTask
	OnWake func(text string)
	agent  *Agent
	ctx    context.Context
	stop   context.CancelFunc
}

var waitIDCounter atomic.Int64

func newWaitRegistry(a *Agent) *waitRegistry {
	ctx, stop := context.WithCancel(context.Background())
	return &waitRegistry{waits: map[string]*waitTask{}, agent: a, ctx: ctx, stop: stop}
}

func (a *Agent) Waits() *waitRegistry { return a.waits() }

func (a *Agent) waits() *waitRegistry {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.waitReg == nil {
		a.waitReg = newWaitRegistry(a)
	}
	return a.waitReg
}

type WaitTaskSpec struct {
	Command  string
	Until    string
	Interval time.Duration
	Timeout  time.Duration
}

const (
	waitMinInterval = 2 * time.Second
	waitMaxTimeout  = time.Hour

	waitMaxErrStrikes = 3
)

func (a *Agent) StartWait(spec WaitTaskSpec) (*waitTask, error) {
	if spec.Command == "" {
		return nil, errors.New("command is required")
	}
	var untilRe *regexp.Regexp
	if spec.Until != "" {
		re, err := regexp.Compile(spec.Until)
		if err != nil {
			return nil, fmt.Errorf("until regex: %w", err)
		}
		untilRe = re
	}
	if spec.Interval < waitMinInterval {
		spec.Interval = waitMinInterval
	}
	if spec.Timeout <= 0 {
		spec.Timeout = 10 * time.Minute
	}
	if spec.Timeout > waitMaxTimeout {
		spec.Timeout = waitMaxTimeout
	}
	r := a.waits()
	r.mu.Lock()
	id := taskSlug(spec.Command, waitIDCounter.Add(1))
	id = "wait-" + id
	ctx, cancel := context.WithCancel(r.ctx)
	w := &waitTask{
		ID: id, Command: spec.Command, Until: spec.Until,
		Interval: spec.Interval, Timeout: spec.Timeout,
		Started: time.Now(), Done: make(chan struct{}),
		cancel: cancel,
	}
	r.waits[id] = w
	r.mu.Unlock()

	go r.poll(ctx, w, untilRe)
	return w, nil
}

func (r *waitRegistry) poll(ctx context.Context, w *waitTask, until *regexp.Regexp) {
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	deadline := time.NewTimer(w.Timeout)
	defer deadline.Stop()
	strikes := 0
	check := func() (done bool) {

		res := bashrun.Run(ctx, bashrun.Options{Command: w.Command, Timeout: min(max(w.Interval, 30*time.Second), 60*time.Second)})
		if ctx.Err() != nil {
			return true
		}
		if res.TimedOut || res.Exit != "" {
			strikes++
			if strikes >= waitMaxErrStrikes {
				r.deliver(w, WaitFailed, fmt.Sprintf("[wait %s] gave up: command failed %d consecutive times (last: %s)\n\nLast output:\n%s",
					w.ID, strikes, res.Exit, tailLines(res.Output, 20)))
				return true
			}
			return false
		}
		strikes = 0
		if until == nil || until.MatchString(res.Output) {
			r.deliver(w, WaitMet, fmt.Sprintf("[wait %s done] condition met (ran every %s):\n$ %s\n\n%s",
				w.ID, w.Interval, w.Command, tailLines(res.Output, 40)))
			return true
		}
		return false
	}

	if check() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			r.deliver(w, WaitTimeout, fmt.Sprintf("[wait %s timeout] %s elapsed without the condition being met:\n$ %s",
				w.ID, w.Timeout, w.Command))
			return
		case <-ticker.C:
			if check() {
				return
			}
		}
	}
}

func (r *waitRegistry) deliver(w *waitTask, status WaitStatus, msg string) {
	if !w.delivered.CompareAndSwap(false, true) {
		return
	}
	w.setStatus(status)
	w.Detail = msg

	if r.agent.TurnRunning() {
		r.agent.Steer(msg)
	} else if r.OnWake != nil {
		r.OnWake(msg)
	}

	close(w.Done)
	w.cancel()

	r.mu.Lock()
	delete(r.waits, w.ID)
	r.mu.Unlock()
}

func (r *waitRegistry) Close() {
	if r.stop != nil {
		r.stop()
	}
}

func (r *waitRegistry) CancelWait(id string) bool {
	r.mu.Lock()
	w, ok := r.waits[id]
	running := ok && w.Status() == WaitRunning
	r.mu.Unlock()
	if !running {
		return false
	}
	if !w.delivered.CompareAndSwap(false, true) {
		return false
	}
	w.setStatus(WaitKilled)
	close(w.Done)
	w.cancel()
	r.mu.Lock()
	delete(r.waits, id)
	r.mu.Unlock()
	return true
}

func tailLines(s string, n int) string {
	lines := []byte(s)
	count, idx := 0, len(lines)
	for idx > 0 && count < n {
		idx--
		if lines[idx] == '\n' {
			count++
		}
	}
	if idx > 0 {
		return "[…]\n" + string(lines[idx+1:])
	}
	return s
}

func waitTool(a *Agent) tools.Tool {
	return tools.Tool{
		Def: ai.NewTool("wait",
			"Wait for an external condition without burning LLM turns: a background poller re-runs the shell command on the given interval (no model involvement while waiting) and you are notified EXACTLY ONCE when the condition is met, the timeout elapses, or the command keeps failing. Use this instead of `sleep N && check` loops (those spend a full turn per poll). Typical uses: CI finishing (`gh pr checks 55 | grep -q pass` or until the command exits 0), a deploy going live, a server coming up. The notification arrives as a message — do NOT poll for it.",
			`{"type":"object","properties":{"command":{"type":"string","description":"Shell command to run repeatedly; success means exit 0"},"until":{"type":"string","description":"Optional regex the command's output must match (in addition to exit 0) to count as met"},"interval":{"type":"number","description":"Seconds between runs (default 10, min 2)"},"timeout":{"type":"number","description":"Seconds before giving up (default 600, max 3600)"}},"required":["command"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var spec struct {
				Command  string  `json:"command"`
				Until    string  `json:"until"`
				Interval float64 `json:"interval"`
				Timeout  float64 `json:"timeout"`
			}
			if err := json.Unmarshal(args, &spec); err != nil {
				return "", err
			}
			w, err := a.StartWait(WaitTaskSpec{
				Command:  spec.Command,
				Until:    spec.Until,
				Interval: time.Duration(spec.Interval * float64(time.Second)),
				Timeout:  time.Duration(spec.Timeout * float64(time.Second)),
			})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Waiting (%s): `%s` every %s, giving up after %s. You will be notified once — keep working or answer the user; do NOT sleep-poll.",
				w.ID, w.Command, w.Interval, w.Timeout), nil
		},
	}
}
