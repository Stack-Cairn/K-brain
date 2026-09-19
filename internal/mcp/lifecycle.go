package mcp

import "context"

func (m *Manager) ownsLocked(s *server) bool {
	return !m.closed && m.servers[s.name] == s && s.ctx.Err() == nil
}

func (m *Manager) launchLocked(s *server, fn func()) bool {
	if !m.ownsLocked(s) {
		return false
	}
	m.workers.Add(1)
	s.workers.Add(1)
	go func() {
		defer m.workers.Done()
		defer s.workers.Done()
		fn()
	}()
	return true
}

func (m *Manager) startLocked(ctx context.Context, s *server) {
	if s.started || !m.ownsLocked(s) {
		return
	}
	s.started = true
	if ctx.Err() != nil {
		s.cancel()
		s.mu.Lock()
		s.setStateLocked(StatusFailed, ctx.Err().Error())
		s.mu.Unlock()
		return
	}
	stop := context.AfterFunc(ctx, s.cancel)
	if !m.launchLocked(s, func() {
		defer stop()
		s.run(s.ctx, m)
	}) {
		stop()
		s.mu.Lock()
		s.setStateLocked(StatusFailed, context.Canceled.Error())
		s.mu.Unlock()
	}
}

func (s *server) stopSession(m *Manager) {
	m.onChangeMu.Lock()
	s.mu.Lock()
	sess := s.sess
	s.sess, s.defs, s.instr = nil, nil, ""
	s.gen++
	changed := !m.closed && m.servers[s.name] == s
	if changed {
		s.setStateLocked(StatusFailed, context.Canceled.Error())
	}
	s.mu.Unlock()
	m.onChangeMu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
	if changed {
		m.fireOnChange()
	}
}

func (s *server) setStateLocked(st Status, errMsg string) {
	firstSettle := !s.settled && st != StatusConnecting
	if st != StatusConnecting {
		s.settled = true
	}
	s.status, s.err = st, errMsg
	logf("server %s -> %s %s", s.name, st, errMsg)
	if firstSettle {
		close(s.ready)
	}
}
