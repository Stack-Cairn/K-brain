package session

import (
	"context"
	"fmt"
	"sort"
	"time"
)

type PageCursor struct {
	UpdatedAt time.Time `json:"updatedAt"`
	ID        string    `json:"id"`
}

func (c PageCursor) Valid() bool {
	return !c.UpdatedAt.IsZero() && validID(c.ID)
}

type PageOptions struct {
	CWD             *string
	After           *PageCursor
	Limit           int
	Offset          int
	IncludeEmpty    bool
	Shared          *bool
	SharedOnly      bool
	IncludeArchived bool
	// Archive selects by archived flag; IncludeArchived (older callers) widens
	// the default ArchiveExclude to ArchiveInclude.
	Archive ArchiveFilter
	Pinned  *bool
}

type Page struct {
	Sessions []Meta
	// MessageCounts[i] is the stored message count of Sessions[i], read with the
	// page so callers never reload a transcript just to count its messages.
	MessageCounts []int
	Next          *PageCursor
	Total         int
	Epoch         string
	Revision      uint64
}

func (s *Store) ListPage(ctx context.Context, options PageOptions) (Page, error) {
	return s.listPageContext(ctx, options)
}

func (s *Store) listPage(options PageOptions) (Page, error) {
	return s.listPageContext(context.Background(), options)
}

func (s *Store) listPageContext(ctx context.Context, options PageOptions) (Page, error) {
	if options.Limit < 1 || options.Limit > 1000 {
		return Page{}, fmt.Errorf("session page size must be between 1 and 1000")
	}
	if options.Offset < 0 {
		return Page{}, fmt.Errorf("invalid session page offset")
	}
	if options.After != nil && !options.After.Valid() {
		return Page{}, fmt.Errorf("invalid session page cursor")
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	archive := options.Archive
	if options.IncludeArchived && archive == ArchiveExclude {
		archive = ArchiveInclude
	}
	type entry struct {
		meta  Meta
		count int
	}
	entries := make([]entry, 0)
	page := Page{}
	total := 0
	err := s.withLock(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.syncSummariesLocked(ctx); err != nil {
			return err
		}
		for _, cached := range s.summaries.entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			meta := cached.meta
			if meta.TaskID != "" || (!options.IncludeEmpty && cached.count == 0) || !archive.Matches(meta.Archived) {
				continue
			}
			if options.Pinned != nil && meta.Pinned != *options.Pinned {
				continue
			}
			if options.SharedOnly && !meta.Shared {
				continue
			}
			if options.Shared != nil && meta.Shared != *options.Shared {
				continue
			}
			if options.CWD != nil && meta.CWD != *options.CWD {
				continue
			}
			total++
			if after := options.After; after != nil {
				if meta.UpdatedAt.After(after.UpdatedAt) || meta.UpdatedAt.Equal(after.UpdatedAt) && meta.ID <= after.ID {
					continue
				}
			}
			entries = append(entries, entry{meta: cloneMeta(meta), count: cached.count})
		}
		page.Epoch = s.summaries.epoch
		page.Revision = s.summaries.revision
		return ctx.Err()
	})
	if err != nil {
		return Page{}, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return metaBefore(entries[i].meta, entries[j].meta)
	})
	entries = entries[min(options.Offset, len(entries)):]
	hasMore := len(entries) > options.Limit
	entries = entries[:min(options.Limit, len(entries))]
	page.Sessions = make([]Meta, len(entries))
	page.MessageCounts = make([]int, len(entries))
	page.Total = total
	for i, e := range entries {
		page.Sessions[i], page.MessageCounts[i] = e.meta, e.count
	}
	if hasMore {
		last := page.Sessions[len(page.Sessions)-1]
		page.Next = &PageCursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	return page, nil
}
