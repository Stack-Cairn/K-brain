package session

import "fmt"

// ArchiveFilter selects sessions by their archived flag. The zero value keeps
// the historical behavior: archived sessions are hidden.
type ArchiveFilter uint8

const (
	// ArchiveExclude lists only sessions that are not archived (the default).
	ArchiveExclude ArchiveFilter = iota
	// ArchiveInclude lists archived and unarchived sessions.
	ArchiveInclude
	// ArchiveOnly lists only archived sessions.
	ArchiveOnly
)

// ParseArchiveFilter reads the HTTP `archived` query value; empty means exclude.
func ParseArchiveFilter(raw string) (ArchiveFilter, error) {
	switch raw {
	case "", "exclude":
		return ArchiveExclude, nil
	case "include":
		return ArchiveInclude, nil
	case "only":
		return ArchiveOnly, nil
	}
	return ArchiveExclude, fmt.Errorf("invalid archived: want exclude, include or only")
}

// Matches reports whether a session with this archived flag passes the filter.
func (f ArchiveFilter) Matches(archived bool) bool {
	switch f {
	case ArchiveInclude:
		return true
	case ArchiveOnly:
		return archived
	default:
		return !archived
	}
}

// Listed reports whether a session summary belongs to a list under this filter:
// a top-level (non-subagent) session with at least one message whose archived
// flag passes the filter.
func (f ArchiveFilter) Listed(meta Meta, messageCount int) bool {
	return meta.TaskID == "" && messageCount > 0 && f.Matches(meta.Archived)
}
