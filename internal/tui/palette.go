package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/browser"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
)

type paletteItem struct {
	title    string
	category string

	dynDesc func(m *model) string
	dynHint func(m *model) string

	suggested bool

	run func(m *model) (tea.Model, tea.Cmd)

	panel func(m *model) *ppanel

	stepBack func(m *model)
	stepFwd  func(m *model)
}

type panelKind int

const (
	panelSubagent panelKind = iota
	panelModel
	panelEffort
	panelGoal
	panelCompact
	panelTheme
	panelLanguage
	panelBrowser
	panelMCP
)

type mcpRow struct {
	name     string
	source   bool
	on       bool
	detail   string
	filtered bool
	disabled bool
}

type ppanel struct {
	kind  panelKind
	title string

	items []modelItem
	idx   int

	levels []string
	lidx   int

	prepare string

	list []string
	midx int

	filter modelFilter

	note string

	mcps []mcpRow

	err string

	direct bool
}

type palette struct {
	items  []paletteItem
	all    []paletteItem
	idx    int
	filter string
	stack  []*ppanel
}

const (
	palHintRewind   = "esc esc"
	palDescRewind   = "rewind the conversation"
	palHintThinking = "ctrl+o"
	palHintQuit     = "ctrl+c ctrl+c"
)

func slashHint(m *model, name string) string {
	if e := registryFind(name); e != nil {
		return m.tr(e.Hint)
	}
	return name
}

