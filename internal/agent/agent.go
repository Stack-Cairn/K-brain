package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/hooks"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/tools"
	"github.com/Stack-Cairn/K-brain/internal/workflow"
)

func newMessageID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err == nil {
		return "msg-" + hex.EncodeToString(buf)
	}
	return fmt.Sprintf("msg-%d", time.Now().UnixNano())
}

type Events struct {
	OnPhase        func(stage string)
	OnLifecycle    func(event string)
	OnText         func(delta string)
	OnThink        func(delta string)
	OnHostedSearch func(ai.HostedSearch)
	OnToolStart    func(id, name, args string)
	OnToolEnd      func(id, name, result string)
	OnToolResult   func(id, name string, result tools.Result)

	OnToolCall func(id, name, args string)

	OnToolOutput func(id, outputSoFar string)
	OnSteer      func(text string)
	OnCompact    func(took, kept int)

	OnCompacted func(summary string, cutoff int, info CompactInfo)

	OnCompactStart    func(took, estTokens int)
	OnNewContextStart func(took, estTokens int)
	OnNewContext      func(cutoff int)
	OnUsage           func(u ai.Usage)
	OnRetry           func(ev ai.RetryEvent)

	OnDecay func(n int)

	// OnOutputLimitContinue fires before the agent resumes a reply that stopped at
	// the output-token limit; attempt counts from 1 to MaxOutputLimitContinuations.
	OnOutputLimitContinue func(attempt int)
}

type CompactInfo struct {
	Model    string
	Provider string
	Usage    ai.Usage
	Fresh    bool
}

func (a *Agent) SetOnTodos(fn func(items []Todo)) {
	a.todosMu.Lock()
	a.onTodos = fn
	a.todosMu.Unlock()
}

type retryReporter interface {
	SetOnRetry(func(ai.RetryEvent))
}

func (a *Agent) reportRetries(ev Events) func() {
	client, ok := a.Client.(retryReporter)
	if !ok {
		return func() {}
	}

	a.clientMu.Lock()
	client.SetOnRetry(ev.OnRetry)
	a.clientMu.Unlock()
	return func() {
		a.clientMu.Lock()
		client.SetOnRetry(nil)
		a.clientMu.Unlock()
	}
}

type Agent struct {
	planMode        *atomic.Bool
	Client          ai.Client
	Model           string
	ModelName       string
	Provider        string
	MaxTokens       int
	Effort          string
	Vision          bool
	NativeWebSearch bool

	Temperature *float64
	TopP        *float64
	Tools       []tools.Tool
	// RequestToolFilter narrows schemas sent to the model; execution still uses AllTools.
	RequestToolFilter func([]tools.Tool) []tools.Tool
	RunTools          []tools.Tool
	RunToolsSet       bool
	Messages          []ai.Message

	ContextLimit int

	CompactClient   ai.Client
	CompactModel    string
	CompactProvider string

	CompactThreshold float64

	// CompactionDir resolves the directory that receives compacted-away turns as
	// markdown segments (see compaction_segments.go). Nil or "" disables archiving.
	CompactionDir func() string

	pointerMu       sync.Mutex
	pointerResolved bool
	pointerSummary  string
	pointerText     string

	TaskDefault SubModel

	ResolveModel func(model, provider string) (SubModel, error)

	MaxTurns int

	// ContinueOnOutputLimit resumes a reply cut off by the output-token limit with
	// a continuation reminder, at most MaxOutputLimitContinuations times per turn.
	ContinueOnOutputLimit bool

	WorkingDir string

	WorktreeSubagents bool

	mu               sync.Mutex
	pending          []pendingSteer
	compacted        bool
	contextRequested atomic.Bool
	// planRevise is set when the user asked for changes to a plan submitted
	// with ExitPlanMode, so the turn goes on to revise it (plan_review.go).
	planRevise      atomic.Bool
	contextNoticeMu sync.Mutex
	contextNotice   string
	// contextNoticeOff: the notice quotes a user message written while memory was off.
	contextNoticeOff bool
	running          atomic.Bool
	turnMu           sync.Mutex
	waitReg          *waitRegistry

	msgsMu sync.Mutex

	loopMu      sync.Mutex
	lastCallKey string
	lastCallRun int

	files *fileLocks
	bg    *taskRegistry

	subagentInflight atomic.Int64
	otherInflight    atomic.Int64

	Todos []Todo

	todosMu sync.Mutex
	onTodos func(items []Todo)

	sessionID atomic.Pointer[string]
	cacheKey  string

	toolsMu     sync.Mutex
	mcpTools    []tools.Tool
	pluginTools []tools.Tool

	wfMu sync.Mutex
	wf   *workflow.Manager

	experimental []string

	BrowserDisabled bool

	ComputerDisabled bool
	ComputerConfig   config.ComputerConfig
	SandboxPolicy    *sandbox.Policy

	OnOrphanedSteer func(text string)

	// Reminders run after each tool call and append <system-reminder> context to its
	// result (LSP diagnostics by default). Subagents inherit them.
	Reminders []Reminder

	Hooks              *hooks.Runner
	PluginHook         func(context.Context, hooks.Event) error
	startMu            sync.Mutex
	sessionStarted     bool
	startedSessionID   string
	memoryBlock        string
	runtimeMemoryBlock string
	memoryDisabled     bool
	memoryOff          atomic.Bool
	// markdownMemoryReadOnly is set once a memory runtime owns memory.
	markdownMemoryReadOnly bool
	memoryRuntime          MemoryRuntime
	systemPromptResolver   func() (string, error)
	turnTimeContext        bool

	usageMu    sync.Mutex
	usage      ai.Usage
	modelUsage map[string]ai.Usage

	clientMu sync.Mutex

	subUsage map[string]ai.Usage

	usageSink func(model string, u ai.Usage)

	lastPrompt int
}

func (a *Agent) TurnRunning() bool { return a.running.Load() }

func (a *Agent) Steer(text string) { a.steer(pendingSteer{text: text}) }

