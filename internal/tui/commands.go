package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	tea "github.com/charmbracelet/bubbletea"
)

func busyCmd(text string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "/permissions", "/language", "/editor", "/copy", "/diff", "/prompts", "/help", "/theme", "/mouse", "/effort", "/subagents", "/tasks", "/subagent", "/cd", "/pwd", "/report", "/export", "/import", "/fork", "/forks", "/context", "/context-doctor", "/doctor", "/info", "/mcps", "/mcp", "/plugins", "/new", "/new-context", "/new_context", "/plan", "/session-info", "/status", "/title", "/undo", "/rewind", "/view-plan", "/privacy", "/ancient":
		return true
	case "/auth":
		return true
	case "/goal":
		return len(fields) == 1 || fields[1] == "clear" || fields[1] == "rounds"
	case "/settings", "/extensions", "/memory":
		return true
	}
	return false
}

func (m *model) command(text string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return m, nil
	}
	switch fields[0] {
	case "/quit", "/exit", "/q":
		return m, tea.Quit
	case "/new", "/clear":
		if m.busy {
			m.append(dimStyle.Render("(busy — /clear after this turn)"))
			return m, nil
		}
		m.endSession("clear")
		m.agent.Messages = m.agent.Messages[:1]
		m.agent.ResetUsage()
		m.lastResp = ai.Usage{}
		m.resetTranscript()
		m.future = nil
		m.setGoal("")
		m.sessionID = ""
		m.resetHistory()
		m.sessTitle = ""
		m.titled = false

		m.images = nil
		m.imageSeq = 0
		m.agent.Tasks().SetSessionID("")
		m.agent.SetSessionID("")
		m.saved = 1
	case "/permissions":
		m.permissionCommand(fields[1:])
	case "/privacy":
		m.privacyCommand(fields[1:])
	case "/ancient":
		m.ancientCommand(fields[1:])
	case "/memory":
		m.memoryCommand(fields[1:])
	case "/schedule":
		m.scheduleCommand(fields[1:])
	case "/editor":
		if len(fields) != 1 {
			m.append(errStyle.Render("usage: /editor (Ctrl+G edits the current draft)"))
			return m, nil
		}
		return m, m.openPromptEditor()
	case "/brain":
		return m, m.openBrain()
	case "/system":
		return m, m.openSystem()
	case "/compact":
		if len(fields) > 1 {
			switch fields[1] {
			case "retry":
				m.compactRetry()
				return m, nil
			case "log":
				m.compactLog()
				return m, nil
			case "fresh", "new-context":
				return m.command("/new-context")
			}
			m.compactCommand(fields[1:])
			return m, nil
		}
		if m.busy {
			m.append(dimStyle.Render("(busy — /compact will land after this turn)"))
			return m, nil
		}
		m.prepareHistory()
		m.busy = true
		before := m.agent.MessagesSnapshot()
		took := len(before)
		m.append(dimStyle.Render(fmt.Sprintf("◎ compacting %d msgs (est. %s) with %s…",
			took, fmtTok(agent.EstimateTokens(before)), m.compactModelLabel())))
		p := m.prog
		ag := m.agent
		ctx, cancel := context.WithCancel(context.Background())
		ctx = sandbox.WithPolicy(ctx, m.sandboxPolicy)
		m.cancel = cancel
		go func() {
			var summary string
			var cutoff int
			var info agent.CompactInfo
			err := ag.ManualCompact(ctx, agent.Events{
				OnCompacted: func(s string, c int, ci agent.CompactInfo) { summary, cutoff, info = s, c, ci },
			})
			if p != nil {
				after := ag.MessagesSnapshot()
				p.Send(compactMsg{took: took - len(after), kept: len(after), summary: summary, cutoff: cutoff, info: info, err: err,
					before: before, after: after})
				p.Send(turnDoneMsg{})
			}
		}()
		return m, m.spin.Tick
	case "/new-context", "/new_context":
		if len(fields) != 1 {
			m.append(errStyle.Render("usage: /new-context"))
			return m, nil
		}
		if m.busy {
			m.append(dimStyle.Render("(busy — /new-context will land after this turn)"))
			return m, nil
		}
		m.prepareHistory()
		m.busy = true
		p := m.prog
		ag := m.agent
		ctx, cancel := context.WithCancel(context.Background())
		ctx = sandbox.WithPolicy(ctx, m.sandboxPolicy)
		m.cancel = cancel
		go func() {
			before := ag.MessagesSnapshot()
			var cutoff int
			err := ag.ManualNewContext(ctx, agent.Events{
				OnNewContextStart: func(took, est int) {
					if p != nil {
						p.Send(contextStartMsg{took: took, est: est})
					}
				},
				OnNewContext: func(c int) { cutoff = c },
			})
			if p != nil {
				p.Send(compactMsg{took: len(before), kept: len(ag.MessagesSnapshot()), cutoff: cutoff, before: before, after: ag.MessagesSnapshot(), fresh: true, err: err})
				p.Send(turnDoneMsg{})
			}
		}()
		return m, m.spin.Tick
	case "/plugins":
		return m.pluginCommand(fields)
	case "/mcp", "/mcps":
		return m.mcpCommand(fields)
	case "/lsp":
		return m.lspCommand(fields)
	case "/cd":
		m.cdCommand(strings.TrimSpace(strings.TrimPrefix(text, "/cd")))
		return m, fetchGitStatus()
	case "/pwd":
		m.append(dimStyle.Render(cwd()))
		return m, nil
	case "/subagent":
		m.taskCommand(strings.TrimSpace(strings.TrimPrefix(text, "/subagent")))
		return m, nil
	case "/subagents", "/tasks":
		if len(fields) > 1 {
			m.openTask(fields[1])
			return m, nil
		}

		if len(m.dockTasks()) > 0 {
			m.tasksFocus = true
			m.clampTaskSel()
			return m, nil
		}
		m.append(m.tasksView())
		return m, nil
	case "/theme":
		m.themeCommand(fields[1:])
		return m, nil
	case "/mouse":
		m.mouseOn = !m.mouseOn
		cfg := m.cfg
		b := m.mouseOn
		cfg.Mouse = &b
		if err := cfg.Save(); err != nil {
			m.append(errStyle.Render("config save failed: " + err.Error()))
		}
		m.append(dimStyle.Render("mouse capture: " + onOff(m.mouseOn) + " (on = wheel scroll + ✦ clicks, the default, drag to select/copy; off = native drag-to-copy, but tmux captures the wheel)"))

		if m.mouseOn {
			enableClickWheelMouse(os.Stdout)

		} else {
			disableClickWheelMouse(os.Stdout)
		}
		return m, nil
	case "/effort":
		levels := m.effortsFor()
		if len(fields) > 1 {
			lv, ok := parseEffort(levels, fields[1])
			if !ok {
				names := make([]string, len(levels))
				for i, e := range levels {
					names[i] = effortLabel(e)
				}
				m.append(errStyle.Render("unknown effort level; " + m.agent.Model + " supports: " + strings.Join(names, ", ")))
				break
			}
			m.setEffort(lv)
		} else {
			m.openPaletteOn("reasoning effort")
		}
	case "/goal-from-context":
		return m.startGoalFromContext(fields)
	case "/computer-use", "/computer":
		m.computerUseCommand(fields[1:], text)
		return m, nil
	case "/goal":
		switch {
		case len(fields) == 1:
			if m.goal == "" {
				m.append(dimStyle.Render("no goal set — /goal <text> to set one"))
			} else {
				m.append(dimStyle.Render(fmt.Sprintf("◎ goal (round %d/%d): %s", m.goalRounds, m.goalMaxRounds(), m.goal)))
			}
		case fields[1] == "clear":
			m.setGoal("")
		case fields[1] == "rounds":
			m.goalRoundsCommand(fields[2:])
		case fields[1] == "resume":
			if m.goal == "" {
				m.append(errStyle.Render("no goal to resume — set one with /goal <text>"))
				break
			}
			m.goalRounds = 0
			m.append(dimStyle.Render("◎ resuming goal: " + m.goal))
			return m.submitGoal(goalContinuePrompt(m.goal))
		default:
			goal := strings.TrimSpace(strings.TrimPrefix(text, "/goal"))
			m.setGoal(goal)
			m.append(dimStyle.Render("◎ goal set: " + goal))
			return m.submit(goal)
		}
	case "/fork":

		m.forkCommand(strings.TrimSpace(strings.TrimPrefix(text, "/fork")))
		return m, nil
	case "/forks":
		m.forksCommand()
		return m, nil
	case "/rename", "/title":
		if m.busy {
			m.append(dimStyle.Render("(busy — /rename after this turn)"))
			return m, nil
		}
		name := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
		m.renameCommand(name)
		return m, nil
	case "/resume":
		if m.busy {
			m.append(dimStyle.Render("(busy — /resume after this turn)"))
			return m, nil
		}
		if len(fields) > 1 {
			if err := m.resume(fields[1]); err != nil {
				m.append(errStyle.Render(err.Error()))
			}
			break
		}
		m.openPicker()
	case "/context", "/context-doctor", "/doctor":
		m.append(m.doctorReport())
	case "/info", "/session-info", "/status":
		m.append(m.sessionInfo())
	case "/plan", "/view-plan":
		m.append(m.planInfo())
	case "/undo", "/rewind":
		if m.busy {
			m.append(dimStyle.Render("(busy — /rewind after this turn)"))
			return m, nil
		}
		m.openRewind()
	case "/copy":
		return m, m.copyCommand(strings.TrimSpace(strings.TrimPrefix(text, "/copy")))
	case "/diff":
		return m, m.diffCommand(fields[1:])
	case "/prompts":
		m.promptsCommand(fields[1:])
	case "/export":
		m.exportCommand(strings.TrimSpace(strings.TrimPrefix(text, "/export")))
	case "/import":
		m.importCommand(strings.TrimSpace(strings.TrimPrefix(text, "/import")))
	case "/report":
		m.append(m.reportBlock())
	case "/archive":
		m.sessionManageCommand(fields[1:])
	case "/search":
		m.searchSessions(strings.TrimSpace(strings.TrimPrefix(text, "/search")))
	case "/tag":
		m.tagSession(fields[1:])
	case "/btw":
		return m, m.btwCommand(strings.TrimSpace(strings.TrimPrefix(text, "/btw")))
	case "/review":
		return m, m.reviewCommand(fields[1:])
	case "/language":
		m.languageCommand(fields[1:])
	case "/help":
		m.helpCommand(fields[1:])
	case "/settings":
		m.settingsCommand(fields[1:])
	case "/extensions":
		m.extensionsCommand(fields[1:])
	case "/auth":
		m.append(errStyle.Render("auth was removed; configure baseUrl and apiKey in ~/.liveagent/config.json"))
	case "/model", "/model-for-session":
		persist := fields[0] == "/model"
		if len(fields) < 2 {
			m.openModelPicker(!persist)
			break
		}
		if fields[1] == "refresh" {
			m.append(dimStyle.Render("refreshing model catalogs…"))
			go func() {
				m.fetchCatalogs(true)
				if m.prog != nil {
					m.prog.Send(noticeMsg("model catalogs refreshed — /model shows newly announced models"))
				}
			}()
			break
		}
		prov := ""
		if len(fields) > 2 {
			prov = fields[2]
		}
		name := fields[1]
		resolved, ok, alts := resolveModelFuzzy(m.cfg, name)
		if !ok {
			if len(alts) > 0 {
				m.append(errStyle.Render(fmt.Sprintf("ambiguous model %q — did you mean: %s?", name, strings.Join(alts, ", "))))
				return m, nil
			}
			m.append(errStyle.Render("unknown model " + name))
			return m, nil
		}
		m.switchModel(resolved, prov, persist)
	default:
		if !m.expandPromptCommand(text) {
			m.append(errStyle.Render("unknown command " + fields[0]))
		}
	}
	return m, nil
}

