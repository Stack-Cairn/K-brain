package bashrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/process"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
)

func userShell() string {
	return resolveDefaultShell(runtime.GOOS, os.Getenv, exec.LookPath)
}

type Result struct {
	Output      string
	OutputFile  string
	OutputBytes int64

	Exit string

	// ExitCode is the status the command exited with, 0 included; nil when it
	// did not start, timed out, was cancelled or was killed by a signal.
	ExitCode *int

	TimedOut bool

	Killed bool

	Interactive bool

	// Detached is the still-running command Run handed over when Options.Detach
	// fired; the other fields then describe only what it printed so far.
	Detached *Detached
}

type Options struct {
	Command string

	Dir string

	Shell string

	Timeout time.Duration

	Interactive bool

	InactivityTimeout time.Duration

	OnOutput func(chunk string)

	// OnUpdate receives a bounded tail snapshot when output changes.
	OnUpdate func(outputTail string)

	OnAwaitInput func(secLeft int)

	Keys <-chan []byte

	Env []string

	Sandbox *sandbox.Policy

	// Detach, once it fires, makes Run return the running command as
	// Result.Detached instead of waiting for it. The command keeps its process
	// group and sandbox and no longer ends with ctx or Timeout. Interactive
	// commands ignore it.
	Detach <-chan struct{}
}

type workingDirKey struct{}

func WithWorkingDir(ctx context.Context, dir string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, workingDirKey{}, dir)
}

func WorkingDir(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	dir, _ := ctx.Value(workingDirKey{}).(string)
	return dir
}

func Run(ctx context.Context, opts Options) Result {
	if opts.Timeout <= 0 {
		opts.Timeout = 120 * time.Second
	}
	if opts.Interactive && opts.InactivityTimeout <= 0 {
		opts.InactivityTimeout = 15 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	// A detachable command is bound to a context that follows ctx only until
	// it is detached: after that, cancelling the turn or the timeout firing no
	// longer kills it.
	commandCtx := ctx
	detach := func() bool { return false }
	release := func() {}
	if opts.Detach != nil && !opts.Interactive {
		detached, cancelDetached := context.WithCancelCause(context.WithoutCancel(ctx))
		stop := context.AfterFunc(ctx, func() { cancelDetached(context.Cause(ctx)) })
		commandCtx, detach = detached, stop
		release = func() { stop(); cancelDetached(nil) }
	}
	handedOver := false
	defer func() {
		if !handedOver {
			release()
		}
	}()

	cmd, err := Command(commandCtx, opts.Shell, opts.Command)
	if err != nil {
		return Result{Exit: err.Error()}
	}
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	} else {
		cmd.Dir = WorkingDir(ctx)
	}
	policy := opts.Sandbox
	if policy == nil {
		policy = sandbox.FromContext(ctx)
	}
	if policy != nil && policy.Enabled() {
		cmd, err = policy.Wrap(commandCtx, cmd)
		if err != nil {
			return Result{Exit: err.Error()}
		}
	}
	env := cmd.Env
	if env == nil {
		// A wrapped command carries the environment the sandbox filtered.
		env = os.Environ()
	}
	cmd.Env = append(ScrubEnv(env), ChildMarkers...)
	if len(opts.Env) > 0 {
		cmd.Env = append(cmd.Env, opts.Env...)
	}

	if opts.Interactive {
		return runInteractive(ctx, cmd, opts)
	}
	res := runDetachable(ctx, cmd, opts.OnUpdate, opts.Detach, detach, release)
	handedOver = res.Detached != nil
	return res
}

const updateInterval = 100 * time.Millisecond

// pipedRun is a started command whose output is drained into a bounded buffer,
// or into sink once a detached command's owner takes the stream over.
type pipedRun struct {
	cmd     *exec.Cmd
	started time.Time
	state   *trackedProcess
	stdout  *os.File
	stderr  *os.File
	wg      sync.WaitGroup

	waited  chan struct{}
	waitErr error

	mu   sync.Mutex
	out  outputBuffer
	sink io.Writer
}

func (r *pipedRun) drain(f *os.File) {
	defer r.wg.Done()
	buf := make([]byte, 4096)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			r.mu.Lock()
			if r.sink != nil {
				_, _ = r.sink.Write(buf[:n])
			} else {
				r.out.Write(buf[:n])
			}
			r.mu.Unlock()
		}
		if rerr != nil {
			return
		}
	}
}

// finish waits for the command and its output, then closes the pipes. A
// grandchild still holding them open is given a short grace period.
func (r *pipedRun) finish() (waitErr error, killed bool) {
	<-r.waited
	killed = r.state.finish(r.waitErr)

	drained := make(chan struct{})
	go func() { r.wg.Wait(); close(drained) }()
	graceTimer := time.NewTimer(500 * time.Millisecond)
	select {
	case <-drained:
		graceTimer.Stop()
	case <-graceTimer.C:
	}

	_ = r.stdout.Close()
	_ = r.stderr.Close()
	r.wg.Wait()
	return r.waitErr, killed
}

func runPiped(ctx context.Context, cmd *exec.Cmd, onUpdate func(string)) Result {
	return runDetachable(ctx, cmd, onUpdate, nil, nil, nil)
}