// Notify delivers a notice the runtime produced on its own (a wait result, a
// process exit, a background report) the way Steer delivers user text: into the
// running turn, or through OnOrphanedSteer when none is running. The message it
// adds to the history carries ai.OriginBackground.
func (a *Agent) Notify(text string) { a.steer(pendingSteer{text: text, notice: true}) }

func (a *Agent) steer(s pendingSteer) {
	// The running check and the append share a.mu with the end of a turn, so a
	// message is either drained by that turn's teardown or handed to OnOrphanedSteer,
	// never left in pending with no turn to read it.
	a.mu.Lock()
	if orphaned := a.OnOrphanedSteer; !a.running.Load() && orphaned != nil {
		a.mu.Unlock()
		orphaned(s.text)
		return
	}
	a.pending = append(a.pending, s)
	a.mu.Unlock()
}

type pendingSteer struct {
	text   string
	parts  []ai.ContentPart
	notice bool
}

func (a *Agent) SteerImages(text string, parts []ai.ContentPart) {
	a.mu.Lock()
	a.pending = append(a.pending, pendingSteer{text: text, parts: parts})
	a.mu.Unlock()
}

func (a *Agent) AppendUser(content string) {
	a.mu.Lock()
	a.msgsMu.Lock()
	now := time.Now()
	a.Messages = append(a.Messages, ai.Message{Role: "user", Content: content, SentAt: &now})
	a.msgsMu.Unlock()
	a.mu.Unlock()
}

func (a *Agent) drainPending() []pendingSteer {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.pending
	a.pending = nil
	return p
}

func (a *Agent) notePrompt(u ai.Usage) {
	if u.PromptTokens <= 0 {
		return
	}
	a.usageMu.Lock()
	a.lastPrompt = u.PromptTokens
	a.usageMu.Unlock()
}

func (a *Agent) AddUsage(u ai.Usage) {
	a.clientMu.Lock()
	label := a.usageLabel()
	a.clientMu.Unlock()
	a.addModelUsage(label, u)
}

func (a *Agent) addModelUsage(label string, u ai.Usage) {
	a.usageMu.Lock()
	addUsage(&a.usage, u)
	if a.modelUsage == nil {
		a.modelUsage = map[string]ai.Usage{}
	}
	current := a.modelUsage[label]
	addUsage(&current, u)
	a.modelUsage[label] = current
	sink := a.usageSink
	a.usageMu.Unlock()
	if sink != nil {
		sink(label, u)
	}
}

func (a *Agent) AddSubUsage(model string, u ai.Usage) {
	a.usageMu.Lock()
	if a.subUsage == nil {
		a.subUsage = map[string]ai.Usage{}
	}
	cur := a.subUsage[model]
	addUsage(&cur, u)
	a.subUsage[model] = cur
	sink := a.usageSink
	a.usageMu.Unlock()
	if sink != nil {
		sink(model, u)
	}
}

func (a *Agent) usageLabel() string { return a.Model + " @ " + a.Provider }

func addUsage(dst *ai.Usage, u ai.Usage) {
	dst.Add(u)
}

func (a *Agent) SetUsage(u ai.Usage) {
	a.usageMu.Lock()
	a.usage = u
	a.modelUsage = map[string]ai.Usage{a.usageLabel(): copyUsage(u)}
	a.usageMu.Unlock()
}

func (a *Agent) SetSubUsage(m map[string]ai.Usage) {
	a.usageMu.Lock()
	a.subUsage = copyUsageMap(m)
	a.usageMu.Unlock()
}

func (a *Agent) ResetUsage() {
	a.usageMu.Lock()
	a.usage = ai.Usage{}
	a.modelUsage = nil
	a.subUsage = nil
	a.usageMu.Unlock()
}

func (a *Agent) Usage() ai.Usage {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	return copyUsage(a.usage)
}

func (a *Agent) SubUsage() map[string]ai.Usage {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	return copyUsageMap(a.subUsage)
}

func (a *Agent) TotalUsage() ai.Usage {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	t := copyUsage(a.usage)
	for _, u := range a.subUsage {
		addUsage(&t, u)
	}
	return t
}

func copyUsage(u ai.Usage) ai.Usage {
	if u.PromptTokensDetails != nil {
		d := *u.PromptTokensDetails
		u.PromptTokensDetails = &d
	}
	return u
}

func copyUsageMap(m map[string]ai.Usage) map[string]ai.Usage {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]ai.Usage, len(m))
	for k, u := range m {
		out[k] = copyUsage(u)
	}
	return out
}

func New(client ai.Client, model string, maxTokens int, systemPrompt string, opts ...Option) *Agent {
	a := &Agent{
		planMode:  &atomic.Bool{},
		Client:    client,
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  []ai.Message{{Role: "system", Content: systemPrompt}},
	}
	for _, o := range opts {
		o(a)
	}
	a.Tools = tools.All()
	if tools.LSPToolAvailable() {
		a.Tools = append(a.Tools, tools.LSPTool())
	}
	if !a.BrowserDisabled {
		a.Tools = append(a.Tools, tools.BrowserExec())
	}
	if !a.ComputerDisabled {
		if a.ComputerConfig.Backend == "legacy" {
			a.Tools = append(a.Tools, tools.LegacyComputerExec())
		} else {
			a.Tools = append(a.Tools, tools.ComputerExec())
		}
	}
	sessionTools := []tools.Tool{tools.QuestionTool(), exitPlanTool(a), taskTool(a), taskSteerTool(a)}
	sessionTools = append(sessionTools, taskControlTools(a)...)
	sessionTools = append(sessionTools, newContextTool(a))
	if a.experimentalEnabled(FeatureWorkflows) {
		sessionTools = append(sessionTools, workflowTool(a))
	}
	sessionTools = append(sessionTools, todoTool(a), waitTool(a))
	sessionTools = append(sessionTools, skillsManagerTool())
	sessionTools = append(sessionTools, memoryTools(a)...)
	sessionTools = append(sessionTools, memoryStoreTool(a))
	for _, tool := range sessionTools {
		tool.NoInherit = true
		a.Tools = append(a.Tools, tool)
	}
	a.Reminders = DefaultReminders()
	a.files = newFileLocks()
	a.bg = newTaskRegistry()
	return a
}

