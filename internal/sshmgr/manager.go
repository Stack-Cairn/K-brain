// Package sshmgr runs SSHManager: SSH connections, shells, remote commands
// and SFTP for the hosts stored in sshhosts, opened by the K-brain process
// itself.
//
// A session is one SSH connection to one host, owned by a project (the
// ProjectPathKey of the run's working directory) and shared by every
// conversation in that project. It may carry an interactive PTY shell and an
// SFTP client, both started on first use. Sessions stay open across turns
// and close after IdleTimeout without use, when their host's settings
// change, or when the backend closes.
package sshmgr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/sshhosts"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	// DefaultIdleTimeout closes a session nobody used for this long.
	DefaultIdleTimeout = 30 * time.Minute
	keepaliveInterval  = 30 * time.Second
	keepaliveTimeout   = 15 * time.Second
	maxTransfers       = 32
)

// Options configure a Manager.
type Options struct {
	IdleTimeout time.Duration
	// dial replaces the network dialer in tests.
	dial dialer
}

// Manager owns every SSH session of the backend.
type Manager struct {
	hosts *sshhosts.Store
	idle  time.Duration
	dial  dialer

	mu       sync.Mutex
	sessions map[string]*Session
	slots    map[string]chan struct{}
	closed   bool
	stop     chan struct{}
	done     chan struct{}
}

// New starts a manager for the hosts in store.
func New(store *sshhosts.Store, opts Options) *Manager {
	m := &Manager{hosts: store, idle: opts.IdleTimeout, dial: opts.dial, sessions: map[string]*Session{}, slots: map[string]chan struct{}{}, stop: make(chan struct{}), done: make(chan struct{})}
	if m.idle <= 0 {
		m.idle = DefaultIdleTimeout
	}
	if m.dial == nil {
		m.dial = dialHost
	}
	go m.reap()
	return m
}

// Hosts is the host store.
func (m *Manager) Hosts() *sshhosts.Store { return m.hosts }

// Close closes every session.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	close(m.stop)
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = map[string]*Session{}
	m.mu.Unlock()
	<-m.done
	for _, s := range sessions {
		s.close("the backend is closing")
	}
}

// Sync closes the sessions whose host was deleted or whose connection
// settings changed. Call it after every change to the host store.
func (m *Manager) Sync() {
	m.mu.Lock()
	var stale []*Session
	for id, s := range m.sessions {
		h, ok := m.hosts.Host(s.HostID)
		if !ok || h.ConnectionFingerprint() != s.fingerprint {
			stale = append(stale, s)
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
	for _, s := range stale {
		s.close("the host's SSH settings changed")
	}
}

func (m *Manager) reap() {
	defer close(m.done)
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-tick.C:
			m.reapIdle(time.Now())
		}
	}
}

