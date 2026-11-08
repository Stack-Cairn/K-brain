// Package backend exposes K-brain's canonical session and Agent runtime over HTTP.
package backend

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/approval"
	"github.com/Stack-Cairn/K-brain/internal/elicit"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/memoryruntime"
	"github.com/Stack-Cairn/K-brain/internal/permissions"
	"github.com/Stack-Cairn/K-brain/internal/planning"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/resources"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/session/recording"
	"github.com/Stack-Cairn/K-brain/internal/sshhosts"
	"github.com/Stack-Cairn/K-brain/internal/sshmgr"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const maxBodyBytes = 4 << 20

type Factory func(context.Context, string, protocol.ModelRef) (*agent.Agent, error)
type MemoryRuntimeFactory func(context.Context, string, protocol.ModelRef) (*memoryruntime.Runtime, error)

type Options struct {
	Image                   *ImageModule
	Store                   *session.Store
	Factory                 Factory
	EventDir                string
	Token                   string
	Models                  []protocol.ModelRef
	DefaultCWD              string
	FilesystemRoots         []string
	Settings                *SettingsStore
	Prompts                 *resources.PromptStore
	MemoryRoot              string
	MemoryStore             *memory.Store
	MemoryRuntimeFactory    MemoryRuntimeFactory
	MemoryOrganizerInterval time.Duration
	QuestionTimeout         time.Duration
	MCP                     *mcp.LiveManager
	MCPCredentialBridge     mcp.CredentialBridge
	// Build identifies this binary in /v1/health; zero value reports a dev build.
	Build protocol.Build
	// PermissionRulesDir is the data directory holding user and project permission
	// rules. Empty limits remembered rules to the session.
	PermissionRulesDir string
	// SandboxMasked lists directories hidden from commands of sandboxed runs in
	// addition to the credential directories: the backend's own data and config.
	SandboxMasked []string
	// SandboxEnvKeep lists the credential-looking variables commands of
	// sandboxed runs still inherit (sandbox.envKeep).
	SandboxEnvKeep []string
	// SSHDir holds SSHManager's hosts (hosts.json) and K-brain's own
	// known_hosts. Empty uses EventDir/ssh.
	SSHDir string
}

type Server struct {
	image                *ImageModule
	store                *session.Store
	factory              Factory
	token                string
	models               []protocol.ModelRef
	settings             *SettingsStore
	usage                *ProviderUsageService
	prompts              *resources.PromptStore
	eventDir             string
	permissionDir        string
	sandboxMasked        []string
	sandboxEnvKeep       []string
	defaultCWD           string
	filesystemRoots      []string
	filesystemMu         sync.Mutex
	memoryStore          *memory.Store
	questionWait         time.Duration
	memoryRuntimeFactory MemoryRuntimeFactory
	organizerRuntime     *memoryruntime.Runtime
	organizerCancel      func()
	cron                 *cronManager
	planning             *planning.Store
	planningCancel       context.CancelFunc
	planningDone         chan struct{}
	hookStore            *HookStore
	hookRunner           *BackendHookRunner
	mcp                  *mcp.LiveManager
	build                protocol.Build
	instance             instanceIdentity
	// hostBridge is the LiveAgent desktop's tool bridge, kept in memory only.
	hostBridge hostBridge
	// ssh runs SSHManager's connections to the hosts in Settings > SSH.
	ssh *sshmgr.Manager

	mu                  sync.Mutex
	sessions            map[string]*runtimeSession
	closed              bool
	shuttingDown        atomic.Bool
	reaperStop          chan struct{}
	reaperDone          chan struct{}
	snapshotMu          sync.Mutex
	snapshots           map[string]sessionHTTPSnapshot
	snapshotBuilders    int
	snapshotBuildCancel context.CancelFunc
	// background tracks the server's own goroutines that are not runs (wake
	// dispatch, deferred event writes); Close waits for them.
	background goGroup
}

// goGroup runs goroutines and waits for them. Unlike sync.WaitGroup it may start
// a goroutine while Wait blocks, as background work finishing during Close does.
type goGroup struct {
	mu   sync.Mutex
	n    int
	idle chan struct{}
}

func (g *goGroup) Go(fn func()) {
	g.mu.Lock()
	if g.n == 0 {
		g.idle = make(chan struct{})
	}
	g.n++
	g.mu.Unlock()
	go func() {
		defer func() {
			g.mu.Lock()
			g.n--
			if g.n == 0 {
				close(g.idle)
			}
			g.mu.Unlock()
		}()
		fn()
	}()
}

// Wait returns once no goroutine of the group runs, including ones started meanwhile.
func (g *goGroup) Wait() {
	for {
		g.mu.Lock()
		if g.n == 0 {
			g.mu.Unlock()
			return
		}
		idle := g.idle
		g.mu.Unlock()
		<-idle
	}
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
	// gate and root re-evaluate the prompt when a rule is remembered meanwhile;
	// ruleSettleable is false when a review sent the call to a person.
	gate           tools.GateRequest
	root           string
	ruleSettleable bool
}

