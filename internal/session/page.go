package session

import (
	"context"
	"fmt"
	"slices"
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
	CWD   *string
	After *PageCursor
	Limit int
}

type Page struct {
	Sessions []Meta
	Next     *PageCursor
}

func (s *Store) ListPage(ctx context.Context, options PageOptions) (Page, error) {
	if options.Limit < 1 || options.Limit > 1000 {
		return Page{}, fmt.Errorf("session page size must be between 1 and 1000")
	}
	if options.After != nil && !options.After.Valid() {
		return Page{}, fmt.Errorf("invalid session page cursor")
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	metas := make([]Meta, 0)
	err := s.withLock(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		ids, err := s.ids()
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return err
			}
			d, err := s.read(id)
			if err != nil {
				return err
			}
			meta := d.Meta
			if meta.Archived || meta.TaskID != "" || len(d.Messages) == 0 {
				continue
			}
			if options.CWD != nil && meta.CWD != *options.CWD {
				continue
			}
			if after := options.After; after != nil {
				if meta.UpdatedAt.After(after.UpdatedAt) || meta.UpdatedAt.Equal(after.UpdatedAt) && meta.ID <= after.ID {
					continue
				}
			}
			metas = append(metas, meta)
		}
		return ctx.Err()
	})
	if err != nil {
		return Page{}, err
	}
	sort.Slice(metas, func(i, j int) bool {
		return metaBefore(metas[i], metas[j])
	})
	page := Page{Sessions: metas}
	if len(metas) > options.Limit {
		page.Sessions = slices.Clone(metas[:options.Limit])
		last := page.Sessions[len(page.Sessions)-1]
		page.Next = &PageCursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	return page, nil
}
