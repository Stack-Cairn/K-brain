package acp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Stack-Cairn/K-brain/internal/session"
)

const sessionPageSize = 100

type sessionListCursor struct {
	Version int                `json:"version"`
	After   session.PageCursor `json:"after"`
	CWD     *string            `json:"cwd"`
}

func (b *Bridge) ListSessions(ctx context.Context, params acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	if b.store == nil {
		return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
	}
	options := session.PageOptions{CWD: params.Cwd, Limit: sessionPageSize}
	if params.Cursor != nil {
		cursor, err := decodeSessionCursor(*params.Cursor, params.Cwd)
		if err != nil {
			return acp.ListSessionsResponse{}, acp.NewInvalidParams(err.Error())
		}
		options.After = &cursor.After
	}
	page, err := b.store.ListPage(ctx, options)
	if err != nil {
		return acp.ListSessionsResponse{}, acp.NewInternalError(err.Error())
	}
	out := acp.ListSessionsResponse{Sessions: make([]acp.SessionInfo, 0, len(page.Sessions))}
	for _, meta := range page.Sessions {
		info := acp.SessionInfo{SessionId: acp.SessionId(meta.ID), Cwd: meta.CWD}
		if meta.Title != "" {
			info.Title = new(meta.Title)
		}
		if !meta.UpdatedAt.IsZero() {
			info.UpdatedAt = new(meta.UpdatedAt.UTC().Format(time.RFC3339))
		}
		out.Sessions = append(out.Sessions, info)
	}
	if page.Next != nil {
		encoded, err := json.Marshal(sessionListCursor{Version: 1, After: *page.Next, CWD: params.Cwd})
		if err != nil {
			return acp.ListSessionsResponse{}, acp.NewInternalError(err.Error())
		}
		out.NextCursor = new(base64.RawURLEncoding.EncodeToString(encoded))
	}
	return out, nil
}

func decodeSessionCursor(raw string, cwd *string) (sessionListCursor, error) {
	invalid := errors.New("invalid session cursor; restart listing without a cursor")
	if raw == "" || len(raw) > 8192 {
		return sessionListCursor{}, invalid
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return sessionListCursor{}, invalid
	}
	var cursor sessionListCursor
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cursor); err != nil {
		return sessionListCursor{}, invalid
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return sessionListCursor{}, invalid
	}
	if cursor.Version != 1 || !cursor.After.Valid() {
		return sessionListCursor{}, invalid
	}
	if (cursor.CWD == nil) != (cwd == nil) || cursor.CWD != nil && *cursor.CWD != *cwd {
		return sessionListCursor{}, invalid
	}
	return cursor, nil
}