type runtimeSession struct {
	leases           atomic.Int32
	lastUsed         time.Time
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
	eventBytes       int
	eventsTruncated  bool
	changed          chan struct{}
	runs             map[string]runRecord
	terminalManager  *tools.TerminalManager
	processManager   *tools.ManagedProcessManager
	terminalContexts map[string]context.Context
	permissions      map[string]*permissionWaiter
	// computerAllowAll is set once the user approves a computer_exec call: routine
	// Cua tools then run without asking for as long as the runtime stays loaded.
	computerAllowAll bool
	questions        map[string]*questionWaiter
	elicitations     map[string]*elicitationWaiter
	clientTools      map[string]*clientToolWaiter
	eventDir         string
	// journal is the event journal kept open while a run streams; journalMarks
	// are seq/offset pairs that let replay start near a cursor (event_journal.go).
	journal         *os.File
	journalPath     string
	journalMarks    []journalMark
	journalIdentity os.FileInfo
	mcpActivation   *mcp.ToolActivation
	memoryRuntime   agent.MemoryRuntime
	runtimeErr      error
	goal            goalRuntime
	queue           *queueDocument
	queueWorker     bool
	queueDispatchID string
	// wake holds background notices that arrived while no turn was running.
	wake wakeState
	// subagents tracks this runtime's subagents; see subagents.go.
	subagentMu sync.Mutex
	subagents  map[string]*liveSubagent
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
	memoryStore := opts.MemoryStore
	if memoryStore == nil {
		memoryStore, err = memory.OpenStore(opts.MemoryRoot)
		if err != nil {
			return nil, err
		}
	}
	if opts.SSHDir == "" {
		opts.SSHDir = filepath.Join(opts.EventDir, "ssh")
	}
	sshHosts, err := sshhosts.Open(opts.SSHDir)
	if err != nil {
		return nil, fmt.Errorf("open SSH settings: %w", err)
	}
	cron, err := newCronManager(opts.Store, opts.DefaultCWD, opts.Settings)
	if err != nil {
		return nil, err
	}
	server := &Server{
		image:           opts.Image,
		filesystemRoots: append([]string(nil), opts.FilesystemRoots...),
		store:           opts.Store, factory: opts.Factory, token: opts.Token, models: append([]protocol.ModelRef(nil), opts.Models...),
		settings: opts.Settings, usage: NewProviderUsageService(opts.Settings), prompts: opts.Prompts,
		eventDir: opts.EventDir, permissionDir: opts.PermissionRulesDir, sandboxMasked: append([]string(nil), opts.SandboxMasked...), sandboxEnvKeep: append([]string(nil), opts.SandboxEnvKeep...), defaultCWD: opts.DefaultCWD, memoryStore: memoryStore, questionWait: opts.QuestionTimeout, memoryRuntimeFactory: opts.MemoryRuntimeFactory, cron: cron, hookStore: hookStore, hookRunner: NewBackendHookRunner(hookStore), mcp: opts.MCP, sessions: make(map[string]*runtimeSession), snapshots: make(map[string]sessionHTTPSnapshot),
		build: opts.Build, ssh: sshmgr.New(sshHosts, sshmgr.Options{}),
	}
	if len(server.filesystemRoots) == 0 && opts.DefaultCWD != "" {
		server.filesystemRoots = []string{opts.DefaultCWD}
	}
	cron.promptExecutor = server.executeCronPromptCanonical
	cron.loopResults = server.deliverLoopResults
	go cron.loop()
	if opts.MemoryRuntimeFactory != nil && opts.MemoryOrganizerInterval != 0 {
		interval := opts.MemoryOrganizerInterval
		if interval < 0 {
			interval = 24 * time.Hour
		}
		runtime, runtimeErr := opts.MemoryRuntimeFactory(context.Background(), opts.DefaultCWD, protocol.ModelRef{})
		if runtimeErr != nil {
			cron.close()
			server.ssh.Close()
			return nil, fmt.Errorf("start memory organizer runtime: %w", runtimeErr)
		}
		server.organizerRuntime = runtime
		server.organizerCancel = runtime.StartOrganizerScheduler(context.Background(), opts.DefaultCWD, interval)
	}
	server.startRuntimeReaper()
	// Probe the platform sandbox off the request path; /v1/health reports it.
	go sandbox.Probe()
	return server, nil
}

