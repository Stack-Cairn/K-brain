package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func metaBefore(a, b Meta) bool {
	if a.UpdatedAt.Equal(b.UpdatedAt) {
		return a.ID < b.ID
	}
	return a.UpdatedAt.After(b.UpdatedAt)
}

func (s *Store) SetGoal(id, goal string) error {
	return s.update(id, func(d *sessionData) error {
		d.Meta.Goal = goal
		if goal == "" {
			d.Meta.GoalMemoryOff = false
		}
		return nil
	})
}

// SetGoalMemory stores the goal with whether its text was written while memory was off.
func (s *Store) SetGoalMemory(id, goal string, memoryOff bool) error {
	return s.update(id, func(d *sessionData) error {
		d.Meta.Goal, d.Meta.GoalMemoryOff = goal, memoryOff && goal != ""
		return nil
	})
}
func (s *Store) SetTodos(id, todos string) error {
	return s.update(id, func(d *sessionData) error { d.Todos = todos; return nil })
}
func (s *Store) Todos(id string) string {
	d, err := s.get(id)
	if err != nil {
		return ""
	}
	return d.Todos
}
func (s *Store) SetModel(id, model, provider string) error {
	return s.update(id, func(d *sessionData) error {
		d.Meta.Model, d.Meta.Provider = model, provider
		return nil
	})
}
func (s *Store) SetEffort(id, effort string) error {
	return s.update(id, func(d *sessionData) error { d.Meta.Effort = effort; return nil })
}
func (s *Store) SetUsage(id string, usage ai.UsageSummary) error {
	return s.update(id, func(d *sessionData) error {
		d.Meta.setUsage(usage)
		return nil
	})
}
func (s *Store) SetTitle(id, title string) error {
	return s.update(id, func(d *sessionData) error { d.Meta.Title = title; return nil })
}
func (s *Store) SetTags(id string, tags []string) error {
	return s.update(id, func(d *sessionData) error { d.Meta.Tags = tags; return nil })
}
func (s *Store) SetPinned(id string, pinned bool) error {
	return s.update(id, func(d *sessionData) error { d.Meta.Pinned = pinned; return nil })
}
func (s *Store) SetArchived(id string, archived bool) error {
	return s.update(id, func(d *sessionData) error { d.Meta.Archived = archived; return nil })
}