const (
	FeatureWorkflows = "workflows"
)

type Option func(*Agent)

func WithExperimental(features []string) Option {
	return func(a *Agent) { a.experimental = features }
}

func (a *Agent) Experimental() []string { return a.experimental }

func (a *Agent) experimentalEnabled(name string) bool {
	return slices.Contains(a.experimental, name)
}

func (a *Agent) MessagesSnapshot() []ai.Message {
	a.msgsMu.Lock()
	defer a.msgsMu.Unlock()
	return append([]ai.Message(nil), a.Messages...)
}

func (a *Agent) ContextTokens() int {
	return EstimateTokens(a.MessagesSnapshot())
}

func (a *Agent) RestoreMessages(messages []ai.Message) {
	a.msgsMu.Lock()
	a.Messages = append([]ai.Message(nil), messages...)
	a.msgsMu.Unlock()
}

func (a *Agent) SetMessageRewoundFrom(index int, value string) {
	a.msgsMu.Lock()
	if index > 0 && index < len(a.Messages) {
		a.Messages[index].RewoundFrom = value
	}
	a.msgsMu.Unlock()
}

var (
	suggesterMu      sync.Mutex
	suggesterCurrent atomic.Pointer[Agent]
)

func (a *Agent) SetMCPTools(ts []tools.Tool) {
	a.toolsMu.Lock()
	a.mcpTools = ts
	a.toolsMu.Unlock()
	suggesterMu.Lock()
	suggesterCurrent.Store(a)
	tools.Suggester = func(name string) []string {
		if cur := suggesterCurrent.Load(); cur != nil {
			return cur.suggest(name)
		}
		return nil
	}
	suggesterMu.Unlock()
}

func (a *Agent) SetPluginTools(ts []tools.Tool) {
	a.toolsMu.Lock()
	a.pluginTools = ts
	a.toolsMu.Unlock()
}

func (a *Agent) suggest(name string) []string {
	a.toolsMu.Lock()
	all := append(append(append([]tools.Tool(nil), a.Tools...), a.mcpTools...), a.pluginTools...)
	a.toolsMu.Unlock()
	names := make([]string, len(all))
	for i, t := range all {
		names[i] = t.Def.Function.Name
	}
	return tools.SuggestTool(name, names)
}

func (a *Agent) AvailableTools() []tools.Tool {
	a.toolsMu.Lock()
	defer a.toolsMu.Unlock()
	return a.withoutMemoryTools(a.modeTools(append(append(append([]tools.Tool(nil), a.Tools...), a.mcpTools...), a.pluginTools...)))
}

func (a *Agent) AllTools() []tools.Tool {
	a.toolsMu.Lock()
	defer a.toolsMu.Unlock()
	if a.RunToolsSet {
		return a.withoutMemoryTools(append([]tools.Tool(nil), a.RunTools...))
	}
	return a.withoutMemoryTools(a.modeTools(append(append(append([]tools.Tool(nil), a.Tools...), a.mcpTools...), a.pluginTools...)))
}

func (a *Agent) Turn(ctx context.Context, input string, ev Events) (string, error) {
	return a.turn(ctx, input, nil, false, ev)
}

func (a *Agent) TurnAuthored(ctx context.Context, input string, ev Events) (string, error) {
	return a.turn(ctx, input, nil, true, ev)
}

func (a *Agent) TurnParts(ctx context.Context, input string, parts []ai.ContentPart, ev Events) (string, error) {
	return a.turn(ctx, input, parts, true, ev)
}

func (a *Agent) TurnWithImages(ctx context.Context, input string, parts []ai.ContentPart, ev Events) (string, error) {
	return a.turn(ctx, input, parts, true, ev)
}

func (a *Agent) turn(ctx context.Context, input string, parts []ai.ContentPart, authored bool, ev Events) (string, error) {
	return a.turnPending(ctx, input, parts, authored, "", ev)
}

// ContinueUser runs the persisted tail user message without appending another user turn.
func (a *Agent) ContinueUser(ctx context.Context, messageID string, ev Events) (string, error) {
	return a.turnPending(ctx, "", nil, true, messageID, ev)
}