func (s *Server) Close() error {
	s.shuttingDown.Store(true)
	s.snapshotMu.Lock()
	if s.snapshotBuildCancel != nil {
		s.snapshotBuildCancel()
	}
	for id, snapshot := range s.snapshots {
		if snapshot.timer != nil {
			snapshot.timer.Stop()
		}
		delete(s.snapshots, id)
	}
	s.snapshotMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	if s.reaperStop != nil {
		close(s.reaperStop)
	}
	planningCancel, planningDone := s.planningCancel, s.planningDone
	s.mu.Unlock()
	if s.reaperDone != nil {
		<-s.reaperDone
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
		old := rt.changed
		rt.changed = make(chan struct{})
		close(old)
		if rt.cancel != nil {
			rt.cancel()
		}
		// Background subagents outlive their turn, not the backend.
		if rt.agent != nil {
			rt.agent.Tasks().CancelRunning()
		}
		rt.mu.Unlock()
	}
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
	if s.ssh != nil {
		s.ssh.Close()
	}
	if planningCancel != nil {
		planningCancel()
		<-planningDone
	}
	for _, rt := range runtimes {
		// Runs persist history, checkpoints and terminal events after cancellation.
		rt.runWorkers.Wait()
		// So do background subagents (their record, journal events and report);
		// one a run started before it ended is cancelled here.
		rt.mu.Lock()
		ag := rt.agent
		rt.mu.Unlock()
		if ag != nil {
			ag.Tasks().CancelRunning()
			ag.Tasks().Wait()
		}
	}
	// Reports delivered above may still be dispatching or journaling a checkpoint
	// restore; nothing they start runs now, and none may reopen a closed journal.
	s.background.Wait()
	for _, rt := range runtimes {
		rt.mu.Lock()
		memoryRuntime := rt.memoryRuntime
		rt.memoryRuntime = nil
		terminalManager := rt.terminalManager
		processManager := rt.processManager
		rt.terminalManager = nil
		rt.processManager = nil
		rt.closeJournalLocked()
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
	if s.shuttingDown.Load() {
		writeJSONError(w, http.StatusServiceUnavailable, "backend is closing")
		return
	}
	s.cors(w, r)
	if r.URL.Path == "/v1/image" || strings.HasPrefix(r.URL.Path, "/v1/image/") {
		r = imageRequest(r)
		w.Header().Set("X-Image-Version", imageWireVersion())
		w.Header().Set("X-Request-ID", r.Context().Value(imageRequestIDKey{}).(string))
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !s.authorized(r) {
			imageUnauthorized(w, r)
			return
		}
		s.handleImage(w, r)
		return
	}
	// Replies are written after the handler returns and releases its locks
	// (deferred_response.go); streaming handlers flush and write directly.
	deferred := newDeferredResponse(w)
	defer deferred.finish()
	w = deferred
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
		writeJSON(w, http.StatusOK, s.health())
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/debug/stacks" {
		s.debugStacks(w, r)
		return
	}
	if r.URL.Path == "/v1/host-bridge" {
		s.handleHostBridge(w, r)
		return
	}
	if r.URL.Path == "/v1/sandbox/setup" {
		s.handleSandboxSetup(w, r)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		s.handleModels(w)
		return
	}
	if r.URL.Path == "/v1/fs" {
		s.handleFilesystem(w, r)
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
	if r.Method == http.MethodPost && r.URL.Path == "/v1/generate" {
		s.generate(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/terminal") {
		s.handleTerminal(w, r)
		return
	}
	if r.URL.Path == "/v1/planning" {
		s.handlePlanning(w, r)
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
	if r.Method == http.MethodGet && r.URL.Path == "/v1/commands" {
		s.handleSlashCommands(w, r)
		return
	}
	if r.URL.Path == "/v1/permissions/rules" || strings.HasPrefix(r.URL.Path, "/v1/permissions/rules/") {
		s.permissionRules(w, r)
		return
	}
	if r.URL.Path == "/v1/settings" {
		s.handleSettings(w, r)
		return
	}
	if r.URL.Path == "/v1/network/proxy" {
		s.handleNetworkProxy(w, r)
		return
	}
	if r.URL.Path == "/v1/computer" {
		s.handleComputer(w, r)
		return
	}
	if r.URL.Path == sshSettingsPath || strings.HasPrefix(r.URL.Path, sshSettingsPath+"/") {
		s.handleSSHSettings(w, r)
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
	if r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/exists" {
		s.sessionsExist(w, r)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/changes" {
		s.sessionChanges(w, r)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/workdirs" {
		s.sessionWorkdirs(w, r)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/snapshots" {
		s.createSessionSnapshot(w, r)
		return
	}
	if snapshotID, ok := snapshotPathID(r.URL.Path); ok && (r.Method == http.MethodGet || r.Method == http.MethodDelete) {
		s.sessionSnapshot(w, r, snapshotID)
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
	if len(parts) >= 2 && parts[1] == "subagents" {
		s.handleSubagents(w, r, id, parts[2:])
		return
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		s.deleteSession(w, r, id)
		return
	}
	if len(parts) >= 2 && parts[1] == "queue" && (len(parts) == 2 || (len(parts) == 3 && parts[2] == "actions")) {
		s.handleQueue(w, r, id, len(parts) == 3)
		return
	}
	if len(parts) == 2 && parts[1] == "command-notes" {
		s.handleCommandNote(w, r, id)
		return
	}
	if len(parts) >= 2 && parts[1] == "loops" {
		s.handleSessionLoops(w, r, id, parts[2:])
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
		case "goal":
			if r.Method == http.MethodGet || r.Method == http.MethodPost {
				s.handleGoal(w, r, id)
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
	if len(parts) == 2 && parts[1] == "runs" && r.Method == http.MethodGet {
		s.lookupRun(w, r, id)
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
	if len(parts) == 6 && parts[1] == "runs" && parts[2] != "" && parts[3] == "tools" && parts[4] != "" && parts[5] == "background" && r.Method == http.MethodPost {
		s.backgroundTool(w, r, id, parts[2], parts[4])
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
	if len(parts) == 3 && parts[1] == "elicitations" && parts[2] != "" && r.Method == http.MethodPost {
		s.answerElicitation(w, r, id, parts[2])
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
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Cache-Control, Idempotency-Key, X-Idempotency-Key, Last-Event-ID")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Expose-Headers", "X-Image-Version, X-Request-ID, Content-Length, Content-Type")
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
	defer s.releaseRuntime(rt)
	writeJSON(w, http.StatusCreated, s.sessionView(rt))
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request, id string) {
	rt, err := s.historyRuntime(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	defer s.releaseRuntime(rt)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	view, err := s.sessionViewLocked(rt)
	if err != nil {
		mutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// Read-only history does not require credentials or a configured model.
func (s *Server) historyRuntime(id string) (*runtimeSession, error) {
	if !validID(id) {
		return nil, session.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rt := s.sessions[id]
	if rt != nil {
		rt.leases.Add(1)
		return rt, nil
	}
	if _, _, err := s.store.Load(id); err != nil {
		return nil, err
	}
	if err := s.reconcileStaleTasks(id); err != nil {
		log.Printf("backend: settle stale subagents of %s: %v", id, err)
	}
	rt = &runtimeSession{id: id, runs: map[string]runRecord{}}
	if err := rt.loadJournal(s.eventDir); err != nil {
		return nil, err
	}
	return rt, nil
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
	return s.sessionSnapshotView(rt, snap)
}

func (s *Server) sessionSnapshotView(rt *runtimeSession, snap session.HistorySnapshot) (protocol.Session, error) {
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
	out := protocol.SessionSummary{ID: meta.ID, Title: meta.Title, CWD: meta.CWD, Model: protocol.ModelRef{Provider: meta.Provider, Model: meta.Model}, CreatedAt: meta.CreatedAt, UpdatedAt: meta.UpdatedAt, MessageCount: count, Pinned: meta.Pinned, Archived: meta.Archived, Shared: meta.Shared}
	// Subagent sessions reuse ForkedFrom for their parent; that is not a user-visible branch.
	if meta.TaskID == "" {
		out.ForkedFrom, out.ForkSeq = meta.ForkedFrom, meta.ForkSeq
	}
	return out
}

func (s *Server) loadRuntimeByID(id string) (*runtimeSession, error) {
	if !validID(id) {
		return nil, session.ErrNotFound
	}
	s.mu.Lock()
	if rt := s.sessions[id]; rt != nil {
		rt.leases.Add(1)
		s.mu.Unlock()
		rt.mu.Lock()
		deleted := rt.deleted
		rt.mu.Unlock()
		if deleted {
			s.releaseRuntime(rt)
		}
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
	model := protocol.ModelRef{Provider: meta.Provider, Model: meta.Model}
	// Historical sessions may refer to a model that was removed from the
	// current configuration. Loading history still needs a runtime for task
	// and event inspection, so use the current default active model as the
	// runtime implementation while preserving the stored session metadata.
	if !s.knownModel(model) {
		if fallback, ok := s.fallbackModel(); ok {
			model = fallback
		}
	}
	return s.loadRuntime(id, model, meta.CWD)
}

func (s *Server) fallbackModel() (protocol.ModelRef, bool) {
	if s.settings == nil {
		return protocol.ModelRef{}, false
	}
	projection := projectSettings(s.settings.Snapshot())
	if len(projection.Models) == 0 {
		return protocol.ModelRef{}, false
	}
	for _, model := range projection.Models {
		if model.Provider == projection.DefaultProvider && model.ID == projection.DefaultModel {
			return protocol.ModelRef{Provider: model.Provider, Model: model.ID}, true
		}
	}
	model := projection.Models[0]
	return protocol.ModelRef{Provider: model.Provider, Model: model.ID}, true
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
	return s.rebuildAgentLocked(rt, selected, cwd)
}

// switchWorkingDirLocked moves a session to another workspace. A loaded agent is rebuilt with
// the same model so tools, memory and project instructions follow the new directory; an
// unloaded session only needs the stored metadata, which the next load reads.
func (s *Server) switchWorkingDirLocked(rt *runtimeSession, cwd string) error {
	oldCWD := ""
	if rt.agent != nil {
		oldCWD = rt.agent.WorkingDir
	}
	if oldCWD == "" {
		if meta, _, err := s.store.Load(rt.id); err == nil {
			oldCWD = meta.CWD
		}
	}
	if oldCWD == cwd {
		return nil
	}
	if err := s.store.SetCWD(rt.id, cwd); err != nil {
		return err
	}
	if rt.agent == nil || rt.agent.WorkingDir == cwd {
		return nil
	}
	selected := protocol.ModelRef{Provider: rt.agent.Provider, Model: rt.agent.ModelName}
	if err := s.rebuildAgentLocked(rt, selected, cwd); err != nil {
		if rollbackErr := s.store.SetCWD(rt.id, oldCWD); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("rollback workspace after agent rebuild: %w", rollbackErr))
		}
		return err
	}
	return nil
}

func (s *Server) rebuildAgentLocked(rt *runtimeSession, selected protocol.ModelRef, cwd string) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return errors.New("backend is closed")
	}
	// The running subagents belong to the current agent's registry and report
	// through it; a replacement agent would orphan them.
	if !rt.backgroundSettledLocked() {
		return errBackgroundRunning
	}
	ag, err := s.factory(context.Background(), cwd, selected)
	if err != nil {
		return err
	}
	ag.WorkingDir = cwd
	terminalManager := rt.terminalManager
	processManager := rt.processManager
	ag.Tools = append(tools.WithoutCLIOnlyTools(ag.Tools), tools.LiveAgentCatalogWithManagers([]string{selected.Provider}, terminalManager, processManager)...)
	ag.SetManagedProcesses(processManager)
	if s.cron != nil {
		s.cron.attachTool(ag, selected)
	}
	s.attachPlanningTools(ag)
	s.syncHostTools(ag)
	ag.ModelName, ag.Provider = selected.Model, selected.Provider
	if s.mcp != nil {
		mcpTools, filter := s.mcp.ToolsForTurn(context.Background(), cwd, nil, rt.mcpActivation)
		ag.SetMCPTools(mcpTools)
		ag.RequestToolFilter = filter
	}
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
		rt.leases.Add(1)
		s.mu.Unlock()
		rt.mu.Lock()
		deleted := rt.deleted
		rt.mu.Unlock()
		if deleted {
			s.releaseRuntime(rt)
		}
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
	ag.Tools = append(tools.WithoutCLIOnlyTools(ag.Tools), tools.LiveAgentCatalogWithManagers([]string{model.Provider}, terminalManager, processManager)...)
	ag.SetManagedProcesses(processManager)
	if s.cron != nil {
		s.cron.attachTool(ag, model)
	}
	s.attachPlanningTools(ag)
	s.syncHostTools(ag)
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
	processManager.SetExitNotifier(func(text string) { s.steerRuntime(rt, text) })
	if err := rt.loadJournal(s.eventDir); err != nil {
		return nil, err
	}
	if err := rt.loadQueueLocked(); err != nil {
		return nil, err
	}
	s.wireTasks(rt, ag)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("backend is closed")
	}
	if existing := s.sessions[id]; existing != nil {
		existing.leases.Add(1)
		s.mu.Unlock()
		existing.mu.Lock()
		deleted := existing.deleted
		existing.mu.Unlock()
		if deleted {
			s.releaseRuntime(existing)
		}
		if deleted {
			return nil, session.ErrNotFound
		}
		return existing, nil
	}
	if _, _, err := s.store.Load(id); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if err := s.reconcileStaleTasks(id); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	rt.leases.Store(1)
	rt.lastUsed = time.Now()
	s.sessions[id] = rt
	s.mu.Unlock()
	adopted = true
	return rt, nil
}

// staleTaskReport matches the TUI's wording for a subagent whose process ended first.
const staleTaskReport = "interrupted — k-brain exited before this subagent finished"

// reconcileStaleTasks settles stored tasks still marked running when no runtime
// owns the session. A runtime is never evicted or rebuilt while its subagents
// run, so such a task belonged to a k-brain process that exited. The caller
// holds s.mu, so no runtime for the session can start one meanwhile.
func (s *Server) reconcileStaleTasks(id string) error {
	tasks, err := s.store.LoadTasks(id)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.Status != string(agent.TaskRunning) {
			continue
		}
		task.Status, task.Report = string(agent.TaskError), staleTaskReport
		if task.EndedAt.IsZero() {
			task.EndedAt = time.Now()
		}
		if err := s.store.SaveTask(id, task); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) wireTasks(rt *runtimeSession, ag *agent.Agent) {
	s.wireQuestions(rt, ag)
	s.wireWakes(rt, ag)
	ag.Tasks().SetSessionID(rt.id)
	ag.Tasks().OnEvents = rt.trajectoryChildEvents
	ag.Tasks().OnRecord = func(id string, task *agent.BackgroundTask) {
		rt.mu.Lock()
		deleted := rt.deleted
		rt.mu.Unlock()
		if deleted {
			// A subagent cancelled by the delete must not write the session back.
			return
		}
		if task.Status == agent.TaskCancelled && s.shuttingDown.Load() {
			// Cancelled by Close: keep it stored as running so the next start reports
			// it interrupted, as after any exit, but journal the text it produced.
			rt.flushSubagent(task.ID)
			return
		}
		if task.Status != agent.TaskRunning {
			// A settled child task may be all that kept pending notices waiting.
			defer s.background.Go(func() { s.kickWakes(rt) })
		}
		model, provider, _ := strings.Cut(task.SubModel, " @ ")
		stored := session.Task{Model: model, Provider: provider, ID: task.ID, Description: task.Description, Prompt: task.Prompt, Status: string(task.Status), Report: task.Report, StartedAt: task.StartedAt, EndedAt: task.EndedAt, ParentToolCallID: task.ParentToolCallID}
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
	running := t.Status == agent.TaskRunning
	if running {
		// Mark it live before the start is journaled so readers never see a
		// started subagent as already over.
		rt.subagent(t.ID, runID)
		rt.setSubagentRunning(t.ID, true, nil)
	} else {
		rt.flushSubagent(t.ID)
	}
	rt.subagentStats(t.ID, &view)
	if usage := protocol.FromAIUsage(&t.SubUsage); usage != nil && *usage != (protocol.Usage{}) {
		view.Usage = usage
	}
	_, _ = rt.publishTrajectory(runID, typ, protocol.SubagentEvent{Subagent: view}, "")
	if !running {
		rt.setSubagentRunning(t.ID, false, nil)
	}
}

func subagentView(t agent.BackgroundTask) protocol.Subagent {
	view := storedSubagentView(session.Task{ID: t.ID, Description: t.Description, Status: string(t.Status), Report: t.Report, StartedAt: t.StartedAt, EndedAt: t.EndedAt, ParentToolCallID: t.ParentToolCallID})
	model, provider, _ := strings.Cut(t.SubModel, " @ ")
	view.Model = protocol.ModelRef{Provider: provider, Model: model}
	return view
}
func storedSubagentView(t session.Task) protocol.Subagent {
	view := protocol.Subagent{ID: t.ID, Description: t.Description, Status: t.Status, Report: t.Report, Model: protocol.ModelRef{Model: t.Model, Provider: t.Provider}, ParentToolCallID: t.ParentToolCallID, Background: true, StartedAt: ptrTime(t.StartedAt), EndedAt: ptrTime(t.EndedAt)}
	if !t.StartedAt.IsZero() && !t.EndedAt.IsZero() {
		view.DurationMS = t.EndedAt.Sub(t.StartedAt).Milliseconds()
	}
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
	rt.appendEventLocked(e)
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
	rt.appendEventLocked(e)
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
	defer s.releaseRuntime(rt)
	var in protocol.PromptRequest
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	// A user run may answer a goal waiting for input or get a paused-goal notice; cron runs never do.
	accepted, status, err := s.startCanonicalRunWith(context.Background(), rt, in, s.goalUserRunAdmit(rt))
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	writeJSON(w, status, accepted)
}

// end cancels ctx once the run is over; a stop that came first keeps its own cause.
func (s *Server) executeRun(rt *runtimeSession, ctx context.Context, end func(), runID string, in protocol.PromptRequest, options protocol.RunOptions, sandboxPolicy *sandbox.Policy) {
	progress := &runProgress{started: time.Now(), publish: rt.publish}
	progress.phase("preparing")
	rt.mu.Lock()
	restoreRunOptions := applyRunOptionsWith(rt.agent, options, s.withSSHManagerTool(s.clientTools(rt, options.ClientTools), rt.agent.WorkingDir, options))
	rt.mu.Unlock()
	defer func() {
		rt.mu.Lock()
		restoreRunOptions()
		end()
		rt.runDone = true
		rt.cancel = nil
		rt.closeJournalLocked()
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
	reviewer := s.newRunReviewer(ctx, rt, in.Prompt, options.PermissionMode, options.ReviewScope, options.Reasoning)
	// Loop and cron workers rebuild rt.agent under rt.mu; read it the same way.
	rt.mu.Lock()
	workingDir := rt.agent.WorkingDir
	rt.mu.Unlock()
	ruleRoot := projectRootFor(options, workingDir)
	gate := func(req tools.GateRequest) (tools.GateDecision, string) {
		if options.Mode == "chat" {
			return tools.GateReject, "chat mode disables tools"
		}
		if options.ApprovalPolicy == "deny" {
			return tools.GateReject, "approval policy denies this action"
		}
		if isUnattendedRun(req.Context) {
			if hostBridgeTool(req.Tool) || req.Tool == tools.SSHManagerTool {
				// Nobody can answer a prompt: host reads (ReadTerminal, TunnelManager
				// list/check) run as read-only, host actions only under an explicit allow
				// rule or per-tool policy, and anything that would ask fails at once.
				return runToolGate(options, req, s.evaluateRules(rt.id, ruleRoot, req), func(tools.GateRequest) (tools.GateDecision, string) {
					return tools.GateReject, "unattended scheduled runs cannot request interactive approval; add an allow rule for " + req.Tool
				})
			}
			return tools.GateReject, "unattended scheduled runs cannot request interactive approval"
		}
		gateOptions := options
		if reviewer != nil {
			// A configured reviewer still sees every command of a sandboxed run.
			gateOptions.Sandbox = nil
		}
		computerReadOnly, computerRememberable := false, false
		// A run that names computer_exec "ask" itself wants every call confirmed: the
		// read-only exemption and the session grant must not override it.
		explicitComputerAsk := options.Tools != nil && options.Tools.Policies["computer_exec"] == "ask"
		if req.Tool == "computer_exec" {
			policy := computerApprovalPolicy(rt.agent.ComputerConfig)
			if policy == "deny" {
				return tools.GateReject, "computer use denied by backend configuration"
			}
			if options.Tools != nil && options.Tools.Policies["computer_exec"] == "deny" {
				return tools.GateReject, "computer use denied for this run"
			}
			req.Rule, computerReadOnly, computerRememberable = computerApprovalRule(req.Command)
			switch policy {
			case "ask":
				gateOptions.ApprovalPolicy = "ask"
				gateOptions.PermissionMode = "normal"
				gateOptions.Tools = &protocol.ToolSelection{Policies: map[string]string{"computer_exec": "ask"}}
			case "allow":
				// The backend grant applies unless this run asked for computer_exec explicitly;
				// an explicit ask then prompts in every permission mode, as under backend "ask".
				if explicitComputerAsk {
					gateOptions.ApprovalPolicy = "ask"
					gateOptions.PermissionMode = "normal"
				}
				policies := map[string]string{}
				if options.Tools != nil {
					for tool, toolPolicy := range options.Tools.Policies {
						policies[tool] = toolPolicy
					}
				}
				if policies["computer_exec"] == "" {
					policies["computer_exec"] = "allow"
				}
				gateOptions.Tools = &protocol.ToolSelection{Policies: policies}
			}
		}
		rules := s.evaluateRules(rt.id, ruleRoot, req)
		// A run that names this tool "ask" wants every call confirmed by the user:
		// no rule is offered or settles its prompts.
		explicitToolAsk := gateOptions.Tools != nil && gateOptions.Tools.Policies[req.Tool] == "ask"
		manualReview := reviewer != nil && reviewer.RequiresManual(req.Context)
		if manualReview && approval.RuleStandsInForReview(req.Context, rules.Effect == permissions.Allow && rules.Source == permissions.SourceRule) {
			manualReview = false
		}
		if manualReview {
			gateOptions.ApprovalPolicy = "ask"
			gateOptions.PermissionMode = "normal"
			policies := make(map[string]string)
			if gateOptions.Tools != nil {
				for tool, policy := range gateOptions.Tools.Policies {
					policies[tool] = policy
				}
			}
			if policies[req.Tool] != "deny" {
				policies[req.Tool] = "ask"
			}
			gateOptions.Tools = &protocol.ToolSelection{Policies: policies}
		}
		decision, reason := runToolGate(gateOptions, req, rules, func(req tools.GateRequest) (tools.GateDecision, string) {
			rt.mu.Lock()
			active := rt.runID == runID && !rt.runDone && !rt.deleted
			rt.mu.Unlock()
			if !active {
				return tools.GateReject, "interactive approval requires the owning active run"
			}
			// Reached only after plan mode and deny policies passed. A manual review
			// verdict or an explicit run-level ask overrides read-only and session approvals.
			if req.Tool == "computer_exec" && !manualReview && !explicitComputerAsk {
				if computerReadOnly {
					return tools.GateAllowOnce, "read-only computer action"
				}
				rt.mu.Lock()
				remembered := computerRememberable && rt.computerAllowAll
				rt.mu.Unlock()
				if remembered {
					return tools.GateAllowOnce, "computer use allowed for this session"
				}
			}
			if reviewer != nil {
				if reviewed, ok := req.Context.Value(approval.ReviewedActionKey{}).(string); ok && reviewed == req.Tool {
					return tools.GateAllowOnce, "action already approved"
				}
				if report, ok := req.Context.Value(approval.ActionReportKey{}).(approval.Report); ok {
					req.Description = report.Description()
					if reviewer.Mode == "auto" && report.Safe() && req.Context.Err() == nil {
						return tools.GateAllowOnce, "AI approved this action once"
					}
				}
			}
			// Approving any Cua call grants the session; batch and install tools
			// still ask on their own next time.
			sessionGrant := req.Tool == "computer_exec" && strings.HasPrefix(req.Rule, "call:") && !manualReview && !explicitComputerAsk
			rememberable := !sessionGrant && !explicitToolAsk && approval.RulesUsable(req.Context)
			var suggestion *protocol.PermissionSuggestion
			if rememberable {
				suggestion = s.suggestionFor(req, ruleRoot)
			}
			decision, reason := s.waitPermission(rt, runID, req, sessionGrant, req.Context, permissionPrompt{suggestion: suggestion, root: ruleRoot, settleable: rememberable && !manualReview})
			if decision != tools.GateReject && sessionGrant {
				rt.mu.Lock()
				rt.computerAllowAll = true
				// Prompts for other Cua calls that are already waiting are covered by
				// the grant too; settle them so the user is not asked again.
				for id, waiter := range rt.permissions {
					_, _, rememberable := computerApprovalRule(waiter.request.Command)
					if waiter.request.Tool != "computer_exec" || waiter.request.Scope != "session" || !rememberable {
						continue
					}
					delete(rt.permissions, id)
					select {
					case waiter.reason <- "computer use allowed for this session":
					default:
					}
					select {
					case waiter.result <- tools.GateAllowOnce:
					default:
					}
				}
				rt.mu.Unlock()
			}
			return decision, reason
		})
		// A call the review gate already decided reaches this gate again from the tool
		// itself; report the automatic decision once.
		if decision == tools.GateAllowOnce && (reason == ruleAllowReason || reason == readOnlyAllowReason) {
			if reviewed, _ := req.Context.Value(approval.ReviewedActionKey{}).(string); reviewed != req.Tool {
				s.publishAutoResolved(rt, req, rules)
			}
		}
		return decision, reason
	}
	roots := make([]tools.WorkspaceRoot, len(options.WorkspaceRoots))
	for i, root := range options.WorkspaceRoots {
		roots[i] = tools.WorkspaceRoot{Path: root.Path, Access: root.Access}
	}
	ctx = tools.WithWorkspaceRoots(ctx, roots)
	// The system prompt points the model at SKILL.md files by absolute path; grant those
	// directories read-only so following the prompt is not rejected by the root policy.
	ctx = tools.WithSkillReadRoots(ctx, runReadRoots(s.store, rt.id, rt.agent.WorkingDir))
	ctx = tools.WithGate(ctx, gate)
	if reviewer != nil {
		ctx = tools.WithReview(ctx, reviewer.Hooks(gate))
	}
	ctx = tools.WithAsk(ctx, func(questionCtx context.Context, req tools.AskRequest) ([]tools.AskAnswer, bool) {
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
		answers := make([]tools.AskAnswer, len(result.Answers))
		for i, answer := range result.Answers {
			answers[i] = askAnswer(answer)
		}
		// The question tool's history keeps the answers as question.resolved has them.
		tools.ReportQuestionAnswers(questionCtx, result.Answers)
		return answers, true
	})
	ctx = elicit.With(ctx, func(elicitCtx context.Context, req elicit.Request) elicit.Response {
		switch {
		case isUnattendedRun(elicitCtx):
			return elicit.Response{Action: elicit.Decline, Reason: elicit.ReasonUnattended}
		case !options.MCPElicitation:
			return elicit.Response{Action: elicit.Decline, Reason: elicit.ReasonNoClient}
		}
		return s.waitElicitation(elicitCtx, rt, runID, tools.ToolCallID(elicitCtx), req)
	})
	ctx = context.WithValue(ctx, questionRunKey{}, runID)
	ctx = tools.WithRunIdentity(ctx, tools.RunIdentity{ConversationID: rt.id, RunID: runID})
	ctx = tools.WithWorkingDir(ctx, rt.agent.WorkingDir)
	ctx = sandbox.WithPolicy(ctx, sandboxPolicy)
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
	if in.Snapshot != "" {
		ctx = agent.WithTurnSnapshot(ctx, ai.PromptSnapshot{Source: "turn-command", Text: "\n\n" + in.Snapshot})
	}
	hookEvents := s.hookRunner.Scope(ctx, rt.id, runID, rt.agent.WorkingDir, func(hook BackendHook, event string, err error) {
		_, _ = rt.publish(protocol.EventHookWarning, map[string]any{"hookName": hook.Name, "hookType": hook.Type, "event": event, "message": err.Error()}, "")
	})
	ctx = agent.WithLifecycleStart(ctx, func() { hookEvents("agent_start") })
	toolOutput := newToolOutputThrottle(toolOutputInterval, func(id, output string) {
		_, _ = rt.publish(protocol.EventToolStatus, protocol.ToolStatus{ToolCallID: id, Status: "running", Message: output}, "")
	})
	defer toolOutput.close()
	ev := agent.Events{OnPhase: progress.phase, OnCompactStart: func(int, int) { progress.phase("compacting") }, OnLifecycle: hookEvents, OnText: func(d string) {
		if d != "" {
			progress.phase("responding")
		}
		_, _ = rt.publish(protocol.EventTextDelta, protocol.TextDelta{Text: d}, "")
	}, OnThink: func(d string) {
		if d != "" {
			progress.phase("thinking")
		}
		_, _ = rt.publish(protocol.EventThinkingDelta, protocol.TextDelta{Text: d}, "")
	}, OnHostedSearch: func(search ai.HostedSearch) {
		_, _ = rt.publish(protocol.EventHostedSearch, protocol.HostedSearch{Type: search.Type, ID: search.ID, Provider: search.Provider, Status: search.Status, Queries: search.Queries, Sources: func() []protocol.HostedSearchSource {
			out := make([]protocol.HostedSearchSource, len(search.Sources))
			for i, source := range search.Sources {
				out[i] = protocol.HostedSearchSource{URL: source.URL, Title: source.Title, SourceType: source.SourceType}
			}
			return out
		}(), Error: search.Error}, "")
	}, OnToolStart: func(id, name, args string) {
		progress.phase("tool")
		_, _ = rt.publish(protocol.EventToolCall, protocol.ToolCallEvent{ToolCall: protocol.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(args)}}, "")
	}, OnToolResult: func(id, name string, result tools.Result) {
		toolOutput.finish(id)
		_, _ = rt.publish(protocol.EventToolResult, protocol.ToolResultEvent{ToolResult: protocol.ToolResult{ID: id, Name: name, Output: result.Text, Content: protocol.FromAIMessage(ai.Message{Role: "tool", Parts: result.Parts}).Content, Failed: result.Failed, Cancelled: result.Cancelled, ToolRunInfo: toolRunInfo(result)}}, "")
	}, OnToolOutput: toolOutput.update, OnUsage: func(u ai.Usage) {
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
		turnCtx := ctx
		if runtimePromptRun(ctx) {
			turnCtx = agent.WithBackgroundNotice(ctx)
		}
		_, err = rt.agent.TurnParts(turnCtx, converted.Content, converted.Parts, ev)
	}
	toolOutput.close()
	progress.phase("persisting")
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
		s.evaluateGoal(rt, runID, "cancelled")
		finish(protocol.EventRunCancelled, protocol.RunTerminal{State: "cancelled"})
	} else if err != nil {
		if failedAssistant != nil {
			_, _ = rt.publish(protocol.EventAssistantMessage, *failedAssistant, "")
		}
		s.evaluateGoal(rt, runID, "failed")
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
		s.evaluateGoal(rt, runID, "completed")
		finish(protocol.EventRunCompleted, protocol.RunTerminal{State: "completed"})
	}
}

// toolRunInfo is the display metadata of a tool result for tool.result events.
func toolRunInfo(result tools.Result) protocol.ToolRunInfo {
	info := protocol.ToolRunInfo{ExitCode: result.ExitCode, TimedOut: result.TimedOut, DurationMs: result.DurationMs(), QuestionAnswers: result.QuestionAnswers, Elicitations: result.Elicitations}
	if d := result.SandboxDenial; d != nil {
		info.SandboxDenial = &protocol.SandboxDenial{Kind: d.Kind, Path: d.Path, SuggestedRoot: d.SuggestedRoot}
	}
	return info
}

func permissionPreview(p *tools.EditPreview) *protocol.PermissionPreview {
	if p == nil {
		return nil
	}
	return &protocol.PermissionPreview{Path: p.Path, Diff: p.Diff, NewFile: p.NewFile, Truncated: p.Truncated}
}

// waitPermission asks the client; sessionGrant marks a prompt whose approval also
// grants the session, labelling allow_once accordingly.
// permissionPrompt carries what a prompt can remember: the suggested rules, the
// project root they resolve against, and whether remembered rules may settle it.
type permissionPrompt struct {
	suggestion *protocol.PermissionSuggestion
	root       string
	settleable bool
}

func (s *Server) waitPermission(rt *runtimeSession, runID string, req tools.GateRequest, sessionGrant bool, ctx context.Context, prompt permissionPrompt) (tools.GateDecision, string) {
	id := newRunID()
	allow := protocol.PermissionOption{ID: "allow_once", Label: "Allow once", Kind: "allow_once"}
	scope := ""
	if sessionGrant {
		allow.Label, scope = "Allow for this session", "session"
	}
	options := []protocol.PermissionOption{allow, {ID: "reject", Label: "Reject", Kind: "reject_once"}}
	if prompt.suggestion != nil {
		options = append(options, protocol.PermissionOption{ID: "allow_always", Label: "Allow and remember", Kind: "allow_always"})
	}
	gateReq := req
	gateReq.Context = nil
	p := &permissionWaiter{request: protocol.PermissionRequest{PermissionID: id, Tool: req.Tool, Command: req.Command, Shell: req.Shell, Rule: req.Rule, Description: req.Description, Options: options, Suggestion: prompt.suggestion, Scope: scope, ToolCallID: tools.ToolCallID(ctx), Preview: permissionPreview(req.Preview)}, result: make(chan tools.GateDecision, 1), reason: make(chan string, 1), gate: gateReq, root: prompt.root, ruleSettleable: prompt.settleable && !sessionGrant}
	rt.mu.Lock()
	_, _, rememberable := computerApprovalRule(req.Command)
	if sessionGrant && rememberable && rt.computerAllowAll {
		rt.mu.Unlock()
		return tools.GateAllowOnce, "computer use allowed for this session"
	}
	rt.permissions[id] = p
	err := rt.publishEventLocked(runID, protocol.EventPermissionRequest, p.request)
	if err != nil {
		delete(rt.permissions, id)
	}
	rt.mu.Unlock()
	if err != nil {
		return tools.GateReject, "publish permission request: " + err.Error()
	}
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
	s.eventsWithHeartbeat(w, r, id, 15*time.Second)
}

func (s *Server) eventsWithHeartbeat(w http.ResponseWriter, r *http.Request, id string, interval time.Duration) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	defer s.releaseRuntime(rt)
	after, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	// Flush headers before waiting so clients and proxies establish the live stream.
	flusher.Flush()
	firstText, firstThinking := true, true
	heartbeat := time.NewTicker(interval)
	defer heartbeat.Stop()
	for {
		rt.mu.Lock()
		if rt.deleted {
			rt.mu.Unlock()
			return
		}
		pending := make([]protocol.Event, 0)
		active := rt.cancel != nil && !rt.runDone
		replay, err := rt.eventsAfterLocked(after)
		if err != nil {
			rt.mu.Unlock()
			return
		}
		for _, e := range replay {
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
		for i, e := range pending {
			if err := writeSSE(w, e); err != nil {
				return
			}
			after = e.Seq
			flush := (i+1)%32 == 0
			if e.Type == protocol.EventTextDelta && firstText {
				firstText, flush = false, true
			}
			if e.Type == protocol.EventThinkingDelta && firstThinking {
				firstThinking, flush = false, true
			}
			if e.Type == protocol.EventRunCompleted || e.Type == protocol.EventRunFailed || e.Type == protocol.EventRunCancelled {
				flusher.Flush()
				return
			}
			if flush {
				flusher.Flush()
			}
		}
		flusher.Flush()
		if len(pending) > 0 {
			continue
		}
		if !active {
			return
		}
		select {
		case <-changed:
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
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
	defer s.releaseRuntime(rt)
	rt.mu.Lock()
	if rt.runID != run || rt.cancel == nil {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "run is not active")
		return
	}
	cancel := rt.cancel
	// Stopping a turn the backend started on its own also keeps it from starting
	// another until someone else starts a run.
	rt.holdWakesLocked(true, false)
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
	defer s.releaseRuntime(rt)
	rt.mu.Lock()
	// A closed session does not wake itself; the next run reopens delivery.
	rt.holdWakesLocked(false, true)
	if rt.cancel != nil {
		rt.cancel()
	}
	// Background subagents outlive their turn, not the session: closing ends them.
	if rt.agent != nil {
		rt.agent.Tasks().CancelRunning()
	}
	// So do commands the user moved to the background, which a cancelled run
	// leaves running.
	processManager := rt.processManager
	rt.mu.Unlock()
	if processManager != nil {
		processManager.StopBackgrounded()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": id})
}

// backgroundTool moves the foreground shell command of a tool call in the
// active run to the background: the call returns at once and the command keeps
// running as a managed process.
func (s *Server) backgroundTool(w http.ResponseWriter, r *http.Request, id, run, toolCallID string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	defer s.releaseRuntime(rt)
	rt.mu.Lock()
	active := rt.runID == run && rt.cancel != nil
	ag := rt.agent
	rt.mu.Unlock()
	if !active || ag == nil {
		writeJSONError(w, http.StatusConflict, "run is not active")
		return
	}
	processID, err := ag.Foreground().Background(r.Context(), toolCallID)
	if errors.Is(err, tools.ErrNotBackgroundable) {
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tool_call_id": toolCallID, "process_id": processID})
}
func (s *Server) permission(w http.ResponseWriter, r *http.Request, id, pid string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	defer s.releaseRuntime(rt)
	var in protocol.PermissionDecisionRequest
	if err = decodeJSON(w, r, &in); err != nil {
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
	// Claim the prompt before storing anything, so only the answer that wins it can
	// remember a rule.
	rt.mu.Lock()
	p := rt.permissions[pid]
	delete(rt.permissions, pid)
	rt.mu.Unlock()
	if p == nil {
		writeJSONError(w, http.StatusNotFound, "permission request not found")
		return
	}
	remember := d == tools.GateAllowAlways && p.request.Suggestion != nil
	if remember {
		if err := s.rememberSuggestion(rt, p, in.Decision.Scope); err != nil {
			// A refused scope or a failed write leaves the prompt waiting.
			rt.mu.Lock()
			if _, taken := rt.permissions[pid]; !taken {
				rt.permissions[pid] = p
			}
			rt.mu.Unlock()
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if in.Decision.Reason != "" {
		select {
		case p.reason <- in.Decision.Reason:
		default:
		}
	}
	p.result <- d
	if remember {
		s.settleCoveredPermissions(rt)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return decodeJSONLimit(w, r, v, maxBodyBytes)
}

// maxOversizedBodyDrainBytes bounds how much of a rejected body is read so the
// 413 reply reaches the client instead of a connection reset.
const maxOversizedBodyDrainBytes = 1 << 30

func decodeJSONLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	body := r.Body
	r.Body = http.MaxBytesReader(w, body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			// Closing with unread request bytes makes the peer see a reset, not this response.
			_, _ = io.Copy(io.Discard, io.LimitReader(body, maxOversizedBodyDrainBytes))
			writeJSONError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body exceeds %d MiB limit", limit>>20))
			return err
		}
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

// idSequence keeps ids unique when two are minted inside one clock tick: Windows
// reports time in 100ns steps, so parallel tool calls asking for permission at
// once used to share an id and the second answer found no waiter.
var idSequence atomic.Uint64

func newRunID() string {
	return fmt.Sprintf("run-%d-%d", time.Now().UnixNano(), idSequence.Add(1))
}
