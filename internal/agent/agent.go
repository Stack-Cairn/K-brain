package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
	"github.com/Stack-Cairn/K-brain/internal/workflow"
)

type Events struct {
	OnText      func(delta string)
	OnThink     func(delta string)
	OnToolStart func(id, name, args string)
	OnToolEnd   func(id, name, result string)

	OnToolCall func(id, name, args string)

	OnToolOutput func(id, outputSoFar string)
	OnSteer      func(text string)
	OnCompact    func(took, kept int)

	OnCompacted func(summary string, cutoff int, info CompactInfo)

	OnCompactStart func(took, estTokens int)
	OnUsage        func(u ai.Usage)
	OnRetry        func(ev ai.RetryEvent)

	OnDecay func(n int)
}

type CompactInfo struct {
	Model string
	Usage ai.Usage
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
	Client    ai.Client
	Model     string
	ModelName string
	Provider  string
	MaxTokens int
	Effort    string

	Temperature *float64
	TopP        *float64
	Tools       []tools.Tool
	Messages    []ai.Message

	ContextLimit int

	CompactClient ai.Client
	CompactModel  string

	CompactThreshold float64

	TaskDefault SubModel

	ResolveModel func(model, provider string) (SubModel, error)

	MaxTurns int

	WorktreeSubagents bool

	mu        sync.Mutex
	pending   []pendingSteer
	compacted bool
	running   atomic.Bool
	waitReg   *waitRegistry

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

	toolsMu  sync.Mutex
	mcpTools []tools.Tool

	wfMu sync.Mutex
	wf   *workflow.Manager

	experimental []string

	BrowserDisabled bool

	ComputerDisabled bool

	OnOrphanedSteer func(text string)

	usageMu sync.Mutex
	usage   ai.Usage

	clientMu sync.Mutex

	subUsage map[string]ai.Usage

	usageSink func(model string, u ai.Usage)

	lastPrompt int
}

func (a *Agent) TurnRunning() bool { return a.running.Load() }

func (a *Agent) Steer(text string) {
	if !a.running.Load() && a.OnOrphanedSteer != nil {
		a.OnOrphanedSteer(text)
		return
	}
	a.mu.Lock()
	a.pending = append(a.pending, pendingSteer{text: text})
	a.mu.Unlock()
}

type pendingSteer struct {
	text  string
	parts []ai.ContentPart
}

func (a *Agent) SteerImages(text string, parts []ai.ContentPart) {
	a.mu.Lock()
	a.pending = append(a.pending, pendingSteer{text: text, parts: parts})
	a.mu.Unlock()
}