func (m *model) paletteItems() []paletteItem {
	return []paletteItem{
		{title: "Language", category: "Display", dynDesc: func(m *model) string { return slashHint(m, "/language") }, dynHint: func(m *model) string { return "/language" }, panel: func(m *model) *ppanel { return m.languagePanel() }},
		{
			title: "Model", category: "Agent", suggested: true,

			dynDesc: func(m *model) string { return m.modelName + " @ " + m.provName },
			dynHint: func(m *model) string { return "/model · tab" },
			panel: func(m *model) *ppanel {
				items := buildModelItems(m.cfg)
				if len(items) == 0 {
					return nil
				}
				pp := &ppanel{kind: panelModel, title: "Model", items: items}
				for i, it := range items {
					if it.model == m.modelName && it.provider == m.provName {
						pp.idx = i
						break
					}
				}
				return pp
			},
		},
		{
			title: "Reasoning effort", category: "Agent",
			dynDesc: func(m *model) string {
				return effortLabel(m.agent.Effort) + " · " + effortDescription(m.agent.Effort) + " · " + m.agent.Model
			},
			dynHint: func(m *model) string { return "/effort " + slashHint(m, "/effort") },
			panel: func(m *model) *ppanel {
				levels := m.effortsFor()
				pp := &ppanel{kind: panelEffort, title: "Reasoning effort", levels: levels}
				for i, e := range levels {
					if e == m.agent.Effort {
						pp.lidx = i
						break
					}
				}
				return pp
			},
			stepBack: func(m *model) { m.setEffort(prevEffort(m.effortsFor(), m.agent.Effort)) },
			stepFwd:  func(m *model) { m.setEffort(nextEffort(m.effortsFor(), m.agent.Effort)) },
		},
		{
			title: "Resume session", category: "Session", suggested: true,
			dynDesc: func(m *model) string { return slashHint(m, "/resume") },
			dynHint: func(m *model) string { return "/resume" },
			run: func(m *model) (tea.Model, tea.Cmd) {
				m.palette = nil
				m.openPicker()
				return m, nil
			},
		},
		{
			title: "Rewind conversation", category: "Session", suggested: true,
			dynDesc: func(m *model) string {
				if len(m.future) > 0 {
					return "rewound — browse to go back further or forward again"
				}
				return "jump back (or forward) to any earlier message"
			},
			dynHint: func(m *model) string { return palHintRewind },
			run: func(m *model) (tea.Model, tea.Cmd) {
				m.palette = nil
				if m.busy {
					return m, nil
				}
				m.openRewind()
				return m, nil
			},
		},
		{
			title: "Fork session", category: "Session",
			dynDesc: func(m *model) string { return slashHint(m, "/fork") },
			dynHint: func(m *model) string { return "/fork" },
			run: func(m *model) (tea.Model, tea.Cmd) {
				m.palette = nil
				m.forkCommand("")
				return m, nil
			},
		},
		{
			title: "Rename session", category: "Session",
			dynDesc: func(m *model) string {
				if m.sessionID == "" || m.store == nil {
					return "retitle this session"
				}
				if meta, _, err := m.store.Load(m.sessionID); err == nil && meta.Title != "" {
					return meta.Title
				}
				return "retitle this session"
			},
			dynHint: func(m *model) string { return "/rename " + slashHint(m, "/rename") },
			run: func(m *model) (tea.Model, tea.Cmd) {
				m.palette = nil
				if !m.busy {
					m.renameCommand("")
				}
				return m, nil
			},
		},
		{
			title: "New session", category: "Session",
			dynDesc: func(m *model) string { return slashHint(m, "/clear") },
			dynHint: func(m *model) string { return "/clear" },
			run: func(m *model) (tea.Model, tea.Cmd) {
				m.palette = nil
				return m.command("/clear")
			},
		},
		{
			title: "Compact session", category: "Session", suggested: true,
			dynDesc: func(m *model) string { return slashHint(m, "/compact") },
			dynHint: func(m *model) string { return "/compact" },
			run:     func(m *model) (tea.Model, tea.Cmd) { return m.command("/compact") },
		},
		{
			title: "Context doctor", category: "Session",
			dynDesc: func(m *model) string { return slashHint(m, "/context-doctor") },
			dynHint: func(m *model) string { return "/context-doctor" },
			run:     func(m *model) (tea.Model, tea.Cmd) { return m.command("/context-doctor") },
		},
		{
			title: "Bug report", category: "Session",
			dynDesc: func(m *model) string { return slashHint(m, "/report") },
			dynHint: func(m *model) string { return "/report" },
			run:     func(m *model) (tea.Model, tea.Cmd) { return m.command("/report") },
		},
		{
			title: "MCPs", category: "Session",
			dynDesc: func(m *model) string { return slashHint(m, "/mcp") + "; toggle claude/codex imports" },
			dynHint: func(m *model) string { return "/mcp" },
			panel: func(m *model) *ppanel {
				rows := m.buildMCPRows()
				if len(rows) == 0 {
					return nil
				}
				return &ppanel{kind: panelMCP, title: "MCPs", mcps: rows}
			},
		},
		{
			title: "Compaction model", category: "Session",
			dynDesc: func(m *model) string {
				if m.compactModel == "" {
					return "default (" + config.DefaultCompactModel + ")"
				}
				return m.compactModel
			},
			dynHint: func(m *model) string { return "/compact <model>" },
			panel: func(m *model) *ppanel {
				return m.routePanel(panelCompact, "Compaction model", config.DefaultCompactModel, m.compactModel, m.compactProv)
			},
		},
		{
			title: "Compaction level", category: "Session",
			dynDesc: func(m *model) string {
				return "auto-compact at this share of the context window"
			},
			dynHint:  func(m *model) string { return "←/→" },
			stepBack: func(m *model) { m.setCompactPct(m.compactPct() - 10) },
			stepFwd:  func(m *model) { m.setCompactPct(m.compactPct() + 10) },
		},
		{
			title: "Goal", category: "Session",
			dynDesc: func(m *model) string {
				if m.goal == "" {
					return fmt.Sprintf("keep working until the goal is met (max %d rounds)", m.goalMaxRounds())
				}
				return truncLine(m.goal, 40)
			},
			dynHint: func(m *model) string { return "/goal " + slashHint(m, "/goal") },
			panel: func(m *model) *ppanel {
				pp := &ppanel{kind: panelGoal, title: "Goal", prepare: m.goal}
				return pp
			},
		},

		{
			title: "Subagent model", category: "Session",
			dynDesc: func(m *model) string {
				if m.cfg.TaskModel == "" {
					return "default (" + config.DefaultTaskModel + ")"
				}
				return m.cfg.TaskModel
			},
			dynHint: func(m *model) string { return "config taskModel" },
			panel: func(m *model) *ppanel {
				return m.routePanel(panelSubagent, "Subagent model", config.DefaultTaskModel, m.cfg.TaskModel, m.cfg.TaskProvider)
			},
		},
		{
			title: "Thinking tokens", category: "Display",
			dynDesc: func(m *model) string { return "show or hide model reasoning" },
			dynHint: func(m *model) string { return palHintThinking },
			run: func(m *model) (tea.Model, tea.Cmd) {
				m.toggleThinking()
				return m, nil
			},
			stepBack: func(m *model) { m.setThinking(false) },
			stepFwd:  func(m *model) { m.setThinking(true) },
		},
		{
			title: "Theme", category: "Display",
			dynDesc: func(m *model) string { return m.tr("current: ") + CurrentTheme() },
			dynHint: func(m *model) string { return "/theme " + slashHint(m, "/theme") },
			panel: func(m *model) *ppanel {
				list := []string{"auto", "light", "dark"}
				cur := m.cfg.Theme
				if cur == "" {
					cur = "auto"
				}
				pp := &ppanel{kind: panelTheme, title: "Theme", list: list}
				for i, t := range list {
					if t == cur {
						pp.midx = i
						break
					}
				}
				return pp
			},
			stepBack: func(m *model) { m.setTheme("light") },
			stepFwd:  func(m *model) { m.setTheme("dark") },
		},

		{
			title: "Browser driver", category: "Display",
			dynDesc: func(m *model) string {
				return "current: " + browser.Driver + " — which automation engine drives Chrome"
			},
			dynHint: func(m *model) string { return "K_BRAIN_BROWSER_DRIVER" },
			panel: func(m *model) *ppanel {
				list := browser.Drivers
				pp := &ppanel{kind: panelBrowser, title: "Browser driver", list: list}
				for i, d := range list {
					if d == browser.Driver {
						pp.midx = i
						break
					}
				}
				return pp
			},
			stepBack: func(m *model) { m.switchBrowserDriver(browser.DriverRod) },
			stepFwd:  func(m *model) { m.switchBrowserDriver(browser.DriverChromedp) },
		},
		{
			title: "Mouse capture", category: "Display",
			dynDesc: func(m *model) string { return slashHint(m, "/mouse") },
			dynHint: func(m *model) string { return "/mouse" },
			run: func(m *model) (tea.Model, tea.Cmd) {
				return m.command("/mouse")
			},
			stepBack: func(m *model) { m.setMouse(false) },
			stepFwd:  func(m *model) { m.setMouse(true) },
		},
		{
			title: "Help", category: "App",
			dynDesc: func(m *model) string { return slashHint(m, "/help") },
			dynHint: func(m *model) string { return "/help" },
			run: func(m *model) (tea.Model, tea.Cmd) {
				m.palette = nil
				return m.command("/help")
			},
		},
		{
			title: "Quit", category: "App",
			dynDesc: func(m *model) string { return "exit k-brain" },
			dynHint: func(m *model) string { return "/quit · " + palHintQuit },
			run:     func(m *model) (tea.Model, tea.Cmd) { return m, tea.Quit },
		},
	}
}

