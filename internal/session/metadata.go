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

func (s *Store) SetGoal(id, goal string) error {
	return s.update(id, func(d *sessionData) error { d.Meta.Goal = goal; return nil })
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
func (s *Store) SetEffort(id, effort string) error {
	return s.update(id, func(d *sessionData) error { d.Meta.Effort = effort; return nil })
}
func (s *Store) SetUsage(id string, in, cached, out int, sub map[string]ai.Usage) error {
	return s.update(id, func(d *sessionData) error {
		d.Meta.UsageIn, d.Meta.UsageCached, d.Meta.UsageOut, d.Meta.SubUsage = in, cached, out, sub
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
		d := newData(Meta{CWD: m.CWD, Model: m.Model, Provider: m.Provider, Goal: m.Goal, Effort: m.Effort,
			Title: title, ForkedFrom: srcID, ForkSeq: uptoSeq, UpdatedAt: time.Now().UTC()})
		for seq, msg := range src.Messages {
			if uptoSeq > 0 && seq <= uptoSeq {
				d.Messages[seq] = msg
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