func (a *Agent) turnPending(ctx context.Context, input string, parts []ai.ContentPart, authored bool, resumeID string, ev Events) (answer string, turnErr error) {
	if !a.turnMu.TryLock() {
		return "", ErrBusy
	}
	defer a.turnMu.Unlock()
	if err := a.refreshSystemPrompt(); err != nil {
		return "", err
	}
	if resumeID != "" {
		msgs := a.MessagesSnapshot()
		if len(msgs) == 0 || msgs[len(msgs)-1].Role != "user" || msgs[len(msgs)-1].ID != resumeID {
			return "", errors.New("resume message must be the tail user message")
		}
		input, parts = msgs[len(msgs)-1].Content, msgs[len(msgs)-1].Parts
	}
	a.running.Store(true)
	defer func() {
		a.mu.Lock()
		a.running.Store(false)
		a.mu.Unlock()
		a.drainOrphanedSteers()
	}()
	if a.WorkingDir != "" {
		ctx = tools.WithWorkingDir(ctx, a.WorkingDir)
	}
	if a.SandboxPolicy != nil && sandbox.FromContext(ctx) == nil {
		ctx = sandbox.WithPolicy(ctx, a.SandboxPolicy)
	}
	lifecycle := newTurnLifecycle(ev.OnLifecycle)
	defer lifecycle.close()
	if start := lifecycleStart(ctx); start != nil {
		start()
	}
	if ev.OnPhase != nil {
		ev.OnPhase("preparing")
	}
	if err := a.StartSession(ctx); err != nil {
		return "", err
	}
	if !a.ComputerDisabled && a.ComputerConfig.Backend != "legacy" {
		var closeComputer func() error
		ctx, closeComputer = a.computerTurn(ctx)
		defer func() { turnErr = errors.Join(turnErr, closeComputer()) }()
	}
	a.RefreshMemory()
	if a.memoryRuntime != nil && a.MemoryEnabled() {
		if block, err := a.memoryRuntime.Inject(ctx, a.WorkingDir); err == nil {
			a.installRuntimeMemory(block)
		}
	}
	prompt := ai.Message{Content: input, Parts: parts}
	if err := a.runHook(ctx, hooks.Event{Name: hooks.UserPromptSubmit, Prompt: prompt.TextContent()}); err != nil {
		return "", err
	}

	if n := a.decay(); n > 0 && ev.OnDecay != nil {
		ev.OnDecay(n)
	}
	defer func() {
		_ = a.runHook(context.WithoutCancel(ctx), hooks.Event{Name: hooks.Stop})
	}()
	messageID := newMessageID()
	if requestedID, ok := ctx.Value(userMessageIDKey{}).(string); ok && requestedID != "" {
		messageID = requestedID
	}
	userMessage := ai.Message{ID: messageID, Role: "user", Content: input, Parts: parts, Authored: authored,
		PromptContext: a.todoContext(), PromptSnapshots: a.contextSnapshots()}
	if backgroundNotice(ctx) {
		userMessage.Authored, userMessage.Origin = false, ai.OriginBackground
	}
	if snapshot, ok := turnSnapshot(ctx); ok {
		userMessage.PromptSnapshots = append(userMessage.PromptSnapshots, snapshot)
	}
	if memoryOffAuthored(ctx) && !isMemoryOffMessage(userMessage) {
		userMessage.PromptSnapshots = append(userMessage.PromptSnapshots, MemoryOffSnapshot())
	}
	// The snapshot and the notice mark belong to this turn only; subagents, workflow
	// agents and background tasks started by its tools inherit ctx and must not get them.
	ctx = withoutBackgroundNotice(withoutTurnSnapshot(ctx))
	if resumeID != "" {
		userMessage.ID = resumeID
	}
	msg := userMessage
	now := time.Now()
	msg.SentAt = &now
	a.msgsMu.Lock()
	if resumeID == "" {
		a.Messages = append(a.Messages, msg)
	} else {
		markResumedMessage(&a.Messages[len(a.Messages)-1], userMessage.PromptSnapshots, a.MemoryEnabled())
		msg = a.Messages[len(a.Messages)-1]
	}
	a.msgsMu.Unlock()
	// The History copy of this turn's user message; the loop below reuses msg.
	writtenOff := isMemoryOffMessage(msg)
	// A cancelled, failed or plan-exit turn keeps its user message in History,
	// which later turns treat as context only. The runtime holds it for the
	// next completed turn: a resume reports it once and an edit can still
	// remove it. No agent lock is taken here, so a panic under msgsMu still
	// propagates.
	memoryReported := false
	defer func() {
		if !memoryReported && a.memoryRuntime != nil && a.MemoryEnabled() && !writtenOff {
			a.memoryRuntime.Abandoned(a.SessionIDValue(), userMessage)
		}
	}()
	if err := observeUserMessage(ctx, msg); err != nil {
		return "", err
	}
	rounds := 0
	continuations := 0
	for {
		lifecycle.startRound()
		if a.MaxTurns > 0 && rounds >= a.MaxTurns {

			return a.finalAnswer(ctx, ev)
		}
		rounds++
		if err := a.maybeCompact(ctx, ev); err != nil {
			return "", err
		}
		// Compact before sending when the estimated input plus the requested output
		// would exceed the provider context window. This avoids relying on a
		// provider error to discover an already-overflowing request.
		if err := a.maybeCompactForRequest(ctx, ev); err != nil {
			return "", err
		}
		if notice, quotesOff := a.takeContextNotice(); notice != "" {
			now := time.Now()
			snapshots := a.contextSnapshots()
			if quotesOff && a.MemoryEnabled() {
				snapshots = append(snapshots, MemoryOffSnapshot())
			}
			message := ai.Message{Role: "user", Content: notice, Origin: ai.OriginBackground, SentAt: &now, PromptContext: a.todoContext(), PromptSnapshots: snapshots}
			a.msgsMu.Lock()
			a.Messages = append(a.Messages, message)
			a.msgsMu.Unlock()
		}
		msgs := a.Messages
		requestTools := a.AllTools()
		if a.RequestToolFilter != nil {
			requestTools = a.RequestToolFilter(requestTools)
		}
		msg, usage, err := a.streamObserved(ctx, ai.Request{
			Model:                a.Model,
			Messages:             a.requestMessages(msgs),
			Tools:                tools.Defs(requestTools),
			MaxTokens:            a.MaxTokens,
			ReasoningEffort:      a.Effort,
			NativeWebSearch:      a.NativeWebSearch,
			NativeSearchProvider: a.Provider,
			Temperature:          a.Temperature,
			TopP:                 a.TopP,
		}, ev)
		a.AddUsage(usage)
		a.notePrompt(usage)
		if ev.OnUsage != nil {
			ev.OnUsage(usage)
		}
		if err != nil {
			if !a.compacted && ai.IsContextLimit(err) && ctx.Err() == nil {
				a.learnContextLimit(err)
				a.compacted = true
				took := len(a.Messages)
				if ev.OnCompactStart != nil {
					ev.OnCompactStart(took, EstimateTokens(a.Messages))
				}
				sum, cutoff, info, cerr := a.compactHooked(ctx, compactAuto)
				if cerr != nil {

					a.compacted = false
					return "", cerr
				}
				if ev.OnCompact != nil {
					ev.OnCompact(took-len(a.Messages), len(a.Messages))
				}
				if ev.OnCompacted != nil {
					ev.OnCompacted(sum, cutoff, info)
				}
				continue
			}
			a.appendCancelledReply(ctx, msg, usage)
			return "", err
		}
		lifecycle.endMessage()
		a.appendResponse(msg, usage)
		resume := a.continueAfterOutputLimit(ctx, msg, rounds, continuations)
		if resume {
			continuations++
			if ev.OnText != nil {
				// Keep the resumed text off the truncation note in streamed output.
				ev.OnText("\n\n")
			}
			if ev.OnOutputLimitContinue != nil {
				ev.OnOutputLimitContinue(continuations)
			}
			reminder := outputLimitReminder(msg)
			a.msgsMu.Lock()
			a.Messages = append(a.Messages, reminder)
			a.msgsMu.Unlock()
		}
		if len(msg.ToolCalls) > 0 {
			results := a.runTools(ctx, msg.ToolCalls, ev)
			if a.contextRequested.Swap(false) {
				before := a.MessagesSnapshot()
				if ev.OnNewContextStart != nil {
					ev.OnNewContextStart(len(before), EstimateTokens(before))
				}
				a.resetContextMessages(true)
				if ev.OnNewContext != nil {
					ev.OnNewContext(len(before))
				}
				continue
			}
			a.msgsMu.Lock()
			for i, tc := range msg.ToolCalls {
				var stopReason ai.StopReason
				if results[i].Cancelled {
					stopReason = ai.StopReasonCancelled
				} else if results[i].Failed {
					stopReason = ai.StopReasonError
				}
				a.Messages = append(a.Messages, ai.Message{
					StopReason: stopReason,
					Role:       "tool",
					Content:    results[i].Text,
					Parts:      results[i].Parts,
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
				})
			}
			a.msgsMu.Unlock()
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			revise := a.planRevise.Swap(false)
			for i, call := range msg.ToolCalls {
				if a.PlanMode() && call.Function.Name == "ExitPlanMode" && !results[i].Failed && !revise {
					return results[i].Text, nil
				}
			}
		}
		steered := a.drainPending()
		if len(steered) > 0 {
			a.msgsMu.Lock()
		}
		for _, s := range steered {
			if ev.OnSteer != nil {
				ev.OnSteer(s.text)
			}
			now := time.Now()
			message := ai.Message{Role: "user", Content: s.text, Parts: s.parts, SentAt: &now, PromptSnapshots: a.memoryOffMarks()}
			if s.notice {
				message.Origin = ai.OriginBackground
			}
			a.Messages = append(a.Messages, message)
		}
		if len(steered) > 0 {
			a.msgsMu.Unlock()
		}
		if len(msg.ToolCalls) == 0 && len(steered) == 0 && !resume {

			if !a.compacted {
				if cerr := a.maybeCompact(ctx, ev); cerr != nil {
					return "", cerr
				}
			}
			a.compacted = false
			// A message written while memory was off stays out of extraction, also when
			// it is resumed after memory is back on.
			if a.memoryRuntime != nil && a.MemoryEnabled() && !writtenOff {
				memoryReported = true
				a.memoryRuntime.Completed(ctx, MemoryTurn{SessionID: a.SessionIDValue(), Workdir: a.WorkingDir, Model: a.ModelName, User: userMessage, History: withoutMemoryOffTurns(a.MessagesSnapshot())})
			}
			return msg.Content, nil
		}
	}
}