func (m *model) setMouse(on bool) {
	if m.mouseOn == on {
		return
	}
	m.command("/mouse")
}

func (m *model) openPalette() {
	all := m.paletteItems()
	m.palette = &palette{all: all}
	m.palette.applyFilter(m)
}

func (m *model) openPaletteOn(title string) {
	m.openPalette()
	for i, it := range m.palette.items {
		if strings.EqualFold(it.title, title) && it.panel != nil {
			m.palette.idx = i
			pp := it.panel(m)
			pp.direct = true
			m.palette.stack = append(m.palette.stack, pp)
			return
		}
	}
}

func paletteFilterMatch(query, hay string) bool {
	if query == "" {
		return true
	}
	hay = strings.ToLower(hay)
	for _, r := range strings.ToLower(query) {
		i := strings.IndexRune(hay, r)
		if i < 0 {
			return false
		}
		hay = hay[i+1:]
	}
	return true
}

func itemHaystack(m *model, it paletteItem) string {
	s := it.title + " " + it.category
	if title := m.tr(it.title); title != it.title {
		s += " " + title
	}
	if category := m.tr(it.category); category != it.category {
		s += " " + category
	}
	if it.dynHint != nil {
		if f := strings.Fields(it.dynHint(m)); len(f) > 0 && strings.HasPrefix(f[0], "/") {
			s += " " + f[0]
		}
	}
	return s
}

