package browser

import (
	"context"
	"errors"
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
	noticed bool
}

const fallbackNotice = "[Note: no debuggable live browser found — using k-brain's dedicated Chrome (logins live in its own profile). To drive your everyday browser instead: chrome://inspect/#remote-debugging, or set browser.mode/cdpUrl in config.]\n\n"

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
	s, ok := m.sessions[key]
	if !ok {
		s = &Session{name: name, mode: mode, sem: make(chan struct{}, 1)}
		m.sessions[key] = s
	}
	return s, nil
}

func (s *Session) Do(ctx context.Context, fn func(b Backend) (string, error)) (string, error) {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	b, err := s.get(ctx)
	if err != nil {
		return "", err
	}
	out, err := fn(b)
	if err != nil && isConnErr(err) {
		s.drop()
		b, rerr := s.get(ctx)
		if rerr != nil {
			return "", err
		}
		out, err = fn(b)
	}

	if err == nil && out != "" {
		out = s.takeNotice(b) + out
	}
	return out, err
}

func (s *Session) takeNotice(b Backend) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.noticed || s.mode != ModeLive || b.Obtained() == ObtainedLive {
		return ""
	}
	s.noticed = true
	return fallbackNotice
}

func (s *Session) get(ctx context.Context) (Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend != nil {
		return s.backend, nil
	}
	b, err := openNamed(ctx, s.mode, s.name)
	if err != nil {
		return nil, err
	}
	s.backend = b
	s.noticed = false
	return b, nil
}

var openNamed = func(ctx context.Context, mode Mode, name string) (Backend, error) {
	return OpenNamed(ctx, mode, name)
}

func (s *Session) drop() {
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
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		s.drop()
	}
}

func isConnErr(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		msg := e.Error()
		if strings.Contains(msg, "websocket") && (strings.Contains(msg, "closed") || strings.Contains(msg, "close 100")) {
			return true
		}
		if strings.Contains(msg, "connection reset") || strings.Contains(msg, "broken pipe") || strings.Contains(msg, "EOF") {
			return true
		}
	}
	return false
}