func (a *Agent) drainOrphanedSteers() {
	if a.OnOrphanedSteer == nil {
		return
	}
	for _, s := range a.drainPending() {
		a.OnOrphanedSteer(s.text)
	}
}

func (a *Agent) requestNewContext() {
	a.contextRequested.Store(true)
}

func (a *Agent) resetContextMessages(notice bool) {
	lastUser, lastUserOff := "", false
	a.msgsMu.Lock()
	if len(a.Messages) > 0 {
		for i := len(a.Messages) - 1; i >= 0; i-- {
			if a.Messages[i].Role == "user" {
				lastUser = truncateField(a.Messages[i].TextContent(), 8000)
				lastUserOff = isMemoryOffMessage(a.Messages[i])
				break
			}
		}
		a.Messages = []ai.Message{a.Messages[0]}
	}
	a.msgsMu.Unlock()
	a.usageMu.Lock()
	a.lastPrompt = 0
	a.usageMu.Unlock()
	if notice {
		a.contextNoticeMu.Lock()
		a.contextNotice = "A new context window is active. Continue the current task using the available workspace and tools. The previous conversation was intentionally discarded."
		if lastUser != "" {
			a.contextNotice += "\n\nCurrent task:\n" + lastUser
		}
		a.contextNoticeOff = lastUser != "" && lastUserOff
		a.contextNoticeMu.Unlock()
	}
}

func (a *Agent) takeContextNotice() (string, bool) {
	a.contextNoticeMu.Lock()
	defer a.contextNoticeMu.Unlock()
	n, off := a.contextNotice, a.contextNoticeOff
	a.contextNotice, a.contextNoticeOff = "", false
	return n, off
}

const (
	compactTailMinTokens = 2_000
	compactTailMaxTokens = 15_000
	compactTailFraction  = 0.25
)

const defaultCompactThreshold = 0.5

func (a *Agent) threshold() float64 {
	if a.CompactThreshold > 0 {
		return a.CompactThreshold
	}
	return defaultCompactThreshold
}

func (a *Agent) maybeCompact(ctx context.Context, ev Events) error {
	if a.ContextLimit == 0 {
		return nil
	}
	limit := int(a.threshold() * float64(a.ContextLimit))
	a.usageMu.Lock()
	reported := a.lastPrompt
	a.usageMu.Unlock()
	if reported > 0 {

		if reported < limit {
			return nil
		}
	} else if EstimateTokens(a.Messages) < limit {
		return nil
	}
	took := len(a.Messages)
	if ev.OnCompactStart != nil {
		ev.OnCompactStart(took, EstimateTokens(a.Messages))
	}
	sum, cutoff, info, err := a.compactHooked(ctx, compactAuto)
	if err != nil {
		if err.Error() == "not enough history to compact" {
			return nil
		}
		return err
	}
	if ev.OnCompact != nil {
		ev.OnCompact(took-len(a.Messages), len(a.Messages))
	}
	if ev.OnCompacted != nil {
		ev.OnCompacted(sum, cutoff, info)
	}

	a.compacted = true
	return nil
}