func (p *palette) applyFilter(m *model) {
	q := p.filter
	var items []paletteItem
	for _, it := range p.all {
		if paletteFilterMatch(q, itemHaystack(m, it)) {
			items = append(items, it)
		}
	}

	seen := map[string]bool{}
	var cats []string
	for _, it := range items {
		if !seen[it.category] {
			seen[it.category] = true
			cats = append(cats, it.category)
		}
	}
	var grouped []paletteItem
	for _, c := range cats {
		for _, it := range items {
			if it.category == c {
				grouped = append(grouped, it)
			}
		}
	}
	if q == "" {
		var sugg []paletteItem
		for _, it := range grouped {
			if it.suggested {
				sugg = append(sugg, it)
			}
		}
		if len(sugg) > 0 {
			for i := range sugg {
				sugg[i].category = "Suggested"
			}
			grouped = append(sugg, grouped...)
		}
	}
	p.items = grouped
	if p.idx >= len(p.items) {
		p.idx = max(len(p.items)-1, 0)
	}
}

func (p *palette) selected() *paletteItem {
	if len(p.items) == 0 {
		return nil
	}
	return &p.items[p.idx]
}

func (p *palette) top() *ppanel {
	if len(p.stack) == 0 {
		return nil
	}
	return p.stack[len(p.stack)-1]
}

func (p *palette) move(delta int) {
	n := len(p.items)
	if n == 0 {
		return
	}
	p.idx = (p.idx + delta + n) % n
}

func (m *model) paletteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.palette
	if pp := p.top(); pp != nil {
		return m.panelKey(msg, pp)
	}
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.palette = nil
	case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
		p.move(-1)
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		p.move(1)
	case tea.KeyLeft:
		if it := p.selected(); it != nil && it.stepBack != nil {
			it.stepBack(m)
		}
	case tea.KeyRight:
		it := p.selected()
		if it == nil {
			break
		}
		if it.stepFwd != nil {
			it.stepFwd(m)
		} else if it.panel != nil {
			m.pushPanel(it)
		}
	case tea.KeyEnter:
		it := p.selected()
		if it == nil {
			return m, nil
		}
		switch {
		case it.panel != nil:
			m.pushPanel(it)
		case it.run != nil:
			return it.run(m)
		}
	case tea.KeyBackspace, tea.KeyDelete:
		if len(p.filter) > 0 {
			p.filter = p.filter[:len(p.filter)-1]
			p.applyFilter(m)
		}
	case tea.KeyRunes:
		p.filter += string(msg.Runes)
		p.idx = 0
		p.applyFilter(m)
	}
	return m, nil
}

func (m *model) pushPanel(it *paletteItem) {
	pp := it.panel(m)
	if pp == nil {
		m.append(errStyle.Render(it.title + ": nothing to choose from (check ~/.k-brain/config.json)"))
		return
	}
	m.palette.stack = append(m.palette.stack, pp)
}