func (m *Manager) reapIdle(now time.Time) {
	m.mu.Lock()
	var idle []*Session
	for id, s := range m.sessions {
		if s.idleSince(now) > m.idle {
			idle = append(idle, s)
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
	for _, s := range idle {
		s.close("idle timeout")
	}
}

// Session is one SSH connection.
type Session struct {
	ID         string
	ProjectKey string
	HostID     string
	Title      string
	CreatedAt  time.Time

	manager     *Manager
	fingerprint string
	client      *ssh.Client

	// sftpInit serializes starting the SFTP client.
	sftpInit sync.Mutex

	mu         sync.Mutex
	updatedAt  time.Time
	busy       int
	closed     bool
	closeCause string
	shell      *shell
	// ptyCols and ptyRows size the shell when it starts; 0 means default.
	ptyCols   int
	ptyRows   int
	sftp      *sftp.Client
	transfers []*transfer
}

// SessionInfo is a session as list_sessions reports it.
type SessionInfo struct {
	SessionID   string `json:"session_id"`
	HostID      string `json:"host_id"`
	Status      string `json:"status"`
	SFTPEnabled bool   `json:"sftpEnabled"`
	Shell       bool   `json:"shell"`
	Title       string `json:"title"`
	Running     bool   `json:"running"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// Info describes s.
func (s *Session) Info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := "connected"
	if s.closed {
		status = "disconnected"
	}
	return SessionInfo{SessionID: s.ID, HostID: s.HostID, Status: status, SFTPEnabled: s.sftp != nil, Shell: s.shell != nil, Title: s.Title, Running: !s.closed,
		CreatedAt: s.CreatedAt.UnixMilli(), UpdatedAt: s.updatedAt.UnixMilli()}
}

func (s *Session) idleSince(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy > 0 {
		return 0
	}
	return now.Sub(s.updatedAt)
}

// use marks s busy until the returned func runs; it fails when s is closed.
func (s *Session) use() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, s.closedError()
	}
	s.busy++
	s.updatedAt = time.Now()
	return func() {
		s.mu.Lock()
		s.busy--
		s.updatedAt = time.Now()
		s.mu.Unlock()
	}, nil
}

func (s *Session) closedError() error {
	return fmt.Errorf("SSH session %s is closed (%s)", s.ID, s.closeCause)
}

func (s *Session) close(cause string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.closeCause = cause
	sh, sf := s.shell, s.sftp
	for _, t := range s.transfers {
		t.cancel()
	}
	s.mu.Unlock()
	if sh != nil {
		sh.close()
	}
	if sf != nil {
		sf.Close()
	}
	s.client.Close()
	if s.manager != nil {
		s.manager.mu.Lock()
		if s.manager.sessions[s.ID] == s {
			delete(s.manager.sessions, s.ID)
		}
		s.manager.mu.Unlock()
	}
}

// watch closes s when the connection drops or stops answering keepalives.
func (s *Session) watch() {
	dropped := make(chan struct{})
	go func() {
		s.client.Wait()
		close(dropped)
		s.close("the connection was closed")
	}()
	tick := time.NewTicker(keepaliveInterval)
	defer tick.Stop()
	for {
		select {
		case <-dropped:
			return
		case <-tick.C:
			reply := make(chan error, 1)
			go func() {
				_, _, err := s.client.SendRequest("keepalive@openssh.com", true, nil)
				reply <- err
			}()
			select {
			case err := <-reply:
				if err != nil {
					s.close("the connection was lost")
					return
				}
			case <-time.After(keepaliveTimeout):
				s.close("the server stopped answering keepalives")
				return
			case <-dropped:
				return
			}
		}
	}
}

// Strategy is how a call finds its session when it names no session_id.
type Strategy string

const (
	ReuseOrCreate   Strategy = "reuse_or_create"
	NewSession      Strategy = "new"
	RequireExisting Strategy = "require_existing"
)

// Resolved is the session a call runs on and how it was found.
type Resolved struct {
	Session  *Session
	Strategy string
	Reused   bool
	Created  bool
}

// errNoSession is returned by require_existing when no session is open.
var errNoSession = errors.New("no SSH session is open for this host in the current project (session_strategy=require_existing)")

// Acquire returns a session to host for project per strategy, connecting
// when needed.
func (m *Manager) Acquire(ctx context.Context, project string, host sshhosts.Host, strategy Strategy, title string, inter Interaction) (Resolved, error) {
	if strategy == NewSession {
		s, err := m.open(ctx, project, host, title, inter)
		return Resolved{Session: s, Strategy: string(strategy), Created: s != nil}, err
	}
	release, err := m.slot(ctx, project+"\x00"+host.ID)
	if err != nil {
		return Resolved{}, err
	}
	defer release()
	if s := m.reusable(project, host); s != nil {
		return Resolved{Session: s, Strategy: string(strategy), Reused: true}, nil
	}
	if strategy == RequireExisting {
		return Resolved{}, errNoSession
	}
	s, err := m.open(ctx, project, host, title, inter)
	return Resolved{Session: s, Strategy: string(strategy), Created: s != nil}, err
}

// slot serializes session acquisition per project and host, so concurrent
// calls reuse one connection instead of each opening their own.
func (m *Manager) slot(ctx context.Context, key string) (func(), error) {
	m.mu.Lock()
	ch := m.slots[key]
	if ch == nil {
		ch = make(chan struct{}, 1)
		m.slots[key] = ch
	}
	m.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// reusable is the oldest open session of project to host with the host's
// current settings.
func (m *Manager) reusable(project string, host sshhosts.Host) *Session {
	fp := host.ConnectionFingerprint()
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *Session
	for _, s := range m.sessions {
		if s.ProjectKey != project || s.HostID != host.ID || s.fingerprint != fp {
			continue
		}
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			continue
		}
		if best == nil || s.CreatedAt.Before(best.CreatedAt) || (s.CreatedAt.Equal(best.CreatedAt) && s.ID < best.ID) {
			best = s
		}
	}
	return best
}

func (m *Manager) open(ctx context.Context, project string, host sshhosts.Host, title string, inter Interaction) (*Session, error) {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, errors.New("the backend is closing")
	}
	c := &connector{host: host, known: m.hosts.KnownHosts(), inter: inter, dial: m.dial, ctx: ctx}
	client, err := c.connect()
	if err != nil {
		return nil, err
	}
	if title == "" {
		title = "SSHManager: " + host.Label()
	}
	now := time.Now()
	s := &Session{ID: newID("ssh"), ProjectKey: project, HostID: host.ID, Title: title, CreatedAt: now, updatedAt: now, manager: m, fingerprint: host.ConnectionFingerprint(), client: client}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		client.Close()
		return nil, errors.New("the backend is closing")
	}
	m.sessions[s.ID] = s
	m.mu.Unlock()
	go s.watch()
	return s, nil
}

// Session returns the open session id of project.
func (m *Manager) Session(project, id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || s.ProjectKey != project {
		return nil, false
	}
	return s, true
}

// Sessions lists the open sessions of project, oldest first.
func (m *Manager) Sessions(project string) []*Session {
	m.mu.Lock()
	var out []*Session
	for _, s := range m.sessions {
		if s.ProjectKey == project {
			out = append(out, s)
		}
	}
	m.mu.Unlock()
	slices.SortFunc(out, func(a, b *Session) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		if a.ID < b.ID {
			return -1
		}
		return 1
	})
	return out
}

// CloseHostSessions closes every session to the host id, in every project.
func (m *Manager) CloseHostSessions(id string) {
	m.mu.Lock()
	var out []*Session
	for _, s := range m.sessions {
		if s.HostID == id {
			out = append(out, s)
		}
	}
	m.mu.Unlock()
	for _, s := range out {
		s.close("the host's trusted keys were removed")
	}
}

// CloseSession closes one session.
func (m *Manager) CloseSession(s *Session) { s.close("closed by SSHManager") }

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}

// fakeAddr is a net.Addr for in-memory connections.
type fakeAddr string

func (a fakeAddr) Network() string { return "pipe" }
func (a fakeAddr) String() string  { return string(a) }

var _ net.Addr = fakeAddr("")
