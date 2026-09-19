package tui

import (
	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/routing"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

func (m *model) selectModel(name, provider string) error {
	if m.busy || m.agent.TurnRunning() {
		return agent.ErrBusy
	}
	route, err := routing.ResolveRoute(m.cfg, name, provider, true)
	if err != nil {
		return err
	}
	if err := m.agent.SetModel(route.AgentModel()); err != nil {
		return err
	}
	m.modelName, m.provName = route.ModelName, route.ProviderName
	m.applyTaskModel()
	bashrun.SetMarkers(m.sessionID, m.agent.Model)
	m.lastResp = ai.Usage{}
	return nil
}