func (m *model) compactCommand(args []string) {
	if args[0] == "off" {
		m.compactModel, m.compactProv = "", ""
		m.applyCompactModel()
		m.cfg.CompactModel, m.cfg.CompactProvider = "", ""
		if err := m.cfg.Save(); err != nil {
			m.append(errStyle.Render("config save failed: " + err.Error()))
		}
		m.append(dimStyle.Render("◎ compaction model: default (" + config.DefaultCompactModel + ")"))
		return
	}
	name := args[0]
	if _, ok := m.cfg.Models[name]; !ok && !catalogAdvertises(m.cfg, name) {
		resolved, ok2, cands := resolveModelFuzzy(m.cfg, name)
		if !ok2 {
			if len(cands) > 0 {
				m.append(errStyle.Render("ambiguous model " + name + " — could be " + strings.Join(cands, ", ")))
			} else {
				m.append(errStyle.Render("unknown model " + name))
			}
			return
		}
		name = resolved
	}
	m.compactModel = name
	m.compactProv = ""
	if len(args) > 1 {
		m.compactProv = args[1]
	}
	m.applyCompactModel()
	if m.agent.CompactModel == "" {
		m.compactModel, m.compactProv = "", ""
		return
	}
	m.cfg.CompactModel, m.cfg.CompactProvider = m.compactModel, m.compactProv
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
	}
	note := "◎ compaction model: " + m.compactModel
	if prov := resolvedProvider(m.cfg, m.compactModel, m.compactProv); prov != "" {
		note += "@" + prov
	}
	m.append(dimStyle.Render(note))
}

func resolvedProvider(cfg *config.Config, model, prov string) string {
	if prov != "" {
		return prov
	}
	if mdl := cfg.Models[model]; len(mdl.Providers) > 0 {
		return mdl.Providers[0]
	}
	cats := config.LoadCatalogs()
	for name := range cfg.Providers {
		if cat, ok := cats[name]; ok && cat.Find(model) != nil {
			return name
		}
	}
	return ""
}

func catalogAdvertises(cfg *config.Config, name string) bool {
	cats := config.LoadCatalogs()
	for p := range cfg.Providers {
		if cat, ok := cats[p]; ok && cat.Find(name) != nil {
			return true
		}
	}
	return false
}

func (m *model) compactPct() int {
	pct := m.cfg.CompactPct
	if pct == 0 {
		pct = config.DefaultCompactPct
	}
	return min(max(pct, 10), 90)
}

func (m *model) setCompactPct(pct int) {
	pct = min(max(pct, 10), 90)
	m.agent.CompactThreshold = float64(pct) / 100
	m.cfg.CompactPct = pct
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
	}
}
