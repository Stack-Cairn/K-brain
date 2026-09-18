package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/memory"
)

func (m *model) memoryCommand(args []string) {
	scopes := []memory.Scope{memory.Installation(), memory.Session(m.sessionID)}

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

	var b strings.Builder
	b.WriteString(dimStyle.Render("memory — injected into every turn · /memory <n> [session] marks done · edit the files directly:"))
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
