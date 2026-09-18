package tui

import tea "github.com/charmbracelet/bubbletea"

func syncSend(p *tea.Program, m tea.Msg) {
	p.Send(m)
}

func detachedSend(p *tea.Program, m tea.Msg) {
	go p.Send(m)
}

func closureSend(p *tea.Program, m tea.Msg) {
	go func() { p.Send(m) }()
}

func whitelistedSend(p *tea.Program, m tea.Msg) {
	p.Send(m)
}
