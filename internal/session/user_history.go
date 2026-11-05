package session

import (
	"strings"
	"time"
)

// UserHistoryEntry is one prompt the user wrote, with the directory its
// session ran in and when that session was last updated.
type UserHistoryEntry struct {
	Text string
	CWD  string
	At   time.Time
}

// UserHistoryEntries is UserHistory with each prompt's working directory, so
// a caller can scope the history to one directory. Entries come newest
// session first and newest message first within a session; a prompt repeated
// in the same directory is listed once, at its newest position. A limit of 0
// or less means no limit.
func (s *Store) UserHistoryEntries(limit int) (out []UserHistoryEntry, err error) {
	err = s.withLock(func() error {
		all, err := s.all()
		if err != nil {
			return err
		}
		seen := map[[2]string]bool{}
		for _, d := range all {
			msgs := d.rawMessages()
			for i := len(msgs) - 1; i >= 0; i-- {
				m := msgs[i]
				text := strings.TrimSpace(m.TextContent())
				key := [2]string{d.Meta.CWD, text}
				if m.Role != "user" || !m.Authored || text == "" || seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, UserHistoryEntry{Text: text, CWD: d.Meta.CWD, At: d.Meta.UpdatedAt})
				if limit > 0 && len(out) >= limit {
					return nil
				}
			}
		}
		return nil
	})
	return
}