// runDetachable runs cmd like runPiped; once detachReq fires and detach
// succeeds it returns the still-running command instead of waiting for it.
func runDetachable(ctx context.Context, cmd *exec.Cmd, onUpdate func(string), detachReq <-chan struct{}, detach func() bool, release func()) Result {

	stdout, outW, err := os.Pipe()
	if err != nil {
		return Result{Exit: "pipe: " + err.Error()}
	}
	stderr, errW, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		_ = outW.Close()
		return Result{Exit: "pipe: " + err.Error()}
	}
	cmd.Stdout = outW
	cmd.Stderr = errW
	if devNull := openDevNull(); devNull != nil {
		cmd.Stdin = devNull
		defer devNull.Close()
	}

	process.Configure(cmd, true)

	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = outW.Close()
		_ = stderr.Close()
		_ = errW.Close()
		return Result{Exit: exitString(err)}
	}

	_ = outW.Close()
	_ = errW.Close()
	r := &pipedRun{cmd: cmd, started: time.Now(), state: track(cmd), stdout: stdout, stderr: stderr, waited: make(chan struct{})}
	r.wg.Add(2)
	go r.drain(stdout)
	go r.drain(stderr)
	go func() { r.waitErr = cmd.Wait(); close(r.waited) }()

	if onUpdate != nil {
		updatesDone := make(chan struct{})
		stopped := make(chan struct{})
		defer func() { close(updatesDone); <-stopped }()
		go func() {
			defer close(stopped)
			var previous int64
			ticker := time.NewTicker(updateInterval)
			defer ticker.Stop()
			for {
				select {
				case <-updatesDone:
					return
				case <-ticker.C:
					r.mu.Lock()
					if r.out.total == previous {
						r.mu.Unlock()
						continue
					}
					previous = r.out.total
					snap := r.out.snapshot()
					r.mu.Unlock()
					onUpdate(snap)
				}
			}
		}()
	}

	select {
	case <-r.waited:
	case <-detachReq:
		select {
		case <-r.waited:
		default:
			// detach fails once ctx has ended: the command is being killed.
			if detach() {
				return Result{Detached: newDetached(r, release)}
			}
		}
	}

	waitErr, killed := r.finish()
	untrack(cmd)

	r.mu.Lock()
	res := Result{Output: r.out.result(), OutputFile: r.out.path(), OutputBytes: r.out.total}
	r.mu.Unlock()
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.Killed = true
		res.Exit = "timed out"
		return res
	}
	if isCancelled(ctx, waitErr) {
		res.Killed = true
		res.Exit = "cancelled"
		return res
	}
	res.Exit = exitString(waitErr)
	res.Killed = killed
	res.ExitCode = exitCode(cmd)
	return res
}

// exitCode is the status of a command that has exited on its own; nil when it
// never ran or a signal ended it.
func exitCode(cmd *exec.Cmd) *int {
	state := cmd.ProcessState
	if state == nil || !state.Exited() {
		return nil
	}
	code := state.ExitCode()
	return &code
}

func exitString(err error) string {
	if err == nil {
		return ""
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return fmt.Sprintf("(exit: %s)", exitErr)
	}
	return fmt.Sprintf("(exit: %v)", err)
}

func isKilledBySignal(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
		return ws.Signaled()
	}
	return false
}

func isCancelled(ctx context.Context, _ error) bool {
	return errors.Is(ctx.Err(), context.Canceled)
}

func openDevNull() *os.File {
	f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil
	}
	return f
}

var (
	trackMu sync.Mutex
	tracked = map[int]*trackedProcess{}
)

type trackedProcess struct {
	cmd      *exec.Cmd
	mu       sync.Mutex
	killed   bool
	finished bool
}

func (p *trackedProcess) kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished || p.killed || p.cmd.Process == nil {
		return
	}
	p.killed = process.Kill(p.cmd) == nil
}

func (p *trackedProcess) finish(err error) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.finished = true
	return err != nil && (p.killed || isKilledBySignal(err))
}

func track(cmd *exec.Cmd) *trackedProcess {
	p := &trackedProcess{cmd: cmd}
	if cmd.Process == nil {
		return p
	}
	trackMu.Lock()
	tracked[cmd.Process.Pid] = p
	trackMu.Unlock()
	return p
}

func untrack(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	trackMu.Lock()
	if p := tracked[cmd.Process.Pid]; p != nil && p.cmd == cmd {
		delete(tracked, cmd.Process.Pid)
	}
	trackMu.Unlock()
}

func KillAll() {
	trackMu.Lock()
	procs := make([]*trackedProcess, 0, len(tracked))
	for _, c := range tracked {
		procs = append(procs, c)
	}
	trackMu.Unlock()
	for _, p := range procs {
		p.kill()
	}

	deadline := time.Now().Add(2 * time.Second)
	for _, p := range procs {
		c := p.cmd
		if c.Process == nil {
			continue
		}
		for time.Now().Before(deadline) {
			if !process.Alive(c) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

const (
	KeyEnter = "\r"
	KeyEsc   = "\x1b"
	KeyTab   = "\t"
	KeyBS    = "\x7f"
	KeyUp    = "\x1b[A"
	KeyDown  = "\x1b[B"
	KeyRight = "\x1b[C"
	KeyLeft  = "\x1b[D"
)

func KeyBytes(name string) string {
	switch name {
	case "enter":
		return KeyEnter
	case "esc":
		return KeyEsc
	case "tab":
		return KeyTab
	case "backspace", "delete":
		return KeyBS
	case "up":
		return KeyUp
	case "down":
		return KeyDown
	case "right":
		return KeyRight
	case "left":
		return KeyLeft
	}
	return ""
}
