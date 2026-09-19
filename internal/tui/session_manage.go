package tui

import (
	"fmt"
	"strings"
)

func (m *model) sessionManageCommand(fields []string) {
	if m.store == nil {
		m.append(errStyle.Render("session store unavailable"))
		return
	}
	if m.busy {
		m.append(dimStyle.Render("(busy — session management after this turn)"))
		return
	}
	if len(fields) == 0 {
		if m.sessionID == "" {
			m.append(errStyle.Render("usage: /archive [id]"))
			return
		}
		fields = []string{m.sessionID}
	}
	if len(fields) != 1 {
		m.append(errStyle.Render("usage: /archive [id]"))
		return
	}
	id := fields[0]
	if err := m.store.SetArchived(id, true); err != nil {
		m.append(errStyle.Render("/archive: " + err.Error()))
		return
	}
	m.append(dimStyle.Render("archived session " + id))
}

func (m *model) searchSessions(query string) {
	if m.store == nil || strings.TrimSpace(query) == "" {
		m.append(errStyle.Render("usage: /search <query>"))
		return
	}
	metas, err := m.store.Search(query, false)
	if err != nil {
		m.append(errStyle.Render("/search: " + err.Error()))
		return
	}
	if len(metas) == 0 {
		m.append(dimStyle.Render("no sessions found"))
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "sessions matching %q\n", query)
	for _, meta := range metas {
		title := meta.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(&b, "%s  %s  %s\n", meta.ID, title, meta.CWD)
	}
	m.append(dimStyle.Render(strings.TrimRight(b.String(), "\n")))
}

func (m *model) tagSession(fields []string) {
	if m.store == nil || m.sessionID == "" {
		m.append(errStyle.Render("no active session"))
		return
	}
	meta, _, err := m.store.Load(m.sessionID)
	if err != nil {
		m.append(errStyle.Render("/tag: " + err.Error()))
		return
	}
	if len(fields) == 0 || fields[0] == "list" {
		m.append(dimStyle.Render("tags: " + strings.Join(meta.Tags, ", ")))
		return
	}
	if len(fields) != 2 || (fields[0] != "add" && fields[0] != "remove") || strings.TrimSpace(fields[1]) == "" {
		m.append(errStyle.Render("usage: /tag [add|remove] <tag>"))
		return
	}
	tag := strings.TrimSpace(fields[1])
	tags := append([]string(nil), meta.Tags...)
	idx := -1
	for i, value := range tags {
		if value == tag {
			idx = i
			break
		}
	}
	if fields[0] == "add" && idx < 0 {
		tags = append(tags, tag)
	}
	if fields[0] == "remove" && idx >= 0 {
		tags = append(tags[:idx], tags[idx+1:]...)
	}
	if err := m.store.SetTags(m.sessionID, tags); err != nil {
		m.append(errStyle.Render("/tag: " + err.Error()))
		return
	}
	m.append(dimStyle.Render("tags: " + strings.Join(tags, ", ")))
}