// SetCWD updates the stored workspace and moves project-scoped sessions to the
// corresponding workspace bucket before writing the new metadata.
func (s *Store) SetCWD(id, cwd string) error {
	if !s.projectScoped {
		return s.update(id, func(d *sessionData) error { d.Meta.CWD = cwd; return nil })
	}
	return s.withLock(func() error {
		oldPath := s.TranscriptPath(id)
		if oldPath == "" {
			return ErrNotFound
		}
		oldDir := filepath.Dir(oldPath)
		if _, err := os.Stat(oldPath); errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		d, err := s.read(id)
		if err != nil {
			return err
		}
		newDir := filepath.Join(s.filesDir, projectDir(cwd), id)
		if oldDir == newDir {
			d.Meta.CWD = cwd
			d.Meta.UpdatedAt = time.Now().UTC()
			return s.write(d)
		}
		if _, err := os.Stat(newDir); err == nil {
			return fmt.Errorf("session destination already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(newDir), 0700); err != nil {
			return err
		}
		if err := os.Rename(oldDir, newDir); err != nil {
			return err
		}
		d.Meta.CWD = cwd
		d.Meta.UpdatedAt = time.Now().UTC()
		if err := s.write(d); err != nil {
			_ = os.Rename(newDir, oldDir)
			return err
		}
		oldProjectDir := filepath.Dir(oldDir)
		if entries, readErr := os.ReadDir(oldProjectDir); readErr == nil && len(entries) == 0 {
			_ = os.Remove(oldProjectDir)
		}
		return nil
	})
}
func (s *Store) SetShared(id, token string, enabled, redactToolContent bool) error {
	return s.update(id, func(d *sessionData) error {
		if enabled && !d.Meta.Shared {
			d.Meta.ShareCreatedAt = time.Now().UTC()
		}
		d.Meta.ShareUpdatedAt = time.Now().UTC()
		if !enabled {
			token = ""
		}
		d.Meta.Shared = enabled
		d.Meta.ShareToken = token
		d.Meta.ShareRedactTool = redactToolContent
		return nil
	})
}
func (s *Store) SharedByToken(token string) (Meta, []ai.Message, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Meta{}, nil, ErrNotFound
	}
	var meta Meta
	var messages []ai.Message
	err := s.withLock(func() error {
		all, err := s.all()
		if err != nil {
			return err
		}
		for _, d := range all {
			if d.Meta.Shared && d.Meta.ShareToken == token {
				meta, messages = d.Meta, d.rawMessages()
				return nil
			}
		}
		return ErrNotFound
	})
	return meta, messages, err
}
func (s *Store) SharedPage(limit int, after *PageCursor) (Page, error) {
	return s.listPage(PageOptions{Limit: limit, After: after, SharedOnly: true})
}
func (s *Store) Delete(id string) error {
	return s.withLock(func() error {
		path := s.TranscriptPath(id)
		if path == "" {
			return ErrNotFound
		}
		dir := filepath.Dir(path)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if err := s.guardExternalRunLocked(id); err != nil {
			return err
		}
		// Keep deletion intent outside the transcript so automatic imports cannot revive it.
		deletedDir := filepath.Join(s.filesDir, ".deleted-history")
		if err := os.MkdirAll(deletedDir, 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(deletedDir, id), nil, 0600); err != nil {
			return err
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		if s.projectScoped {
			parent := filepath.Dir(dir)
			entries, _ := os.ReadDir(parent)
			if len(entries) == 0 {
				_ = os.Remove(parent)
			}
		}
		s.removeSummaryLocked(id)
		s.removeRunLeaseFilesLocked(id)
		s.bumpSyncBeacon()
		return nil
	})
}
func (s *Store) Search(query string, includeArchived bool) (out []Meta, err error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return s.Recent(-1)
	}
	err = s.withLock(func() error {
		all, err := s.all()
		if err != nil {
			return err
		}
		for _, d := range all {
			if d.Meta.Archived && !includeArchived {
				continue
			}
			parts := []string{d.Meta.ID, d.Meta.Title, d.Meta.CWD, d.Meta.Model, d.Meta.Provider, strings.Join(d.Meta.Tags, " ")}
			matched := false
			for _, part := range parts {
				if strings.Contains(strings.ToLower(part), q) {
					matched = true
					break
				}
			}
			if !matched {
				for _, msg := range d.rawMessages() {
					if strings.Contains(strings.ToLower(msg.TextContent()), q) {
						matched = true
						break
					}
				}
			}
			if matched {
				out = append(out, d.Meta)
			}
		}
		return nil
	})
	return
}
func (s *Store) Recent(n int) (out []Meta, err error) {
	err = s.withLock(func() error {
		all, err := s.all()
		if err != nil {
			return err
		}
		for _, d := range all {
			if d.Meta.Archived {
				continue
			}
			if n >= 0 && len(out) >= n {
				break
			}
			if len(d.Messages) > 0 {
				out = append(out, d.Meta)
			}
		}
		return nil
	})
	return
}
func (s *Store) LatestInDir(dir string) (meta Meta, err error) {
	err = s.withLock(func() error {
		all, err := s.all()
		if err != nil {
			return err
		}
		for _, d := range all {
			if d.Meta.CWD == dir && d.Meta.TaskID == "" && len(d.Messages) > 0 {
				meta = d.Meta
				return nil
			}
		}
		return ErrNotFound
	})
	return
}
func (s *Store) UserHistory(limit int) (out []string, err error) {
	err = s.withLock(func() error {
		all, err := s.all()
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, d := range all {
			msgs := d.rawMessages()
			for i := len(msgs) - 1; i >= 0; i-- {
				m := msgs[i]
				text := strings.TrimSpace(m.TextContent())
				if m.Role != "user" || !m.Authored || text == "" || seen[text] {
					continue
				}
				seen[text] = true
				out = append(out, text)
				if limit > 0 && len(out) >= limit {
					return nil
				}
			}
		}
		return nil
	})
	return
}
func (s *Store) Fork(srcID string, uptoSeq int, title string) (id string, err error) {
	err = s.withLock(func() error {
		src, err := s.read(srcID)
		if err != nil {
			return err
		}
		m := src.Meta
		d := newData(Meta{CWD: m.CWD, Model: m.Model, Provider: m.Provider, Goal: m.Goal, GoalMemoryOff: m.GoalMemoryOff, Effort: m.Effort,
			Title: title, ForkedFrom: srcID, ForkSeq: uptoSeq, UpdatedAt: time.Now().UTC()})
		for seq, msg := range src.Messages {
			if seq <= uptoSeq {
				d.Messages[seq] = msg
			}
		}
		for seq, c := range src.Compactions {
			if c.Cutoff <= len(d.Messages) {
				d.Compactions[seq] = c
			}
		}
		for seq, ref := range src.Snapshots {
			if seq <= uptoSeq {
				d.Snapshots[seq] = ref
			}
		}
		id, err = s.create(d)
		return err
	})
	return
}
func (s *Store) ForksOf(id string) (out []Meta, err error) {
	err = s.withLock(func() error {
		all, err := s.all()
		if err != nil {
			return err
		}
		for _, d := range all {
			if d.Meta.ForkedFrom == id {
				out = append(out, d.Meta)
			}
		}
		return nil
	})
	return
}
func forkNumber(title string) (string, int) {
	i := strings.LastIndex(title, " (fork #")
	if i <= 0 || !strings.HasSuffix(title, ")") {
		return title, 0
	}
	n, err := strconv.Atoi(title[i+8 : len(title)-1])
	if err != nil || n <= 0 {
		return title, 0
	}
	return title[:i], n
}
func (s *Store) ForkTitle(base string) (title string, err error) {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "session"
	}
	base, _ = forkNumber(base)
	err = s.withLock(func() error {
		all, err := s.all()
		if err != nil {
			return err
		}
		max := 0
		for _, d := range all {
			b, n := forkNumber(d.Meta.Title)
			if b == base && n > max {
				max = n
			}
		}
		title = fmt.Sprintf("%s (fork #%d)", base, max+1)
		return nil
	})
	return
}