// maybeCompactForRequest handles the pre-send overflow trigger. A normal
// threshold check can be disabled or set high enough that the output reserve
// still makes the next request too large.
func (a *Agent) maybeCompactForRequest(ctx context.Context, ev Events) error {
	if a.compacted || a.ContextLimit <= 0 || a.MaxTokens <= 0 {
		return nil
	}
	messages := a.MessagesSnapshot()
	if len(messages) <= 3 {
		return nil
	}
	if EstimateTokens(messages)+a.MaxTokens <= a.ContextLimit {
		return nil
	}
	if ev.OnCompactStart != nil {
		ev.OnCompactStart(len(messages), EstimateTokens(messages))
	}
	sum, cutoff, info, err := a.compactHooked(ctx, compactAuto)
	if err != nil {
		if err.Error() == "not enough history to compact" {
			return nil
		}
		return err
	}
	if ev.OnCompact != nil {
		ev.OnCompact(len(messages)-len(a.Messages), len(a.Messages))
	}
	if ev.OnCompacted != nil {
		ev.OnCompacted(sum, cutoff, info)
	}
	a.compacted = true
	return nil
}

func EstimateTokens(msgs []ai.Message) int {
	total := 0
	for _, m := range msgs {
		total += 4 + (len(m.TextContent())+3)/4
		if context := promptContextText(m); context != "" {
			total += (len(context) + len("\n\n<kbrain-context>\n\n</kbrain-context>") + 3) / 4
		}
		for _, p := range m.Parts {
			if p.Type != "text" {
				total += ai.PartTokens(p)
			}
		}
		for _, tc := range m.ToolCalls {
			total += 8 + (len(tc.Function.Name)+len(tc.Function.Arguments)+3)/4
		}
	}
	return total
}

func (a *Agent) compactTailBudget() int {
	budget := compactTailMaxTokens
	if a.ContextLimit > 0 {
		usable := float64(a.ContextLimit) * (1 - a.threshold())
		budget = int(usable * compactTailFraction)
	}
	return max(min(budget, compactTailMaxTokens), compactTailMinTokens)
}

func compactTailStart(msgs []ai.Message, budget int) int {
	acc := 0
	start := len(msgs)

	for i := len(msgs) - 1; i >= 1; i-- {
		acc += EstimateTokens(msgs[i : i+1])
		if msgs[i].Role == "user" {
			if acc > budget && start < len(msgs) {
				break
			}
			start = i
		}
	}
	return start
}

func (a *Agent) compact(ctx context.Context) (summary string, cutoff int, info CompactInfo, err error) {
	if len(a.Messages) <= 3 {
		return "", 0, CompactInfo{}, errors.New("not enough history to compact")
	}
	const sysIdx = 0
	sysPrompt := a.Messages[sysIdx]
	budget := a.compactTailBudget()
	tailStart := compactTailStart(a.Messages, budget)
	if tailStart <= sysIdx+1 {
		tailStart = sysIdx + 2
	}
	tail := a.Messages[tailStart:]

	for len(tail) > 4 && tail[0].Role == "tool" {
		tail = a.Messages[tailStart-1:]
		tailStart--
	}
	history := a.Messages[sysIdx+1 : tailStart]

	prior := ""
	if len(history) > 0 && history[0].Role == "system" &&
		strings.HasPrefix(history[0].Content, summaryPrefix) {
		prior = strings.TrimPrefix(history[0].Content, summaryPrefix)
		history = history[1:]
	}
	cli, mdl := a.CompactClient, a.CompactModel
	provider := a.CompactProvider
	dedicated := cli != nil
	if cli == nil {
		cli = a.Client
		provider = a.Provider
	}
	if mdl == "" {
		mdl = a.Model
	}
	label := mdl
	if dedicated {

		if u, perr := url.Parse(cli.Endpoint()); perr == nil && u.Host != "" {
			label = mdl + " @ " + u.Host
		}
	}
	usageLabel := mdl + " @ " + provider
	var usage ai.Usage
	var sum string
	var cerr error
	summarized := false
	sameModel := !dedicated && mdl == a.Model
	if sameModel {
		// Re-send the conversation itself so the provider can serve it from the
		// prompt cache, as long as it fits the window.
		prefix := withPromptContext(a.requestMessages(a.Messages[:tailStart]))
		if a.turnTimeContext {
			prefix = withTurnTimes(prefix)
		}
		messages := append(prefix, ai.Message{Role: "user", Content: cachedSummaryInstruction})
		requestTools := a.AllTools()
		if a.RequestToolFilter != nil {
			requestTools = a.RequestToolFilter(requestTools)
		}
		definitions := tools.Defs(requestTools)
		encodedTools, _ := json.Marshal(definitions)
		if a.ContextLimit <= 0 || EstimateTokens(messages)+(len(encodedTools)+3)/4+4096 < a.ContextLimit {
			var u ai.Usage
			sum, u, cerr = a.completeSummary(ctx, cli, ai.Request{
				Model:                mdl,
				Messages:             messages,
				Tools:                definitions,
				ReasoningEffort:      a.Effort,
				Temperature:          a.Temperature,
				TopP:                 a.TopP,
				NativeWebSearch:      a.NativeWebSearch,
				NativeSearchProvider: a.Provider,
			}, a.summaryOutputCeiling(false), usageLabel)
			addUsage(&usage, u)
			summarized = true
			// EstimateTokens is a byte heuristic and undercounts CJK text, images
			// and dense JSON, and the catalog window can exceed what the endpoint
			// accepts. When the provider rejects the prompt as too large, fall
			// back to the bounded transcript path instead of failing compaction.
			if cerr != nil && ai.IsContextLimit(cerr) && ctx.Err() == nil {
				a.learnContextLimit(cerr)
				summarized = false
			}
			// Still cut off at the largest budget: the transcript prompt asks
			// without tools or reasoning effort, which leaves more room to write.
			if _, ok := errors.AsType[*ai.OutputLimitError](cerr); ok && ctx.Err() == nil {
				summarized = false
			}
		}
	}
	if !summarized {
		window := 0
		if sameModel {
			window = a.ContextLimit
		}
		var u ai.Usage
		sum, u, cerr = a.summarizeTranscript(ctx, cli, mdl, usageLabel, sysPrompt, history, prior, window, a.summaryOutputCeiling(dedicated))
		addUsage(&usage, u)
	}
	if cerr != nil {
		return "", 0, CompactInfo{}, fmt.Errorf("compaction summary failed: %w", cerr)
	}
	summary = strings.TrimSpace(sum)
	if summary == "" {
		return "", 0, CompactInfo{}, errors.New("compaction summary is empty")
	}
	a.archiveCompaction(history, summary)
	a.resetTranscriptPointer()
	kept := append([]ai.Message(nil), tail...)
	a.msgsMu.Lock()
	a.Messages = append(append([]ai.Message{}, sysPrompt,
		ai.Message{Role: "system", Content: summaryPrefix + summary},
	), kept...)
	a.msgsMu.Unlock()

	a.usageMu.Lock()
	a.lastPrompt = 0
	a.usageMu.Unlock()
	return summary, tailStart, CompactInfo{Model: label, Provider: provider, Usage: usage}, nil
}

