package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// Called with rt.mu held, before the history mutation loses its original anchors.
func (s *Server) trajectoryHistoryPrefix(rt *runtimeSession, messageID string, edit bool) ([]protocol.Event, error) {
	events, err := rt.allEventsLocked()
	if err != nil {
		return nil, err
	}
	snapshot, err := s.store.HistorySnapshot(rt.id)
	if err != nil {
		return nil, err
	}
	target := -1
	users := 0
	for _, message := range snapshot.Messages {
		if message.Role == "user" {
			if message.ID == messageID {
				target = users
				break
			}
			users++
		}
	}
	if target < 0 {
		return nil, fmt.Errorf("trajectory history anchor not found")
	}
	keep := map[string]bool{}
	identity := map[string]string{}
	userOrder := map[string]int{}
	n := 0
	for _, message := range snapshot.Messages {
		if message.Role == "user" {
			userOrder[message.ID] = n
			n++
		}
	}
	for _, event := range events {
		if event.Type == "trajectory.request.started" {
			p := trajectoryRequestPayload(event)
			if trajectoryString(p, "task_id") == "" {
				if user := trajectoryString(p, "user_message_id"); user != "" {
					identity[event.RunID] = user
				}
			}
		}
	}
	ordinal := -1
	for _, event := range events {
		if event.Type != protocol.EventUserMessage {
			continue
		}
		ordinal++
		position := ordinal
		if user := identity[event.RunID]; user != "" {
			if index, ok := userOrder[user]; ok {
				position = index
			} else {
				continue
			}
		}
		if position < target || (!edit && position == target) {
			keep[event.RunID] = true
		}
	}
	for run, user := range identity {
		if position, ok := userOrder[user]; ok && (position < target || (!edit && position == target)) {
			keep[run] = true
		}
	}
	out := []protocol.Event{}
	for _, event := range events {
		if keep[event.RunID] {
			out = append(out, event)
		}
	}
	return out, nil
}

func atomicTrajectoryWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".trajectory-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *Server) applyTrajectoryHistory(rt *runtimeSession, destination string, events []protocol.Event, edit bool) error {
	copied := append([]protocol.Event(nil), events...)
	if edit {
		// The journal file is about to be replaced: close it and drop its offsets.
		rt.resetJournalLocked()
		runID := newRunID()
		if len(copied) > 0 {
			runID = copied[len(copied)-1].RunID
		}
		rebase, err := protocol.NewEvent(rt.nextSeq+1, rt.id, runID, "trajectory.history.rebased", map[string]any{"reason": "edit"})
		if err != nil {
			return err
		}
		copied = append(copied, rebase)
	}
	if !edit {
		for i := range copied {
			copied[i].ConversationID = destination
			copied[i].Seq = int64(i + 1)
			copied[i].ID = fmt.Sprintf("%s:%d", destination, i+1)
		}
		target := &runtimeSession{id: destination, eventDir: s.eventDir}
		for _, event := range copied {
			if event.Type != "trajectory.request.started" {
				continue
			}
			p := trajectoryRequestPayload(event)
			refs, _ := p["sections"].([]any)
			for _, ref := range refs {
				id, ok := ref.(string)
				if !ok {
					continue
				}
				section, err := s.readTrajectorySection(rt.id, id)
				if err != nil {
					return err
				}
				if _, err = target.storeTrajectorySection(section.Slot, section.Content); err != nil {
					return err
				}
			}
		}
		// Copy only children referenced by the retained runtime events.
		children := buildTrajectorySubagents(copied)
		tasks, err := s.store.LoadTasks(rt.id)
		if err != nil {
			return err
		}
		for _, task := range tasks {
			if children[task.ID] == nil {
				continue
			}
			if err = s.store.SaveTask(destination, task); err != nil {
				return err
			}
			messages, loadErr := s.store.SubagentTranscript(rt.id, task.ID)
			if loadErr != nil {
				return loadErr
			}
			if _, err = s.store.SaveSubagentTranscript(destination, task.ID, messages, task.Model, task.Provider); err != nil {
				return err
			}
		}
	}
	data := []byte{}
	for _, event := range copied {
		line, err := json.Marshal(event)
		if err != nil {
			return err
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := atomicTrajectoryWrite(filepath.Join(s.eventDir, destination+".jsonl"), data); err != nil {
		return err
	}
	runs := map[string]runRecord{}
	keep := map[string]bool{}
	for _, event := range copied {
		keep[event.RunID] = true
	}
	if edit {
		for id, record := range rt.runs {
			if keep[record.RunID] {
				runs[id] = record
			}
		}
	}
	records, err := json.Marshal(runs)
	if err != nil {
		return err
	}
	if err = atomicTrajectoryWrite(filepath.Join(s.eventDir, destination+".runs.json"), records); err != nil {
		return err
	}
	if edit {
		rt.nextSeq = copied[len(copied)-1].Seq
		rt.events = nil
		rt.eventBytes = 0
		rt.eventsTruncated = false
		for _, event := range copied {
			rt.appendEventLocked(event)
		}
		rt.runs = runs
		old := rt.changed
		rt.changed = make(chan struct{})
		close(old)
		if err := s.pruneTrajectorySections(destination, copied); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) pruneTrajectorySections(id string, events []protocol.Event) error {
	refs := map[string]bool{}
	for _, event := range events {
		if event.Type != "trajectory.request.started" {
			continue
		}
		p := trajectoryRequestPayload(event)
		sections, _ := p["sections"].([]any)
		for _, section := range sections {
			if id, ok := section.(string); ok {
				refs[id+".json"] = true
			}
		}
	}
	dir := filepath.Join(s.eventDir, "sections", id)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && !refs[entry.Name()] {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
