package acp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

const (
	ModeAuto = "auto"
	ModeAsk  = "ask"
)

var modes = []acp.SessionMode{
	{Id: ModeAuto, Name: "Auto", Description: new("Run tools without asking (trusted automation)")},
	{Id: ModeAsk, Name: "Ask", Description: new("Ask the editor before bash/write/edit calls")},
}

type Factory func(ctx context.Context, cwd string, servers map[string]mcp.ServerConfig) (*agent.Agent, *mcp.Manager, error)

type Bridge struct {
	version string
	newAg   Factory
	store   *session.Store
	vision  bool
	mcpBase map[string]mcp.ServerConfig

	conn *acp.AgentSideConnection

	mu       sync.Mutex
	sessions map[acp.SessionId]*acpSession
}

var (
	_ acp.Agent       = (*Bridge)(nil)
	_ acp.AgentLoader = (*Bridge)(nil)
)

func NewBridge(version string, newAgent Factory, store *session.Store, vision bool, mcpBase map[string]mcp.ServerConfig) *Bridge {
	return &Bridge{
		version: version,
		newAg:   newAgent,
		store:   store,
		vision:  vision,
		mcpBase: mcpBase,
	}
}

func (b *Bridge) SetAgentConnection(conn *acp.AgentSideConnection) { b.conn = conn }

type acpSession struct {
	id  acp.SessionId
	ag  *agent.Agent
	mcp *mcp.Manager

	storeFrom int

	turnMu    sync.Mutex
	turnCh    chan struct{}
	cancel    context.CancelFunc
	mode      string
	titleSent bool
	allowed   map[string]bool
}

func newACPSession(id acp.SessionId, ag *agent.Agent, m *mcp.Manager) *acpSession {
	s := &acpSession{id: id, ag: ag, mcp: m, mode: ModeAuto, storeFrom: 1, allowed: map[string]bool{}}
	s.turnCh = make(chan struct{}, 1)
	return s
}

func (s *acpSession) close() {
	s.turnMu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.turnMu.Unlock()
	if s.mcp != nil {
		s.mcp.Close()
	}
}

func (b *Bridge) Initialize(_ context.Context, params acp.InitializeRequest) (acp.InitializeResponse, error) {

	v := acp.ProtocolVersion(acp.ProtocolVersionNumber)
	return acp.InitializeResponse{
		ProtocolVersion: v,
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: b.store != nil,
			PromptCapabilities: acp.PromptCapabilities{
				Image:           b.vision,
				EmbeddedContext: true,
			},
			McpCapabilities: acp.McpCapabilities{Http: true},
			SessionCapabilities: acp.SessionCapabilities{
				List:  &acp.SessionListCapabilities{},
				Close: &acp.SessionCloseCapabilities{},
			},
		},
		AgentInfo: &acp.Implementation{
			Name:    "k-brain",
			Title:   new("k-brain"),
			Version: b.version,
		},
		AuthMethods: []acp.AuthMethod{},
	}, nil
}

func (b *Bridge) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, acp.NewMethodNotFound(acp.AgentMethodAuthenticate)
}

func (b *Bridge) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound("logout")
}

func (b *Bridge) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionResume)
}

func (b *Bridge) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetConfigOption)
}

func (b *Bridge) CloseSession(_ context.Context, params acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	b.mu.Lock()
	s, ok := b.sessions[params.SessionId]
	if ok {
		delete(b.sessions, params.SessionId)
	}
	b.mu.Unlock()
	if !ok {
		return acp.CloseSessionResponse{}, acp.NewInternalError(fmt.Sprintf("unknown session %q", params.SessionId))
	}
	s.close()
	return acp.CloseSessionResponse{}, nil
}

func (b *Bridge) CloseAll() {
	b.mu.Lock()
	ss := make([]*acpSession, 0, len(b.sessions))
	for id, s := range b.sessions {
		ss = append(ss, s)
		delete(b.sessions, id)
	}
	b.mu.Unlock()
	for _, s := range ss {
		s.close()
	}
}

func (b *Bridge) mergeMCPServers(client []acp.McpServer) map[string]mcp.ServerConfig {
	out := make(map[string]mcp.ServerConfig, len(b.mcpBase)+len(client))
	maps.Copy(out, b.mcpBase)
	for _, srv := range client {
		var name string
		var cfg mcp.ServerConfig
		switch {
		case srv.Stdio != nil:
			name = srv.Stdio.Name
			cfg.Command = append([]string{srv.Stdio.Command}, srv.Stdio.Args...)
			cfg.Env = map[string]string{}
			for _, e := range srv.Stdio.Env {
				cfg.Env[e.Name] = e.Value
			}
		case srv.Http != nil:
			name = srv.Http.Name
			cfg.URL = srv.Http.Url
			cfg.Headers = map[string]string{}
			for _, h := range srv.Http.Headers {
				cfg.Headers[h.Name] = h.Value
			}
		default:
			continue
		}
		if name == "" {
			continue
		}
		if _, taken := b.mcpBase[name]; taken {
			config_logf("client MCP server %q shadowed by k-brain config — skipped", name)
			continue
		}
		out[name] = cfg
	}
	return out
}

