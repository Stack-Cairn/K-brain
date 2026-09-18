package bubbletea

type Msg interface{}

type Program struct{}

func (p *Program) Send(m Msg) {}
