package acp

import (
	"context"
	"errors"
	"fmt"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/session/recording"
)

func (b *Bridge) restoreSession(ctx context.Context, id acp.SessionId, cwd string, servers []acp.McpServer, replay bool) error {
	if err := ctx.Err(); err != nil {
		return acp.NewInternalError(err.Error())
	}
	meta, _, err := b.store.Load(string(id))
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return &acp.RequestError{Code: -32002, Message: "Resource not found", Data: map[string]any{"sessionId": string(id)}}
		}
		return acp.NewInternalError(fmt.Sprintf("session storage: %v", err))
	}
	if meta.ID != string(id) {
		return acp.NewInvalidParams(fmt.Sprintf("session id %q is not exact", id))
	}
	if cwd != "" && meta.CWD != "" && cwd != meta.CWD {
		return acp.NewInvalidParams(fmt.Sprintf("cwd %q does not match session cwd %q", cwd, meta.CWD))
	}
	ctx, finish, err := b.beginSession(ctx, id)
	if err != nil {
		return err
	}
	defer finish()
	ag, mgr, err := b.newAg(ctx, meta.CWD, b.mergeMCPServers(servers))
	if err != nil {
		return acp.NewInternalError(err.Error())
	}
	ag.WorkingDir = meta.CWD
	s := newACPSession(id, ag, mgr)
	initialLen := len(ag.Messages)
	s.recorder, err = recording.Open(b.store, string(id), ag)
	if err != nil {
		s.close()
		return acp.NewInternalError(fmt.Sprintf("session storage: %v", err))
	}
	msgs := ag.Messages[initialLen:]
	registered := false
	defer func() {
		if !registered {
			s.close()
		}
	}()
	if err := ag.StartSession(ctx); err != nil {
		return acp.NewInternalError(err.Error())
	}
	if replay {
		for _, u := range replayUpdates(msgs) {
			if err := b.update(ctx, id, u); err != nil {
				return acp.NewInternalError(err.Error())
			}
		}
	}
	if err := b.registerSession(ctx, s); err != nil {
		return err
	}
	registered = true
	return nil
}
