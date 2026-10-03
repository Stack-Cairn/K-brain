// Package backend exposes K-brain's canonical session and Agent runtime over HTTP.
package backend

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/memoryruntime"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/resources"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/session/recording"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const maxBodyBytes = 4 << 20

type Factory func(context.Context, string, protocol.ModelRef) (*agent.Agent, error)
type MemoryRuntimeFactory func(context.Context, string, protocol.ModelRef) (*memoryruntime.Runtime, error)

type Options struct {
	Store                   *session.Store
	Factory                 Factory
	EventDir                string
	Token                   string
	Models                  []protocol.ModelRef
	DefaultCWD              string
	Settings                *SettingsStore
	Prompts                 *resources.PromptStore
	MemoryRoot              string
	MemoryRuntimeFactory    MemoryRuntimeFactory
	MemoryOrganizerInterval time.Duration
	QuestionTimeout         time.Duration
	MCP                     *mcp.LiveManager
	MCPCredentialBridge     mcp.CredentialBridge
}

type Server struct {
	store                *session.Store
	factory              Factory
	token                string
	models               []protocol.ModelRef
	settings             *SettingsStore
	usage                *ProviderUsageService
	prompts              *resources.PromptStore
	eventDir             string
	defaultCWD           string
	memoryStore          *memory.Store
	questionWait         time.Duration
	memoryRuntimeFactory MemoryRuntimeFactory
	organizerRuntime     *memoryruntime.Runtime
	organizerCancel      func()
	cron                 *cronManager
	hookStore            *HookStore
	hookRunner           *BackendHookRunner
	mcp                  *mcp.LiveManager

	mu       sync.Mutex
	sessions map[string]*runtimeSession
	closed   bool
}

type runRecord struct {
	ClientRequestID string `json:"client_request_id"`
	PromptHash      string `json:"prompt_hash"`
	RequestHash     string `json:"request_hash,omitempty"`
	Kind            string `json:"kind,omitempty"`
	RunID           string `json:"run_id"`
	AcceptedSeq     int64  `json:"accepted_seq"`
	Terminal        bool   `json:"terminal"`
	State           string `json:"state,omitempty"`
}

type permissionWaiter struct {
	request protocol.PermissionRequest
	result  chan tools.GateDecision
	reason  chan string
}

type runtimeSession struct {
	settingsRevision uint64
	id               string
	deleted          bool
	closing          bool
	runWorkers       sync.WaitGroup
	mu               sync.Mutex
	agent            *agent.Agent
	recorder         *recording.Recorder
	cancel           context.CancelFunc
	checkpoint       *checkpointCapture
	runID            string
	runDone          bool
	nextSeq          int64
	events           []protocol.Event
	changed          chan struct{}
	runs             map[string]runRecord
	terminalManager  *tools.TerminalManager
	processManager   *tools.ManagedProcessManager
	terminalContexts map[string]context.Context
	permissions      map[string]*permissionWaiter
	questions        map[string]*questionWaiter
	clientTools      map[string]*clientToolWaiter
	eventDir         string
	mcpActivation    *mcp.ToolActivation
	memoryRuntime    agent.MemoryRuntime
	runtimeErr       error
}

func New(opts Options) (*Server, error) {
	if opts.Store == nil {
		return nil, errors.New("backend store is required")
	}
	if opts.Factory == nil {
		return nil, errors.New("backend agent factory is required")
	}
	if opts.MCP != nil && opts.MCPCredentialBridge != nil {
		opts.MCP.SetCredentialBridge(opts.MCPCredentialBridge)
	}
	if opts.EventDir == "" {
		opts.EventDir = filepath.Join(opts.Store.SessionsDir(), "backend-events")
	}
	if err := os.MkdirAll(opts.EventDir, 0o700); err != nil {
		return nil, fmt.Errorf("create backend event directory: %w", err)
	}
	hookStore, err := NewHookStore(filepath.Join(opts.EventDir, "hooks.json"))
	if err != nil {
		return nil, err
	}
	memoryStore, err := memory.OpenStore(opts.MemoryRoot)
	if err != nil {
		return nil, err
	}
	cron, err := newCronManager(opts.Store, opts.DefaultCWD, opts.Settings)
	if err != nil {
		return nil, err
	}
	server := &Server{
		store: opts.Store, factory: opts.Factory, token: opts.Token, models: append([]protocol.ModelRef(nil), opts.Models...),
		settings: opts.Settings, usage: NewProviderUsageService(opts.Settings), prompts: opts.Prompts,
		eventDir: opts.EventDir, defaultCWD: opts.DefaultCWD, memoryStore: memoryStore, questionWait: opts.QuestionTimeout, memoryRuntimeFactory: opts.MemoryRuntimeFactory, cron: cron, hookStore: hookStore, hookRunner: NewBackendHookRunner(hookStore), mcp: opts.MCP, sessions: make(map[string]*runtimeSession),
	}
	cron.promptExecutor = server.executeCronPromptCanonical
	go cron.loop()
	if opts.MemoryRuntimeFactory != nil && opts.MemoryOrganizerInterval != 0 {
		interval := opts.MemoryOrganizerInterval
		if interval < 0 {
			interval = 24 * time.Hour
		}
		runtime, runtimeErr := opts.MemoryRuntimeFactory(context.Background(), opts.DefaultCWD, protocol.ModelRef{})
		if runtimeErr != nil {
			cron.close()
			return nil, fmt.Errorf("start memory organizer runtime: %w", runtimeErr)
		}
		server.organizerRuntime = runtime
		server.organizerCancel = runtime.StartOrganizerScheduler(context.Background(), opts.DefaultCWD, interval)
	}
	return server, nil
}