const summaryPrefix = "Summary of the conversation so far:\n\n"

const cachedSummaryInstruction = "Summarize the conversation above, merging any earlier summary with newer information. Capture the user's intent, decisions made, work completed, files touched, and open tasks with the exact next step. Use these sections: Objective / Key decisions / Completed / Active / Blocked / Relevant files. Be concise; do not include verbatim tool output. Reply only with the summary; do not call tools. End with a single line: Open task: <what the assistant was doing last, or none>."

// learnContextLimit lowers the session's context window to the one a provider
// reported when it rejected a prompt as too large. The catalog window can be
// larger than what a relay or endpoint actually accepts; once the endpoint has
// said otherwise, the threshold and pre-send checks must use its figure or the
// session keeps overflowing. SetModel resets it for a new model.
func (a *Agent) learnContextLimit(err error) {
	if n := ai.ContextLimitFromError(err); n > 0 && (a.ContextLimit <= 0 || n < a.ContextLimit) {
		a.ContextLimit = n
	}
}

const (
	// summaryOutputTokens is the MaxTokens a summary request starts with; see
	// completeSummary for the retry after an output-limit cut-off.
	summaryOutputTokens = 4096
	// summaryPromptOverhead covers the summary instructions around the transcript.
	summaryPromptOverhead = 512
	// overflowWindowShare is the share of the input window a transcript may fill
	// once the provider has rejected a summary request as too large.
	// EstimateTokens counts bytes/4, which undercounts CJK text and dense JSON,
	// so after a real overflow leave generous headroom.
	overflowWindowShare = 0.6
	// minTranscriptBudget keeps chunks from shrinking to nothing on tiny windows.
	minTranscriptBudget = 512
	// maxTranscriptShrinks bounds retries after a too-large rejection.
	maxTranscriptShrinks = 4
)

// transcriptBudget is the token budget for the transcript part of one chunked
// summary request: overflowWindowShare of the window left after the output
// reserve, minus the system prompt, the running summary and the instructions.
func transcriptBudget(window int, sysPrompt ai.Message, prior string) int {
	budget := int(float64(window-summaryOutputTokens)*overflowWindowShare) -
		EstimateTokens([]ai.Message{sysPrompt}) - (len(prior)+3)/4 - summaryPromptOverhead
	return max(budget, minTranscriptBudget)
}

// transcriptChunkEnd returns the end of the chunk starting at pos whose rendered
// transcript fits budget. A chunk always holds at least one message, which
// writeTranscript already truncates per field.
func transcriptChunkEnd(history []ai.Message, pos, budget int) int {
	used := 0
	end := pos
	for end < len(history) {
		var b strings.Builder
		writeTranscript(&b, history[end:end+1])
		cost := (b.Len() + 3) / 4
		if end > pos && used+cost > budget {
			break
		}
		used += cost
		end++
	}
	return max(end, min(pos+1, len(history)))
}

// summarizeTranscript summarizes history, merged into prior, with the plain
// transcript prompt. The whole transcript goes out in one request first, as it
// always has. Only if the provider rejects it as too large is the history split
// into chunks sized to the provider-reported window (else the known one), with
// headroom for estimation error, and folded into the running summary in order.
// Each further rejection halves the window and re-chunks the remaining history,
// keeping the summary built so far. window is the caller's known context
// window, or 0 when unknown.
func (a *Agent) summarizeTranscript(ctx context.Context, cli ai.Client, mdl, usageLabel string, sysPrompt ai.Message, history []ai.Message, prior string, window, outputCeiling int) (string, ai.Usage, error) {
	var total ai.Usage
	current := prior
	chunked := false
	shrinks := 0
	// Always send at least one request, even for an empty history, so the
	// summary is regenerated from prior plus the instruction.
	for pos := 0; ; {
		end := len(history)
		if chunked {
			end = transcriptChunkEnd(history, pos, transcriptBudget(window, sysPrompt, current))
		}
		prompt := buildSummaryPrompt(history[pos:end], current)
		messages := []ai.Message{sysPrompt, {Role: "user", Content: prompt}}
		sum, u, err := a.completeSummary(ctx, cli, ai.Request{Model: mdl, Messages: messages}, outputCeiling, usageLabel)
		addUsage(&total, u)
		if err != nil {
			if ai.IsContextLimit(err) && ctx.Err() == nil && shrinks < maxTranscriptShrinks {
				shrinks++
				reported := ai.ContextLimitFromError(err)
				switch {
				case reported > 0 && (window <= 0 || reported < window):
					window = reported
				case !chunked && window > 0:
					// First rejection: chunk within the known window first.
				case window > 0:
					window /= 2
				default:
					window = EstimateTokens(messages) / 2
				}
				chunked = true
				continue // retry from the same position with smaller chunks
			}
			return "", total, err
		}
		current = strings.TrimSpace(sum)
		if current == "" {
			return "", total, errors.New("compaction summary is empty")
		}
		pos = end
		if pos >= len(history) {
			return current, total, nil
		}
	}
}

