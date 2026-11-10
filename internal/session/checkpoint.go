package session

import "fmt"

// RewindSnapshots consumes file checkpoints without changing conversation history.
// apply must undo its filesystem changes if commit fails.
func (s *Store) RewindSnapshots(id string, from int, expected map[int]string, apply func(commit func() error) error) error {
	return s.withLock(func() error {
		d, err := s.read(id)
		if err != nil {
			return err
		}
		for seq, ref := range expected {
			if d.Snapshots[seq] != ref {
				return fmt.Errorf("checkpoint revision conflict")
			}
		}
		for seq, ref := range d.Snapshots {
			if seq >= from && expected[seq] != ref {
				return fmt.Errorf("checkpoint revision conflict")
			}
		}
		return apply(func() error {
			for seq := range expected {
				delete(d.Snapshots, seq)
			}
			return s.write(d)
		})
	})
}

func (s *Store) MessageSequence(id, messageID string) (int, error) {
	d, err := s.get(id)
	if err != nil {
		return 0, err
	}
	for seq, msg := range d.Messages {
		canonical := msg.ID
		if canonical == "" {
			canonical = canonicalAt(d.Meta.ID, seq, msg)
		}
		if canonical == messageID {
			return seq, nil
		}
	}
	return 0, ErrAnchor
}

func (s *Store) ClearSnapshotsFrom(id string, from int) error {
	return s.update(id, func(d *sessionData) error {
		for seq := range d.Snapshots {
			if seq >= from {
				delete(d.Snapshots, seq)
			}
		}
		return nil
	})
}

func (s *Store) CheckpointReferences(id string) (map[int]string, error) {
	d, err := s.get(id)
	if err != nil {
		return nil, err
	}
	return d.Snapshots, nil
}

// TruncateAtSequence removes messages and snapshots at or after seq.
func (s *Store) TruncateAtSequence(id string, seq int) error {
	return s.updateMessages(id, func(d *sessionData) error {
		for current := range d.Messages {
			if current >= seq {
				delete(d.Messages, current)
			}
		}
		for current := range d.Snapshots {
			if current >= seq {
				delete(d.Snapshots, current)
			}
		}
		return nil
	})
}
