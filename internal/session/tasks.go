package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func (s *Store) SaveTask(id string, t Task) error {
	return s.update(id, func(d *sessionData) error { d.Tasks[t.ID] = t; return nil })
}
func (s *Store) LoadTasks(id string) ([]Task, error) {
	d, err := s.get(id)
	if err != nil {
		return nil, err
	}
	var tasks []Task
	for _, t := range d.Tasks {
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].StartedAt.Equal(tasks[j].StartedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].StartedAt.Before(tasks[j].StartedAt)
	})
	return tasks, nil
}
func subagentSessionID(parentID, taskID string) string { return "task-" + parentID + "-" + taskID }
func (s *Store) SaveSubagentTranscript(parentID, taskID string, msgs []ai.Message, model, provider string) (string, error) {
	if parentID == "" || taskID == "" {
		return "", nil
	}
	id := subagentSessionID(parentID, taskID)
	err := s.withLock(func() error {
		d, err := s.read(id)
		if errors.Is(err, ErrNotFound) {
			cwd := ""
			if parent, parentErr := s.read(parentID); parentErr == nil {
				cwd = parent.Meta.CWD
			}
			d = newData(Meta{ID: id, Title: "subagent " + taskID, CWD: cwd, ForkedFrom: parentID, TaskID: taskID, UpdatedAt: time.Now().UTC()})
			dir := filepath.Join(s.filesDir, id)
			if s.projectScoped {
				dir = filepath.Join(s.filesDir, projectDir(cwd), id)
			}
			if err := os.MkdirAll(dir, 0700); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err := saveMessages(d, 0, msgs, model, provider); err != nil {
			return err
		}
		for seq := range d.Messages {
			if seq >= len(msgs) {
				delete(d.Messages, seq)
			}
		}
		return s.write(d)
	})
	if err != nil {
		return "", err
	}
	return id, nil
}
func (s *Store) SubagentTranscript(parentID, taskID string) ([]ai.Message, error) {
	if parentID == "" || taskID == "" {
		return nil, nil
	}
	d, err := s.get(subagentSessionID(parentID, taskID))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d.contextMessages(), nil
}
func (s *Store) SetSnapshot(id string, seq int, ref string) error {
	if seq < 0 {
		return fmt.Errorf("invalid snapshot sequence %d", seq)
	}
	return s.update(id, func(d *sessionData) error {
		if ref == "" {
			delete(d.Snapshots, seq)
		} else {
			d.Snapshots[seq] = ref
		}
		return nil
	})
}
func (s *Store) Snapshots(id string) map[int]string {
	d, err := s.get(id)
	if err != nil {
		return nil
	}
	return d.Snapshots
}
func (s *Store) ClearSnapshots(id string) error {
	return s.update(id, func(d *sessionData) error { clear(d.Snapshots); return nil })
}
func (s *Store) AddSchedule(sessionID, schedule, prompt string, anchor time.Time) (id int, err error) {
	err = s.update(sessionID, func(d *sessionData) error {
		for n := range d.Schedules {
			if n > id {
				id = n
			}
		}
		id++
		d.Schedules[id] = Schedule{ID: id, Schedule: schedule, Prompt: prompt, Anchor: anchor}
		return nil
	})
	return
}
func (s *Store) Schedules(id string) []Schedule {
	d, err := s.get(id)
	if err != nil {
		return nil
	}
	var out []Schedule
	for _, n := range sortedKeys(d.Schedules) {
		out = append(out, d.Schedules[n])
	}
	return out
}
func (s *Store) MarkFired(sessionID string, id int, at time.Time) error {
	return s.update(sessionID, func(d *sessionData) error {
		sc, ok := d.Schedules[id]
		if ok {
			sc.LastFire = at
			d.Schedules[id] = sc
		}
		return nil
	})
}
func (s *Store) DeleteSchedule(sessionID string, id int) error {
	return s.update(sessionID, func(d *sessionData) error { delete(d.Schedules, id); return nil })
}
func (s *Store) RecordCompaction(id string, cutoff int, summary, model string, usage ai.Usage) error {
	return s.update(id, func(d *sessionData) error {
		n := 0
		for seq := range d.Compactions {
			if seq > n {
				n = seq
			}
		}
		d.Compactions[n+1] = Compaction{Seq: n + 1, Cutoff: cutoff, Summary: summary, Model: model, Usage: usage}
		return nil
	})
}
func (s *Store) Compactions(id string) []Compaction {
	d, err := s.get(id)
	if err != nil {
		return nil
	}
	var out []Compaction
	for _, seq := range sortedKeys(d.Compactions) {
		out = append(out, d.Compactions[seq])
	}
	return out
}
func (s *Store) DeleteCompaction(id string, seq int) error {
	return s.update(id, func(d *sessionData) error { delete(d.Compactions, seq); return nil })
}
