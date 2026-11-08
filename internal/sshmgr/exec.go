package sshmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Exec limits, as LiveAgent v1.
const (
	DefaultExecTimeout = 30 * time.Second
	MinExecTimeout     = time.Second
	MaxExecTimeout     = 300 * time.Second
	DefaultExecBytes   = 64 << 10
	MinExecBytes       = 4 << 10
	MaxExecBytes       = 256 << 10
	// exitGrace is how long a stopped command may take to report its end.
	exitGrace = 3 * time.Second
)

// ExecResult is the outcome of a remote command. Field names follow
// LiveAgent v1's terminal_ssh_exec result.
type ExecResult struct {
	ExitCode        *int   `json:"exitCode"`
	ExitSignal      string `json:"exitSignal,omitempty"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	StdoutBytes     int64  `json:"stdoutBytes"`
	StderrBytes     int64  `json:"stderrBytes"`
	StdoutTruncated bool   `json:"stdoutTruncated"`
	StderrTruncated bool   `json:"stderrTruncated"`
	TimedOut        bool   `json:"timedOut"`
	Cancelled       bool   `json:"cancelled,omitempty"`
	DurationMs      int64  `json:"durationMs"`
}

// capture keeps the first limit bytes written and counts the rest.
type capture struct {
	mu    sync.Mutex
	limit int
	buf   []byte
	total int64
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total += int64(len(p))
	if room := c.limit - len(c.buf); room > 0 {
		c.buf = append(c.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (c *capture) result() (string, int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.ToValidUTF8(string(c.buf), "�"), c.total, c.total > int64(len(c.buf))
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// Exec runs command on s over a new channel. Cancelling ctx (the run was
// cancelled) or reaching timeout sends SIGTERM and closes the channel.
func (s *Session) Exec(ctx context.Context, command, cwd string, timeout time.Duration, maxBytes int) (ExecResult, error) {
	done, err := s.use()
	if err != nil {
		return ExecResult{}, err
	}
	defer done()
	sess, err := s.client.NewSession()
	if err != nil {
		return ExecResult{}, fmt.Errorf("cannot open a channel on SSH session %s: %v", s.ID, err)
	}
	defer sess.Close()
	stdout, stderr := &capture{limit: maxBytes}, &capture{limit: maxBytes}
	sess.Stdout, sess.Stderr = stdout, stderr
	full := command
	if cwd != "" {
		full = "cd " + shellQuote(cwd) + " && " + command
	}
	started := time.Now()
	if err := sess.Start(full); err != nil {
		return ExecResult{}, fmt.Errorf("the SSH server refused to run the command: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- sess.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var result ExecResult
	var waitErr error
	select {
	case waitErr = <-waited:
	case <-timer.C:
		result.TimedOut = true
		waitErr = stop(sess, waited)
	case <-ctx.Done():
		result.Cancelled = true
		waitErr = stop(sess, waited)
	}
	result.DurationMs = time.Since(started).Milliseconds()
	result.Stdout, result.StdoutBytes, result.StdoutTruncated = stdout.result()
	result.Stderr, result.StderrBytes, result.StderrTruncated = stderr.result()
	var exitErr *ssh.ExitError
	switch {
	case waitErr == nil:
		code := 0
		result.ExitCode = &code
	case errors.As(waitErr, &exitErr):
		if exitErr.Signal() != "" {
			result.ExitSignal = exitErr.Signal()
		} else {
			code := exitErr.ExitStatus()
			result.ExitCode = &code
		}
	}
	if result.Cancelled {
		return result, ctx.Err()
	}
	return result, nil
}

// stop asks the remote command to end, then closes its channel.
func stop(sess *ssh.Session, waited <-chan error) error {
	_ = sess.Signal(ssh.SIGTERM)
	_ = sess.Close()
	select {
	case err := <-waited:
		return err
	case <-time.After(exitGrace):
		return io.ErrUnexpectedEOF
	}
}
