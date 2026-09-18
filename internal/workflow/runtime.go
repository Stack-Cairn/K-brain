package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

const (
	MaxConcurrency  = 16
	MaxAgentsPerRun = 1000
	MaxFanoutItems  = 4096
	maxNestDepth    = 1
)

type AgentOptions struct {
	Label     string          `json:"label,omitempty"`
	Phase     string          `json:"phase,omitempty"`
	Schema    json.RawMessage `json:"schema,omitempty"`
	Model     string          `json:"model,omitempty"`
	Effort    string          `json:"effort,omitempty"`
	TimeoutMs int             `json:"timeoutMs,omitempty"`
}

type AgentRequest struct {
	Prompt  string
	Options AgentOptions
	Phase   string
	Model   string
	Effort  string
	Index   int
	Workdir string
}

type Usage struct{ Total int }

type Runner func(ctx context.Context, req AgentRequest) (result any, usage Usage, err error)

type Events struct {
	OnAgentStart func(index int, label, phase, model string)
	OnAgentEnd   func(index int, label, phase string, result any, tokens int, err string)
	OnJournal    func(e JournalEntry)
	OnPhase      func(title string)
	OnLog        func(msg string)
}

type Result struct {
	RunID      string
	Meta       Meta
	Value      any
	AgentCount int
	Tokens     int
	Phases     []string
	Duration   time.Duration
	Journal    []JournalEntry
}

type Options struct {
	RunID string
	Cwd   string
	Args  any
	Run   Runner

	ResumeJournal map[int]JournalEntry
	Events        Events
	Concurrency   int
	MaxAgents     int

	shared *sharedRuntime
}

type sharedRuntime struct {
	state *sharedState
	depth int
}

type sharedState struct {
	mu         sync.Mutex
	agentCount int
	sem        chan struct{}
	spent      atomic.Int64

	maxAgents int
}

func (s *sharedRuntime) reserveAgent(callMax int) (n int, ok bool) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	limit := s.state.maxAgents
	if limit <= 0 || callMax < limit {
		limit = callMax
	}
	if s.state.agentCount >= limit {
		return 0, false
	}
	s.state.agentCount++
	return s.state.agentCount, true
}

func (s *sharedRuntime) count() int {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	return s.state.agentCount
}

func (s *sharedRuntime) child() *sharedRuntime {
	return &sharedRuntime{state: s.state, depth: s.depth + 1}
}

type runState struct {
	phases    []string
	phase     string
	callSeq   int
	firstMiss int
	tokens    int
	journal   []JournalEntry
	resume    map[int]JournalEntry

	reservedIndex int
}

const determinismPrelude = `"use strict";
Math.random = function() { throw new Error("Math.random() is unavailable in a workflow (it breaks resume); vary by index or pass via args"); };
(function(g) {
  var RealDate = Date;
  var fail = function(w) { throw new Error(w + " is unavailable in a workflow (it breaks resume); pass a timestamp via args"); };
  function SafeDate() {
    if (!(this instanceof SafeDate)) { fail("Date()"); }
    if (arguments.length === 0) { fail("new Date()"); }
    var args = [null].concat(Array.prototype.slice.call(arguments));
    return new (Function.prototype.bind.apply(RealDate, args))();
  }
  SafeDate.UTC = RealDate.UTC;
  SafeDate.parse = RealDate.parse;
  SafeDate.now = function() { fail("Date.now()"); };
  SafeDate.prototype = Object.create(RealDate.prototype);
  SafeDate.prototype.constructor = SafeDate;
  g.Date = SafeDate;
})(this);`