func (a *Agent) AppendUser(content string) {
	a.mu.Lock()
	a.msgsMu.Lock()
	a.Messages = append(a.Messages, ai.Message{Role: "user", Content: content})
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
	a.usageMu.Lock()
	addUsage(&a.usage, u)
	sink := a.usageSink
	a.usageMu.Unlock()
	if sink != nil {
		sink(a.usageLabel(), u)
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
	dst.PromptTokens += u.PromptTokens
	dst.CompletionTokens += u.CompletionTokens
	if u.PromptTokensDetails != nil {
		if dst.PromptTokensDetails == nil {
			dst.PromptTokensDetails = &struct {
				CachedTokens int `json:"cached_tokens"`
			}{}
		}
		dst.PromptTokensDetails.CachedTokens += u.PromptTokensDetails.CachedTokens
	}
}

func (a *Agent) SetUsage(u ai.Usage) {
	a.usageMu.Lock()
	a.usage = u
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
		Client:    client,
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  []ai.Message{{Role: "system", Content: systemPrompt}},
	}
	for _, o := range opts {
		o(a)
	}
	a.Tools = append(tools.All(), tools.QuestionTool())
	if !a.BrowserDisabled {
		a.Tools = append(a.Tools, tools.BrowserExec())
	}
	if !a.ComputerDisabled {
		a.Tools = append(a.Tools, tools.ComputerExec())
	}
	a.Tools = append(a.Tools, taskTool(a), taskSteerTool(a))
	if a.experimentalEnabled(FeatureWorkflows) {
		a.Tools = append(a.Tools, workflowTool(a))
	}
	a.Tools = append(a.Tools, todoTool(a))
	a.Tools = append(a.Tools, waitTool(a))
	a.Tools = append(a.Tools, memoryTools(a)...)
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

func (a *Agent) suggest(name string) []string {
	a.toolsMu.Lock()
	all := append(append([]tools.Tool(nil), a.Tools...), a.mcpTools...)
	a.toolsMu.Unlock()
	names := make([]string, len(all))
	for i, t := range all {
		names[i] = t.Def.Function.Name
	}
	return tools.SuggestTool(name, names)
}

func (a *Agent) AllTools() []tools.Tool {
	a.toolsMu.Lock()
	defer a.toolsMu.Unlock()
	return append(append([]tools.Tool(nil), a.Tools...), a.mcpTools...)
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

	if n := a.decay(); n > 0 && ev.OnDecay != nil {
		ev.OnDecay(n)
	}
	a.running.Store(true)
	defer func() {
		a.running.Store(false)
		a.drainOrphanedSteers()
	}()
	msg := ai.Message{Role: "user", Content: input, Parts: parts, Authored: authored}
	if authored {
		now := time.Now()
		msg.SentAt = &now
	}
	a.msgsMu.Lock()
	a.Messages = append(a.Messages, msg)
	a.msgsMu.Unlock()
	rounds := 0
	for {
		if a.MaxTurns > 0 && rounds >= a.MaxTurns {

			return a.finalAnswer(ctx, ev)
		}
		rounds++
		if err := a.maybeCompact(ctx, ev); err != nil {
			return "", err
		}
		msgs := a.Messages
		if block := a.todoBlock(); block != "" {

			msgs = append(append([]ai.Message(nil), a.Messages...),
				ai.Message{Role: "system", Content: block})
		}

		clearRetry := a.reportRetries(ev)
		msg, usage, err := a.Client.Stream(ctx, ai.Request{
			Model:           a.Model,
			Messages:        msgs,
			Tools:           tools.Defs(a.AllTools()),
			MaxTokens:       a.MaxTokens,
			ReasoningEffort: a.Effort,
			Temperature:     a.Temperature,
			TopP:            a.TopP,
		}, ev.OnText, ev.OnThink, ev.OnToolCall)
		clearRetry()
		a.AddUsage(usage)
		a.notePrompt(usage)
		if ev.OnUsage != nil {
			ev.OnUsage(usage)
		}
		if err != nil {
			if !a.compacted && ai.IsContextLimit(err) && ctx.Err() == nil {
				a.compacted = true
				took := len(a.Messages)
				if ev.OnCompactStart != nil {
					ev.OnCompactStart(took, EstimateTokens(a.Messages))
				}
				sum, cutoff, info, cerr := a.compact(ctx)
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
			return "", err
		}
		msg.Usage = &usage
		msg.Model = a.Model + " @ " + a.Provider
		a.msgsMu.Lock()
		a.Messages = append(a.Messages, msg)
		a.msgsMu.Unlock()
		if len(msg.ToolCalls) > 0 {
			results := a.runTools(ctx, msg.ToolCalls, ev)
			a.msgsMu.Lock()
			for i, tc := range msg.ToolCalls {
				a.Messages = append(a.Messages, ai.Message{
					Role:       "tool",
					Content:    results[i],
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
				})
			}
			a.msgsMu.Unlock()
			if ctx.Err() != nil {
				return "", ctx.Err()
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
			a.Messages = append(a.Messages, ai.Message{Role: "user", Content: s.text, Parts: s.parts})
		}
		if len(steered) > 0 {
			a.msgsMu.Unlock()
		}
		if len(msg.ToolCalls) == 0 && len(steered) == 0 {

			if !a.compacted {
				if cerr := a.maybeCompact(ctx, ev); cerr != nil {
					return "", cerr
				}
			}
			a.compacted = false
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

func (a *Agent) trackTool(name string, delta int64) {
	if name == "subagent" {
		a.subagentInflight.Add(delta)
	} else {
		a.otherInflight.Add(delta)
	}
}

func (a *Agent) WaitingOnSubagents() bool {
	return a.TurnRunning() && a.subagentInflight.Load() > 0 && a.otherInflight.Load() == 0
}

func (a *Agent) runTools(ctx context.Context, calls []ai.ToolCall, ev Events) []string {
	results := make([]string, len(calls))
	type outcome struct {
		i    int
		out  string
		ms   int64
		code int
	}
	outCh := make(chan outcome, len(calls))

	refused := a.markDoomLoops(calls)

	var wg sync.WaitGroup
	for i, tc := range calls {
		wg.Add(1)
		go func(i int, tc ai.ToolCall) {
			defer wg.Done()
			name, args := tc.Function.Name, tc.Function.Arguments

			if refused[i] {

				out := doomLoopRefusal(name)
				if ev.OnToolStart != nil {
					ev.OnToolStart(tc.ID, name, args)
				}
				if ev.OnToolEnd != nil {
					ev.OnToolEnd(tc.ID, name, out)
				}
				outCh <- outcome{i, out, 0, 1}
				return
			}

			var release func()
			if path, ok := toolMutationPath(name, args); ok {
				release = a.files.acquirePath(path)
			} else if name == "bash" {
				release = a.files.acquireGlobal()
			}
			if release != nil {
				defer release()
			}

			if ev.OnToolStart != nil {
				ev.OnToolStart(tc.ID, name, args)
			}
			a.trackTool(name, 1)
			defer a.trackTool(name, -1)
			start := time.Now()
			callCtx := ctx
			if ev.OnToolOutput != nil && name == "bash" {
				callCtx = tools.WithOnUpdate(ctx, func(soFar string) {
					ev.OnToolOutput(tc.ID, soFar)
				})
			}
			out := tools.Execute(callCtx, a.AllTools(), name, json.RawMessage(args))
			ms := time.Since(start).Milliseconds()
			if ev.OnToolEnd != nil {
				ev.OnToolEnd(tc.ID, name, out)
			}
			outCh <- outcome{i, out, ms, toolExitCode(out)}
		}(i, tc)
	}

	go func() {
		wg.Wait()
		close(outCh)
	}()
	for oc := range outCh {
		results[oc.i] = oc.out
		calls[oc.i].DurationMs = oc.ms
		calls[oc.i].ExitCode = oc.code
	}
	return results
}

const doomLoopMaxRun = 3

var doomLoopExempt = map[string]bool{"wait": true}

func (a *Agent) markDoomLoops(calls []ai.ToolCall) []bool {
	refused := make([]bool, len(calls))
	a.loopMu.Lock()
	defer a.loopMu.Unlock()
	for i, tc := range calls {
		key := tc.Function.Name + "\x00" + tc.Function.Arguments
		if key == a.lastCallKey {
			a.lastCallRun++
		} else {
			a.lastCallKey = key
			a.lastCallRun = 1
		}
		if a.lastCallRun >= doomLoopMaxRun && !doomLoopExempt[tc.Function.Name] {
			refused[i] = true
		}
	}
	return refused
}

func doomLoopRefusal(name string) string {
	return fmt.Sprintf("Error: refused to run %s — this exact call (same arguments) has already run %d times in a row with no other tool call in between. Repeating it will not produce new information. Change the approach: adjust the command/arguments, do the work instead of polling for it, or ask the user for guidance.",
		name, doomLoopMaxRun)
}

func toolExitCode(out string) int {
	if strings.HasPrefix(out, "error") || strings.HasPrefix(out, "Error") {
		return 1
	}
	return 0
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
	sum, cutoff, info, err := a.compact(ctx)
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

func EstimateTokens(msgs []ai.Message) int {
	total := 0
	for _, m := range msgs {
		total += 4 + (len(m.TextContent())+3)/4
		for _, p := range m.Parts {
			total += ai.PartTokens(p)
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
	summaryPrompt := buildSummaryPrompt(history, prior)
	cli, mdl := a.CompactClient, a.CompactModel
	dedicated := cli != nil
	if cli == nil {
		cli = a.Client
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
	sum, usage, cerr := cli.Complete(ctx, ai.Request{
		Model:     mdl,
		MaxTokens: 4096,
		Messages: []ai.Message{
			sysPrompt,
			{Role: "user", Content: summaryPrompt},
		},
	})
	a.AddUsage(usage)
	if cerr != nil {
		return "", 0, CompactInfo{}, fmt.Errorf("compaction summary failed: %w", cerr)
	}
	summary = strings.TrimSpace(sum)
	kept := append([]ai.Message(nil), tail...)
	a.msgsMu.Lock()
	a.Messages = append(append([]ai.Message{}, sysPrompt,
		ai.Message{Role: "system", Content: summaryPrefix + summary},
	), kept...)
	a.msgsMu.Unlock()

	a.usageMu.Lock()
	a.lastPrompt = 0
	a.usageMu.Unlock()
	return summary, tailStart, CompactInfo{Model: label, Usage: usage}, nil
}

const summaryPrefix = "Summary of the conversation so far:\n\n"

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
		return s[:n-1] + "…"
	}
	return s
}

func (a *Agent) ManualCompact(ctx context.Context, ev Events) error {
	if ev.OnCompactStart != nil {
		ev.OnCompactStart(len(a.Messages), EstimateTokens(a.Messages))
	}
	sum, cutoff, info, err := a.compact(ctx)
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

func (a *Agent) finalAnswer(ctx context.Context, ev Events) (string, error) {
	msgs := append(append([]ai.Message(nil), a.Messages...),
		ai.Message{Role: "system", Content: "You have reached the tool-call limit. Do NOT request any more tools. Give your final answer now using only what you have already gathered."})
	clearRetry := a.reportRetries(ev)
	msg, usage, err := a.Client.Stream(ctx, ai.Request{
		Model:           a.Model,
		Messages:        msgs,
		Tools:           nil,
		ReasoningEffort: a.Effort,
		Temperature:     a.Temperature,
		TopP:            a.TopP,
	}, ev.OnText, ev.OnThink, ev.OnToolCall)
	clearRetry()
	a.AddUsage(usage)
	a.notePrompt(usage)
	if ev.OnUsage != nil {
		ev.OnUsage(usage)
	}
	if err != nil {
		return "", err
	}
	a.compacted = false
	return msg.Content, nil
}
