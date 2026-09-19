package session

import (
	"fmt"
	"slices"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func (h *History) Sequence(index int) (int, bool) {
	if index < 0 || index >= len(h.refs) || h.refs[index] < 0 {
		return 0, false
	}
	return h.refs[index], true
}

func (h *History) Boundary(cut int) (int, error) {
	if cut < h.prefix || cut > len(h.refs) {
		return 0, fmt.Errorf("invalid history boundary %d for %d messages", cut, len(h.refs))
	}
	if cut == h.prefix {
		return h.start, nil
	}
	for _, seq := range h.refs[cut:] {
		if seq >= 0 {
			return seq, nil
		}
	}
	return h.next, nil
}

func (s *Store) TruncateHistory(id string, h *History, cut int) error {
	boundary, err := h.Boundary(cut)
	if err != nil {
		return err
	}
	if len(h.pending) > 0 || len(h.compactions) > 0 {
		return fmt.Errorf("save history before rewinding")
	}
	err = s.update(id, func(d *sessionData) error {
		for seq := range d.Messages {
			if seq >= boundary {
				delete(d.Messages, seq)
			}
		}
		for seq := range d.Snapshots {
			if seq >= boundary {
				delete(d.Snapshots, seq)
			}
		}
		for seq, c := range d.Compactions {
			if cut == h.prefix || c.Cutoff > len(d.Messages) {
				delete(d.Compactions, seq)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	h.refs = slices.Clone(h.refs[:cut])
	h.raw = slices.DeleteFunc(h.raw, func(seq int) bool { return seq >= boundary })
	h.next = boundary
	return nil
}

func (s *Store) UndoCompaction(id string, initial []ai.Message) (*History, []ai.Message, error) {
	var h *History
	var msgs []ai.Message
	err := s.update(id, func(d *sessionData) error {
		keys := sortedKeys(d.Compactions)
		if len(keys) == 0 {
			return fmt.Errorf("no compaction to undo")
		}
		delete(d.Compactions, keys[len(keys)-1])
		h, msgs = d.history(initial)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return h, msgs, nil
}

func (s *Store) SnapshotReferenced(ref string) (bool, error) {
	used := false
	err := s.withLock(func() error {
		ids, err := s.ids()
		if err != nil {
			return err
		}
		for _, id := range ids {
			d, err := s.read(id)
			if err != nil {
				return err
			}
			for _, stored := range d.Snapshots {
				if stored == ref {
					used = true
					return nil
				}
			}
		}
		return nil
	})
	return used, err
}