func (m *model) panelKey(msg tea.KeyMsg, pp *ppanel) (tea.Model, tea.Cmd) {
	p := m.palette
	pop := func() {
		p.stack = p.stack[:len(p.stack)-1]

		if pp.direct && len(p.stack) == 0 {
			m.palette = nil
		}
	}

	switch pp.kind {
	case panelModel:
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			pop()
		case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
			pp.idx = (pp.idx - 1 + len(pp.items)) % len(pp.items)
			m.previewModel(pp.items[pp.idx])
		case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
			pp.idx = (pp.idx + 1) % len(pp.items)
			m.previewModel(pp.items[pp.idx])
		case tea.KeyEnter:
			it := pp.items[pp.idx]
			m.switchModel(it.model, it.provider, true)
			pop()
		}

	case panelEffort:
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			pop()
		case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
			pp.lidx = (pp.lidx - 1 + len(pp.levels)) % len(pp.levels)
		case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
			pp.lidx = (pp.lidx + 1) % len(pp.levels)
		case tea.KeyLeft, tea.KeyRight, tea.KeyEnter:

			m.setEffort(pp.levels[pp.lidx])
			if msg.Type == tea.KeyEnter {
				pop()
			}
		}

	case panelSubagent, panelCompact:

		view := pp.filter.view(len(pp.list))
		apply := func() {
			if len(view) == 0 || pp.midx >= len(view) {
				return
			}
			row := view[pp.midx]
			name, prov := splitRouteKey(pp.list[row])
			pp.err = ""
			if row == 0 {
				if pp.kind == panelCompact {
					m.compactCommand([]string{"off"})
				} else {
					m.subagentModelCommand([]string{"off"})
				}
				return
			}
			if pp.kind == panelCompact {
				m.compactCommand([]string{name, prov})
				if m.compactModel != name {
					pp.err = "couldn't resolve " + name + " — kept previous"
				}
			} else {
				m.subagentModelCommand([]string{name, prov})
				if m.cfg.TaskModel != name {
					pp.err = "couldn't resolve " + name + " — kept previous"
				}
			}
		}
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			pop()
		case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
			if len(view) > 0 {
				pp.midx = (pp.midx - 1 + len(view)) % len(view)
			}
		case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
			if len(view) > 0 {
				pp.midx = (pp.midx + 1) % len(view)
			}
		case tea.KeyLeft, tea.KeyRight:
			apply()
		case tea.KeyEnter:
			apply()
			if pp.err == "" {
				pop()
			}
		case tea.KeyBackspace, tea.KeyDelete:
			if pp.filter.backspace() {
				pp.filter.applyModelList(pp.list)
				pp.midx = 0
			}
		case tea.KeyRunes, tea.KeySpace:
			if pp.filter.typeRunes(msg.Runes) {
				pp.filter.applyModelList(pp.list)
				pp.midx = 0
			}
		}

	case panelLanguage:
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			pop()
		case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
			pp.midx = (pp.midx - 1 + len(pp.list)) % len(pp.list)
		case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
			pp.midx = (pp.midx + 1) % len(pp.list)
		case tea.KeyEnter:
			if m.setLanguage(pp.list[pp.midx]) {
				pop()
			}
		}

	case panelTheme:
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			pop()
		case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
			pp.midx = (pp.midx - 1 + len(pp.list)) % len(pp.list)
		case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
			pp.midx = (pp.midx + 1) % len(pp.list)
		case tea.KeyLeft, tea.KeyRight, tea.KeyEnter:
			m.setTheme(pp.list[pp.midx])
			if msg.Type == tea.KeyEnter {
				pop()
			}
		}

	case panelBrowser:
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			pop()
		case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
			pp.midx = (pp.midx - 1 + len(pp.list)) % len(pp.list)
		case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
			pp.midx = (pp.midx + 1) % len(pp.list)
		case tea.KeyLeft, tea.KeyRight, tea.KeyEnter:
			m.switchBrowserDriver(pp.list[pp.midx])
			if msg.Type == tea.KeyEnter {
				pop()
			}
		}

	case panelMCP:
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			pop()
		case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
			pp.midx = (pp.midx - 1 + len(pp.mcps)) % len(pp.mcps)
		case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
			pp.midx = (pp.midx + 1) % len(pp.mcps)
		case tea.KeyLeft, tea.KeyRight, tea.KeyEnter:
			row := &pp.mcps[pp.midx]
			if row.disabled {
				return m, nil
			}
			if row.source {
				m.mcpSetImport(row.name, !row.on)
			} else {
				m.mcpSetEnabled(row.name, !row.on)
			}

			pp.mcps = m.buildMCPRows()
			if pp.midx >= len(pp.mcps) {
				pp.midx = len(pp.mcps) - 1
			}
		}

	case panelGoal:
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			m.commitGoal(pp)
			pop()
		case tea.KeyEnter:
			m.commitGoal(pp)
			pop()
		case tea.KeyBackspace, tea.KeyDelete:
			if len(pp.prepare) > 0 {
				pp.prepare = pp.prepare[:len(pp.prepare)-1]
			}
		case tea.KeyRunes, tea.KeySpace:
			pp.prepare += string(msg.Runes)
		}
	}
	return m, nil
}

