package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/memory"
	tea "github.com/charmbracelet/bubbletea"
)

func (m *model) memoryCommand(args []string) {
	scopes := []memory.Scope{memory.Installation(), memory.Session(m.sessionID)}

	if len(args) == 1 && (args[0] == "list" || args[0] == "text") {
		m.memoryListing(scopes)
		return
	}
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		if err != nil {
			m.append(errStyle.Render("/memory: entry number expected, got " + args[0]))
			return
		}
		which := "installation"
		if len(args) > 1 {
			which = args[1]
		}
		var s memory.Scope
		switch which {
		case "installation", "install", "global":
			s = scopes[0]
		case "session":
			s = scopes[1]
		default:
			m.append(errStyle.Render("/memory: scope must be installation or session, got " + which))
			return
		}
		if s.Path == "" {
			m.append(dimStyle.Render("(no " + which + " memory scope — start a session first)"))
			return
		}
		if err := s.Forget(n); err != nil {
			m.append(errStyle.Render("/memory: " + err.Error()))
			return
		}
		m.append(dimStyle.Render(fmt.Sprintf("✓ %s memory %d marked done", which, n)))
		return
	}

	if m.height > 0 && (len(args) == 0) {
		m.openMemoryView(scopes)
		return
	}
	m.memoryListing(scopes)
}

// memoryListing is the plain-text /memory listing.
func (m *model) memoryListing(scopes []memory.Scope) {
	var b strings.Builder
	if m.agent != nil && !m.agent.MemoryEnabled() {
		b.WriteString(dimStyle.Render("memory is off — not injected; turn it back on in LiveAgent's memory settings · /memory <n> [session] marks done · edit the files directly:"))
	} else {
		b.WriteString(dimStyle.Render("memory — injected into every turn · /memory <n> [session] marks done · edit the files directly:"))
	}
	found := false
	for _, s := range scopes {
		entries := s.Entries()
		if s.Path == "" || len(entries) == 0 {
			continue
		}
		found = true
		fmt.Fprintf(&b, "\n%s (%s)", s.Name, s.Path)
		for _, e := range entries {
			if e.Done {
				fmt.Fprintf(&b, "\n  %s", dimStyle.Render(fmt.Sprintf("%d. ~~%s~~", e.N, e.Text)))
			} else {
				fmt.Fprintf(&b, "\n  %d. %s", e.N, e.Text)
			}
		}
	}
	if !found {
		b.WriteString("\n  (empty — the model saves facts with remember, or write a line like \"- [ ] prefers pnpm\" yourself)")
	}
	m.append(b.String())
}

// openMemoryView lists both memory scopes in a modal with a preview of the
// selected entry; Enter marks an open entry done, as /memory <n> does.
func (m *model) openMemoryView(scopes []memory.Scope) {
	s := &surfaceModal{
		kind:  "memory",
		title: "Memory",
		tabs: []surfaceTab{{
			name:  "Memory",
			rows:  func(m *model) []surfaceRow { return memoryRows(scopes) },
			empty: "empty — the model saves facts with remember, or write a line like \"- [ ] prefers pnpm\" yourself",
			hints: "↑/↓ select · Enter mark done · Esc close",
		}},
	}
	if m.agent != nil && !m.agent.MemoryEnabled() {
		s.note = "memory is off — entries are not injected into turns"
	}
	m.openSurface(s)
}

func memoryRows(scopes []memory.Scope) []surfaceRow {
	var rows []surfaceRow
	for _, sc := range scopes {
		if sc.Path == "" {
			continue
		}
		for _, e := range sc.Entries() {
			sc, e := sc, e
			value := ""
			if e.Done {
				value = "done"
			}
			rows = append(rows, surfaceRow{
				section: sc.Name,
				key:     strconv.Itoa(e.N) + ".",
				desc:    e.Text,
				value:   value,
				muted:   e.Done,
				preview: func(m *model) []string {
					w, _ := m.surfaceSize()
					var out []string
					for _, l := range wrapPreview(e.Text, w) {
						out = append(out, textSecondaryStyle.Render(l))
					}
					state := "open — Enter marks it done"
					if e.Done {
						state = "done"
					}
					out = append(out, textHintStyle.Render(m.tr(state)))
					out = append(out, textMutedStyle.Render(sc.Name+" · "+shortSkillsDir(sc.Path)))
					return out
				},
				enter: func(m *model) tea.Cmd {
					if e.Done {
						return nil
					}
					if err := sc.Forget(e.N); err != nil {
						m.app.modal.note = "/memory: " + err.Error()
						return nil
					}
					m.app.modal.note = fmt.Sprintf("✓ %s memory %d marked done", sc.Name, e.N)
					return nil
				},
			})
		}
	}
	return rows
}
