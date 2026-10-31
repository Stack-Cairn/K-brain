package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/process"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

const (
	liveagentProcessDefaultWait = 30 * time.Second
	liveagentProcessMaxWait     = 5 * time.Minute
	liveagentProcessDefaultLog  = 64 * 1024
	liveagentProcessMaxLog      = 512 * 1024
	liveagentProcessStopWait    = 5 * time.Second
)

type liveagentProcessStatus string

const (
	liveagentProcessRunning   liveagentProcessStatus = "running"
	liveagentProcessCompleted liveagentProcessStatus = "completed"
	liveagentProcessFailed    liveagentProcessStatus = "failed"
	liveagentProcessCancelled liveagentProcessStatus = "cancelled"
	liveagentProcessTimedOut  liveagentProcessStatus = "timed_out"
)

type liveagentProcess struct {
	id             string
	command        string
	conversationID string
	runID          string
	cwd            string
	label          string
	pid            int
	startedAt      time.Time
	finishedAt     *time.Time
	cmd            *exec.Cmd
	log            *os.File
	isolated       bool

	mu       sync.Mutex
	status   liveagentProcessStatus
	exitCode *int
	bytes    int64
	done     chan struct{}
	closed   bool
	stopOnce sync.Once
}

type liveagentProcessManager struct {
	mu        sync.Mutex
	processes map[string]*liveagentProcess
	closed    bool
}

func newLiveAgentProcessManager() *liveagentProcessManager {
	return &liveagentProcessManager{processes: make(map[string]*liveagentProcess)}
}

// ManagedProcessManager retains tool processes across model changes in a session.
type ManagedProcessManager = liveagentProcessManager

func NewManagedProcessManager() *ManagedProcessManager { return newLiveAgentProcessManager() }

func (m *liveagentProcessManager) CloseAll() {
	m.mu.Lock()
	m.closed = true
	ids := make([]string, 0, len(m.processes))
	for id, p := range m.processes {
		if !p.isolated {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		_, _, _ = m.stop(id)
	}
}

func (m *liveagentProcessManager) start(ctx context.Context, command, cwd, label string, timeout time.Duration, isolated bool) (*liveagentProcess, error) {
	return m.startWithID(ctx, "mp", command, cwd, label, timeout, isolated)
}

func (m *liveagentProcessManager) startBashSession(ctx context.Context, command, cwd string, timeout time.Duration) (*liveagentProcess, error) {
	return m.startWithID(ctx, "bash", command, cwd, "", timeout, false)
}

func (m *liveagentProcessManager) startWithID(ctx context.Context, prefix, command, cwd, label string, timeout time.Duration, isolated bool) (*liveagentProcess, error) {
	if strings.TrimSpace(command) == "" {
		return nil, errors.New("command is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	log, err := os.CreateTemp("", "k-brain-managed-process-*.log")
	if err != nil {
		return nil, fmt.Errorf("create process log: %w", err)
	}
	policy := sandbox.FromContext(ctx)
	commandContext := ctx
	if isolated {
		commandContext = bashrun.WithShell(sandbox.WithPolicy(context.Background(), policy), bashrun.ResolveShell(ctx, ""))
	}
	cmd, err := liveagentShellCommand(commandContext, command)
	if err != nil {
		_ = log.Close()
		return nil, err
	}
	cmd.Dir = cwd
	if policy != nil && policy.Enabled() {
		cmd, err = policy.Wrap(commandContext, cmd)
		if err != nil {
			_ = log.Close()
			return nil, err
		}
	}
	identity, _ := RunIdentityFromContext(ctx)
	p := &liveagentProcess{
		id: newLiveAgentProcessID(prefix), command: command, conversationID: identity.ConversationID, runID: identity.RunID, cwd: cwd, label: label,
		startedAt: time.Now(), cmd: cmd, log: log, isolated: isolated,
		status: liveagentProcessRunning, done: make(chan struct{}),
	}
	writer := &liveagentProcessWriter{p: p, file: log}
	cmd.Stdout, cmd.Stderr = writer, writer
	process.Configure(cmd, false)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		_ = log.Close()
		_ = os.Remove(log.Name())
		return nil, errors.New("managed process manager is closed")
	}
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		return nil, err
	}
	p.pid = cmd.Process.Pid
	m.processes[p.id] = p
	go m.reap(commandContext, p, timeout)
	return p, nil
}

type liveagentProcessWriter struct {
	p    *liveagentProcess
	file *os.File
}

func (w *liveagentProcessWriter) Write(data []byte) (int, error) {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	if w.p.closed {
		return len(data), nil
	}
	if _, err := w.file.Write(data); err != nil {
		return 0, err
	}
	w.p.bytes += int64(len(data))
	return len(data), nil
}

func (m *liveagentProcessManager) reap(ctx context.Context, p *liveagentProcess, timeout time.Duration) {
	result := make(chan error, 1)
	go func() { result <- p.cmd.Wait() }()
	var timer <-chan time.Time
	var timeoutTimer *time.Timer
	if timeout > 0 {
		timeoutTimer = time.NewTimer(timeout)
		defer timeoutTimer.Stop()
		timer = timeoutTimer.C
	}
	var waitErr error
	select {
	case waitErr = <-result:
	case <-timer:
		p.mu.Lock()
		if p.status == liveagentProcessRunning {
			p.status = liveagentProcessTimedOut
		}
		p.mu.Unlock()
		_ = process.Kill(p.cmd)
		waitErr = <-result
	}

	p.mu.Lock()
	if p.status == liveagentProcessRunning {
		if ctx.Err() != nil {
			p.status = liveagentProcessCancelled
		} else if waitErr == nil {
			p.status = liveagentProcessCompleted
		} else {
			p.status = liveagentProcessFailed
		}
	}
	code := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok && exitErr.ProcessState != nil {
			code = exitErr.ProcessState.ExitCode()
		} else {
			code = -1
		}
	}
	p.exitCode = &code
	now := time.Now()
	p.finishedAt = &now
	p.closed = true
	_ = p.log.Sync()
	_ = p.log.Close()
	close(p.done)
	p.mu.Unlock()
}

