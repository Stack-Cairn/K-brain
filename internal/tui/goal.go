package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/goalloop"
)

const goalMetToken = goalloop.MetToken

func (m *model) goalMaxRounds() int {
	wd, _ := os.Getwd()
	global := 0
	if m.cfg != nil {
		global = m.cfg.GoalMaxRounds
	}
	return goalloop.MaxRounds(wd, global)
}

func goalContinuePrompt(goal string) string { return goalloop.ContinuePrompt(goal) }

func goalMet(final string) bool { return goalloop.Met(final) }

func goalNeedsInput(final string) bool { return goalloop.NeedsInput(final) }

func (m *model) goalRoundsCommand(args []string) {
	global := false
	var num string
	for _, a := range args {
		if a == "--global" || a == "-g" {
			global = true
		} else if num == "" {
			num = a
		} else {
			m.append(errStyle.Render("usage: /goal rounds [n|default] [--global]"))
			return
		}
	}
	wd, _ := os.Getwd()
	proj := config.ProjectGoalMaxRounds(wd)
	cfgN := 0
	if m.cfg != nil {
		cfgN = m.cfg.GoalMaxRounds
	}

	switch num {
	case "":
		src := fmt.Sprintf("built-in default (%d)", config.DefaultGoalMaxRounds)
		if proj > 0 {
			src = "project override"
		} else if cfgN > 0 {
			src = "global config"
		}
		m.append(dimStyle.Render(fmt.Sprintf("◎ goal rounds: %d (%s) — /goal rounds <n>|default [--global]", m.goalMaxRounds(), src)))
		return
	case "default":

	default:
		n := 0
		if _, err := fmt.Sscan(num, &n); err != nil || n <= 0 {
			m.append(errStyle.Render("rounds must be a positive number (or \"default\")"))
			return
		}
		if global {
			m.cfg.GoalMaxRounds = n
			if err := m.cfg.Save(); err != nil {
				m.append(errStyle.Render("couldn't save config: " + err.Error()))
				return
			}
			m.append(dimStyle.Render(fmt.Sprintf("◎ global goal rounds: %d%s", n, overriddenNote(proj))))
			return
		}
		if err := config.SetProjectGoalMaxRounds(wd, n); err != nil {
			m.append(errStyle.Render("couldn't save project override: " + err.Error()))
			return
		}
		m.append(dimStyle.Render(fmt.Sprintf("◎ goal rounds for this project: %d", n)))
		return
	}

	if global {
		m.cfg.GoalMaxRounds = 0
		if err := m.cfg.Save(); err != nil {
			m.append(errStyle.Render("couldn't save config: " + err.Error()))
			return
		}
		m.append(dimStyle.Render(fmt.Sprintf("◎ global goal rounds reset to %d%s", config.DefaultGoalMaxRounds, overriddenNote(proj))))
		return
	}
	if err := config.SetProjectGoalMaxRounds(wd, 0); err != nil {
		m.append(errStyle.Render("couldn't save project override: " + err.Error()))
		return
	}
	m.append(dimStyle.Render(fmt.Sprintf("◎ project goal rounds cleared — using %d", m.goalMaxRounds())))
}

func overriddenNote(proj int) string {
	if proj > 0 {
		return fmt.Sprintf(" (this project overrides it with %d)", proj)
	}
	return ""
}

// goalCheckRow is the transcript's one quiet row for the prompt the goal loop
// sends after an unfinished round, in place of the whole prompt as a user
// message; the model still gets every word of it.
func (m *model) goalCheckRow(text string, authored bool) (string, bool) {
	goal, ok := goalloop.ContinueGoal(text)
	if authored || !ok {
		return "", false
	}
	row := "◎ " + m.tr("goal check")
	if m.goal != "" && m.goalRounds > 0 {
		row += " · " + fmt.Sprintf(m.tr("round %d/%d"), m.goalRounds+1, m.goalMaxRounds())
	}
	return dimStyle.Render(chromeGlyphs(row + " · " + ansi.Truncate(strings.TrimSpace(goal), 60, "…"))), true
}