func Run(ctx context.Context, script string, opts Options) (*Result, error) {
	started := time.Now()
	meta, body, err := Parse(script)
	if err != nil {
		return nil, err
	}
	if opts.Run == nil {
		return nil, errors.New("workflow: Options.Run (the subagent runner) is required")
	}
	maxAgents := opts.MaxAgents
	if maxAgents <= 0 {
		maxAgents = MaxAgentsPerRun
	}
	cwd := opts.Cwd
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}

	shared := opts.shared
	if shared == nil {
		conc := opts.Concurrency
		if conc <= 0 {
			if n := runtime.NumCPU() - 2; n > 0 {
				conc = n
			} else {
				conc = 1
			}
			if conc > MaxConcurrency {
				conc = MaxConcurrency
			}
		}

		shared = &sharedRuntime{state: &sharedState{sem: make(chan struct{}, conc), maxAgents: maxAgents}}
	}

	st := &runState{firstMiss: 1 << 30, resume: opts.ResumeJournal, reservedIndex: -1}

	if len(meta.Phases) > 0 {
		st.phase = meta.Phases[0].Title
		st.phases = []string{meta.Phases[0].Title}
	}

	vm := goja.New()

	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	sc := &scheduler{vm: vm, jobs: make(chan func()), done: make(chan struct{})}

	must(vm.Set("args", opts.Args))
	must(vm.Set("cwd", cwd))
	must(vm.Set("console", map[string]any{
		"log": func(args ...any) { sc.log(opts.Events.OnLog, joinArgs(args)) },
	}))
	must(vm.Set("budget", map[string]any{
		"total": nil,
		"spent": func() int64 { return shared.state.spent.Load() },

		"remaining": func() goja.Value { return goja.PositiveInf() },
	}))
	must(vm.Set("log", func(msg any) { sc.log(opts.Events.OnLog, fmt.Sprint(msg)) }))
	must(vm.Set("phase", func(title string) { sc.setPhase(st, title, opts.Events.OnPhase) }))
	must(vm.Set("agent", sc.agentFunc(ctx, st, opts, shared, meta, maxAgents, cwd)))
	must(vm.Set("parallel", sc.parallelFunc(st, opts.Events.OnLog)))
	must(vm.Set("pipeline", sc.pipelineFunc(st, opts.Events.OnLog)))
	must(vm.Set("workflow", sc.nestedWorkflowFunc(ctx, opts, shared)))

	if _, err := vm.RunString(determinismPrelude); err != nil {
		return nil, fmt.Errorf("workflow: determinism prelude: %w", err)
	}

	vm.SetPromiseRejectionTracker(func(p *goja.Promise, op goja.PromiseRejectionOperation) {
		if op == goja.PromiseRejectionReject && opts.Events.OnLog != nil {
			msg := fmt.Sprintf("unhandled promise rejection: %v", p.Result())
			go func() { opts.Events.OnLog(msg) }()
		}
	})

	go sc.loop()

	var runErr error
	var runValue any
	finished := make(chan struct{})
	sc.enqueue(func() {
		var promise *goja.Promise
		if exc := vm.Try(func() {
			v, err := vm.RunString("(async () => {\n" + body + "\n})()")
			if err != nil {
				panic(err)
			}
			p, ok := v.Export().(*goja.Promise)
			if !ok {
				panic(errors.New("workflow body did not evaluate to a promise"))
			}
			promise = p
		}); exc != nil {
			runErr = fmt.Errorf("%w", exc)
			close(finished)
			return
		}

		awaitValue(vm, vm.ToValue(promise).ToObject(vm),
			func(v goja.Value) { runValue = v.Export(); close(finished) },
			func(e goja.Value) {
				runErr = fmt.Errorf("%v", exportErr(e))
				close(finished)
			})
	})

	select {
	case <-finished:
	case <-ctx.Done():
		vm.Interrupt(context.Canceled)

		select {
		case <-finished:
		case <-time.After(100 * time.Millisecond):
		}
		runErr = context.Canceled
	}
	close(sc.done)

	if shared.count() == 0 && runErr == nil {
		runErr = errors.New("workflow scripts must call agent() at least once; this run declared phases but spawned no agents")
	}
	if runErr != nil {
		return nil, runErr
	}
	return &Result{
		RunID:      opts.RunID,
		Meta:       meta,
		Value:      runValue,
		AgentCount: shared.count(),
		Tokens:     st.tokens,
		Phases:     st.phases,
		Duration:   time.Since(started),
		Journal:    st.journal,
	}, nil
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

