package tui

import (
	"fmt"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/routing"
)

func (m *model) applyTaskModel() {
	snap := m.cfg.Snapshot()
	m.agent.ResolveModel = func(model, provider string) (agent.SubModel, error) {
		return routing.SubModelFor(snap, model, provider)
	}
	o, err := routing.TaskDefaultFor(snap)
	if err != nil {
		m.agent.TaskDefault = agent.SubModel{}
		m.append(errStyle.Render("task model: " + err.Error() + " — subagents use the current model"))
		return
	}
	m.agent.TaskDefault = o
}

func (m *model) taskCommand(rest string) {
	model, prov := "", ""
	if strings.HasPrefix(rest, "-m ") {
		rest = strings.TrimSpace(rest[3:])
		spec, tail, found := strings.Cut(rest, " ")
		if !found {
			rest = ""
		} else {
			rest = strings.TrimSpace(tail)
			if at, prov2, ok := strings.Cut(spec, "@"); ok {
				model, prov = at, prov2
			} else {
				model = spec
			}
		}
	}
	if rest == "" {
		m.append(dimStyle.Render("usage: /subagent [-m model[@provider]] <prompt> — spawn a background subagent (ctrl+t to watch, /subagents to list)"))
		return
	}
	var o agent.SubModel
	if model != "" {
		if m.agent.ResolveModel == nil {
			m.append(errStyle.Render("task model: overrides unavailable"))
			return
		}
		var err error
		if o, err = m.agent.ResolveModel(model, prov); err != nil {
			m.append(errStyle.Render("task model: " + err.Error()))
			return
		}
	}
	t := m.agent.StartBackground(taskDesc(rest), rest, o)
	m.append(dimStyle.Render(fmt.Sprintf("⚙ %s started — %s  (ctrl+t to watch · /subagents %s to open)", t.ID, taskDesc(rest), t.ID)))
}

func (m *model) subagentModelCommand(args []string) {
	if args[0] == "off" {
		m.cfg.TaskModel, m.cfg.TaskProvider = "", ""
		m.applyTaskModel()
		if err := m.cfg.Save(); err != nil {
			m.append(errStyle.Render("config save failed: " + err.Error()))
		}
		m.append(dimStyle.Render("◎ subagent model: current model"))
		return
	}
	model, prov := args[0], ""
	if at, p, ok := strings.Cut(model, "@"); ok {
		model, prov = at, p
	}
	if _, ok := m.cfg.Models[model]; !ok && !catalogAdvertises(m.cfg, model) {
		resolved, ok2, cands := resolveModelFuzzy(m.cfg, model)
		if !ok2 {
			if len(cands) > 0 {
				m.append(errStyle.Render("ambiguous model " + model + " — could be " + strings.Join(cands, ", ")))
			} else {
				m.append(errStyle.Render("unknown model " + model))
			}
			return
		}
		model = resolved
	}
	if len(args) > 1 {
		prov = args[1]
	}
	if _, err := routing.SubModelFor(m.cfg, model, prov); err != nil {
		m.append(errStyle.Render("task model: " + err.Error()))
		return
	}
	m.cfg.TaskModel, m.cfg.TaskProvider = model, prov
	m.applyTaskModel()
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
	}
	note := "◎ subagent model: " + model
	if p := routing.ResolvedProvider(m.cfg, model, prov); p != "" {
		note += " @ " + p
	}
	m.append(dimStyle.Render(note))
}

func taskDesc(prompt string) string {
	f := strings.Fields(prompt)
	if len(f) > 8 {
		return strings.Join(f[:8], " ") + "…"
	}
	return strings.Join(f, " ")
}