func (m *model) previewModel(it modelItem) {
	if it.model == m.modelName && it.provider == m.provName {
		return
	}
	ag, mn, pn, err := buildAgent(m.cfg, it.model, it.provider, m.sysPrompt)
	if err != nil {
		return
	}
	ag.Effort = m.agent.Effort
	ag.Messages = append(ag.Messages, m.agent.Messages[1:]...)
	ag.CompactClient, ag.CompactModel = m.agent.CompactClient, m.agent.CompactModel
	ag.CompactThreshold = m.agent.CompactThreshold
	m.agent, m.modelName, m.provName = ag, mn, pn
	m.applyTaskModel()
	if !slices.Contains(m.effortsFor(), ag.Effort) {
		m.setEffort("")
	}
}

func (m *model) commitGoal(pp *ppanel) {
	goal := strings.TrimSpace(pp.prepare)
	if goal == m.goal {
		if goal != "" && !m.busy {
			m.goalRounds = 0
			m.append(dimStyle.Render("◎ resuming goal: " + goal))
			m.submitGoal(goalContinuePrompt(goal))
		}
		return
	}
	m.setGoal(goal)
	if goal == "" {
		m.append(dimStyle.Render("(goal cleared)"))
		return
	}
	m.append(dimStyle.Render("◎ goal set: " + goal))
	if !m.busy {
		m.submit(goal)
	}
}

func prevEffort(levels []string, cur string) string {
	for i, e := range levels {
		if e == cur {
			return levels[(i-1+len(levels))%len(levels)]
		}
	}
	return levels[0]
}

func (m *model) paletteView() string {
	p := m.palette
	var b strings.Builder
	title := " " + m.tr("Commands")
	if pp := p.top(); pp != nil {
		title = " " + m.tr("Commands") + " › " + m.tr(pp.title)
	}
	b.WriteString(botStyle.Render(title))
	if p.top() == nil && p.filter != "" {
		b.WriteString(dimStyle.Render(m.tr("  — type to filter")))
	}
	b.WriteString("\n\n")

	if pp := p.top(); pp != nil {
		b.WriteString(m.panelView(pp))
		return b.String()
	}

	b.WriteString(" " + youStyle.Render(glyphUser) + p.filter + dimStyle.Render("█"))
	b.WriteString("\n\n")

	lastCat := ""
	hintW := 0
	for _, it := range p.items {
		if it.dynHint != nil {
			hintW = max(hintW, len(it.dynHint(m)))
		}
	}
	for i, it := range p.items {
		if it.category != lastCat {
			if lastCat != "" {
				b.WriteString("\n")
			}
			b.WriteString(dimStyle.Render("  " + m.tr(it.category)))
			b.WriteString("\n")
			lastCat = it.category
		}
		hint := ""
		if it.dynHint != nil {
			hint = dimStyle.Render(fmt.Sprintf("%*s", hintW, it.dynHint(m)))
		}
		line := " " + m.tr(it.title)
		if it.dynDesc != nil {
			line += dimStyle.Render("  — " + m.tr(it.dynDesc(m)))
		}
		state := paletteState(m, it)
		if i == p.idx {
			b.WriteString(botStyle.Render("→") + line + state + "  " + hint)
		} else {
			b.WriteString(" " + line + state + "  " + hint)
		}
		b.WriteString("\n")
	}
	if len(p.items) == 0 {
		b.WriteString(dimStyle.Render(m.tr("  (no matches)")))
		b.WriteString("\n")
	}
	b.WriteString("\n" + dimStyle.Render(fmt.Sprintf(m.tr("  (%d/%d) ↑/↓ select · enter open/apply · ←/→ change · esc close"),
		min(p.idx+1, len(p.items)), len(p.items))))
	return b.String()
}