type scheduler struct {
	vm   *goja.Runtime
	jobs chan func()
	done chan struct{}
}

func (s *scheduler) enqueue(job func()) {
	select {
	case s.jobs <- job:
	case <-s.done:
	}
}

func (s *scheduler) loop() {
	for {
		select {
		case job := <-s.jobs:
			job()

			_, _ = s.vm.RunString("0")
		case <-s.done:
			return
		}
	}
}

func (s *scheduler) log(onLog func(string), msg string) {

	if onLog != nil {
		onLog(msg)
	}
}

func (s *scheduler) setPhase(st *runState, title string, onPhase func(string)) {
	st.phase = title
	if slices.Contains(st.phases, title) {
		if onPhase != nil {
			onPhase(title)
		}
		return
	}
	st.phases = append(st.phases, title)
	if onPhase != nil {
		onPhase(title)
	}
}

func joinArgs(args []any) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = fmt.Sprint(a)
	}
	return strings.Join(parts, " ")
}

func awaitValue(vm *goja.Runtime, obj *goja.Object, onOK, onErr func(goja.Value)) {
	if fn, ok := goja.AssertFunction(obj.Get("then")); ok {
		_, _ = fn(obj, vm.ToValue(onOK), vm.ToValue(onErr))
	}
}

func exportErr(v goja.Value) any {
	if e := v.Export(); e != nil {
		if err, ok := e.(error); ok {
			return err
		}
		return e
	}
	return v.String()
}

func (s *scheduler) agentFunc(ctx context.Context, st *runState, opts Options, shared *sharedRuntime, meta Meta, maxAgents int, cwd string) func(goja.FunctionCall) goja.Value {
	vm := s.vm
	return func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			panic(vm.ToValue("agent(prompt, opts): prompt is required"))
		}
		prompt, ok := call.Argument(0).Export().(string)
		if !ok {
			panic(vm.ToValue("agent(prompt, opts): prompt must be a string"))
		}
		var ao AgentOptions
		if len(call.Arguments) > 1 {

			var od struct {
				Label     string `json:"label,omitempty"`
				Phase     string `json:"phase,omitempty"`
				Schema    any    `json:"schema,omitempty"`
				Model     string `json:"model,omitempty"`
				Effort    string `json:"effort,omitempty"`
				TimeoutMs int    `json:"timeoutMs,omitempty"`
			}
			if err := vm.ExportTo(call.Argument(1), &od); err != nil {
				panic(vm.ToValue("agent(prompt, opts): " + err.Error()))
			}
			ao = AgentOptions{
				Label: od.Label, Phase: od.Phase, Model: od.Model,
				Effort: od.Effort, TimeoutMs: od.TimeoutMs,
			}
			if od.Schema != nil {
				b, err := json.Marshal(od.Schema)
				if err != nil {
					panic(vm.ToValue("agent(prompt, opts): schema is not a valid JSON value: " + err.Error()))
				}
				ao.Schema = b
			}
		}
		if ctx.Err() != nil {
			panic(vm.ToValue("workflow aborted"))
		}

		assignedPhase := ao.Phase
		if assignedPhase == "" {
			assignedPhase = st.phase
		}
		model := ao.Model
		if model == "" {
			model = phaseModel(meta, assignedPhase)
		}
		effort := ao.Effort
		if effort == "" {
			effort = phaseEffort(meta, assignedPhase)
		}

		callIndex := st.callSeq
		if st.reservedIndex >= 0 {

			callIndex = st.reservedIndex
			st.reservedIndex = -1
		} else {
			st.callSeq++
		}
		callHash := callKey(prompt, model, effort, assignedPhase, ao.Schema)

		n, ok := shared.reserveAgent(maxAgents)
		if !ok {
			panic(vm.ToValue(fmt.Sprintf("agent limit exceeded (%d) — a runaway-loop backstop", maxAgents)))
		}
		label := strings.TrimSpace(ao.Label)
		if label == "" {
			if assignedPhase != "" {
				label = fmt.Sprintf("%s agent %d", assignedPhase, n)
			} else {
				label = fmt.Sprintf("agent %d", n)
			}
		}
		if len(label) > 80 {
			label = label[:80]
		}

		cached, hit := st.resume[callIndex]
		if hit && cached.Hash == callHash && callIndex < st.firstMiss {
			if opts.Events.OnAgentStart != nil {
				opts.Events.OnAgentStart(callIndex, label, assignedPhase, model)
			}
			if opts.Events.OnAgentEnd != nil {
				opts.Events.OnAgentEnd(callIndex, label, assignedPhase, cached.Result, 0, "")
			}
			st.journal = append(st.journal, JournalEntry{Index: callIndex, Hash: callHash, Result: cached.Result})
			return vm.ToValue(cached.Result)
		}
		if !hit || cached.Hash != callHash {
			if callIndex < st.firstMiss {
				st.firstMiss = callIndex
			}
		}

		promise, resolve, reject := vm.NewPromise()
		req := AgentRequest{
			Prompt: prompt, Options: ao, Phase: assignedPhase,
			Model: model, Effort: effort, Index: callIndex, Workdir: cwd,
		}
		go s.runAgent(ctx, st, opts, shared, req, label, callHash, resolve, reject)
		return vm.ToValue(promise)
	}
}

