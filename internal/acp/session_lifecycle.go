package acp

import (
	"context"
	"fmt"

	acp "github.com/coder/acp-go-sdk"
)

func (b *Bridge) beginSession(ctx context.Context, id acp.SessionId) (context.Context, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, nil, acp.NewInternalError("bridge is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, acp.NewInternalError(err.Error())
	}
	if id != "" {
		if b.sessions[id] != nil || b.starting[id] {
			return nil, nil, acp.NewInternalError(fmt.Sprintf("session %q is already active or loading", id))
		}
		if b.starting == nil {
			b.starting = make(map[acp.SessionId]bool)
		}
		b.starting[id] = true
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(b.lifetime, cancel)
	b.setupWG.Add(1)
	return ctx, func() {
		stop()
		cancel()
		b.mu.Lock()
		delete(b.starting, id)
		b.mu.Unlock()
		b.setupWG.Done()
	}, nil
}

func (b *Bridge) registerSession(ctx context.Context, s *acpSession) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return acp.NewInternalError("bridge is closed")
	}
	if err := ctx.Err(); err != nil {
		return acp.NewInternalError(err.Error())
	}
	if b.sessions[s.id] != nil {
		return acp.NewInternalError(fmt.Sprintf("session %q is already active", s.id))
	}
	if b.sessions == nil {
		b.sessions = make(map[acp.SessionId]*acpSession)
	}
	b.sessions[s.id] = s
	return nil
}

func (b *Bridge) removeSession(s *acpSession) {
	b.mu.Lock()
	if b.sessions[s.id] == s {
		delete(b.sessions, s.id)
	}
	b.mu.Unlock()
}
