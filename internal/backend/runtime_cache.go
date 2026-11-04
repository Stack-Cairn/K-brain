package backend

import (
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
)

const runtimeIdleTTL = 5 * time.Minute
const maxIdleRuntimes = 8

func (s *Server) releaseRuntime(rt *runtimeSession) {
	rt.mu.Lock()
	if rt.leases.Load() > 0 {
		rt.leases.Add(-1)
	}
	rt.lastUsed = time.Now()
	rt.mu.Unlock()
}

func (s *Server) reapRuntimes(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, rt := range s.sessions {
		if !rt.mu.TryLock() {
			continue
		}
		idle := rt.leases.Load() == 0 && !rt.activeLocked() && rt.backgroundSettledLocked() && !rt.closing && rt.runtimeErr == nil &&
			len(rt.permissions) == 0 && len(rt.questions) == 0 && len(rt.clientTools) == 0 &&
			(rt.terminalManager == nil || !rt.terminalManager.HasRunningSessions()) &&
			(rt.processManager == nil || !rt.processManager.HasRunningProcesses()) &&
			// A pending wait or undelivered notice would otherwise be lost with the runtime.
			(rt.agent == nil || rt.agent.Waits().Active() == 0) && !rt.wakesPendingLocked()
		if idle && (now.Sub(rt.lastUsed) >= runtimeIdleTTL || len(s.sessions) > maxIdleRuntimes) {
			// Readers acquire a lease while holding s.mu, so none can revive this instance.
			rt.closing = true
			if rt.memoryRuntime != nil {
				_ = rt.memoryRuntime.Close()
				rt.memoryRuntime = nil
			}
			if rt.terminalManager != nil {
				rt.terminalManager.CloseAll()
				rt.terminalManager = nil
			}
			if rt.processManager != nil {
				rt.processManager.CloseAll()
				rt.processManager = nil
			}
			rt.agent = nil
			rt.recorder = nil
			rt.events = nil
			rt.eventBytes = 0
			rt.runs = nil
			rt.terminalContexts = nil
			delete(s.sessions, id)
		}
		rt.mu.Unlock()
	}
}

func (s *Server) startRuntimeReaper() {
	s.reaperStop = make(chan struct{})
	s.reaperDone = make(chan struct{})
	go func() {
		defer close(s.reaperDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.reaperStop:
				return
			case now := <-ticker.C:
				s.reapRuntimes(now)
			}
		}
	}()
}

func (rt *runtimeSession) backgroundSettledLocked() bool {
	if rt.agent == nil {
		return true
	}
	for _, task := range rt.agent.Tasks().List() {
		if task.FollowingUp || task.Status == agent.TaskRunning {
			return false
		}
		if task.Done != nil {
			select {
			case <-task.Done:
			default:
				return false
			}
		}
	}
	return true
}
