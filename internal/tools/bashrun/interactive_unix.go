//go:build !windows

package bashrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/Stack-Cairn/K-brain/internal/process"
)

func runInteractive(ctx context.Context, cmd *exec.Cmd, opts Options) Result {

	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}

	ptmx, err := pty.Start(cmd)
	if err != nil {

		return runPiped(ctx, cmd, nil)
	}
	defer ptmx.Close()
	track(cmd)
	defer untrack(cmd)

	stop := sync.OnceFunc(func() {
		if cmd.Process != nil {
			_ = process.Kill(cmd)
		}
	})
	go func() {
		<-ctx.Done()
		stop()
	}()

	var buf bytes.Buffer
	outCh := make(chan []byte, 16)

	go func() {
		tmp := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(tmp)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, tmp[:n])
				select {
				case outCh <- cp:
				case <-ctx.Done():
					return
				}
			}
			if rerr != nil {
				select {
				case outCh <- nil:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	quiet := time.Now()
	var quietMu sync.Mutex
	touch := func() {
		quietMu.Lock()
		quiet = time.Now()
		quietMu.Unlock()
	}

	keyStop := make(chan struct{})
	defer close(keyStop)
	if opts.Keys != nil {
		go func() {
			for {
				select {
				case b, ok := <-opts.Keys:
					if !ok {
						return
					}
					if len(b) > 0 {
						_, _ = ptmx.Write(b)
					}
					touch()
				case <-keyStop:
					return
				}
			}
		}()
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case chunk, ok := <-outCh:

			if !ok || chunk == nil {
				waitErr := cmd.Wait()
				res := Result{Output: buf.String(), Interactive: true}
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
				return res
			}
			buf.Write(chunk)
			if opts.OnOutput != nil {
				opts.OnOutput(string(chunk))
			}
			touch()
		case <-ticker.C:
			quietMu.Lock()
			idle := time.Since(quiet)
			quietMu.Unlock()

			if ctxErr := ctx.Err(); ctxErr != nil {
				stop()
				_ = cmd.Wait()
				res := Result{Output: buf.String(), Killed: true, Interactive: true}
				if errors.Is(ctxErr, context.DeadlineExceeded) {
					res.TimedOut = true
					res.Exit = "timed out"
				} else {
					res.Exit = "cancelled"
				}
				return res
			}
			if idle >= opts.InactivityTimeout {
				stop()
				_ = cmd.Wait()
				res := Result{
					Output:      buf.String(),
					Exit:        "timed out waiting for input",
					Killed:      true,
					Interactive: true,
				}
				res.Output += fmt.Sprintf(
					"\n[k-brain: interactive command killed after %s with no input]",
					opts.InactivityTimeout.Round(time.Second),
				)
				return res
			}
			if opts.OnAwaitInput != nil {
				secs := max(int((opts.InactivityTimeout-idle+time.Second-1)/time.Second), 0)
				opts.OnAwaitInput(secs)
			}
		}
	}
}