func paletteState(m *model, it paletteItem) string {
	switch it.title {
	case "Reasoning effort":
		return effortBadge(m.agent.Effort, true)
	case "Thinking tokens":
		return dimStyle.Render("  [" + onOff(m.showThinking) + "]")
	case "Mouse capture":
		return dimStyle.Render("  [" + onOff(m.mouseOn) + "]")
	case "Goal":
		if m.goal != "" {
			return dimStyle.Render("  [on]")
		}
	case "Compaction level":
		return dimStyle.Render(fmt.Sprintf("  [%d%%]", m.compactPct()))
	case "MCPs":
		if m.mcpMgr == nil {
			return ""
		}
		ready, total := 0, 0
		for _, st := range m.mcpMgr.Statuses() {
			total++
			if st.Status == mcp.StatusReady {
				ready++
			}
		}
		return dimStyle.Render(fmt.Sprintf("  [%d/%d ready]", ready, total))
	}
	return ""
}

func (m *model) panelView(pp *ppanel) string {
	var b strings.Builder
	switch pp.kind {
	case panelModel:
		var rows []string
		selRow := 0
		lastModel := ""
		for i, it := range pp.items {
			if it.model != lastModel {
				heading := " " + it.model
				if it.fromCatalog {
					heading = dimStyle.Render(heading + dimNew)
				}
				rows = append(rows, heading)
				lastModel = it.model
			}
			cur := ""
			if it.model == m.modelName && it.provider == m.provName {
				cur = dimStyle.Render(m.tr("  (current)"))
			}
			line := fmt.Sprintf("%-12s  ", it.provider) + dimStyle.Render(it.url)
			if it.fromCatalog {
				line = dimStyle.Render(line)
			}
			if i == pp.idx {
				selRow = len(rows)
				rows = append(rows, botStyle.Render("   → "+line)+cur)
			} else {
				rows = append(rows, "     "+line+cur)
			}
		}

		lo, hi := 0, len(rows)
		if avail := m.height - 7; avail > 0 && len(rows) > avail {
			lo, hi = viewportWindow(len(rows), selRow, avail)
		}
		if lo > 0 {
			b.WriteString(dimStyle.Render(fmt.Sprintf(m.tr("   ↑ %d more"), lo)) + "\n")
		}
		for _, r := range rows[lo:hi] {
			b.WriteString(r + "\n")
		}
		if hi < len(rows) {
			b.WriteString(dimStyle.Render(fmt.Sprintf(m.tr("   ↓ %d more"), len(rows)-hi)) + "\n")
		}
		b.WriteString("\n" + dimStyle.Render(fmt.Sprintf("  (%d/%d) ↑/↓ preview · enter switch · esc back", pp.idx+1, len(pp.items))))

	case panelEffort:
		return m.effortPanelView(pp)

	case panelSubagent, panelCompact:

		b.WriteString("  " + botStyle.Render("/") + pp.filter.query + dimStyle.Render("▏") + "\n")
		view := pp.filter.view(len(pp.list))
		current, curProv := m.compactModel, m.compactProv
		if pp.kind == panelSubagent {
			current, curProv = m.cfg.TaskModel, m.cfg.TaskProvider
		}
		width := 0
		for _, it := range pp.items {
			width = max(width, len(it.model))
		}

		lo, hi := 0, len(view)
		if avail := m.height - 9; avail > 0 && len(view) > avail {
			lo, hi = viewportWindow(len(view), pp.midx, avail)
		}
		if lo > 0 {
			b.WriteString(dimStyle.Render(fmt.Sprintf(m.tr("   ↑ %d more"), lo)) + "\n")
		}
		for i := lo; i < hi; i++ {
			row := view[i]
			line := pp.list[row]
			isCur := row == 0 && current == ""
			if row > 0 {
				it := pp.items[row-1]
				isCur = it.model == current && (curProv == "" || it.provider == curProv)
				line = fmt.Sprintf("%-*s  %-20s  ", width, it.model, it.provider) + dimStyle.Render(it.url)
				if it.fromCatalog {
					line = dimStyle.Render(fmt.Sprintf("%-*s  %-20s  ", width, it.model+dimNew, it.provider) + it.url)
				}
			}
			if isCur {
				line += dimStyle.Render(m.tr("  (current)"))
			}
			if i == pp.midx {
				b.WriteString(botStyle.Render(" → "+line) + "\n")
			} else {
				b.WriteString("   " + line + "\n")
			}
		}
		if hi < len(view) {
			b.WriteString(dimStyle.Render(fmt.Sprintf(m.tr("   ↓ %d more"), len(view)-hi)) + "\n")
		}
		if len(view) == 0 {
			b.WriteString(dimStyle.Render("  no models match "+strconv.Quote(pp.filter.query)) + "\n")
		}
		if pp.err != "" {
			b.WriteString(errStyle.Render("  "+pp.err) + "\n")
		}
		if pp.note != "" {
			b.WriteString(dimStyle.Render("  "+pp.note) + "\n")
		}
		b.WriteString("\n" + dimStyle.Render(fmt.Sprintf(m.tr("  (%d/%d) type to filter · ↑/↓ select · enter/←/→ apply · esc back"), pp.midx+1, len(view))))

	case panelLanguage:
		for i, language := range pp.list {
			line := languageLabel(language)
			if language == m.language() {
				line += m.tr("  (current)")
			}
			if i == pp.midx {
				b.WriteString(botStyle.Render(" → "+line) + "\n")
			} else {
				b.WriteString("   " + line + "\n")
			}
		}
		b.WriteString("\n" + dimStyle.Render(m.tr("  ↑/↓ select · enter apply · esc back")))

	case panelTheme:
		cur := m.cfg.Theme
		if cur == "" {
			cur = "auto"
		}
		for i, name := range pp.list {
			mark := ""
			if name == cur {
				mark = dimStyle.Render(m.tr("  (current)"))
			}
			if i == pp.midx {
				b.WriteString(botStyle.Render(" → "+name) + mark + "\n")
			} else {
				b.WriteString("   " + name + mark + "\n")
			}
		}
		b.WriteString("\n" + dimStyle.Render(m.tr("  ↑/↓ select · enter/←/→ apply · esc back")))

	case panelBrowser:
		for i, name := range pp.list {
			mark := ""
			if name == browser.Driver {
				mark = dimStyle.Render(m.tr("  (current)"))
			}
			if i == pp.midx {
				b.WriteString(botStyle.Render(" → "+name) + mark + "\n")
			} else {
				b.WriteString("   " + name + mark + "\n")
			}
		}
		b.WriteString("\n" + dimStyle.Render(m.tr("  ↑/↓ select · enter/←/→ apply · esc back")))

	case panelGoal:
		b.WriteString(" " + youStyle.Render(glyphUser) + pp.prepare + dimStyle.Render("█"))
		b.WriteString("\n\n" + dimStyle.Render(fmt.Sprintf("  type the goal · empty clears · enter/esc apply · max %d rounds (/goal rounds)", m.goalMaxRounds())))

	case panelMCP:
		for i, row := range pp.mcps {
			box := "[x]"
			if !row.on {
				box = "[ ]"
			}
			label := row.name
			if row.source {
				label = map[string]string{"claude": "Import Claude MCPs", "codex": "Import Codex MCPs"}[row.name]
			}
			line := fmt.Sprintf("%s %-22s %s", box, label, dimStyle.Render(row.detail))
			if row.filtered {
				line += dimStyle.Render("  (name filters set — edit config)")
			}
			if row.disabled {
				line = dimStyle.Render(line)
			}
			if i == pp.midx {
				b.WriteString(botStyle.Render(" → "+line) + "\n")
			} else {
				b.WriteString("   " + line + "\n")
			}
		}
		b.WriteString("\n" + dimStyle.Render("  ↑/↓ select · enter/←/→ toggle · esc back · /mcp for reconnect"))
	}
	b.WriteString("\n")
	return b.String()
}

func (m *model) routePanel(kind panelKind, title, defaultModel, current, currentProv string) *ppanel {
	items := buildModelItems(m.cfg)
	pp := &ppanel{kind: kind, title: title, items: items, list: make([]string, 0, len(items)+1)}
	pp.list = append(pp.list, "default ("+defaultModel+")")
	for i, it := range items {
		pp.list = append(pp.list, routeKey(it))
		if it.model == current && (currentProv == "" || it.provider == currentProv) && pp.midx == 0 {
			pp.midx = i + 1
		}
	}
	if st := staleCatalogs(m.cfg, config.LoadCatalogs()); len(st) > 0 {
		pp.note = "catalog stale for " + strings.Join(st, ", ") + " — /model refresh pulls newly announced models"
	}
	return pp
}
