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

	cmd, err := Command(ctx, opts.Shell, opts.Command)
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
		cmd, err = policy.Wrap(ctx, cmd)
		if err != nil {
			return Result{Exit: err.Error()}
		}
	}
	cmd.Env = append(ScrubEnv(os.Environ()), ChildMarkers...)
	if len(opts.Env) > 0 {
		cmd.Env = append(cmd.Env, opts.Env...)
	}

	if opts.Interactive {
		return runInteractive(ctx, cmd, opts)
	}
	return runPiped(ctx, cmd, opts.OnUpdate)
}

const updateInterval = 100 * time.Millisecond

func runPiped(ctx context.Context, cmd *exec.Cmd, onUpdate func(string)) Result {

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
	state := track(cmd)
	defer untrack(cmd)

	var out outputBuffer
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(2)
	drain := func(r io.Reader) {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				mu.Lock()
				out.Write(buf[:n])
				mu.Unlock()
			}
			if rerr != nil {
				return
			}
		}
	}
	go drain(stdout)
	go drain(stderr)

	var updatesDone chan struct{}
	if onUpdate != nil {
		updatesDone = make(chan struct{})
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
					mu.Lock()
					if out.total == previous {
						mu.Unlock()
						continue
					}
					previous = out.total
					snap := out.snapshot()
					mu.Unlock()
					onUpdate(snap)
				}
			}
		}()
	}

	waitErr := cmd.Wait()
	killed := state.finish(waitErr)

	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()
	graceTimer := time.NewTimer(500 * time.Millisecond)
	select {
	case <-drained:
		graceTimer.Stop()
	case <-graceTimer.C:
	}

	_ = stdout.Close()
	_ = stderr.Close()
	wg.Wait()

	mu.Lock()
	res := Result{Output: out.result(), OutputFile: out.path(), OutputBytes: out.total}
	mu.Unlock()
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
