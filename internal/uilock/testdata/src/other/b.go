package other

import tea "github.com/charmbracelet/bubbletea"

func notTUI(p *tea.Program, m tea.Msg) {
	p.Send(m)
}