func (m *liveagentProcessManager) getOwned(ctx context.Context, id string) (*liveagentProcess, error) {
	p, err := m.get(id)
	if err != nil {
		return nil, err
	}
	identity, ok := RunIdentityFromContext(ctx)
	if !ok && p.conversationID == "" && p.runID == "" {
		return p, nil
	}
	if !ok {
		return nil, errors.New("managed process requires canonical run identity")
	}
	p.mu.Lock()
	conversationID, runID := p.conversationID, p.runID
	p.mu.Unlock()
	if conversationID == "" || runID == "" || conversationID != identity.ConversationID || runID != identity.RunID {
		return nil, errors.New("managed process belongs to another canonical run")
	}
	return p, nil
}

func (m *liveagentProcessManager) get(id string) (*liveagentProcess, error) {
	m.mu.Lock()
	p := m.processes[strings.TrimSpace(id)]
	m.mu.Unlock()
	if p == nil {
		return nil, fmt.Errorf("managed process not found: %s", id)
	}
	return p, nil
}

func (m *liveagentProcessManager) stopOwned(ctx context.Context, id string) (*liveagentProcess, bool, error) {
	p, err := m.getOwned(ctx, id)
	if err != nil {
		return nil, false, err
	}
	return m.stop(p.id)
}

func (m *liveagentProcessManager) stop(id string) (*liveagentProcess, bool, error) {
	p, err := m.get(id)
	if err != nil {
		return nil, false, err
	}
	p.mu.Lock()
	running := p.status == liveagentProcessRunning
	if running {
		p.status = liveagentProcessCancelled
	}
	p.mu.Unlock()
	if !running {
		return p, false, nil
	}
	var killErr error
	p.stopOnce.Do(func() { killErr = process.Kill(p.cmd) })
	if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) && !errors.Is(killErr, os.ErrPermission) {
		// The process can exit on its own between the status check and Kill; on
		// Windows Kill then fails with "invalid argument". It is stopped if it is gone.
		select {
		case <-p.done:
			return p, true, nil
		case <-time.After(liveagentProcessStopWait):
			return p, false, killErr
		}
	}
	select {
	case <-p.done:
	case <-time.After(liveagentProcessStopWait):
		return p, false, errors.New("process did not stop within 5s")
	}
	return p, true, nil
}

