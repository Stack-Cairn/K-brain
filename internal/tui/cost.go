package tui

import (
	"math"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func (m *model) sessionCost() (float64, bool) {
	if m.cfg.SubscriptionBilled(m.provName) {
		return 0, false
	}
	usage := m.agent.UsageSummary()
	total := 0.0
	for label, u := range usage.Models {
		model, prov, _ := strings.Cut(label, " @ ")
		if prov == "" {
			prov = m.provName
		}
		c, ok := m.usageCost(model, prov, u)
		if !ok {
			return 0, false
		}
		total += c
	}
	for label, u := range usage.Subagents {
		model, prov, _ := strings.Cut(label, " @ ")
		c, ok := m.usageCost(model, prov, u)
		if !ok {
			return 0, false
		}
		total += c
	}
	if math.IsInf(total, 0) {
		return 0, false
	}
	return total, true
}

func (m *model) usageCost(model, prov string, u ai.Usage) (float64, bool) {
	return config.UsageCost(m.catalogs, model, prov, u)
}

func (m *model) turnCost(cut int) (float64, bool) {
	total := 0.0
	found := false
	for i := cut + 1; i < len(m.agent.Messages)+len(m.future); i++ {
		msg := m.messageAt(i)
		if msg.Role == "user" {
			break
		}
		if msg.Role != "assistant" || msg.Usage == nil {
			continue
		}
		modelID, prov, _ := strings.Cut(msg.Model, " @ ")
		cost, ok := m.usageCost(modelID, prov, *msg.Usage)
		if !ok {
			return 0, false
		}
		total += cost
		found = true
	}
	if math.IsInf(total, 0) {
		return 0, false
	}
	return total, found
}