func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	if s.organizerCancel != nil {
		s.organizerCancel()
	}
	var err error
	if s.organizerRuntime != nil {
		err = errors.Join(err, s.organizerRuntime.Close())
		s.organizerRuntime = nil
	}
	if s.cron != nil {
		s.cron.close()
	}
	s.mu.Lock()
	runtimes := make([]*runtimeSession, 0, len(s.sessions))
	for _, rt := range s.sessions {
		runtimes = append(runtimes, rt)
	}
	s.mu.Unlock()
	for _, rt := range runtimes {
		rt.mu.Lock()
		rt.closing = true
		if rt.cancel != nil {
			rt.cancel()
		}
		rt.mu.Unlock()
	}
	for _, rt := range runtimes {
		// Runs persist history, checkpoints and terminal events after cancellation.
		rt.runWorkers.Wait()
		rt.mu.Lock()
		memoryRuntime := rt.memoryRuntime
		rt.memoryRuntime = nil
		terminalManager := rt.terminalManager
		processManager := rt.processManager
		rt.terminalManager = nil
		rt.processManager = nil
		rt.mu.Unlock()
		if terminalManager != nil {
			terminalManager.CloseAll()
		}
		if processManager != nil {
			processManager.CloseAll()
		}
		err = errors.Join(err, closeMemoryRuntime(memoryRuntime))
	}
	return err
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.cors(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodGet && (strings.HasPrefix(r.URL.Path, "/v1/shares/") || strings.HasPrefix(r.URL.Path, "/share/")) {
		s.publicShare(w, r)
		return
	}
	if !s.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/health" {
		writeJSON(w, http.StatusOK, map[string]any{"version": protocol.Version, "status": "ok"})
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		s.handleModels(w)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/skills") {
		s.handleSkills(w, r)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/text/generate" {
		s.generateText(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/terminal") {
		s.handleTerminal(w, r)
		return
	}
	if r.URL.Path == "/v1/memory/manage" {
		s.handleMemory(w, r)
		return
	}
	if r.URL.Path == "/v1/history/search" {
		s.historySearch(w, r)
		return
	}
	if r.URL.Path == "/v1/memory/organize" {
		s.handleMemoryOrganizer(w, r)
		return
	}
	if r.URL.Path == "/v1/cron" || strings.HasPrefix(r.URL.Path, "/v1/cron/") {
		s.handleCron(w, r)
		return
	}
	if r.URL.Path == "/v1/hooks" {
		s.handleHooks(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/prompts") {
		s.handlePrompts(w, r)
		return
	}
	if r.URL.Path == "/v1/settings" {
		s.handleSettings(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/settings/providers/") && strings.HasSuffix(r.URL.Path, "/models") {
		s.handleProviderModels(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/settings/providers/") && strings.HasSuffix(r.URL.Path, "/secrets") {
		s.handleProviderSecrets(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/providers/") {
		s.providerUsage(w, r)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/migrations/liveagent-history" {
		s.importLegacyHistory(w, r)
		return
	}
	if r.URL.Path == "/v1/mcp" || strings.HasPrefix(r.URL.Path, "/v1/mcp/") {
		s.handleMCP(w, r)
		return
	}
	if r.URL.Path == "/v1/sessions" {
		if r.Method == http.MethodGet {
			s.listSessions(w, r)
			return
		}
		if r.Method == http.MethodPost {
			s.createSession(w, r)
			return
		}
	}
	const prefix = "/v1/sessions/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeJSONError(w, http.StatusNotFound, "route not found")
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSONError(w, http.StatusNotFound, "session not found")
		return
	}
	id, err := urlPathID(parts[0])
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.Method == http.MethodGet && len(parts) >= 2 && parts[1] == "trajectory" {
		if len(parts) == 2 || (len(parts) == 3 && parts[2] == "stats") {
			s.trajectory(w, r, id, len(parts) == 3)
			return
		}
		if len(parts) == 3 && (parts[2] == "sections" || parts[2] == "subagents") {
			s.trajectoryDetails(w, r, id, parts[2])
			return
		}
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		s.deleteSession(w, r, id)
		return
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "checkpoints":
			if r.Method == http.MethodGet {
				s.checkpointList(w, r, id)
				return
			}
		case "compact":
			if r.Method == http.MethodPost {
				s.compactSession(w, r, id)
				return
			}
		case "history":
			if r.Method == http.MethodGet {
				s.history(w, r, id)
				return
			}
		case "branch", "edit":
			if r.Method == http.MethodPost {
				s.mutateHistory(w, r, id, parts[1] == "edit")
				return
			}
		case "share":
			if r.Method == http.MethodGet || r.Method == http.MethodPost {
				s.share(w, r, id)
				return
			}
		}
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		s.getSession(w, r, id)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodPatch {
		s.updateSession(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodGet {
		s.events(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "runs" && r.Method == http.MethodPost {
		s.startRun(w, r, id)
		return
	}
	if len(parts) == 4 && parts[1] == "checkpoints" && r.Method == http.MethodPost && (parts[3] == "preview" || parts[3] == "rewind") {
		seq, parseErr := strconv.Atoi(parts[2])
		if parseErr != nil || seq < 0 {
			writeJSONError(w, http.StatusBadRequest, "invalid checkpoint turn sequence")
			return
		}
		if parts[3] == "preview" {
			s.checkpointPreview(w, r, id, seq)
		} else {
			s.checkpointRewind(w, r, id, seq)
		}
		return
	}
	if len(parts) == 2 && parts[1] == "close" && r.Method == http.MethodPost {
		s.closeSession(w, r, id)
		return
	}
	if len(parts) == 4 && parts[1] == "runs" && parts[2] != "" && parts[3] == "cancel" && r.Method == http.MethodPost {
		s.cancelRun(w, r, id, parts[2])
		return
	}
	if len(parts) == 3 && parts[1] == "permissions" && parts[2] != "" && r.Method == http.MethodPost {
		s.permission(w, r, id, parts[2])
		return
	}
	if len(parts) == 3 && parts[1] == "questions" && parts[2] != "" && r.Method == http.MethodPost {
		s.answerQuestion(w, r, id, parts[2])
		return
	}
	if len(parts) == 3 && parts[1] == "client-tools" && parts[2] != "" && r.Method == http.MethodPost {
		s.resolveClientTool(w, r, id, parts[2])
		return
	}
	writeJSONError(w, http.StatusNotFound, "route not found")
}

func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return true
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(got) != len(s.token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

func (s *Server) cors(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Cache-Control")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var in protocol.CreateSessionRequest
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	cwd := strings.TrimSpace(in.CWD)
	if cwd == "" {
		cwd = s.defaultCWD
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if strings.TrimSpace(in.Model.Model) == "" {
		writeJSONError(w, http.StatusBadRequest, "model.model is required")
		return
	}
	id, err := s.store.Create(cwd, in.Model.Model, in.Model.Provider)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(in.Messages) > 0 {
		aiMessages := make([]ai.Message, 0, len(in.Messages))
		for _, message := range in.Messages {
			converted, convertErr := message.ToAIMessage()
			if convertErr != nil {
				_ = s.store.Delete(id)
				writeJSONError(w, http.StatusBadRequest, "invalid history message: "+convertErr.Error())
				return
			}
			aiMessages = append(aiMessages, converted)
		}
		if err := s.store.Save(id, 0, aiMessages, in.Model.Model, in.Model.Provider); err != nil {
			_ = s.store.Delete(id)
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if in.Title != "" {
		if err := s.store.SetTitle(id, in.Title); err != nil {
			_ = s.store.Delete(id)
			writeJSONError(w, 500, err.Error())
			return
		}
	}
	rt, err := s.loadRuntime(id, in.Model, cwd)
	if err != nil {
		_ = s.store.Delete(id)
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, s.sessionView(rt))
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request, id string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.sessionView(rt))
}

func (s *Server) sessionView(rt *runtimeSession) protocol.Session {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out, _ := s.sessionViewLocked(rt)
	return out
}

func (s *Server) sessionViewLocked(rt *runtimeSession) (protocol.Session, error) {
	if rt.deleted {
		return protocol.Session{}, session.ErrNotFound
	}
	snap, err := s.store.HistorySnapshot(rt.id)
	if err != nil {
		return protocol.Session{}, err
	}
	out := protocol.Session{SessionSummary: summary(snap.Meta, len(snap.Messages)), LastSeq: rt.nextSeq, Revision: snap.Revision, Messages: []protocol.Message{}}
	out.CreatedAt = snap.CreatedAt
	for _, msg := range snap.Messages {
		out.Messages = append(out.Messages, protocol.FromAIMessage(msg))
	}
	seen := make(map[string]bool)
	if rt.agent != nil {
		for _, task := range rt.agent.Tasks().List() {
			out.Tasks = append(out.Tasks, subagentView(task))
			seen[task.ID] = true
		}
	}
	storedTasks, err := s.store.LoadTasks(rt.id)
	if err != nil {
		return protocol.Session{}, err
	}
	for _, task := range storedTasks {
		if !seen[task.ID] {
			out.Tasks = append(out.Tasks, storedSubagentView(task))
		}
	}
	return out, nil
}

func summary(meta session.Meta, count int) protocol.SessionSummary {
	return protocol.SessionSummary{ID: meta.ID, Title: meta.Title, CWD: meta.CWD, Model: protocol.ModelRef{Provider: meta.Provider, Model: meta.Model}, CreatedAt: meta.CreatedAt, UpdatedAt: meta.UpdatedAt, MessageCount: count, Pinned: meta.Pinned, Archived: meta.Archived, Shared: meta.Shared}
}

func (s *Server) loadRuntimeByID(id string) (*runtimeSession, error) {
	if !validID(id) {
		return nil, session.ErrNotFound
	}
	s.mu.Lock()
	if rt := s.sessions[id]; rt != nil {
		s.mu.Unlock()
		rt.mu.Lock()
		deleted := rt.deleted
		rt.mu.Unlock()
		if deleted {
			return nil, session.ErrNotFound
		}
		return rt, nil
	}
	s.mu.Unlock()
	meta, _, err := s.store.Load(id)
	if err != nil {
		return nil, err
	}
	return s.loadRuntime(id, protocol.ModelRef{Provider: meta.Provider, Model: meta.Model}, meta.CWD)
}

func closeMemoryRuntime(runtime agent.MemoryRuntime) error {
	if runtime == nil {
		return nil
	}
	return runtime.Close()
}

func (s *Server) switchModelLocked(rt *runtimeSession, selected protocol.ModelRef) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return errors.New("backend is closed")
	}
	if strings.TrimSpace(selected.Model) == "" {
		return errors.New("model.model is required")
	}
	if rt.agent != nil && rt.agent.ModelName == selected.Model && rt.agent.Provider == selected.Provider {
		return nil
	}
	cwd := ""
	if rt.agent != nil {
		cwd = rt.agent.WorkingDir
	}
	if cwd == "" {
		if meta, _, err := s.store.Load(rt.id); err == nil {
			cwd = meta.CWD
		}
	}
	ag, err := s.factory(context.Background(), cwd, selected)
	if err != nil {
		return err
	}
	ag.WorkingDir = cwd
	terminalManager := rt.terminalManager
	processManager := rt.processManager
	ag.Tools = append(ag.Tools, tools.LiveAgentCatalogWithManagers([]string{selected.Provider}, terminalManager, processManager)...)
	if s.cron != nil {
		s.cron.attachTool(ag, selected)
	}
	ag.ModelName, ag.Provider = selected.Model, selected.Provider
	ag.SetSessionID(rt.id)
	var memoryRuntime agent.MemoryRuntime
	if s.memoryRuntimeFactory != nil {
		memoryRuntime, err = s.memoryRuntimeFactory(context.Background(), cwd, selected)
		if err != nil {
			return err
		}
		ag.SetMemoryRuntime(memoryRuntime)
	}
	adopted := false
	defer func() {
		if !adopted {
			_ = closeMemoryRuntime(memoryRuntime)
		}
	}()
	rec, err := recording.Open(s.store, rt.id, ag)
	if err != nil {
		return err
	}
	if err := s.store.SetModel(rt.id, selected.Model, selected.Provider); err != nil {
		return err
	}
	s.wireTasks(rt, ag)
	previousMemory := rt.memoryRuntime
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("backend is closed")
	}
	rt.agent = ag
	rt.memoryRuntime = memoryRuntime
	rt.terminalManager = terminalManager
	rt.processManager = processManager
	rt.settingsRevision = 0
	rt.recorder = rec
	s.mu.Unlock()
	adopted = true
	_ = closeMemoryRuntime(previousMemory)
	return nil
}

func (s *Server) loadRuntime(id string, model protocol.ModelRef, cwd string) (*runtimeSession, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("backend is closed")
	}
	if rt := s.sessions[id]; rt != nil {
		s.mu.Unlock()
		rt.mu.Lock()
		deleted := rt.deleted
		rt.mu.Unlock()
		if deleted {
			return nil, session.ErrNotFound
		}
		return rt, nil
	}
	s.mu.Unlock()
	activation := mcp.NewToolActivation()
	ag, err := s.factory(context.Background(), cwd, model)
	if err != nil {
		return nil, err
	}
	ag.WorkingDir = cwd
	terminalManager := tools.NewTerminalManager()
	processManager := tools.NewManagedProcessManager()
	ag.Tools = append(ag.Tools, tools.LiveAgentCatalogWithManagers([]string{model.Provider}, terminalManager, processManager)...)
	if s.cron != nil {
		s.cron.attachTool(ag, model)
	}
	ag.ModelName, ag.Provider = model.Model, model.Provider
	if s.mcp != nil {
		mcpTools, filter := s.mcp.ToolsForTurn(context.Background(), cwd, nil, activation)
		ag.SetMCPTools(mcpTools)
		ag.RequestToolFilter = filter
	}
	ag.SetSessionID(id)
	var memoryRuntime agent.MemoryRuntime
	if s.memoryRuntimeFactory != nil {
		memoryRuntime, err = s.memoryRuntimeFactory(context.Background(), cwd, model)
		if err != nil {
			return nil, err
		}
		ag.SetMemoryRuntime(memoryRuntime)
	}
	adopted := false
	defer func() {
		if !adopted {
			_ = closeMemoryRuntime(memoryRuntime)
		}
	}()
	rec, err := recording.Open(s.store, id, ag)
	if err != nil {
		return nil, err
	}
	rt := &runtimeSession{id: id, agent: ag, memoryRuntime: memoryRuntime, recorder: rec, changed: make(chan struct{}), runs: map[string]runRecord{}, terminalManager: terminalManager, processManager: processManager, permissions: map[string]*permissionWaiter{}, questions: map[string]*questionWaiter{}, eventDir: s.eventDir, mcpActivation: activation}
	if err := rt.loadJournal(s.eventDir); err != nil {
		return nil, err
	}
	s.wireTasks(rt, ag)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("backend is closed")
	}
	if existing := s.sessions[id]; existing != nil {
		s.mu.Unlock()
		existing.mu.Lock()
		deleted := existing.deleted
		existing.mu.Unlock()
		if deleted {
			return nil, session.ErrNotFound
		}
		return existing, nil
	}
	s.sessions[id] = rt
	s.mu.Unlock()
	adopted = true
	return rt, nil
}

func (s *Server) wireTasks(rt *runtimeSession, ag *agent.Agent) {
	s.wireQuestions(rt, ag)
	ag.Tasks().SetSessionID(rt.id)
	ag.Tasks().OnEvents = rt.trajectoryChildEvents
	ag.Tasks().OnRecord = func(id string, task *agent.BackgroundTask) {
		model, provider, _ := strings.Cut(task.SubModel, " @ ")
		stored := session.Task{Model: model, Provider: provider, ID: task.ID, Description: task.Description, Prompt: task.Prompt, Status: string(task.Status), Report: task.Report, StartedAt: task.StartedAt, EndedAt: task.EndedAt}
		if err := s.store.SaveTask(id, stored); err != nil {
			_, _ = rt.publish(protocol.EventToolStatus, protocol.ToolStatus{Tool: "subagent", Status: "error", Message: "save task: " + err.Error()}, "")
			return
		}
		if task.Status != agent.TaskRunning && task.SubMessages != nil {
			model, provider, _ := strings.Cut(task.SubModel, " @ ")
			if _, err := s.store.SaveSubagentTranscript(id, task.ID, task.SubMessages, model, provider); err != nil {
				_, _ = rt.publish(protocol.EventToolStatus, protocol.ToolStatus{Tool: "subagent", Status: "error", Message: "save task transcript: " + err.Error()}, "")
				return
			}
		}
		rt.taskEvent(task)
	}
}

func (rt *runtimeSession) taskEvent(t *agent.BackgroundTask) {
	view := subagentView(*t)
	typ := protocol.EventSubagentUpdate
	switch t.Status {
	case agent.TaskRunning:
		typ = protocol.EventSubagentStarted
	case agent.TaskDone:
		typ = protocol.EventSubagentCompleted
	case agent.TaskError, agent.TaskCancelled:
		typ = protocol.EventSubagentFailed
	}
	rt.mu.Lock()
	runID := rt.trajectoryTaskRunLocked(t.ID)
	rt.mu.Unlock()
	_, _ = rt.publishTrajectory(runID, typ, protocol.SubagentEvent{Subagent: view}, "")
}

func subagentView(t agent.BackgroundTask) protocol.Subagent {
	view := storedSubagentView(session.Task{ID: t.ID, Description: t.Description, Status: string(t.Status), Report: t.Report, StartedAt: t.StartedAt, EndedAt: t.EndedAt})
	model, provider, _ := strings.Cut(t.SubModel, " @ ")
	view.Model = protocol.ModelRef{Provider: provider, Model: model}
	return view
}
func storedSubagentView(t session.Task) protocol.Subagent {
	view := protocol.Subagent{ID: t.ID, Description: t.Description, Status: t.Status, Report: t.Report, Model: protocol.ModelRef{Model: t.Model, Provider: t.Provider}, StartedAt: ptrTime(t.StartedAt), EndedAt: ptrTime(t.EndedAt)}
	if t.Status == string(agent.TaskError) {
		view.Error = t.Report
	}
	return view
}
func ptrTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	x := t
	return &x
}

func (rt *runtimeSession) loadJournal(dir string) error {
	path := filepath.Join(dir, rt.id+".jsonl")
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return rt.loadRuns(dir)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxBodyBytes)
	for sc.Scan() {
		var e protocol.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return err
		}
		if err := e.Validate(); err != nil {
			return err
		}
		rt.events = append(rt.events, e)
		if e.Seq > rt.nextSeq {
			rt.nextSeq = e.Seq
		}
		rt.runID = e.RunID
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return rt.loadRuns(dir)
}
func (rt *runtimeSession) loadRuns(dir string) error {
	b, err := os.ReadFile(filepath.Join(dir, rt.id+".runs.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &rt.runs)
}
func (rt *runtimeSession) persistEvent(dir string, e protocol.Event) error {
	f, err := os.OpenFile(filepath.Join(dir, rt.id+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	_, err = f.Write(append(b, '\n'))
	return err
}
func (rt *runtimeSession) persistRuns(dir string) error {
	b, err := json.MarshalIndent(rt.runs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, rt.id+".runs.json"), b, 0o600)
}

func (rt *runtimeSession) publish(typ string, payload any, parent string) (protocol.Event, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.deleted {
		return protocol.Event{}, session.ErrNotFound
	}
	seq := rt.nextSeq + 1
	e, err := protocol.NewEvent(seq, rt.id, rt.runID, typ, payload)
	if err != nil {
		return protocol.Event{}, err
	}
	e.ParentRunID = parent
	if err = rt.persistEvent(rt.eventDir, e); err != nil {
		return protocol.Event{}, err
	}
	rt.nextSeq = seq
	rt.events = append(rt.events, e)
	old := rt.changed
	rt.changed = make(chan struct{})
	close(old)
	return e, nil
}

// publishWithDir is the durable form used by handlers; publish is kept small for task callbacks.
func (s *Server) publish(rt *runtimeSession, typ string, payload any, parent string) (protocol.Event, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.runID == "" {
		rt.runID = "recovery"
	}
	if rt.deleted {
		return protocol.Event{}, session.ErrNotFound
	}
	seq := rt.nextSeq + 1
	e, err := protocol.NewEvent(seq, rt.id, rt.runID, typ, payload)
	if err != nil {
		return protocol.Event{}, err
	}
	e.ParentRunID = parent
	if err = rt.persistEvent(s.eventDir, e); err != nil {
		return protocol.Event{}, err
	}
	rt.nextSeq = seq
	rt.events = append(rt.events, e)
	old := rt.changed
	rt.changed = make(chan struct{})
	close(old)
	return e, nil
}

type unattendedRunContextKey struct{}

func withUnattendedRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, unattendedRunContextKey{}, true)
}

func isUnattendedRun(ctx context.Context) bool {
	value, _ := ctx.Value(unattendedRunContextKey{}).(bool)
	return value
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request, id string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	var in protocol.PromptRequest
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	accepted, status, err := s.startCanonicalRun(context.Background(), rt, in)
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	writeJSON(w, status, accepted)
}

func (s *Server) executeRun(rt *runtimeSession, ctx context.Context, runID string, in protocol.PromptRequest, options protocol.RunOptions) {
	rt.mu.Lock()
	restoreRunOptions := applyRunOptionsWith(rt.agent, options, s.clientTools(rt, options.ClientTools))
	rt.mu.Unlock()
	defer func() {
		rt.mu.Lock()
		restoreRunOptions()
		if rt.cancel != nil {
			rt.cancel()
		}
		rt.runDone = true
		rt.cancel = nil
		if rec, ok := rt.runs[in.ClientRequestID]; ok {
			rec.Terminal = true
			rt.runs[in.ClientRequestID] = rec
			if err := rt.persistRuns(s.eventDir); err != nil {
				rt.runtimeErr = errors.Join(rt.runtimeErr, fmt.Errorf("persist run completion: %w", err))
			}
		}
		old := rt.changed
		rt.changed = make(chan struct{})
		close(old)
		rt.mu.Unlock()
	}()
	message := protocol.Message{Role: protocol.RoleUser}
	if in.Prompt != "" {
		message.Content = append(message.Content, protocol.ContentBlock{Type: protocol.ContentText, Text: in.Prompt})
	}
	message.Content = append(message.Content, in.Content...)
	converted, _ := message.ToAIMessage()
	gate := func(req tools.GateRequest) (tools.GateDecision, string) {
		if options.Mode == "chat" {
			return tools.GateReject, "chat mode disables tools"
		}
		if isUnattendedRun(req.Context) {
			return tools.GateReject, "unattended scheduled runs cannot request interactive approval"
		}
		return runToolGate(options, req, func(req tools.GateRequest) (tools.GateDecision, string) {
			rt.mu.Lock()
			active := rt.runID == runID && !rt.runDone && !rt.deleted
			rt.mu.Unlock()
			if !active {
				return tools.GateReject, "interactive approval requires the owning active run"
			}
			return s.waitPermission(rt, runID, req, req.Context)
		})
	}
	roots := make([]tools.WorkspaceRoot, len(options.WorkspaceRoots))
	for i, root := range options.WorkspaceRoots {
		roots[i] = tools.WorkspaceRoot{Path: root.Path, Access: root.Access}
	}
	ctx = tools.WithWorkspaceRoots(ctx, roots)
	ctx = tools.WithGate(ctx, gate)
	ctx = tools.WithAsk(ctx, func(questionCtx context.Context, req tools.AskRequest) ([]string, bool) {
		if isUnattendedRun(questionCtx) {
			return nil, false
		}
		questions := make([]protocol.Question, len(req.Questions))
		if len(req.Questions) == 0 {
			questions = []protocol.Question{{Prompt: req.Question, Multiple: req.Multiple, Options: make([]protocol.QuestionOption, len(req.Options))}}
			for i, option := range req.Options {
				questions[0].Options[i] = protocol.QuestionOption{Label: option.Label, Description: option.Description, Recommended: option.Recommended}
			}
		} else {
			for i, question := range req.Questions {
				questions[i] = protocol.Question{ID: question.ID, Header: question.Header, Prompt: question.Prompt, Multiple: question.Multiple, Options: make([]protocol.QuestionOption, len(question.Options))}
				for j, option := range question.Options {
					questions[i].Options[j] = protocol.QuestionOption{Label: option.Label, Description: option.Description, Recommended: option.Recommended}
				}
			}
		}
		normalized, normalizeErr := normalizeQuestions(questions)
		if normalizeErr != nil {
			return nil, false
		}
		result, waitErr := s.waitQuestion(questionCtx, rt, runID, tools.ToolCallID(questionCtx), normalized)
		if waitErr != nil || result == nil || result.Cancelled {
			return nil, false
		}
		answers := make([]string, len(result.Answers))
		for i, answer := range result.Answers {
			answers[i] = answer.SelectedLabel
		}
		return answers, true
	})
	ctx = context.WithValue(ctx, questionRunKey{}, runID)
	ctx = tools.WithRunIdentity(ctx, tools.RunIdentity{ConversationID: rt.id, RunID: runID})
	ctx = tools.WithWorkingDir(ctx, rt.agent.WorkingDir)
	ctx = sandbox.WithPolicy(ctx, rt.agent.SandboxPolicy)
	rt.mu.Lock()
	if rt.terminalContexts == nil {
		rt.terminalContexts = make(map[string]context.Context)
	}
	rt.terminalContexts[runID] = ctx
	rt.mu.Unlock()
	rt.mu.Lock()
	capture := rt.checkpoint
	rt.mu.Unlock()
	if capture != nil {
		messageID := in.TurnID
		if in.ResumeMessageID != "" {
			messageID = in.ResumeMessageID
		}
		if messageID != "" {
			ctx = agent.WithUserMessageID(ctx, messageID)
		}
		ctx = tools.WithFileMutationObserver(ctx, capture.capture)
		ctx = agent.WithUserMessageObserver(ctx, func(message ai.Message) error {
			capture.mu.Lock()
			if capture.turnID == "" {
				capture.turnID = message.ID
			}
			capture.mu.Unlock()
			return nil
		})
	}
	ctx = agent.WithRequestObserver(ctx, &trajectoryObserver{rt: rt, runID: runID})
	hookEvents := s.hookRunner.Scope(ctx, rt.id, runID, rt.agent.WorkingDir, func(hook BackendHook, event string, err error) {
		_, _ = rt.publish(protocol.EventHookWarning, map[string]any{"hookName": hook.Name, "hookType": hook.Type, "event": event, "message": err.Error()}, "")
	})
	ctx = agent.WithLifecycleStart(ctx, func() { hookEvents("agent_start") })
	ev := agent.Events{OnLifecycle: hookEvents, OnText: func(d string) { _, _ = rt.publish(protocol.EventTextDelta, protocol.TextDelta{Text: d}, "") }, OnThink: func(d string) { _, _ = rt.publish(protocol.EventThinkingDelta, protocol.TextDelta{Text: d}, "") }, OnHostedSearch: func(search ai.HostedSearch) {
		_, _ = rt.publish(protocol.EventHostedSearch, protocol.HostedSearch{Type: search.Type, ID: search.ID, Provider: search.Provider, Status: search.Status, Queries: search.Queries, Sources: func() []protocol.HostedSearchSource {
			out := make([]protocol.HostedSearchSource, len(search.Sources))
			for i, source := range search.Sources {
				out[i] = protocol.HostedSearchSource{URL: source.URL, Title: source.Title, SourceType: source.SourceType}
			}
			return out
		}(), Error: search.Error}, "")
	}, OnToolStart: func(id, name, args string) {
		_, _ = rt.publish(protocol.EventToolCall, protocol.ToolCallEvent{ToolCall: protocol.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(args)}}, "")
	}, OnToolResult: func(id, name string, result tools.Result) {
		_, _ = rt.publish(protocol.EventToolResult, protocol.ToolResultEvent{ToolResult: protocol.ToolResult{ID: id, Name: name, Output: result.Text, Failed: result.Failed, Cancelled: result.Cancelled}}, "")
	}, OnToolOutput: func(id, output string) {
		_, _ = rt.publish(protocol.EventToolStatus, protocol.ToolStatus{ToolCallID: id, Status: "running", Message: output}, "")
	}, OnUsage: func(u ai.Usage) {
		_, _ = rt.publish(protocol.EventUsage, protocol.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, CachedTokens: u.Cached(), CacheWriteTokens: u.CacheWrite()}, "")
	}}
	ev = agent.FanIn(rt.recorder.Events(), ev)
	assistantCount := 0
	for _, message := range rt.agent.MessagesSnapshot() {
		if message.Role == protocol.RoleAssistant {
			assistantCount++
		}
	}
	var err error
	if in.ResumeMessageID != "" {
		_, err = rt.agent.ContinueUser(ctx, in.ResumeMessageID, ev)
	} else {
		_, err = rt.agent.TurnParts(ctx, converted.Content, converted.Parts, ev)
	}
	var failedAssistant *protocol.Message
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		messages := rt.agent.MessagesSnapshot()
		currentAssistantCount := 0
		var latestAssistant ai.Message
		for _, message := range messages {
			if message.Role != protocol.RoleAssistant {
				continue
			}
			currentAssistantCount++
			if currentAssistantCount > assistantCount {
				latestAssistant = message
			}
		}
		if currentAssistantCount > assistantCount && latestAssistant.StopReason == ai.StopReasonError {
			if canonical, canonicalErr := protocol.FromAIMessageValidated(latestAssistant); canonicalErr == nil {
				failedAssistant = &canonical
			}
		} else {
			now := time.Now().UTC()
			failureMessage := ai.Message{
				ID:         "assistant-error-" + runID,
				Role:       protocol.RoleAssistant,
				Content:    err.Error(),
				Model:      rt.agent.Model + " @ " + rt.agent.Provider,
				StopReason: ai.StopReasonError,
				SentAt:     &now,
			}
			messages = append(messages, failureMessage)
			rt.agent.RestoreMessages(messages)
			if canonical, canonicalErr := protocol.FromAIMessageValidated(failureMessage); canonicalErr == nil {
				failedAssistant = &canonical
			}
		}
	}
	rt.mu.Lock()
	if rt.recorder != nil {
		err = errors.Join(err, rt.recorder.Save())
	}
	checkpointRun := rt.checkpoint
	rt.checkpoint = nil
	rt.mu.Unlock()
	if checkpointRun != nil {
		sequence, sequenceErr := s.store.MessageSequence(rt.id, checkpointRun.turnID)
		if sequenceErr != nil {
			err = errors.Join(err, sequenceErr)
		} else if record, checkpointErr := checkpointRun.commit(sequence); checkpointErr != nil {
			err = errors.Join(err, checkpointErr)
		} else if snapshotErr := s.store.SetSnapshot(rt.id, sequence, record.ID); snapshotErr != nil {
			err = errors.Join(err, snapshotErr)
		}
	}
	finish := func(kind string, terminal protocol.RunTerminal) {
		_, publishErr := rt.publish(kind, terminal, "")
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if publishErr != nil {
			rt.runtimeErr = errors.Join(rt.runtimeErr, fmt.Errorf("persist run terminal: %w", publishErr))
		}
		if rec, ok := rt.runs[in.ClientRequestID]; ok {
			rec.State = terminal.State
			rt.runs[in.ClientRequestID] = rec
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		finish(protocol.EventRunCancelled, protocol.RunTerminal{State: "cancelled"})
	} else if err != nil {
		if failedAssistant != nil {
			_, _ = rt.publish(protocol.EventAssistantMessage, *failedAssistant, "")
		}
		finish(protocol.EventRunFailed, protocol.RunTerminal{State: "failed", Error: err.Error()})
	} else {
		messages := rt.agent.MessagesSnapshot()
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role != protocol.RoleAssistant {
				continue
			}
			if message, messageErr := protocol.FromAIMessageValidated(messages[i]); messageErr == nil {
				_, _ = rt.publish(protocol.EventAssistantMessage, message, "")
			}
			break
		}
		finish(protocol.EventRunCompleted, protocol.RunTerminal{State: "completed"})
	}
}

func (s *Server) waitPermission(rt *runtimeSession, runID string, req tools.GateRequest, ctx context.Context) (tools.GateDecision, string) {
	id := newRunID()
	p := &permissionWaiter{request: protocol.PermissionRequest{PermissionID: id, Tool: req.Tool, Command: req.Command, Rule: req.Rule, Options: []protocol.PermissionOption{{ID: "allow_once", Label: "Allow once", Kind: "allow_once"}, {ID: "reject", Label: "Reject", Kind: "reject_once"}}}, result: make(chan tools.GateDecision, 1), reason: make(chan string, 1)}
	rt.mu.Lock()
	rt.permissions[id] = p
	rt.mu.Unlock()
	s.publish(rt, protocol.EventPermissionRequest, p.request, "")
	defer func() { rt.mu.Lock(); delete(rt.permissions, id); rt.mu.Unlock() }()
	select {
	case d := <-p.result:
		reason := ""
		select {
		case reason = <-p.reason:
		default:
		}
		s.publish(rt, protocol.EventPermissionResult, protocol.PermissionDecision{PermissionID: id, Decision: decisionName(d), Reason: reason}, "")
		return d, reason
	case <-ctx.Done():
		return tools.GateReject, ctx.Err().Error()
	}
}
func decisionName(d tools.GateDecision) string {
	if d == tools.GateAllowOnce {
		return "allow_once"
	}
	if d == tools.GateAllowAlways {
		return "allow_always"
	}
	return "reject"
}

func (s *Server) events(w http.ResponseWriter, r *http.Request, id string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	for {
		rt.mu.Lock()
		pending := make([]protocol.Event, 0)
		active := rt.cancel != nil && !rt.runDone
		for _, e := range rt.events {
			if e.Seq > after {
				// Consumers may submit the next run as soon as they receive a terminal event.
				if active && e.RunID == rt.runID && (e.Type == protocol.EventRunCompleted || e.Type == protocol.EventRunFailed || e.Type == protocol.EventRunCancelled) {
					break
				}
				pending = append(pending, e)
			}
		}
		changed := rt.changed
		rt.mu.Unlock()
		for _, e := range pending {
			if err := writeSSE(w, e); err != nil {
				return
			}
			after = e.Seq
			if e.Type == protocol.EventRunCompleted || e.Type == protocol.EventRunFailed || e.Type == protocol.EventRunCancelled {
				flusher.Flush()
				return
			}
		}
		flusher.Flush()
		if !active {
			return
		}
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		}
	}
}
func writeSSE(w io.Writer, e protocol.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.Seq, b)
	return err
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request, id, run string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	rt.mu.Lock()
	if rt.runID != run || rt.cancel == nil {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "run is not active")
		return
	}
	cancel := rt.cancel
	rt.mu.Unlock()
	cancel()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (s *Server) closeSession(w http.ResponseWriter, r *http.Request, id string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	rt.mu.Lock()
	if rt.cancel != nil {
		rt.cancel()
	}

	rt.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": id})
}
func (s *Server) permission(w http.ResponseWriter, r *http.Request, id, pid string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	var in protocol.PermissionDecisionRequest
	if err = decodeJSON(w, r, &in); err != nil {
		return
	}
	rt.mu.Lock()
	p := rt.permissions[pid]
	rt.mu.Unlock()
	if p == nil {
		writeJSONError(w, http.StatusNotFound, "permission request not found")
		return
	}
	var d tools.GateDecision
	switch in.Decision.Decision {
	case "allow_once":
		d = tools.GateAllowOnce
	case "allow_always":
		d = tools.GateAllowAlways
	case "reject":
		d = tools.GateReject
	default:
		writeJSONError(w, http.StatusBadRequest, "unsupported permission decision")
		return
	}
	if in.Decision.Reason != "" {
		select {
		case p.reason <- in.Decision.Reason:
		default:
		}
	}
	p.result <- d
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return err
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"version": protocol.Version, "error": msg})
}
func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func urlPathID(raw string) (string, error) {
	id, err := urlPathUnescape(raw)
	if err != nil || !validID(id) {
		return "", errors.New("invalid session id")
	}
	return id, nil
}
func urlPathUnescape(raw string) (string, error) { return strings.ReplaceAll(raw, "%2F", "/"), nil }
func hashPrompt(in protocol.PromptRequest) string {
	b, _ := json.Marshal(in)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func newRunID() string { return fmt.Sprintf("run-%d", time.Now().UnixNano()) }
