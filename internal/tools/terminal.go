package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/process"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

const (
	terminalDefaultTail = 64 * 1024
	terminalMaxTail     = 512 * 1024
	terminalMaxSessions = 64
)

// TerminalRequest and TerminalResponse mirror the local Gateway v2 terminal protocol.
type TerminalRequest struct {
	Action         string `json:"action"`
	ConversationID string `json:"conversation_id"`
	RunID          string `json:"run_id"`
	SessionID      string `json:"session_id"`
	ProjectPathKey string `json:"project_path_key"`
	CWD            string `json:"cwd"`
	Shell          string `json:"shell"`
	Title          string `json:"title"`
	Data           string `json:"data"`
	Cols           uint16 `json:"cols"`
	Rows           uint16 `json:"rows"`
	MaxBytes       int    `json:"max_bytes"`
}

type TerminalRecord struct {
	ID             string `json:"id"`
	ProjectPathKey string `json:"project_path_key"`
	CWD            string `json:"cwd"`
	Shell          string `json:"shell"`
	Title          string `json:"title"`
	PID            int    `json:"pid"`
	Cols           uint16 `json:"cols"`
	Rows           uint16 `json:"rows"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
	FinishedAt     int64  `json:"finished_at"`
	ExitCode       int    `json:"exit_code"`
	Running        bool   `json:"running"`
	Kind           string `json:"kind"`
}

type TerminalShellOption struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Command string `json:"command"`
}

type TerminalResponse struct {
	Action            string                `json:"action"`
	ConversationID    string                `json:"conversation_id"`
	RunID             string                `json:"run_id"`
	Session           *TerminalRecord       `json:"session,omitempty"`
	Sessions          []TerminalRecord      `json:"sessions,omitempty"`
	Output            []byte                `json:"output,omitempty"`
	Truncated         bool                  `json:"truncated"`
	OutputStartOffset uint64                `json:"output_start_offset"`
	OutputEndOffset   uint64                `json:"output_end_offset"`
	ShellOptions      []TerminalShellOption `json:"shell_options,omitempty"`
	DefaultShell      string                `json:"default_shell,omitempty"`
}

type terminalSession struct {
	identity RunIdentity
	pty      terminalProcess
	done     chan struct{}
	mu       sync.Mutex
	record   TerminalRecord
	buffer   []byte
	bytes    uint64
}

type terminalProcess interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
	Wait() error
	Kill() error
	PID() int
}

type TerminalManager struct {
	mu       sync.Mutex
	sessions map[string]*terminalSession
	closed   bool
}

func NewTerminalManager() *TerminalManager {
	return &TerminalManager{sessions: make(map[string]*terminalSession)}
}

func (m *TerminalManager) Start(ctx context.Context, command, cwd string) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", errors.New("command is required")
	}
	record, err := m.create(ctx, TerminalRequest{CWD: cwd}, command)
	if err != nil {
		return "", err
	}
	return record.ID, nil
}

func (m *TerminalManager) create(ctx context.Context, req TerminalRequest, command string) (TerminalRecord, error) {
	identity, ok := RunIdentityFromContext(ctx)
	if !ok {
		return TerminalRecord{}, errors.New("terminal session requires canonical run identity")
	}
	if err := ctx.Err(); err != nil {
		return TerminalRecord{}, err
	}
	cwd := req.CWD
	if cwd == "" {
		cwd = WorkingDir(ctx)
	}
	cwd, err := liveagentPath(ctx, cwd, "write", "TerminalSession", true)
	if err != nil {
		return TerminalRecord{}, err
	}
	if req.ProjectPathKey != "" {
		project, err := liveagentPath(ctx, req.ProjectPathKey, "read", "TerminalSession", true)
		if err != nil {
			return TerminalRecord{}, err
		}
		if !safeWorkspacePath(project, cwd) {
			return TerminalRecord{}, errors.New("terminal cwd is outside the requested project")
		}
		req.ProjectPathKey = project
	}
	shell := bashrun.DefaultShell()
	if req.Shell != "" && req.Shell != shell {
		return TerminalRecord{}, errors.New("shell must match the backend shell_options entry")
	}
	args := terminalArgs(shell, command)
	cmd := exec.CommandContext(ctx, shell, args...)
	cmd.Dir = cwd
	if policy := sandbox.FromContext(ctx); policy != nil && policy.Enabled() {
		cmd, err = policy.Wrap(ctx, cmd)
		if err != nil {
			return TerminalRecord{}, err
		}
	}
	// PTYs create a new session; Setpgid conflicts with Setsid on Unix.
	process.Configure(cmd, true)
	if req.Cols == 0 {
		req.Cols = 80
	}
	if req.Rows == 0 {
		req.Rows = 24
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return TerminalRecord{}, errors.New("terminal manager is closed")
	}
	if len(m.sessions) >= terminalMaxSessions {
		oldestID := ""
		var oldestTime int64
		for id, session := range m.sessions {
			session.mu.Lock()
			if !session.record.Running && (oldestID == "" || session.record.CreatedAt < oldestTime) {
				oldestID, oldestTime = id, session.record.CreatedAt
			}
			session.mu.Unlock()
		}
		if oldestID == "" {
			return TerminalRecord{}, errors.New("terminal session limit reached")
		}
		delete(m.sessions, oldestID)
	}
	terminal, err := startTerminal(ctx, cmd, req.Cols, req.Rows)
	if err != nil {
		return TerminalRecord{}, fmt.Errorf("start terminal: %w", err)
	}
	now := time.Now().UnixMilli()
	record := TerminalRecord{ID: fmt.Sprintf("term-%d", time.Now().UnixNano()), ProjectPathKey: req.ProjectPathKey, CWD: cwd, Shell: shell, Title: req.Title, PID: terminal.PID(), Cols: req.Cols, Rows: req.Rows, CreatedAt: now, UpdatedAt: now, Running: true, Kind: "local"}
	if record.ProjectPathKey == "" {
		record.ProjectPathKey = cwd
	}
	s := &terminalSession{identity: identity, pty: terminal, done: make(chan struct{}), record: record}
	m.sessions[record.ID] = s
	go m.read(s)
	return record, nil
}

func (m *TerminalManager) read(s *terminalSession) {
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		for {
			n, err := s.pty.Read(buf)
			if n > 0 {
				s.mu.Lock()
				s.bytes += uint64(n)
				s.buffer = append(s.buffer, buf[:n]...)
				if len(s.buffer) > terminalMaxTail {
					s.buffer = append([]byte(nil), s.buffer[len(s.buffer)-terminalMaxTail:]...)
				}
				s.record.UpdatedAt = time.Now().UnixMilli()
				s.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	err := s.pty.Wait()
	// Reap the shell before draining output; descendants may retain the PTY.
	_ = s.pty.Kill()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		_ = s.pty.Close()
		<-readDone
	}
	_ = s.pty.Close()
	s.mu.Lock()
	s.record.Running = false
	s.record.FinishedAt = time.Now().UnixMilli()
	s.record.UpdatedAt = s.record.FinishedAt
	if err != nil {
		s.record.ExitCode = -1
		if exit, ok := err.(interface{ ExitCode() int }); ok {
			s.record.ExitCode = exit.ExitCode()
		}
	}
	close(s.done)
	s.mu.Unlock()
}

func (m *TerminalManager) ownedSession(ctx context.Context, id string) (*terminalSession, error) {
	if _, ok := RunIdentityFromContext(ctx); !ok {
		return nil, errors.New("terminal session requires canonical run identity")
	}
	m.mu.Lock()
	s := m.sessions[strings.TrimSpace(id)]
	m.mu.Unlock()
	if s == nil {
		return nil, fmt.Errorf("terminal session not found: %s", id)
	}
	if !sharesConversation(ctx, s.identity) {
		return nil, errors.New("terminal session belongs to another conversation")
	}
	return s, nil
}

func (m *TerminalManager) snapshot(ctx context.Context, id string, maxBytes int) (TerminalResponse, error) {
	s, err := m.ownedSession(ctx, id)
	if err != nil {
		return TerminalResponse{}, err
	}
	if maxBytes < 1 {
		maxBytes = terminalDefaultTail
	}
	maxBytes = min(maxBytes, terminalMaxTail)
	s.mu.Lock()
	defer s.mu.Unlock()
	start := max(0, len(s.buffer)-maxBytes)
	record := s.record
	output := append([]byte(nil), s.buffer[start:]...)
	return TerminalResponse{Session: &record, Output: output, OutputStartOffset: s.bytes - uint64(len(output)), OutputEndOffset: s.bytes, Truncated: s.bytes > uint64(len(output))}, nil
}

func (m *TerminalManager) Read(ctx context.Context, id string, maxBytes int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	response, err := m.snapshot(ctx, id, maxBytes)
	return string(response.Output), err
}

func (m *TerminalManager) Handle(ctx context.Context, req TerminalRequest) (TerminalResponse, error) {
	identity, ok := RunIdentityFromContext(ctx)
	if !ok || req.ConversationID != identity.ConversationID || req.RunID != identity.RunID {
		return TerminalResponse{}, errors.New("terminal request identity mismatch")
	}
	if err := ctx.Err(); err != nil {
		return TerminalResponse{}, err
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	tool := "TerminalSession"
	if action == "read" || action == "snapshot" || action == "attach" || action == "list" || action == "shell_options" {
		tool = "ReadTerminal"
	}
	if err := Authorize(ctx, tool, action+" "+req.SessionID+" "+req.Data); err != nil {
		return TerminalResponse{}, err
	}
	var response TerminalResponse
	var err error
	switch action {
	case "shell_options":
		shell := bashrun.DefaultShell()
		response.DefaultShell = shell
		response.ShellOptions = []TerminalShellOption{{ID: shell, Label: shell, Command: shell}}
	case "create", "start":
		command := ""
		if action == "start" {
			command = req.Data
			if strings.TrimSpace(command) == "" {
				return response, errors.New("command is required")
			}
		}
		var record TerminalRecord
		record, err = m.create(ctx, req, command)
		response.Session = &record
	case "list", "close_project":
		m.mu.Lock()
		ids := make([]string, 0, len(m.sessions))
		for id, s := range m.sessions {
			if sharesConversation(ctx, s.identity) {
				ids = append(ids, id)
			}
		}
		m.mu.Unlock()
		sort.Strings(ids)
		for _, id := range ids {
			snap, e := m.snapshot(ctx, id, 1)
			if e != nil {
				return response, e
			}
			if req.ProjectPathKey != "" && snap.Session.ProjectPathKey != req.ProjectPathKey {
				continue
			}
			if action == "close_project" {
				if e = m.stop(id); e != nil {
					return response, e
				}
				snap, _ = m.snapshot(ctx, id, 1)
			}
			response.Sessions = append(response.Sessions, *snap.Session)
		}
	case "read", "snapshot", "attach", "input", "resize", "rename", "close":
		var s *terminalSession
		s, err = m.ownedSession(ctx, req.SessionID)
		if err != nil {
			return response, err
		}
		s.mu.Lock()
		project := s.record.ProjectPathKey
		s.mu.Unlock()
		if req.ProjectPathKey != "" && req.ProjectPathKey != project {
			return response, errors.New("terminal session is outside the requested project")
		}
		switch action {
		case "input":
			if len(req.Data) > 64*1024 {
				return response, errors.New("terminal input exceeds 64 KiB")
			}
			_, err = s.pty.Write([]byte(req.Data))
		case "resize":
			if req.Cols == 0 || req.Rows == 0 {
				return response, errors.New("cols and rows are required")
			}
			err = s.pty.Resize(req.Cols, req.Rows)
			if err == nil {
				s.mu.Lock()
				s.record.Cols, s.record.Rows = req.Cols, req.Rows
				s.mu.Unlock()
			}
		case "rename":
			s.mu.Lock()
			s.record.Title = req.Title
			s.mu.Unlock()
		case "close":
			err = m.stop(req.SessionID)
		}
		if err == nil {
			response, err = m.snapshot(ctx, req.SessionID, req.MaxBytes)
		}
	default:
		return response, fmt.Errorf("invalid local terminal action %q", action)
	}
	response.Action, response.ConversationID, response.RunID = action, identity.ConversationID, identity.RunID
	return response, err
}

func (m *TerminalManager) stop(id string) error {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return errors.New("terminal session not found")
	}
	s.mu.Lock()
	running := s.record.Running
	s.mu.Unlock()
	if running {
		_ = s.pty.Kill()
	}
	select {
	case <-s.done:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("terminal session did not stop within 5s")
	}
}

func (m *TerminalManager) CloseAll() {
	m.mu.Lock()
	m.closed = true
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.stop(id)
	}
}

func (m *TerminalManager) HasRunningSessions() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, session := range m.sessions {
		session.mu.Lock()
		running := session.record.Running
		session.mu.Unlock()
		if running {
			return true
		}
	}
	return false
}