func (b *Bridge) NewSession(ctx context.Context, params acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if params.Cwd == "" {
		return acp.NewSessionResponse{}, acp.NewInvalidParams("cwd is required")
	}
	ag, mgr, err := b.newAg(ctx, params.Cwd, b.mergeMCPServers(params.McpServers))
	if err != nil {
		return acp.NewSessionResponse{}, acp.NewInternalError(err.Error())
	}
	id := acp.SessionId(newID())
	s := newACPSession(id, ag, mgr)
	if b.store != nil {
		if sid, err := b.store.Create(params.Cwd, ag.ModelName, ag.Provider); err == nil {
			s.id = acp.SessionId(sid)
		}
	}
	b.mu.Lock()
	if b.sessions == nil {
		b.sessions = make(map[acp.SessionId]*acpSession)
	}
	b.sessions[s.id] = s
	b.mu.Unlock()
	return acp.NewSessionResponse{
		SessionId: s.id,
		Modes: &acp.SessionModeState{
			CurrentModeId:  ModeAuto,
			AvailableModes: modes,
		},
	}, nil
}

func (b *Bridge) LoadSession(ctx context.Context, params acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	if b.store == nil {
		return acp.LoadSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionLoad)
	}
	meta, msgs, err := b.store.Load(string(params.SessionId))
	if err != nil {
		return acp.LoadSessionResponse{}, &acp.RequestError{Code: -32002, Message: "Resource not found", Data: map[string]any{"sessionId": string(params.SessionId)}}
	}

	if meta.ID != string(params.SessionId) {
		return acp.LoadSessionResponse{}, acp.NewInvalidParams(fmt.Sprintf("session id %q is not exact", params.SessionId))
	}

	if params.Cwd != "" && meta.CWD != "" && params.Cwd != meta.CWD {
		return acp.LoadSessionResponse{}, acp.NewInvalidParams(fmt.Sprintf("cwd %q does not match session cwd %q", params.Cwd, meta.CWD))
	}
	ag, mgr, err := b.newAg(ctx, meta.CWD, b.mergeMCPServers(params.McpServers))
	if err != nil {
		return acp.LoadSessionResponse{}, acp.NewInternalError(err.Error())
	}

	ag.Messages = append(ag.Messages, msgs...)
	s := newACPSession(acp.SessionId(meta.ID), ag, mgr)
	s.storeFrom = len(ag.Messages)
	b.mu.Lock()
	if b.sessions == nil {
		b.sessions = make(map[acp.SessionId]*acpSession)
	}
	b.sessions[s.id] = s
	b.mu.Unlock()

	for _, u := range replayUpdates(msgs) {
		if err := b.update(ctx, s.id, u); err != nil {
			b.mu.Lock()
			delete(b.sessions, s.id)
			b.mu.Unlock()
			s.close()
			return acp.LoadSessionResponse{}, acp.NewInternalError(err.Error())
		}
	}
	return acp.LoadSessionResponse{
		Modes: &acp.SessionModeState{CurrentModeId: ModeAuto, AvailableModes: modes},
	}, nil
}

func (b *Bridge) ListSessions(_ context.Context, params acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	if b.store == nil {
		return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
	}
	metas, err := b.store.Recent(100)
	if err != nil {
		return acp.ListSessionsResponse{}, acp.NewInternalError(err.Error())
	}
	out := make([]acp.SessionInfo, 0, len(metas))
	for _, m := range metas {
		if params.Cwd != nil && *params.Cwd != m.CWD {
			continue
		}
		info := acp.SessionInfo{SessionId: acp.SessionId(m.ID), Cwd: m.CWD}
		if m.Title != "" {
			info.Title = new(m.Title)
		}
		if !m.UpdatedAt.IsZero() {
			info.UpdatedAt = new(m.UpdatedAt.UTC().Format(time.RFC3339))
		}
		out = append(out, info)
	}
	return acp.ListSessionsResponse{Sessions: out}, nil
}

func (b *Bridge) SetSessionMode(_ context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	s := b.getSession(params.SessionId)
	if s == nil {
		return acp.SetSessionModeResponse{}, acp.NewInternalError(fmt.Sprintf("unknown session %q", params.SessionId))
	}
	switch string(params.ModeId) {
	case ModeAuto, ModeAsk:
	default:
		return acp.SetSessionModeResponse{}, acp.NewInvalidParams(fmt.Sprintf("unknown mode %q", params.ModeId))
	}
	s.turnMu.Lock()
	s.mode = string(params.ModeId)
	s.turnMu.Unlock()
	_ = b.update(context.Background(), s.id, acp.SessionUpdate{
		CurrentModeUpdate: &acp.SessionCurrentModeUpdate{
			SessionUpdate: "current_mode_update",
			CurrentModeId: params.ModeId,
		},
	})
	return acp.SetSessionModeResponse{}, nil
}