func (s *scheduler) runAgent(ctx context.Context, st *runState, opts Options, shared *sharedRuntime, req AgentRequest, label, callHash string, resolve, reject func(any) error) {
	select {
	case shared.state.sem <- struct{}{}:
	case <-ctx.Done():
		s.enqueue(func() { _ = reject("workflow aborted") })
		return
	}
	defer func() { <-shared.state.sem }()

	if opts.Events.OnAgentStart != nil {
		opts.Events.OnAgentStart(req.Index, label, req.Phase, req.Model)
	}

	runCtx := ctx
	if req.Options.TimeoutMs > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(req.Options.TimeoutMs)*time.Millisecond)
		defer cancel()
	}

	result, usage, err := func() (result any, usage Usage, err error) {
		defer func() {
			if r := recover(); r != nil {
				result, usage, err = nil, Usage{}, fmt.Errorf("agent runner panicked: %v", r)
			}
		}()
		return opts.Run(runCtx, req)
	}()

	s.enqueue(func() {
		st.tokens += usage.Total
		shared.state.spent.Add(int64(usage.Total))
		if err != nil {
			s.log(opts.Events.OnLog, fmt.Sprintf("agent %q failed: %v", label, err))
			if opts.Events.OnAgentEnd != nil {
				opts.Events.OnAgentEnd(req.Index, label, req.Phase, nil, usage.Total, err.Error())
			}
			_ = resolve(nil)
			return
		}
		entry := JournalEntry{Index: req.Index, Hash: callHash, Result: result}
		st.journal = append(st.journal, entry)
		if opts.Events.OnJournal != nil {
			opts.Events.OnJournal(entry)
		}
		if opts.Events.OnAgentEnd != nil {
			opts.Events.OnAgentEnd(req.Index, label, req.Phase, result, usage.Total, "")
		}
		_ = resolve(result)
	})
}

func callKey(prompt, model, effort, phase string, schema json.RawMessage) string {
	var b strings.Builder
	b.WriteString(`{"prompt":`)
	writeJSONString(&b, prompt)
	b.WriteString(`,"model":`)
	writeJSONString(&b, model)
	b.WriteString(`,"effort":`)
	writeJSONString(&b, effort)
	b.WriteString(`,"phase":`)
	writeJSONString(&b, phase)
	b.WriteString(`,"schema":`)
	if len(schema) == 0 {
		b.WriteString("null")
	} else {
		var buf bytes.Buffer
		if err := json.Compact(&buf, schema); err == nil {
			b.WriteString(buf.String())
		} else {
			b.Write(schema)
		}
	}
	b.WriteByte('}')
	return HashString(b.String())
}