func buildSummaryPrompt(msgs []ai.Message, prior string) string {
	var b strings.Builder
	if prior != "" {
		b.WriteString("Here is the running summary of the earlier conversation:\n\n<summary>\n")
		b.WriteString(prior)
		b.WriteString("\n</summary>\n\n")
		b.WriteString("Below are the new turns since that summary was written. Merge them into the summary: ")
		b.WriteString("keep everything still relevant (decisions, files touched, state), drop what the new turns ")
		b.WriteString("obsolete, and add the new work. Anything you do not carry into the new summary is lost. ")
	} else {
		b.WriteString("Summarize the following conversation between the user and the assistant. ")
	}
	b.WriteString("Capture the user's intent, decisions made, work completed, files touched, ")
	b.WriteString("and any open task the assistant is mid-way through. ")
	b.WriteString("Use these sections: Objective / Key decisions / Completed / Active (with the exact next step) / Blocked / Relevant files. ")
	b.WriteString("Be concise; use bullet points for code/files. Do not include verbatim tool output. ")
	b.WriteString("End with a single line: \"Open task: <what the assistant was doing last, or none>\".\n\n")
	b.WriteString("---\n\n")
	writeTranscript(&b, msgs)
	b.WriteString("\n---\n\nWrite the summary now.")
	return b.String()
}

func writeTranscript(b *strings.Builder, msgs []ai.Message) {
	for _, m := range msgs {
		switch m.Role {
		case "user":
			fmt.Fprintf(b, "user: %s\n", truncateField(m.TextContent(), 2000))
		case "assistant":
			if c := strings.TrimSpace(m.TextContent()); c != "" {
				fmt.Fprintf(b, "assistant: %s\n", truncateField(c, 2000))
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(b, "assistant called %s(%s)\n", tc.Function.Name, truncateField(tc.Function.Arguments, 500))
			}
		case "tool":
			fmt.Fprintf(b, "tool result: %s\n", truncateField(m.Content, 500))
		}
	}
}

const GoalFromContextDefaultWindow = 8

func GoalFromContextMessages(msgs []ai.Message, n int) ([]ai.Message, error) {
	if n <= 0 {
		n = GoalFromContextDefaultWindow
	}
	if len(msgs) == 0 {
		return nil, errors.New("not enough context to formulate a goal — chat a bit first")
	}
	conv := msgs[1:]
	if len(conv) < 2 {
		return nil, errors.New("not enough context to formulate a goal — chat a bit first")
	}
	if n > len(conv) {
		n = len(conv)
	}
	return conv[len(conv)-n:], nil
}

func BuildGoalFromContextPrompt(tail []ai.Message) string {
	var b strings.Builder
	b.WriteString("Distill the end of this conversation into a detailed goal the assistant should keep working on until it is verifiably done.\n\n")
	b.WriteString("Reply with ONLY the goal: a first line stating the concrete outcome, then a short bullet list of the specific, checkable completion criteria ")
	b.WriteString("(files to change, commands that must pass, behavior to confirm). Include the key constraints, decisions, and identifiers (file paths, function names, ")
	b.WriteString("error messages) from the conversation so the goal stands alone. No preamble, no quotes, no explanation.\n\n---\n\n")
	writeTranscript(&b, tail)
	b.WriteString("\n---\n\nWrite the goal now.")
	return b.String()
}

func truncateField(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) > n {
		// n counts bytes; back off to a rune boundary so multi-byte text
		// (CJK, emoji) is not cut into invalid UTF-8.
		cut := n - 1
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		return s[:cut] + "…"
	}
	return s
}

func (a *Agent) ManualCompact(ctx context.Context, ev Events) error {
	if !a.turnMu.TryLock() {
		return ErrBusy
	}
	defer a.turnMu.Unlock()
	if ev.OnCompactStart != nil {
		ev.OnCompactStart(len(a.Messages), EstimateTokens(a.Messages))
	}
	sum, cutoff, info, err := a.compactHooked(ctx, compactManual)
	if err != nil {
		return err
	}
	if ev.OnCompact != nil {
		ev.OnCompact(0, len(a.Messages))
	}
	if ev.OnCompacted != nil {
		ev.OnCompacted(sum, cutoff, info)
	}
	return nil
}

func (a *Agent) ManualNewContext(ctx context.Context, ev Events) error {
	if !a.turnMu.TryLock() {
		return ErrBusy
	}
	defer a.turnMu.Unlock()
	if len(a.Messages) <= 1 {
		return errors.New("not enough history to start a new context")
	}
	before := a.MessagesSnapshot()
	if ev.OnNewContextStart != nil {
		ev.OnNewContextStart(len(before), EstimateTokens(before))
	}
	a.resetContextMessages(false)
	if ev.OnNewContext != nil {
		ev.OnNewContext(len(before))
	}
	return nil
}

func (a *Agent) finalAnswer(ctx context.Context, ev Events) (string, error) {
	msgs := append(append([]ai.Message(nil), a.Messages...),
		ai.Message{Role: "system", Content: "You have reached the tool-call limit. Do NOT request any more tools. Give your final answer now using only what you have already gathered."})
	msg, usage, err := a.streamObserved(ctx, ai.Request{
		Model:                a.Model,
		Messages:             a.requestMessages(msgs),
		Tools:                nil,
		ReasoningEffort:      a.Effort,
		NativeWebSearch:      a.NativeWebSearch,
		NativeSearchProvider: a.Provider,
		Temperature:          a.Temperature,
		TopP:                 a.TopP,
	}, ev)
	a.AddUsage(usage)
	a.notePrompt(usage)
	if ev.OnUsage != nil {
		ev.OnUsage(usage)
	}
	if err != nil {
		a.appendCancelledReply(ctx, msg, usage)
		return "", err
	}
	a.appendResponse(msg, usage)
	a.compacted = false
	return msg.Content, nil
}
