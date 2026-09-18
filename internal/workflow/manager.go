package workflow

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"
)

type RunStatus string

const (
	RunRunning  RunStatus = "running"
	RunComplete RunStatus = "complete"
	RunError    RunStatus = "error"
	RunStopped  RunStatus = "stopped"
)

type AgentSnapshot struct {
	Index  int
	Label  string
	Phase  string
	Model  string
	Status RunStatus
	Error  string
}

type Snapshot struct {
	RunID    string
	Name     string
	Status   RunStatus
	Phase    string
	Phases   []string
	Agents   []AgentSnapshot
	Logs     []string
	Result   any
	Error    string
	Duration time.Duration
}

type RunSummary struct {
	ID         string
	Name       string
	Status     RunStatus
	ScriptPath string
	Done       chan struct{}
}

func (run *ManagedRun) snapshot() RunSummary {
	run.mu.Lock()
	defer run.mu.Unlock()
	return RunSummary{
		ID: run.ID, Name: run.Name, Status: run.Status,
		ScriptPath: run.ScriptPath, Done: run.Done,
	}
}

type ManagedRun struct {
	ID         string
	Name       string
	Status     RunStatus
	ScriptPath string
	Done       chan struct{}

	ctx    context.Context
	cancel context.CancelFunc

	mu   sync.Mutex
	snap Snapshot
}

type Manager struct {
	runner Runner
	cwd    string

	mu   sync.Mutex
	runs map[string]*ManagedRun

	OnSettle func(run RunSummary)
}

func NewManager(runner Runner, cwd string) *Manager {
	return &Manager{runner: runner, cwd: cwd, runs: map[string]*ManagedRun{}}
}

func (m *Manager) Start(script string, args any, resumeFromRunID string) (RunSummary, error) {
	meta, _, err := Parse(script)
	if err != nil {
		return RunSummary{}, err
	}
	runID := resumeFromRunID
	if runID == "" {
		runID = GenerateRunID()
	} else if !validRunID(runID) {

		return RunSummary{}, fmt.Errorf("invalid resumeFromRunId %q: must match ^[A-Za-z0-9._-]+$ with no '..'", runID)
	} else if r := m.get(runID); r != nil {

		r.mu.Lock()
		running := r.Status == RunRunning
		r.mu.Unlock()
		if running {
			return RunSummary{}, fmt.Errorf("resumeFromRunId %q is still running — wait for it to settle before resuming", runID)
		}
	}
	scriptPath := PersistScript(meta.Name, runID, script)

	var resume map[int]JournalEntry
	if resumeFromRunID != "" {
		resume = JournalMap(LoadRun(resumeFromRunID))
	}

	ctx, cancel := context.WithCancel(context.Background())
	run := &ManagedRun{
		ID: runID, Name: meta.Name, Status: RunRunning, ScriptPath: scriptPath,
		Done: make(chan struct{}), ctx: ctx, cancel: cancel,
		snap: Snapshot{RunID: runID, Name: meta.Name, Status: RunRunning},
	}
	m.mu.Lock()
	m.runs[runID] = run
	m.mu.Unlock()

	persisted := &PersistedRun{
		RunID: runID, Name: meta.Name, ScriptPath: scriptPath,
		Status: string(RunRunning), Args: args, StartedAt: time.Now().UnixMilli(),
	}
	SaveRun(persisted)

	go m.execute(run, script, args, persisted, resume)
	return run.snapshot(), nil
}

func (m *Manager) execute(run *ManagedRun, script string, args any, persisted *PersistedRun, resume map[int]JournalEntry) {
	events := Events{
		OnPhase: func(title string) {
			run.mu.Lock()
			run.snap.Phase = title
			run.mu.Unlock()
		},
		OnLog: func(msg string) {
			run.mu.Lock()
			run.snap.Logs = append(run.snap.Logs, msg)
			run.mu.Unlock()
		},
		OnAgentStart: func(index int, label, phase, model string) {
			run.mu.Lock()
			run.snap.Agents = append(run.snap.Agents, AgentSnapshot{
				Index: index, Label: label, Phase: phase, Model: model, Status: RunRunning,
			})
			for _, p := range phasesOf(run.snap.Agents) {
				if !contains(run.snap.Phases, p) {
					run.snap.Phases = append(run.snap.Phases, p)
				}
			}
			run.mu.Unlock()
		},
		OnAgentEnd: func(index int, label, phase string, result any, tokens int, errStr string) {
			run.mu.Lock()
			for i := range run.snap.Agents {
				if run.snap.Agents[i].Index == index {
					if errStr != "" {
						run.snap.Agents[i].Status = RunError
						run.snap.Agents[i].Error = errStr
					} else {
						run.snap.Agents[i].Status = RunComplete
					}
					break
				}
			}
			run.mu.Unlock()
		},
		OnJournal: func(e JournalEntry) {

			run.mu.Lock()
			defer run.mu.Unlock()
			persisted.Journal = append(persisted.Journal, e)
			SaveRun(persisted)
		},
	}

	result, err := Run(run.ctx, script, Options{
		RunID: runID(run), Cwd: m.cwd, Args: args,
		Run: m.runner, Events: events, ResumeJournal: resume,
	})

	run.mu.Lock()
	switch {
	case err != nil && run.ctx.Err() == context.Canceled:
		run.Status, run.snap.Status = RunStopped, RunStopped
		run.snap.Error = err.Error()
		persisted.Status, persisted.Error = string(RunStopped), err.Error()
	case err != nil:
		run.Status, run.snap.Status = RunError, RunError
		run.snap.Error = err.Error()
		persisted.Status, persisted.Error = string(RunError), err.Error()
	default:
		run.Status, run.snap.Status = RunComplete, RunComplete
		run.snap.Result = result.Value
		run.snap.Duration = result.Duration
		run.snap.Phases = result.Phases
		persisted.Status, persisted.Result = string(RunComplete), result.Value

		persisted.Journal = result.Journal
	}
	persisted.FinishedAt = time.Now().UnixMilli()
	run.mu.Unlock()
	SaveRun(persisted)

	if m.OnSettle != nil {
		m.OnSettle(run.snapshot())
	}
	close(run.Done)
}

func runID(run *ManagedRun) string { return run.ID }

func (m *Manager) get(id string) *ManagedRun {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runs[id]
}

func (m *Manager) List() []RunSummary {
	m.mu.Lock()
	runs := make([]*ManagedRun, 0, len(m.runs))
	for _, r := range m.runs {
		runs = append(runs, r)
	}
	m.mu.Unlock()
	out := make([]RunSummary, len(runs))
	for i, r := range runs {
		out[i] = r.snapshot()
	}
	return out
}

func (m *Manager) Snapshot(id string) (Snapshot, bool) {
	r := m.get(id)
	if r == nil {
		return Snapshot{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	snap := r.snap
	snap.Agents = append([]AgentSnapshot(nil), r.snap.Agents...)
	snap.Phases = append([]string(nil), r.snap.Phases...)
	snap.Logs = append([]string(nil), r.snap.Logs...)
	return snap, true
}

func (m *Manager) Stop(id string) bool {
	r := m.get(id)
	if r == nil {
		return false
	}
	r.mu.Lock()
	running := r.Status == RunRunning
	r.mu.Unlock()
	if !running {
		return false
	}
	r.cancel()
	return true
}

func phasesOf(agents []AgentSnapshot) []string {
	var out []string
	for _, a := range agents {
		if a.Phase != "" && !contains(out, a.Phase) {
			out = append(out, a.Phase)
		}
	}
	return out
}

func contains(ss []string, s string) bool {
	return slices.Contains(ss, s)
}