func writeJSONString(b *strings.Builder, s string) {
	if s == "" {
		b.WriteString("null")
		return
	}
	out, _ := json.Marshal(s)
	b.Write(out)
}

func (s *scheduler) parallelFunc(st *runState, onLog func(string)) func(goja.FunctionCall) goja.Value {
	vm := s.vm
	return func(call goja.FunctionCall) goja.Value {
		thunks, err := s.thunks(call.Argument(0), "parallel", st)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		promise, resolve, _ := vm.NewPromise()
		results := make([]any, len(thunks))
		var wg sync.WaitGroup
		for i, thunk := range thunks {
			wg.Add(1)
			go func(i int, thunk func() (any, error)) {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						s.enqueue(func() { s.log(onLog, fmt.Sprintf("parallel[%d] failed: %v", i, r)) })
						results[i] = nil
					}
				}()
				v, err := thunk()
				if err != nil {
					s.enqueue(func() { s.log(onLog, fmt.Sprintf("parallel[%d] failed: %v", i, err)) })
					v = nil
				}
				results[i] = v
			}(i, thunk)
		}
		go func() {
			wg.Wait()
			s.enqueue(func() { _ = resolve(results) })
		}()
		return vm.ToValue(promise)
	}
}

func (s *scheduler) pipelineFunc(st *runState, onLog func(string)) func(goja.FunctionCall) goja.Value {
	vm := s.vm
	return func(call goja.FunctionCall) goja.Value {
		var items []any
		if err := vm.ExportTo(call.Argument(0), &items); err != nil {
			panic(vm.ToValue("pipeline() expects an array as the first argument"))
		}
		if len(items) > MaxFanoutItems {
			panic(vm.ToValue(fmt.Sprintf("pipeline() accepts at most %d items (got %d)", MaxFanoutItems, len(items))))
		}
		stages := make([]func(reserveIdx int, args ...any) (any, error), 0, len(call.Arguments)-1)
		for _, arg := range call.Arguments[1:] {
			fn, ok := goja.AssertFunction(arg)
			if !ok {
				panic(vm.ToValue("pipeline() stages must be functions"))
			}
			stages = append(stages, s.stage(fn, st))
		}
		start := st.callSeq
		st.callSeq += len(items)
		promise, resolve, _ := vm.NewPromise()
		results := make([]any, len(items))
		var wg sync.WaitGroup
		for i, item := range items {
			wg.Add(1)
			go func(i int, item any) {
				defer wg.Done()
				value := item
				for j, stage := range stages {

					reserveIdx := -1
					if j == 0 {
						reserveIdx = start + i
					}
					v, err := stage(reserveIdx, value, item, i)
					if err != nil {
						s.enqueue(func() { s.log(onLog, fmt.Sprintf("pipeline[%d] dropped at a stage: %v", i, err)) })
						results[i] = nil
						return
					}
					value = v
				}
				results[i] = value
			}(i, item)
		}
		go func() {
			wg.Wait()
			s.enqueue(func() { _ = resolve(results) })
		}()
		return vm.ToValue(promise)
	}
}

func (s *scheduler) thunks(v goja.Value, name string, st *runState) ([]func() (any, error), error) {
	vm := s.vm
	obj := v.ToObject(vm)
	if obj == nil || obj.Get("length") == nil {
		return nil, fmt.Errorf("%s() expects an array of functions", name)
	}
	n := int(obj.Get("length").ToInteger())
	if n > MaxFanoutItems {
		return nil, fmt.Errorf("%s() accepts at most %d items (got %d)", name, MaxFanoutItems, n)
	}
	start := st.callSeq
	st.callSeq += n
	thunks := make([]func() (any, error), 0, n)
	for i := range n {
		fn, ok := goja.AssertFunction(obj.Get(strconv.Itoa(i)))
		if !ok {
			return nil, fmt.Errorf("%s() expects functions, not promises — wrap each call: () => agent(...)", name)
		}
		idx := start + i
		thunks = append(thunks, func() (any, error) {
			return s.callJSWith(idx, st, fn)
		})
	}
	return thunks, nil
}

