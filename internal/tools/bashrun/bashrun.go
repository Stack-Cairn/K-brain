package bashrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/process"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
)

func userShell() string {
	if sh := os.Getenv("K_BRAIN_SHELL"); sh != "" {
		return sh
	}
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("pwsh.exe"); err == nil {
			return "pwsh.exe"
		}
		return "powershell.exe"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	if sh := passwdShell(); sh != "" {
		return sh
	}
	return "bash"
}

func passwdShell() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(data)) {
		fields := strings.Split(strings.TrimRight(line, "\n"), ":")
		if len(fields) == 7 && fields[2] == u.Uid {
			return fields[6]
		}
	}
	return ""
}

type Result struct {
	Output string

	Exit string

	TimedOut bool

	Killed bool

	Interactive bool
}

type Options struct {
	Command string

	Shell string

	Timeout time.Duration

	Interactive bool

	InactivityTimeout time.Duration

	OnOutput func(chunk string)

	OnUpdate func(outputSoFar string)

	OnAwaitInput func(secLeft int)

	Keys <-chan []byte

	Env []string

	Sandbox *sandbox.Policy
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

	shell := opts.Shell
	if shell == "" {
		shell, _ = ctx.Value(shellKey{}).(string)
	}
	cmd, err := shellCommand(ctx, shell, opts.Command)
	if err != nil {
		return Result{Exit: err.Error()}
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
	cmd.Env = append(os.Environ(), ChildMarkers...)
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
	track(cmd)
	defer untrack(cmd)

	var out bytes.Buffer
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
		defer close(updatesDone)
		go func() {
			ticker := time.NewTicker(updateInterval)
			defer ticker.Stop()
			for {
				select {
				case <-updatesDone:
					return
				case <-ticker.C:
					mu.Lock()
					snap := out.String()
					mu.Unlock()
					onUpdate(snap)
				}
			}
		}()
	}

	waitErr := cmd.Wait()

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

	res := Result{Output: out.String()}
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
	if waitErr != nil {
		res.Killed = isKilledBySignal(waitErr)
	}
	return res
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
	tracked = map[int]*exec.Cmd{}
)

func track(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	trackMu.Lock()
	tracked[cmd.Process.Pid] = cmd
	trackMu.Unlock()
}

func untrack(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	trackMu.Lock()
	delete(tracked, cmd.Process.Pid)
	trackMu.Unlock()
}

func KillAll() {
	trackMu.Lock()
	procs := make([]*exec.Cmd, 0, len(tracked))
	for _, c := range tracked {
		procs = append(procs, c)
	}
	trackMu.Unlock()
	for _, c := range procs {
		if c.Process != nil {

			_ = process.Kill(c)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for _, c := range procs {
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