func (p *liveagentProcess) snapshot() liveagentProcessSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	var exit *int
	if p.exitCode != nil {
		v := *p.exitCode
		exit = &v
	}
	var finished *int64
	if p.finishedAt != nil {
		v := p.finishedAt.UnixMilli()
		finished = &v
	}
	return liveagentProcessSnapshot{ID: p.id, Label: p.label, Command: p.command, CWD: p.cwd, PID: p.pid, StartedAt: p.startedAt.UnixMilli(), FinishedAt: finished, Status: p.status, ExitCode: exit, LogPath: p.log.Name(), Isolated: p.isolated}
}

type liveagentProcessSnapshot struct {
	ID         string                 `json:"id"`
	Label      string                 `json:"label,omitempty"`
	Command    string                 `json:"command"`
	CWD        string                 `json:"cwd"`
	PID        int                    `json:"pid"`
	StartedAt  int64                  `json:"started_at"`
	FinishedAt *int64                 `json:"finished_at,omitempty"`
	Status     liveagentProcessStatus `json:"status"`
	ExitCode   *int                   `json:"exit_code"`
	LogPath    string                 `json:"log_path"`
	Isolated   bool                   `json:"isolated"`
}

func (p liveagentProcessSnapshot) running() bool { return p.Status == liveagentProcessRunning }

func (m *liveagentProcessManager) read(p *liveagentProcess, cursor, maxBytes int64) (string, int64, bool, int64, error) {
	p.mu.Lock()
	path := p.log.Name()
	p.mu.Unlock()
	file, err := os.Open(path)
	if err != nil {
		return "", cursor, false, 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", cursor, false, 0, err
	}
	cursor = max(0, min(cursor, info.Size()))
	maxBytes = max(1, min(maxBytes, liveagentProcessMaxLog))
	length := min(info.Size()-cursor, maxBytes)
	data := make([]byte, int(length))
	n, err := file.ReadAt(data, cursor)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", cursor, false, 0, err
	}
	next := cursor + int64(n)
	return string(data[:n]), next, next < info.Size(), int64(n), nil
}