func (b *Bridge) Prompt(ctx context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	s := b.getSession(params.SessionId)
	if s == nil {
		return acp.PromptResponse{}, acp.NewInternalError(fmt.Sprintf("unknown session %q", params.SessionId))
	}

	select {
	case s.turnCh <- struct{}{}:
	default:
		return acp.PromptResponse{}, acp.NewInternalError("session busy: a prompt turn is already running")
	}
	defer func() { <-s.turnCh }()

	text, parts := promptFromBlocks(params.Prompt, b.vision)

	turnCtx, cancel := context.WithCancel(context.Background())
	s.turnMu.Lock()
	s.cancel = cancel
	mode := s.mode
	s.turnMu.Unlock()
	defer func() {
		cancel()
		s.turnMu.Lock()
		s.cancel = nil
		s.turnMu.Unlock()
	}()

	if mode == ModeAsk {
		defer b.installPermissionGate(s, turnCtx)()
	}

	s.ag.SetOnTodos(func(items []agent.Todo) {
		entries := make([]acp.PlanEntry, 0, len(items))
		for _, it := range items {
			entries = append(entries, acp.PlanEntry{
				Content:  it.Content,
				Priority: acp.PlanEntryPriorityMedium,
				Status:   todoStatusToACP(it.Status),
			})
		}
		_ = b.update(turnCtx, s.id, acp.UpdatePlan(entries...))
	})
	defer s.ag.SetOnTodos(nil)

	_, err := s.ag.TurnParts(turnCtx, text, parts, agent.Events{
		OnText:      func(d string) { _ = b.update(turnCtx, s.id, acp.UpdateAgentMessageText(d)) },
		OnThink:     func(d string) { _ = b.update(turnCtx, s.id, updateThoughtText(d)) },
		OnToolStart: func(id, name, args string) { _ = b.update(turnCtx, s.id, startToolCall(id, name, args)) },
		OnToolEnd:   func(id, name, result string) { _ = b.update(turnCtx, s.id, b.endTool(s, id, name, result)) },
		OnUsage:     func(u ai.Usage) { b.sendUsage(turnCtx, s, u) },

		OnDecay: func(int) { s.storeFrom = 1 },
	})

	if b.store != nil {
		if serr := b.store.Save(string(s.id), s.storeFrom, s.ag.MessagesSnapshot(), s.ag.ModelName, s.ag.Provider); serr == nil {
			s.storeFrom = len(s.ag.MessagesSnapshot())
			b.sendTitle(turnCtx, s)
		}
	}

	switch {
	case err == nil:
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	case errors.Is(err, context.Canceled) || errors.Is(turnCtx.Err(), context.Canceled):

		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	case ai.IsContextLimit(err):

		return acp.PromptResponse{StopReason: acp.StopReasonMaxTokens}, nil
	default:
		return acp.PromptResponse{}, acp.NewInternalError(err.Error())
	}
}

func (b *Bridge) Cancel(_ context.Context, params acp.CancelNotification) error {
	s := b.getSession(params.SessionId)
	if s == nil {
		return nil
	}
	s.turnMu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.turnMu.Unlock()
	return nil
}

func (b *Bridge) getSession(id acp.SessionId) *acpSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessions[id]
}

func (b *Bridge) update(ctx context.Context, id acp.SessionId, u acp.SessionUpdate) error {
	if b.conn == nil {
		return nil
	}
	if err := b.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: id, Update: u}); err != nil {

		if ctx.Err() == nil {
			config_logf("session/update: %v", err)
		}
		return err
	}
	return nil
}

func (b *Bridge) sendTitle(ctx context.Context, s *acpSession) {
	if b.store == nil {
		return
	}
	meta, _, err := b.store.Load(string(s.id))
	if err != nil || meta.Title == "" {
		return
	}
	s.turnMu.Lock()
	sent := s.titleSent
	if !sent {
		s.titleSent = true
	}
	s.turnMu.Unlock()
	if sent {
		return
	}
	_ = b.update(ctx, s.id, acp.SessionUpdate{SessionInfoUpdate: &acp.SessionSessionInfoUpdate{
		SessionUpdate: "session_info_update",
		Title:         new(meta.Title),
	}})
}

func (b *Bridge) endTool(s *acpSession, id, name, result string) acp.SessionUpdate {
	args := ""
	for _, m := range s.ag.MessagesSnapshot() {
		for _, tc := range m.ToolCalls {
			if tc.ID == id {
				args = tc.Function.Arguments
			}
		}
	}
	return endToolCall(id, name, args, result)
}

func (b *Bridge) sendUsage(ctx context.Context, s *acpSession, u ai.Usage) {
	if s.ag.ContextLimit <= 0 {
		return
	}
	_ = b.update(ctx, s.id, acp.SessionUpdate{UsageUpdate: &acp.SessionUsageUpdate{
		SessionUpdate: "usage_update",
		Used:          u.PromptTokens,
		Size:          s.ag.ContextLimit,
	}})
}
