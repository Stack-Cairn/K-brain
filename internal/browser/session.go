package browser

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

var sessionNameRe = regexp.MustCompile(`\A[A-Za-z0-9_-]{1,64}\z`)

type Manager struct {
	mu       sync.Mutex
	mode     Mode
	sessions map[string]*Session
}

func NewManager(mode Mode) *Manager {
	return &Manager{mode: mode, sessions: map[string]*Session{}}
}

type Session struct {
	name    string
	mode    Mode
	sem     chan struct{}
	mu      sync.Mutex
	backend Backend
}

func (m *Manager) Session(name string) (*Session, error) {
	mode := m.mode
	if i := strings.Index(name, ":"); i > 0 {
		prefix := Mode(name[:i])
		switch prefix {
		case ModeLive, ModeDedicated, ModeHeadless, ModeExtension:
			mode, name = prefix, name[i+1:]
		default:
			return nil, fmt.Errorf("invalid session %q: unknown mode prefix %q (live|dedicated|headless|extension)", name, prefix)
		}
	}
	if name == "" {
		name = "default"
	}
	if !sessionNameRe.MatchString(name) {
		return nil, fmt.Errorf("invalid session name %q: use 1-64 letters, digits, dashes, or underscores", name)
	}
	key := string(mode) + ":" + name
	m.mu.Lock()
	defer m.mu.Unlock()
	if mode == ModeExtension {
		for other, s := range m.sessions {
			if s.mode == ModeExtension && other != key {
				return nil, fmt.Errorf("extension mode has one pinned tab; reuse session %q instead of sharing it with %q", s.name, name)
			}
		}
	}
	s, ok := m.sessions[key]
	if !ok {
		s = &Session{name: name, mode: mode, sem: make(chan struct{}, 1)}
		m.sessions[key] = s
	}
	return s, nil
}

func (s *Session) Do(ctx context.Context, fn func(b Backend) (string, error)) (string, error) {
	return s.do(ctx, false, fn)
}

// DoFresh explicitly releases the current binding before running a new program.
func (s *Session) DoFresh(ctx context.Context, fn func(b Backend) (string, error)) (string, error) {
	return s.do(ctx, true, fn)
}

func (s *Session) do(ctx context.Context, reset bool, fn func(b Backend) (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.sem }()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if reset {
		s.mu.Lock()
		if s.backend != nil {
			if err := s.backend.Close(); err != nil {
				s.mu.Unlock()
				return "", fmt.Errorf("reset browser session: %w", err)
			}
			s.backend = nil
		}
		s.mu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	b, err := s.get(ctx)
	if err != nil {
		return "", err
	}
	if err := b.Prepare(ctx); err != nil {
		return "", fmt.Errorf("prepare browser session: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	out, err := fn(b)
	if err != nil && (isConnErr(err) || isSessionErr(err) || isTargetClosedErr(err) || ctx.Err() != nil) {
		b.Invalidate(err)
		return out, fmt.Errorf("browser script interrupted; earlier actions may have completed and were not replayed: %w", err)
	}
	return out, err
}

func (s *Session) get(ctx context.Context) (Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend != nil {
		return s.backend, nil
	}
	b, err := openNamed(ctx, s.mode, s.name)
	// Only reopen when startup has not bound a target yet.
	if b == nil && err != nil && ctx.Err() == nil && (isConnErr(err) || isSessionErr(err)) {
		b, err = openNamed(ctx, s.mode, s.name)
	}
	if b != nil {
		s.backend = b
	}
	if err != nil {
		if b != nil {
			b.Invalidate(err)
			if ctx.Err() == nil && (isConnErr(err) || isSessionErr(err)) {
				err = b.Prepare(ctx)
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return b, nil
}

var openNamed = func(ctx context.Context, mode Mode, name string) (Backend, error) {
	return OpenNamed(ctx, mode, name)
}

func (s *Session) drop() {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend != nil {
		_ = s.backend.Close()
		s.backend = nil
	}
}

func (m *Manager) SwitchDriver(d string) {
	SetDriver(d)
	m.CloseAll()
}

func (m *Manager) CloseAll() {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		s.drop()
	}
}