func (m *liveagentProcessManager) wait(ctx context.Context, p *liveagentProcess, cursor, yield, maxBytes int64) (string, int64, bool, int64, bool, error) {
	if yield < 1 {
		yield = 1
	}
	deadline := time.NewTimer(time.Duration(yield) * time.Millisecond)
	defer deadline.Stop()
	for {
		p.mu.Lock()
		current := p.bytes
		running := p.status == liveagentProcessRunning
		p.mu.Unlock()
		if current > cursor || !running {
			out, next, trunc, n, err := m.read(p, cursor, maxBytes)
			return out, next, trunc, n, false, err
		}
		select {
		case <-ctx.Done():
			return "", cursor, false, 0, false, ctx.Err()
		case <-p.done:
		case <-deadline.C:
			return "", cursor, false, 0, true, nil
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func liveagentShellCommand(ctx context.Context, command string) (*exec.Cmd, error) {
	return bashrun.Command(ctx, "", command)
}

func newLiveAgentProcessID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func liveagentProcessWaitBounds(a map[string]any) (int64, int64) {
	yield := int64(laInt(a, "yield_time_ms", int(liveagentProcessDefaultWait/time.Millisecond)))
	if yield < 1 {
		yield = 1
	}
	if yield > int64(liveagentProcessMaxWait/time.Millisecond) {
		yield = int64(liveagentProcessMaxWait / time.Millisecond)
	}
	maxBytes := int64(laInt(a, "max_bytes", liveagentProcessDefaultLog))
	if maxBytes < 1 {
		maxBytes = 1
	}
	if maxBytes > liveagentProcessMaxLog {
		maxBytes = liveagentProcessMaxLog
	}
	return yield, maxBytes
}

func liveagentProcessText(action string, snap liveagentProcessSnapshot, output string, cursor, bytes int64, truncated, timedOut bool) string {
	lines := []string{fmt.Sprintf("ManagedProcess %s", action), fmt.Sprintf("process_id=%s", snap.ID), fmt.Sprintf("status=%s", snap.Status), fmt.Sprintf("pid=%d", snap.PID), fmt.Sprintf("cwd=%s", snap.CWD), fmt.Sprintf("log=%s", snap.LogPath), fmt.Sprintf("cursor=%d", cursor), fmt.Sprintf("bytes=%d", bytes)}
	if snap.ExitCode != nil {
		lines = append(lines, fmt.Sprintf("exit_code=%d", *snap.ExitCode))
	}
	if truncated {
		lines = append(lines, "output_truncated=true")
	}
	if timedOut {
		lines = append(lines, "timed_out=true")
	}
	lines = append(lines, "", output)
	if snap.running() {
		lines = append(lines, fmt.Sprintf("Continue with ManagedProcess(action=\"wait\", process_id=\"%s\", cursor=%d).", snap.ID, cursor))
	}
	return strings.Join(lines, "\n")
}

func liveagentProcessStart(ctx context.Context, manager *liveagentProcessManager, a map[string]any) (string, error) {
	command := laString(a, "command")
	ctx = bashrun.WithShell(ctx, bashrun.ResolveShell(ctx, laString(a, "shell")))
	if err := liveagentValidateShellBackground(ctx, command, true); err != nil {
		return "", err
	}
	cwd, err := liveagentPath(ctx, laString(a, "cwd"), "write", "ManagedProcess", true)
	if err != nil {
		if laString(a, "cwd") == "" {
			cwd, err = liveagentPath(ctx, WorkingDir(ctx), "write", "ManagedProcess", true)
		}
		if err != nil {
			return "", err
		}
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return "", errors.New("ManagedProcess.cwd must be a directory")
	}
	if deny := checkGate(ctx, "ManagedProcess", command); deny != "" {
		return "", errors.New(deny)
	}
	var timeout time.Duration
	if n, ok := a["timeout_ms"].(float64); ok && n > 0 {
		timeout = time.Duration(n) * time.Millisecond
	}
	p, err := manager.start(ctx, command, cwd, laString(a, "label"), timeout, laBool(a, "isolated", false))
	if err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		_, _, _ = manager.stop(p.id)
		return "", ctx.Err()
	}
	snap := p.snapshot()
	reportProcessStarted(ctx, snap.ID)
	return fmt.Sprintf("ManagedProcess started\nprocess_id=%s\npid=%d\ncwd=%s\nlog=%s\ncommand=%s\nisolated=%t\n\nWait with ManagedProcess(action=\"wait\", process_id=\"%s\", cursor=0).", snap.ID, snap.PID, snap.CWD, snap.LogPath, snap.Command, snap.Isolated, snap.ID), nil
}

func liveagentValidateManagedCommand(command string) error {
	var quote byte
	escaped := false
	for i := 0; i < len(command); i++ {
		c := command[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '#' && (i == 0 || strings.ContainsRune(" \t\n;|&()", rune(command[i-1]))) {
			for i+1 < len(command) && command[i+1] != '\n' {
				i++
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			continue
		}
		if c == '&' {
			if i+1 < len(command) && command[i+1] == '&' {
				i++
				continue
			}
			if i > 0 && (command[i-1] == '>' || command[i-1] == '<') {
				continue
			}
			if i+1 < len(command) && command[i+1] == '>' {
				continue
			}
			return errors.New("ManagedProcess.command must be a foreground command; do not append &")
		}
	}
	return nil
}

func (m *liveagentProcessManager) HasRunningProcesses() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.processes {
		select {
		case <-p.done:
		default:
			return true
		}
	}
	return false
}