func (s *scheduler) stage(fn goja.Callable, st *runState) func(reserveIdx int, args ...any) (any, error) {
	return func(reserveIdx int, args ...any) (any, error) {
		return s.callJSWith(reserveIdx, st, fn, args...)
	}
}

func (s *scheduler) callJSWith(reserveIdx int, st *runState, fn goja.Callable, args ...any) (any, error) {
	vm := s.vm
	type out struct {
		v   any
		err error
	}
	done := make(chan out, 1)
	s.enqueue(func() {
		reserve := st != nil && reserveIdx >= 0
		if reserve {
			st.reservedIndex = reserveIdx
		}
		vals := make([]goja.Value, len(args))
		for i, a := range args {
			vals[i] = vm.ToValue(a)
		}
		v, err := fn(goja.Undefined(), vals...)
		if reserve {
			st.reservedIndex = -1
		}
		if err != nil {
			done <- out{err: err}
			return
		}
		if p, ok := v.Export().(*goja.Promise); ok {
			awaitValue(vm, vm.ToValue(p).ToObject(vm),
				func(r goja.Value) { done <- out{v: r.Export()} },
				func(e goja.Value) { done <- out{err: fmt.Errorf("%v", exportErr(e))} })
			return
		}
		done <- out{v: v.Export()}
	})
	select {
	case o := <-done:
		return o.v, o.err
	case <-s.done:
		return nil, context.Canceled
	}
}

func (s *scheduler) nestedWorkflowFunc(ctx context.Context, opts Options, shared *sharedRuntime) func(goja.FunctionCall) goja.Value {
	vm := s.vm
	return func(call goja.FunctionCall) goja.Value {
		if shared.depth >= maxNestDepth {
			panic(vm.ToValue("workflow() nesting is one level only"))
		}
		var scriptPath string
		switch ref := call.Argument(0).Export().(type) {
		case string:

			scriptPath = ref
		case map[string]any:
			scriptPath, _ = ref["scriptPath"].(string)
		}
		if scriptPath == "" {
			panic(vm.ToValue("workflow(nameOrRef): pass { scriptPath } of a persisted workflow script"))
		}
		var childArgs any
		if len(call.Arguments) > 1 {
			childArgs = call.Argument(1).Export()
		}
		promise, resolve, reject := vm.NewPromise()
		go func() {
			data, err := os.ReadFile(filepath.Clean(scriptPath))
			if err != nil {
				s.enqueue(func() { _ = reject("could not read scriptPath: " + err.Error()) })
				return
			}

			childEvents := Events{}
			if opts.Events.OnLog != nil {
				onLog := opts.Events.OnLog
				childEvents.OnLog = onLog
			}
			child, err := Run(ctx, string(data), Options{
				RunID:  opts.RunID + ".nested",
				Cwd:    opts.Cwd,
				Args:   childArgs,
				Run:    opts.Run,
				Events: childEvents,
				shared: shared.child(),
			})
			if err != nil {
				s.enqueue(func() { _ = reject(err.Error()) })
				return
			}
			s.enqueue(func() { _ = resolve(child.Value) })
		}()
		return vm.ToValue(promise)
	}
}

func phaseModel(meta Meta, phase string) string {
	if phase != "" {
		for _, p := range meta.Phases {
			if p.Title == phase && p.Model != "" {
				return p.Model
			}
		}
	}
	return meta.Model
}

func phaseEffort(meta Meta, phase string) string {
	if phase != "" {
		for _, p := range meta.Phases {
			if p.Title == phase && p.Effort != "" {
				return p.Effort
			}
		}
	}
	return meta.Effort
}
