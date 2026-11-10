package backend

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// Loop workers never read or mutate the parent's model context while running.
func (s *Server) executeLoopPrompt(ctx context.Context, task *CronTask, cwd string, run *CronRunRecord) (bool, string) {
	workerID, err := s.store.SaveSubagentTranscript(task.SessionID, task.ID, nil, task.SelectedModel.Model, task.SelectedModel.CustomProviderID)
	if err != nil {
		return false, err.Error()
	}
	if err := s.store.ReplaceFrom(workerID, 0, nil, task.SelectedModel.Model, task.SelectedModel.CustomProviderID); err != nil {
		return false, err.Error()
	}
	if err := s.store.SetCWD(workerID, cwd); err != nil {
		return false, err.Error()
	}
	worker, err := s.loadRuntimeByID(workerID)
	if err != nil {
		return false, err.Error()
	}
	worker.mu.Lock()
	// Rebuild from the empty transcript, retaining the configured system instructions.
	err = s.rebuildAgentLocked(worker, protocol.ModelRef{Provider: task.SelectedModel.CustomProviderID, Model: task.SelectedModel.Model}, cwd)
	worker.mu.Unlock()
	s.releaseRuntime(worker)
	if err != nil {
		return false, err.Error()
	}
	copy := *task
	copy.SessionID = workerID
	copy.Loop = nil
	return s.executeCronPromptCanonical(ctx, &copy, cwd, run)
}

// Completion delivery is retried while the user is running a turn. A stable message
// id makes replay after a crash between history save and acknowledgement harmless.
func (s *Server) deliverLoopResults(task *CronTask) {
	runs, err := s.cron.store.runs(task.ID, cronRunLimit)
	if err != nil {
		return
	}
	for i := len(runs) - 1; i >= 0; i-- {
		run := runs[i]
		if run.FinishedAt == nil || run.Delivered {
			continue
		}
		parent, err := s.loadRuntimeByID(task.SessionID)
		if err != nil {
			return
		}
		parent.mu.Lock()
		if parent.activeLocked() || parent.deleted || parent.closing || parent.runtimeErr != nil {
			parent.mu.Unlock()
			s.releaseRuntime(parent)
			return
		}
		if err = s.reloadStaleHistoryLocked(parent); err != nil {
			parent.mu.Unlock()
			s.releaseRuntime(parent)
			return
		}
		previous := parent.agent.MessagesSnapshot()
		id := "loop-result-" + run.ID
		present := false
		for _, message := range previous {
			if message.ID == id {
				present = true
				break
			}
		}
		if !present {
			output := []rune(strings.TrimSpace(run.Output))
			if len(output) > 4000 {
				output = append(output[:4000], []rune("\n[Output truncated; full result is in scheduled task logs.]")...)
			}
			now := time.Now().UTC()
			message := ai.Message{ID: id, Role: "assistant", Content: fmt.Sprintf("[Loop %s · %s]\n%s", task.ID, task.Prompt, string(output)), StopReason: ai.StopReasonStop, SentAt: &now}
			parent.agent.RestoreMessages(append(previous, message))
			if err = parent.recorder.Save(); err != nil {
				parent.agent.RestoreMessages(previous)
			}
		}
		if err == nil {
			if parent.runID == "" {
				parent.runID = "recovery"
			}
			err = parent.publishEventLocked(parent.runID, protocol.EventHistoryUpdated, map[string]any{"reason": "loop_result", "loop_id": task.ID})
		}
		parent.mu.Unlock()
		s.releaseRuntime(parent)
		if err != nil {
			return
		}
		s.cron.store.mu.Lock()
		old := cloneCronDisk(s.cron.store.disk)
		for j := range s.cron.store.disk.Runs {
			if s.cron.store.disk.Runs[j].ID == run.ID {
				s.cron.store.disk.Runs[j].Delivered = true
			}
		}
		err = s.cron.store.commitLocked(old)
		s.cron.store.mu.Unlock()
		if err != nil {
			return
		}
	}
}
