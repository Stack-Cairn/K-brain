package tui

import (
	"math"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func (m *model) sessionCost() (float64, bool) {
	if m.cfg != nil && m.cfg.Providers[m.provName].API == "openai-codex-responses" {
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
	if u.PromptTokens == 0 && u.CompletionTokens == 0 {
		cost, valid := ai.CalculateCost(u, ai.TokenRates{})
		return cost.Total, valid
	}
	if cat, ok := m.catalogs[prov]; ok {
		if rates, ok := cat.Pricing(model); ok {
			cost, valid := ai.CalculateCost(u, rates)
			return cost.Total, valid
		}
		return 0, false
	}
	if prov != "" {
		return 0, false
	}
	var cost float64
	found := false
	for _, cat := range m.catalogs {
		if cat.Find(model) != nil {
			if found {
				return 0, false
			}
			rates, ok := cat.Pricing(model)
			if !ok {
				return 0, false
			}
			value, valid := ai.CalculateCost(u, rates)
			if !valid {
				return 0, false
			}
			cost, found = value.Total, true
		}
	}
	return cost, found
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
