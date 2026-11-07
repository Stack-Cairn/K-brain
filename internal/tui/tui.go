package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/browser"
	"github.com/Stack-Cairn/K-brain/internal/computer"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/hooks"
	"github.com/Stack-Cairn/K-brain/internal/lsp"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/plugins"
	"github.com/Stack-Cairn/K-brain/internal/routing"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/skills"
	"github.com/Stack-Cairn/K-brain/internal/tools"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
	"github.com/Stack-Cairn/K-brain/internal/update"

	"github.com/charmbracelet/x/ansi"
)

type (
	textMsg      string
	toolStartMsg struct{ id, name, args string }
	toolEndMsg   struct{ id, name, result string }

	toolCallMsg   struct{ id, name, args string }
	toolOutputMsg struct{ id, text string }
	steeredMsg    string
	reviewMsg     struct {
		result string
		err    error
		fix    bool
	}
	btwMsg struct {
		result string
		err    error
	}
)

type compactMsg struct {
	before, after []ai.Message
	turnAt        *int
	preserved     bool
	took, kept    int
	summary       string
	cutoff        int
	info          agent.CompactInfo
	err           error
	fresh         bool
}

type compactStartMsg struct {
	took, est int
}
type contextStartMsg struct{ took, est int }
type turnDoneMsg struct {
	final      string
	stopReason ai.StopReason
	err        error
	at         int
	snap       string
	clean      bool
}
type (
	catalogsMsg    map[string]config.Catalog
	noticeMsg      string
	usageMsg       ai.Usage
	quitArmMsg     struct{}
	taskUpdateMsg  struct{}
	waitWakeMsg    string
	orphanSteerMsg string
	mcpStatusMsg   struct{}
	thinkMsg       string
	imageMsg       struct {
		path    string
		display string
		err     error
	}
)

type model struct {
	promptCatalog promptCatalog

	cfg *config.Config
	// memoryPending is a memory switch LiveAgent set during a turn, applied when it ends.
	memoryPending *bool
	agent         *agent.Agent
	modelName     string
	provName      string
	sysPrompt     string
	promptManaged bool
	sandboxPolicy *sandbox.Policy
	pluginMgr     *plugins.Manager

	cfgExtra map[string]string
	cfgMod   time.Time

	input       textarea.Model
	spin        spinner.Model
	vp          transcriptView
	blocks      []block
	ancientMode bool

	msgBlock  []int
	follow    bool
	width     int
	height    int
	termWidth int

	busy    bool
	btwBusy bool
	current string
	inMsg   bool

	lastResp ai.Usage

	showThinking bool
	curThink     string
	inThink      bool
	menu         *menu
	picker       *picker
	mpicker      *modelPicker
	palette      *palette
	cancel       context.CancelFunc
	goalRequest  *goalFormulation
	prog         *tea.Program

	store           *session.Store
	sessionID       string
	saved           int
	snapshots       map[int]string
	history         *session.History
	historyID       string
	historyEvents   []compactMsg
	historyErr      error
	turnSnapshotSeq *int

	hist     []string
	images   []pastedImage
	imageSeq int
	histIdx  int
	draft    string
	lastUp   time.Time
	now      func() time.Time

	turnStart time.Time

	queue      []string
	queueSel   int
	interrupt1 bool
	quit1      bool

	goal       string
	goalRounds int
	titled     bool

	pendingForkID string

	mouseOn  bool
	sel      *selection
	selDragX int
	selDragY int

	// transientNotice briefly replaces the footer's status cell, e.g. after a
	// copy; noticeGeneration lets a newer notice outlive an older one's timer.
	transientNotice  string
	noticeGeneration uint64

	inputBodyOff int
	// composerTop is where the composer's top rule sits, which is one row above
	// inputBodyOff. Padding goes here so the frame stays whole.
	composerTop int
	// turnFloorH is the tallest the frame has been during the running turn, so a
	// mid-stream shrink cannot bounce the composer back up. Reset when idle.
	turnFloorH   int
	viewportTop  int
	viewportRows int
	inputLeft    int
	inputTop     int
	inputLines   []string
	vpLead       int
	viewTop      int
	viewH        int
	frameTop     int
	frameH       int
	themeHow     string
	sessTitle    string
	compactModel string
	compactProv  string
	git          gitStatus

	updateLatest string
	// stats backs the startup card's "ready" row. MCP fills in asynchronously,
	// so the card restates itself as statuses land.
	stats bannerStats

	// effortX is the column where the footer's effort chip starts; effortRow is
	// its row within the rendered body and effortY the same row on screen. Both
	// rows are -1 when no clickable chip is on screen.
	effortX   int
	effortRow int
	effortY   int

	catalogs map[string]config.Catalog
	mcpMgr   *mcp.Manager
	mcpSeen  map[string]bool
	lspMgr   *lsp.Manager

	skillScan func() []skills.Skill

	irunner *interactiveRunner
	iactive *interactive

	perms          permRules
	permDialog     *permDialog
	askDialog      *askDialog
	permissionMode string
	permissionDone chan struct{}

	tasksFocus   bool
	taskSel      int
	taskExpanded bool
	dockSkip     int
	dockOffsets  []int
	dockLo       int
	dockTaskRows int
	taskVP       *taskView
	dockRows     int
	// dockOrder is the dock's row order at the last render, so a selection
	// follows its task when running rows move ahead of finished ones.
	dockOrder []string

	fg         *fgRegistry
	subAct     map[string]*subActivity
	subStop    map[string]bool
	subTicking bool

	rew    *rewindState
	esc1   bool
	escClr bool
	future []ai.Message

	escGrace escGrace // Esc presses just after an interrupt are ignored

	namePrompt *namePrompt

	initialPrompt string

	// ── Area state ──────────────────────────────────────────────────────────
	// Each larger area of the TUI (transcript engine, tool views, composer,
	// interactive cards, status and app surfaces) keeps its new state behind
	// a single pointer field in this block, e.g. `tx *transcriptState`, with
	// the type and its methods in that area's own files. Add one line per
	// area at the end of the block rather than loose fields elsewhere in
	// model, so branches for different areas merge without conflicts.
	tx  *transcriptState // transcript engine (transcript.go)
	cmp *composerState   // composer: undo, kill ring, chips, stash, history search, queue editing
	app *appSurfaceState // app surfaces: cheatsheet, settings, extensions, memory (picker_view.go)
	tv  *toolViewState   // tool views and the plan pane (toolview.go, todo_pane.go)
	st  *statusState     // status row, footer, notifications (status_state.go)

	cards *cardState // interactive cards: plan review (plan_view.go)
}

type initialPromptMsg struct{}

var (
	tuiRunning bool
	bgCache    bgResult
)

func Run(cfg *config.Config, modelName, provName, sysPrompt, resumeID string, cautious, firstRun bool, initialPrompt string, continueMode, browseMode bool) (string, error) {

	stdinR := bufio.NewReader(os.Stdin)

	if ok, err := onboard(cfg, firstRun, stdinR); err != nil {
		return "", err
	} else if !ok {
		return "", errors.New("folder not trusted")
	}

	ag, mn, pn, err := buildAgent(cfg, modelName, provName, sysPrompt)
	if err != nil {
		return "", err
	}

	ti := newInput()

	ag.Effort = DefaultEffortFor(config.LoadCatalogs(), pn, ag.Model, cfg.DefaultEffort)

	mouseOn := true
	if cfg.Mouse != nil {
		mouseOn = *cfg.Mouse
	}
	showThinking := true
	if cfg.Thinking != nil {
		showThinking = *cfg.Thinking
	}
	m := &model{
		cfg: cfg, agent: ag, modelName: mn, provName: pn, sysPrompt: sysPrompt, promptManaged: true,
		sandboxPolicy: cfg.Sandbox.Policy(cwd()),
		input:         ti, spin: spinner.New(spinner.WithSpinner(spinner.Dot)), follow: true, saved: 1,
		catalogs: config.LoadCatalogs(), mouseOn: mouseOn, now: time.Now, showThinking: showThinking,

		compactModel: cfg.CompactModel, compactProv: cfg.CompactProvider,
		skillScan:     func() []skills.Skill { return skills.Scan(skills.DefaultDirs()...) },
		initialPrompt: initialPrompt,
	}
	ag.SandboxPolicy = m.sandboxPolicy

	// Styles bake their adaptive colours at Render time, so the theme has to be
	// settled before anything is appended to the transcript.
	m.themeHow = m.applyTheme(cfg.Theme)
	m.applyColorEnv()
	m.appendBanner()

	m.applyCompactModel()
	m.applyTaskModel()
	if project, err := os.Getwd(); err == nil {
		if pm, perr := plugins.New(project); perr == nil {
			m.pluginMgr = pm
			ag.SetPluginTools(pluginToolAdapters(pm))
		} else if pm != nil && len(pm.List()) > 0 {
			m.pluginMgr = pm
			ag.SetPluginTools(pluginToolAdapters(pm))
			m.append(errStyle.Render("plugin: " + perr.Error()))
		} else {
			m.append(dimStyle.Render("plugin: " + perr.Error()))
		}
	}
	m.agent.CompactThreshold = compactThresholdFor(cfg)
	m.wireTasks()

	if _, wdErr := os.Getwd(); wdErr == nil {
		servers := mcp.FromConfigMap(cfg.MCPServers)
		if len(servers) > 0 {
			m.mcpMgr = mcp.NewManager(servers)
			m.mcpMgr.EnableElicitation()
			m.mcpMgr.SetOnChange(m.mcpOnChange())
			m.mcpMgr.Start(context.Background())
			ag.SetMCPTools(m.mcpMgr.Tools())
		}

		m.lspMgr = lsp.NewManager(lsp.FromConfigMap(cfg.LSPServers))
		tools.LSP = m.lspMgr
		// The first agent predates the manager; agents built later pick the tool up in New.
		ag.Tools = append(ag.Tools, tools.LSPTool())
	}

	tools.ComputerApprover = m.computerConsent
	m.installAskHook()

	m.permissionMode = "always"
	if cautious {
		m.permissionMode = "normal"
	}
	m.permissionDone = make(chan struct{})
	previousGate := tools.Gate
	m.installPermGate()
	defer func() { close(m.permissionDone); tools.Gate = previousGate }()
	if dir, derr := config.Dir(); derr == nil {
		if st, serr := session.OpenProjectHome(dir); serr == nil {
			m.store = st
			defer func() { _ = st.Close() }()

			if hist, herr := st.UserHistory(500); herr == nil && len(hist) > 0 {
				for _, h := range slices.Backward(hist) {
					m.hist = append(m.hist, h)
				}
				m.histIdx = len(m.hist)
			}
		} else {
			config.LogEvent("session.open", "FAILED: "+serr.Error())
			m.append(errStyle.Render("sessions disabled: " + serr.Error()))
		}
	}

	if resumeID != "" || continueMode || browseMode {
		if m.store == nil {
			return "", errors.New("cannot resume: session store unavailable")
		}
		switch {
		case resumeID != "":
			if err := m.resume(resumeID); err != nil {
				return "", err
			}
		case continueMode:
			if err := m.continueRecent(); err != nil {
				return "", err
			}
		case browseMode:
			m.openPicker()
		}
	}

	m.updateLatest = update.Pending(Version)

	m.applyAppearance()

	tmuxEnableExtendedKeys()
	m.startupReport()

	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithReportFocus()}

	fmt.Fprint(os.Stdout, "\x1b[9999;1H")

	{
		if m.mouseOn {

			enableClickWheelMouse(os.Stdout)
		}

		enableKeyboardEnhancement(os.Stdout)
	}
	if m.cfgExtra == nil {
		m.cfgExtra = map[string]string{}
	}
	if dir, err := config.Dir(); err == nil {
		if fi, err := os.Stat(filepath.Join(dir, "config.json")); err == nil {
			m.cfgMod = fi.ModTime()
		}
	}
	p := tea.NewProgram(m, opts...)
	m.prog = p
	m.wireProgram()
	m.startStatus(os.Stdout)

	m.irunner = newInteractiveRunner(p)
	tools.InteractiveBash = m.irunner
	go m.fetchCatalogs(false)
	go func() { p.Send(cfgSyncTick{}) }()
	go func() { p.Send(scheduleTickMsg{}) }()

	tuiRunning = true
	_, err = p.Run()
	m.stopStatus()
	m.endSession("exit")
	m.cancelGoalFromContext()
	tuiRunning = false

	if m.mouseOn {
		disableClickWheelMouse(os.Stdout)
	}

	{
		disableKeyboardEnhancement(os.Stdout)
	}

	if m.mcpMgr != nil {
		m.mcpMgr.Close()
	}

	if m.lspMgr != nil {
		m.lspMgr.Close()
		tools.LSP = nil
	}

	bashrun.KillAll()
	return m.sessionID, err
}

// memoryOffNotice tells a TUI user why memory tools are gone; only LiveAgent can switch it.
const memoryOffNotice = "◐ memory is off — remember, forget and memory_manager are unavailable, saved memories are not loaded and nothing new is saved; turn memory back on in LiveAgent's memory settings"

// startupReport records the counts the startup card summarises and appends only
// the per-item problems, which are diagnostics the card has no room for.
func (m *model) startupReport() {

	if inMoshEnv() {

		m.append(dimStyle.Render("◐ shift+enter unavailable over mosh — mosh collapses it to enter (no keyboard-protocol support); use ctrl+j or alt+enter for a newline"))
	} else if inTmuxEnv() && !tmuxExtKeysCheck() {

		m.append(dimStyle.Render("◐ shift+enter needs tmux extended-keys on — k-brain couldn't set it; add `set -s extended-keys on` to ~/.tmux.conf (meanwhile ctrl+j / alt+enter insert newlines)"))
	}
	// The memory switch is shared with LiveAgent and cannot be changed here.
	if m.cfg != nil && !m.cfg.Memory.IsEnabled() {
		m.append(dimStyle.Render(memoryOffNotice))
	}
	sk, problems := skills.ScanDetailed(skills.DefaultDirs()...)
	var b strings.Builder

	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}
	m.stats.skills = len(sk)
	for _, s := range sk {
		if s.Warning != "" {
			line("  ⚠ %s: %s", s.Name, s.Warning)
			m.stats.skillWarn++
		}
	}
	for _, p := range problems {
		line("  ⚠ %s: %s", p.Path, p.Err)
		m.stats.skillWarn++
	}
	m.readMCPStats()
	for _, st := range m.mcpStatuses() {
		if st.Status == mcp.StatusFailed {
			line("  ⚠ mcp %s: failed to start", st.Name)
		}
	}
	m.refreshBanner()
	if b.Len() == 0 {
		return
	}
	m.append(errStyle.Render(strings.TrimRight(b.String(), "\n")))
}

func (m *model) mcpStatuses() []mcp.Server {
	if m.mcpMgr == nil {
		return nil
	}
	return m.mcpMgr.Statuses()
}

// readMCPStats re-reads the MCP manager, which brings servers up asynchronously,
// so the card can restate itself as statuses land.
func (m *model) readMCPStats() {
	m.stats.mcpReady, m.stats.mcpFailed, m.stats.mcpTools = 0, 0, 0
	for _, st := range m.mcpStatuses() {
		switch st.Status {
		case mcp.StatusReady:
			m.stats.mcpReady++
			m.stats.mcpTools += st.Tools
		case mcp.StatusFailed:
			m.stats.mcpFailed++
		}
	}
}

func refreshCatalogs(cfg *config.Config, force bool) map[string]config.Catalog {
	return routing.RefreshCatalogs(cfg, force)
}

func (m *model) fetchCatalogs(force bool) {
	cats := refreshCatalogs(m.cfg, force)
	if m.prog != nil {
		m.prog.Send(catalogsMsg(cats))
	}
}

func (m *model) resume(id string) error {
	meta, msgs, err := m.store.Load(id)
	if err != nil {
		return err
	}

	effort := meta.Effort
	if effort == "" {
		effort = m.agent.Effort
	}
	m.endSession("switch")
	if ag, mn, pn, err := buildAgent(m.cfg, meta.Model, meta.Provider, m.sysPrompt); err == nil {
		m.agent, m.modelName, m.provName = ag, mn, pn
	} else {
		m.agent = agent.New(m.agent.Client, m.agent.Model, m.agent.MaxTokens, m.sysPrompt, agent.WithExperimental(m.agent.Experimental()), agent.WithComputerConfig(m.cfg.Computer), agent.WithMemoryEnabled(m.cfg.Memory.IsEnabled()), agent.WithOutputLimitContinuation())
		m.agent.ModelName, m.agent.Provider = m.modelName, m.provName
		m.agent.Vision = m.supportsVision()
		m.agent.ContextLimit = m.contextLimitFor(m.provName, m.agent.Model)
	}
	m.applyCompactModel()
	m.applyTaskModel()
	m.agent.CompactThreshold = compactThresholdFor(m.cfg)
	m.wireTasks()

	m.agent.Tasks().SetSessionID(meta.ID)
	m.agent.SetSessionID(meta.ID)

	if tasks, terr := m.store.LoadTasks(meta.ID); terr == nil {
		for _, st := range tasks {
			status := agent.TaskStatus(st.Status)
			if status == agent.TaskRunning {
				status, st.Report = agent.TaskError, "interrupted — k-brain exited before this subagent finished"
			}
			m.agent.RestoreTask(agent.BackgroundTask{
				ID: st.ID, Description: st.Description, Prompt: st.Prompt,
				Status: status, Report: st.Report,
				StartedAt: st.StartedAt, EndedAt: st.EndedAt,
				Restored: true,
			})
		}
	} else {
		config.LogEvent("session.task", "load failed: "+terr.Error())
	}
	if err := m.loadHistory(meta.ID); err != nil {
		return err
	}
	msgs = m.agent.Messages[1:]
	m.agent.LoadTodosJSON(m.store.Todos(meta.ID))
	m.snapshots = m.store.Snapshots(meta.ID)

	m.agent.RestoreUsage(meta.UsageSummary(msgs))
	if slices.Contains(m.effortsFor(), effort) {
		m.agent.Effort = effort
	}
	m.sessionID = meta.ID
	m.sessTitle = meta.Title
	m.titled = true
	bashrun.SetMarkers(meta.ID, m.agent.Model)
	m.saved = len(m.agent.Messages)

	seen := make(map[string]bool, len(m.hist))
	for _, h := range m.hist {
		seen[h] = true
	}
	for _, msg := range msgs {

		text := msg.TextContent()
		if msg.Role == "user" && msg.Authored && !seen[text] {
			seen[text] = true
			m.hist = append(m.hist, text)
		}
	}
	m.histIdx = len(m.hist)
	m.resetTranscript()
	m.future = nil
	m.goal = meta.Goal
	m.goalRounds = 0
	m.append(dimStyle.Render(fmt.Sprintf("resumed %s · %s · %s @ %s", meta.ID, meta.Title, m.modelName, m.provName)))
	interrupted := 0
	for _, msg := range msgs {
		if msg.Role == "tool" && strings.HasPrefix(msg.Content, "Error: tool call interrupted") {
			interrupted++
		}
	}
	if interrupted > 0 {
		m.append(dimStyle.Render(fmt.Sprintf("⚠ %d tool call(s) were interrupted when this session last ended; the model knows and can retry them.", interrupted)))
	}
	if m.goal != "" {
		m.append(dimStyle.Render("◎ goal restored — /goal resume to keep working on it"))
	}
	m.seedTranscript(msgs, 1)
	return nil
}

func (m *model) continueRecent() error {
	meta, err := m.store.LatestInDir(cwd())
	if errors.Is(err, session.ErrNotFound) {
		m.append(dimStyle.Render("(no previous session in this directory — starting fresh)"))
		return nil
	}
	if err != nil {
		return err
	}
	return m.resume(meta.ID)
}

func (m *model) persist() bool {
	if m.store == nil {
		return true
	}
	if m.busy {
		return false
	}
	msgs := m.agent.MessagesSnapshot()
	if m.sessionID == "" && len(msgs) <= 1 && len(m.historyEvents) == 0 {
		return true
	}
	if err := m.saveHistory(msgs); err != nil {
		config.LogEvent("session.save", "FAILED id="+m.sessionID+": "+err.Error())
		m.append(errStyle.Render("session save failed: " + err.Error()))
		return false
	}
	m.bindSessionIdentity()

	_ = m.store.SetGoal(m.sessionID, m.goal)
	_ = m.store.SetEffort(m.sessionID, m.agent.Effort)
	_ = m.store.SetTodos(m.sessionID, m.agent.TodosJSON())

	if m.sessTitle == "" {
		if meta, _, err := m.store.Load(m.sessionID); err == nil {
			m.sessTitle = meta.Title
		}
	}
	return true
}

func (m *model) setTheme(theme string) {
	if theme != "light" && theme != "dark" {
		theme = "auto"
	}
	how := m.applyTheme(theme)
	m.themeHow = how

	m.cfg.Theme = theme
	if theme == "auto" {
		m.cfg.Theme = ""
	}
	if m.cfgExtra == nil {
		m.cfgExtra = map[string]string{}
	}
	if theme == "auto" {
		delete(m.cfgExtra, "theme")
	} else {
		m.cfgExtra["theme"] = theme
	}
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
	}
	m.refreshVP()
	if theme == "auto" {
		// Auto mode resolves from a detection source the user did not pick, so
		// name it. An explicit light/dark switch needs no line: the palette
		// change is visible on screen already.
		m.append(dimStyle.Render(fmt.Sprintf("◐ theme: %s (auto: %s)", CurrentTheme(), how)))
	}
}

func (m *model) applyTheme(theme string) (how string) {
	switch theme {
	case "light":
		SetLightTheme(true)
		lipgloss.SetHasDarkBackground(false)
		setSchemeOverride("light")
	case "dark":
		SetLightTheme(false)
		lipgloss.SetHasDarkBackground(true)
		setSchemeOverride("dark")
	default:
		setSchemeOverride("")
		how = detectColorScheme()
	}
	return how
}

func (m *model) setEffort(lv string) {
	m.agent.Effort = lv
	m.cfg.DefaultEffort = lv
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
	}
	if m.store != nil && m.sessionID != "" {
		_ = m.store.SetEffort(m.sessionID, lv)
	}
}

func (m *model) resetEffort(lv string) {
	m.agent.Effort = lv
	if m.store != nil && m.sessionID != "" {
		_ = m.store.SetEffort(m.sessionID, lv)
	}
}

func (m *model) setGoal(goal string) {
	m.cancelGoalFromContext()
	m.goal = goal
	m.goalRounds = 0
	if m.store != nil && m.sessionID != "" {
		_ = m.store.SetGoalMemory(m.sessionID, goal, !m.cfg.Memory.IsEnabled())
	}
}

func buildAgent(cfg *config.Config, modelName, provName, sysPrompt string) (*agent.Agent, string, string, error) {
	route, err := routing.ResolveRoute(cfg, modelName, provName, true)
	if err != nil {
		return nil, "", "", err
	}

	ag := agent.New(route.Client, route.APIModel, route.MaxOutput, sysPrompt, agent.WithExperimental(cfg.Experimental), agent.WithComputerConfig(cfg.Computer), agent.WithMemoryEnabled(cfg.Memory.IsEnabled()), agent.WithOutputLimitContinuation())
	if err := ag.SetModel(route.AgentModel()); err != nil {
		return nil, "", "", err
	}
	ag.WorkingDir = cwd()
	ag.Hooks = hooks.New(cfg.Hooks)
	ag.WorktreeSubagents = cfg.WorktreeSubagents != nil && *cfg.WorktreeSubagents

	ag.BrowserDisabled = cfg.Browser.Enabled != nil && !*cfg.Browser.Enabled

	ag.ComputerDisabled = cfg.Computer.Enabled != nil && !*cfg.Computer.Enabled
	if !ag.ComputerDisabled {
		defaultDeny := cfg.Computer.DefaultDeny != nil && *cfg.Computer.DefaultDeny
		tools.ComputerPolicy = computer.NewPolicy(cfg.Computer.Allow, cfg.Computer.Deny, defaultDeny)
	}
	if !ag.BrowserDisabled && tools.Browser == nil {
		mode := browser.ModeLive
		switch cfg.Browser.Mode {
		case "dedicated":
			mode = browser.ModeDedicated
		case "headless":
			mode = browser.ModeHeadless
		case "extension":
			mode = browser.ModeExtension
		}
		tools.Browser = browser.NewManager(mode)
		if cfg.Browser.CDPURL != "" {
			_ = os.Setenv("K_BRAIN_CDP_URL", cfg.Browser.CDPURL)
		}
		browser.AllowPrivateURLs = cfg.Browser.AllowPrivateURLs
	}
	return ag, route.ModelName, route.ProviderName, nil
}

func (m *model) Init() tea.Cmd {
	// No textarea.Blink: the cursor is static, so there is no blink loop to
	// start and nothing repaints while the composer sits idle.
	var cmds []tea.Cmd

	if inTmuxEnv() {

		cmds = append(cmds, themePollTick())
	}
	if m.initialPrompt != "" {

		cmds = append(cmds, func() tea.Msg { return initialPromptMsg{} })
	}
	cmds = append(cmds, fetchGitStatus())
	return tea.Batch(cmds...)
}

type (
	themePollMsg struct{}
	themeSyncMsg struct {
		light, ok bool
	}
)

func themePollTick() tea.Cmd {
	return tea.Tick(10*time.Second, themePollFire)
}

func themePollFire(time.Time) tea.Msg { return themePollMsg{} }

func pollClientTheme() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{client_theme}").Output()
	s := strings.TrimSpace(string(out))
	return themeSyncMsg{light: s == "light", ok: err == nil && (s == "light" || s == "dark")}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func fmtTok(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return strconv.Itoa(n)
	}
}

func fmtUsage(u ai.Usage) string {
	if c := u.Cached(); c > 0 {
		return fmt.Sprintf("%s(%s)/%s tok", fmtTok(u.PromptTokens), fmtTok(c), fmtTok(u.CompletionTokens))
	}
	return fmt.Sprintf("%s/%s tok", fmtTok(u.PromptTokens), fmtTok(u.CompletionTokens))
}

func fmtCost(d float64) string {
	if d >= 1 {
		return fmt.Sprintf("$%.2f", d)
	}
	return fmt.Sprintf("$%.4f", d)
}

func cwd() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "?"
}

func (m *model) Update(msg tea.Msg) (_ tea.Model, statusCmd tea.Cmd) {
	defer m.layout()
	defer func() { statusCmd = m.statusAfter(statusCmd) }()

	if vp, ok := msg.(viewProbe); ok {
		vp.fn(m)
		return m, nil
	}
	if c, ok := m.statusBefore(msg); ok {
		return m, c
	}
	if r, cmd, ok := m.surfaceMsg(msg); ok {
		return r, cmd
	}
	switch msg := msg.(type) {
	case initialPromptMsg:
		if m.initialPrompt == "" || m.busy {
			return m, nil
		}
		text := m.initialPrompt
		m.initialPrompt = ""
		m.hist = append(m.hist, text)
		m.histIdx = len(m.hist)
		return m.submit(text)

	case cfgSyncTick:
		return m.cfgSync()

	case cfgSyncMsg:
		m.applyCfgSync(msg)
		return m, nil

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		w := msg.Width

		resized := w != m.width
		m.width, m.height = w, msg.Height

		// Geometry is unknown until the next render: park every screen-row offset
		// out of reach so a stray click cannot land on a stale row, and drop the
		// turn floor, which was measured against the old geometry.
		m.viewTop, m.frameTop = 1<<30, 1<<30
		m.frameH, m.effortY, m.turnFloorH = 0, -1, 0
		m.input.SetWidth(w - 2)
		if resized {
			m.refreshVP()
		}
		return m, nil

	case themePollMsg:
		if m.cfg.Theme != "" {
			return m, themePollTick()
		}
		return m, tea.Batch(pollClientTheme, themePollTick())

	case themeSyncMsg:
		if !msg.ok || m.cfg.Theme != "" {
			return m, nil
		}
		mdMu.Lock()
		same := mdKnown && mdLight == msg.light
		mdMu.Unlock()
		if same {
			return m, nil
		}

		SetLightTheme(msg.light)
		lipgloss.SetHasDarkBackground(!msg.light)
		bgCache = bgResult{light: msg.light, valid: true}

		m.refreshVP()
		word := "dark"
		if msg.light {
			word = "light"
		}
		m.append(dimStyle.Render("◐ theme: auto → " + word + " (terminal appearance changed)"))
		return m, nil

	case titleMsg:
		if m.store != nil && msg.sessionID != "" && msg.title != "" {
			if meta, _, err := m.store.Load(msg.sessionID); err == nil && meta.Title == msg.previousTitle {
				if err := m.store.SetTitle(msg.sessionID, msg.title); err == nil && m.sessionID == msg.sessionID {
					m.sessTitle = msg.title
				}
			}
		}
		return m, nil

	case permRequest:
		m.receivePermission(msg)
		return m, nil

	case permClose:
		if m.permDialog != nil && m.permDialog.reply == msg.reply {
			m.permDialog = nil
		}
		return m, nil

	case askRequest:
		m.openAsk(msg)
		return m, nil

	case askClose:
		if m.askDialog != nil && m.askDialog.reply == msg.reply {
			m.askDialog = nil
		}
		return m, nil

	case elicitRequest, elicitClose:
		m.elicitMsg(msg)
		return m, nil

	case planRequest, planClose:
		m.planMsg(msg)
		return m, nil

	case selScrollTick:

		return m, m.selEdgeScroll()

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC && m.sel != nil && m.sel.done && !m.busy {
			// With a finished selection, Ctrl+C copies it again instead of
			// arming quit; the next Ctrl+C behaves as usual.
			cmd := m.copySelection()
			m.sel = nil
			return m, cmd
		}
		m.sel = nil
		return m.key(msg)

	case tea.MouseMsg:
		if r, cmd, ok := m.transcriptMouse(msg); ok {
			return r, cmd
		}

		if msg.Shift {
			return m, nil
		}
		if handled, cmd := m.handleMouseSelect(msg); handled {
			return m, cmd
		}

		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft &&
			m.effortY >= 0 && msg.Y == m.effortY && msg.X >= m.effortX {
			m.setEffort(nextEffort(m.effortsFor(), m.agent.Effort))
			return m, nil
		}
		if m.taskVP != nil {

			if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
				var cmd tea.Cmd
				m.taskVP.vp, cmd = m.taskVP.vp.Update(msg)
				return m, cmd
			}
			return m, nil
		}
		if m.picker == nil && m.mpicker == nil && m.palette == nil {

			if top, n := m.dockTop(), len(m.dockTasks()); n > 0 && msg.Y >= top && msg.Y < top+m.dockRows-m.dockSkip {
				if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
					m.tasksFocus = true
					m.taskExpanded = false
					if msg.Button == tea.MouseButtonWheelUp {
						m.taskSel = max(m.taskSel-1, 0)
					} else {
						m.taskSel = min(m.taskSel+1, n-1)
					}
					return m, nil
				}
				if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
					if m.tasksFocus && len(m.dockOffsets) > 0 {
						row := msg.Y - top
						if row >= m.dockTaskRows {

							return m, nil
						}

						sel := m.taskSel
						for i, off := range m.dockOffsets {
							if off <= row {
								sel = m.dockLo + i
							}
						}
						m.taskSel = min(sel, n-1)
						m.taskExpanded = !m.taskExpanded
						return m, nil
					}
					m.tasksFocus = true
					m.taskExpanded = false
					return m, nil
				}
			}

			if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft &&
				msg.Y >= m.viewTop && m.palette == nil {
				m.clickAt(msg.X, msg.Y)
				return m, nil
			}
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			m.follow = m.vp.AtBottom()
			return m, cmd
		}
		return m, nil

	case textMsg:
		return m.onText(msg)

	case thinkMsg:
		return m.onThink(msg)

	case toolCallMsg:
		return m.onToolCall(msg)

	case toolStartMsg:
		return m.onToolStart(msg)

	case toolOutputMsg:
		return m.onToolOutput(msg)

	case toolEndMsg:
		return m.onToolEnd(msg)

	case toolResultMsg, toolTickMsg, hostedSearchMsg, todosMsg:
		return m.onToolView(msg)

	case promptEditedMsg:
		m.applyPromptEdit(msg)
		return m, nil

	case brainEditedMsg:
		if msg.err != nil {
			m.append(errStyle.Render("/brain: editor failed: " + msg.err.Error()))
		} else if n := len(config.BrainInstructions()); n > 0 {
			m.append(dimStyle.Render("✓ brain.md saved — standing instructions updated (" + strconv.Itoa(n) + " chars)"))
		} else {
			m.append(dimStyle.Render("brain.md saved — no standing instructions set (all comments)"))
		}
		return m, nil

	case systemEditedMsg:
		if msg.err != nil {
			m.append(errStyle.Render("/system: editor failed: " + msg.err.Error()))
		} else if n := len(config.SystemInstructions()); n > 0 {
			m.append(dimStyle.Render("✓ system.md saved — system prompt updated (" + strconv.Itoa(n) + " chars)"))
		} else {
			m.append(dimStyle.Render("system.md saved — default system prompt will be used"))
		}
		return m, nil

	case interactiveStartMsg:

		m.flushThink()
		m.flushCurrent()
		m.iactive = &interactive{keys: msg.keys}
		m.append(toolStyle.Render("⚒ bash ") + dimStyle.Render("(interactive — type to respond, 15s inactivity timeout)"))
		return m, nil

	case interactiveOutMsg:
		if m.iactive == nil {
			return m, nil
		}
		m.iactive.output += msg.chunk

		m.iactive.await = false
		return m, nil

	case interactiveAwaitMsg:
		if m.iactive == nil {
			return m, nil
		}
		m.iactive.await = true
		m.iactive.awaitcd = msg.secsLeft
		return m, nil

	case interactiveDoneMsg:
		if m.iactive != nil {

			lines := strings.Split(strings.TrimRight(msg.output, "\n"), "\n")

			preview := lines
			if len(preview) > 5 {
				preview = preview[:5]
			}
			out := dimStyle.Render("  " + strings.Join(preview, "\n  "))
			if len(lines) > 5 {
				out += dimStyle.Render(fmt.Sprintf("\n  … +%d lines", len(lines)-5))
			}
			if msg.exit != "" {
				out += "\n" + dimStyle.Render("  ("+msg.exit+")")
			}
			m.append(out)
			m.iactive = nil
		}
		return m, nil

	case steeredMsg:
		return m.onSteered(msg)

	case shellDoneMsg:

		m.flushThink()
		m.flushCurrent()
		m.applyShellDone(msg)
		return m, nil

	case reviewMsg:
		m.flushThink()
		m.flushCurrent()
		m.busy = false
		m.cancel = nil
		if errors.Is(msg.err, context.Canceled) {
			m.append(dimStyle.Render("(review interrupted)"))
		} else if msg.err != nil {
			m.append(errStyle.Render("review failed: " + msg.err.Error()))
		} else {
			title := "◎ review"
			if msg.fix {
				title += " (fix mode)"
			}
			m.append(accentStyle.Render(title) + "\n" + msg.result)
		}
		return m, nil

	case btwMsg:
		m.btwBusy = false
		if errors.Is(msg.err, context.Canceled) {
			m.append(dimStyle.Render("(side question interrupted)"))
		} else if msg.err != nil {
			m.append(errStyle.Render("/btw failed: " + msg.err.Error()))
		} else {
			m.append(accentStyle.Render("◎ btw") + "\n" + msg.result)
		}
		return m, nil

	case goalFromContextMsg:
		return m.finishGoalFromContext(msg)

	case compactStartMsg:
		return m.onCompactStart(msg)

	case contextStartMsg:
		return m.onContextStart(msg)

	case compactMsg:
		return m.onCompact(msg)

	case turnDoneMsg:
		return m.onTurnDone(msg)

	case catalogsMsg:
		m.updateCatalogs(msg)
		return m, nil

	case noticeMsg:
		m.append(dimStyle.Render(string(msg)))
		return m, nil

	case gitStatusMsg:
		m.git = msg.status
		return m, nil

	case usageMsg:

		m.lastResp = ai.Usage(msg)
		return m, nil

	case quitArmMsg:
		m.quit1 = false
		return m, nil

	case escArmMsg:
		m.esc1 = false
		m.escClr = false
		return m, nil

	case copyDoneMsg:
		return m, m.showNotice(m.copyNotice(msg))

	case noticeExpiredMsg:
		if uint64(msg) == m.noticeGeneration {
			m.transientNotice = ""
		}
		return m, nil

	case taskUpdateMsg:
		return m, m.onSubagentsChanged()

	case orphanSteerMsg:

		if !m.busy {
			if _, ok := parseNotice(string(msg)); ok {
				return m.submitNotice(string(msg))
			}
			return m.submitTurn(string(msg), true)
		}
		return m, nil

	case waitWakeMsg:

		if m.busy {
			// The running turn echoes it back through OnSteer as a notice row.
			m.agent.Notify(string(msg))
			return m, nil
		}
		return m.submitNotice(string(msg))

	case subProgressMsg:
		m.applySubProgress(msg)
		m.refreshSubagentRows()
		return m, m.ensureSubTick()

	case subChangedMsg:
		m.bindForegroundRow(msg)
		return m, m.onSubagentsChanged()

	case subTickMsg:
		m.subTicking = false
		m.refreshSubagentRows()
		return m, m.ensureSubTick()

	case mcpStatusMsg:

		if m.mcpMgr != nil {
			m.readMCPStats()
			m.refreshBanner()
			if m.palette != nil {
				if pp := m.palette.top(); pp != nil && pp.kind == panelMCP {
					m.refreshMCPPanel(pp)
				}
			}
			if m.mcpSeen == nil {
				m.mcpSeen = map[string]bool{}
			}
			for _, srv := range m.mcpMgr.Statuses() {
				if m.mcpSeen[srv.Name] || srv.Status == mcp.StatusConnecting {
					continue
				}
				m.mcpSeen[srv.Name] = true
				switch srv.Status {
				case mcp.StatusReady:
					m.append(dimStyle.Render(fmt.Sprintf("✦ mcp: %s ready (%d tools)", srv.Name, srv.Tools)))
				case mcp.StatusFailed:
					line := fmt.Sprintf("✗ mcp: %s failed: %s", srv.Name, srv.Err)
					if srv.Source != "" {
						line += " (" + srv.Source + ")"
					}
					m.append(errStyle.Render(line + fmt.Sprintf(" (/mcp %s reconnect)", srv.Name)))
				case mcp.StatusDisabled:
					m.append(dimStyle.Render(fmt.Sprintf("○ mcp: %s disabled", srv.Name)))
				}
			}
		}
		return m, nil

	case taskEventMsg:
		if m.applyTaskEvent(msg) {
			m.refreshTaskVP()
		}
		return m, nil

	case taskStreamReadyMsg:
		if msg.view == nil {
			return m, nil
		}
		events, dropped := msg.view.stream.drain()
		if m.taskVP != msg.view {
			return m, nil
		}
		if dropped {
			msg.view.buf.WriteString("\n" + dimStyle.Render("[earlier live output dropped while the interface was busy]") + "\n")
		}
		for _, event := range events {
			m.applyTaskEvent(event)
		}
		m.refreshTaskVP()
		return m, nil

	case imageMsg:
		switch {
		case msg.err != nil:
			m.append(errStyle.Render("image paste failed: " + msg.err.Error()))
		case msg.path == "":
			m.append(dimStyle.Render("(no image on clipboard)"))
		default:

			m.imageSeq++
			img := pastedImage{n: m.imageSeq, path: msg.path, display: msg.display}
			m.images = append(m.images, img)
			m.input.InsertString(img.chipText() + " ")
			m.refreshMenu()
		}
		return m, nil

	case spinner.TickMsg:
		if !m.busy {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case scheduleTickMsg:
		return m, tea.Batch(scheduleTick(), m.fireDueSchedules())
	}

	if s, ok := msg.(interface{ String() string }); ok {
		if isShiftEnterString(s.String()) {
			return m.insertNewline()
		}

		if m.sel != nil && m.sel.done && isSuperC(s.String()) {
			return m, m.copySelection()
		}
		if k, ok := csiUKey(s.String()); ok {
			m.sel = nil
			return m.key(k)
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) nowFn() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (m *model) contextLimitFor(provName, apiID string) int {
	mdl := config.Model{}
	if m.cfg != nil {
		mdl = m.cfg.Models[apiID].ForProvider(provName)
		if selected, ok := m.cfg.Models[m.modelName]; ok {
			id := selected.ID
			if id == "" {
				id = m.modelName
			}
			if id == apiID && provName == m.provName {
				mdl = selected.ForProvider(provName)
			}
		}
	}
	contextLimit, _ := routing.TokenLimits(mdl, m.catalogs[provName], apiID)
	return contextLimit
}

func compactThresholdFor(cfg *config.Config) float64 {
	pct := cfg.CompactPct
	if pct == 0 {
		pct = config.DefaultCompactPct
	}
	return float64(min(max(pct, 10), 90)) / 100
}

func (m *model) applyCompactModel() {
	m.agent.CompactClient, m.agent.CompactModel, m.agent.CompactProvider = nil, "", ""
	cm := m.compactModel
	if cm == "" {
		cm = config.DefaultCompactModel
	}

	compactProv := m.compactProv
	if compactProv == "" {
		if mdl, ok := m.cfg.Models[cm]; ok && len(mdl.Providers) > 0 {
			compactProv = mdl.Providers[0]
		}
	}
	prov, mdl, apiID, err := m.cfg.Resolve(cm, compactProv)
	if err != nil {
		// Fall back silently: compaction runs on the current model. The
		// fallback is the same behaviour the user already sees everywhere else.
		return
	}
	if compactProv == "" {
		compactProv = m.cfg.DefaultProvider
		if len(mdl.Providers) > 0 {
			compactProv = mdl.Providers[0]
		}
	}
	client, err := routing.ClientForProvider(prov, compactProv, m.cfg.MaxRetries)
	if err == nil {
		m.agent.CompactClient = client
		m.agent.CompactModel = apiID
		m.agent.CompactProvider = compactProv
	}
}

func (m *model) wireTasks() {
	if m.cfg != nil {
		m.agent.Hooks = hooks.New(m.cfg.Hooks)
	}
	m.agent.SandboxPolicy = m.sandboxPolicy
	if m.agent.WorkingDir == "" {
		m.agent.WorkingDir = cwd()
	}
	if m.pluginMgr != nil {
		m.agent.PluginHook = m.pluginMgr.RunHook
		m.agent.SetPluginTools(pluginToolAdapters(m.pluginMgr))
	}
	m.agent.SetPlanMode(m.permissionMode == "plan")

	st := m.store
	m.agent.Tasks().OnRecord = func(sessionID string, t *agent.BackgroundTask) {
		if st == nil || sessionID == "" {
			return
		}
		if err := st.SaveTask(sessionID, session.Task{
			ID: t.ID, Description: t.Description, Prompt: t.Prompt,
			Status: string(t.Status), Report: t.Report,
			StartedAt: t.StartedAt, EndedAt: t.EndedAt,
		}); err != nil {
			config.LogEvent("session.task", "save failed: "+err.Error())
		}

		if t.Status != agent.TaskRunning && t.SubMessages != nil {
			id, err := st.SaveSubagentTranscript(sessionID, t.ID, t.SubMessages, t.SubModel, "")
			if err != nil {
				config.LogEvent("session.task", "transcript save failed: "+err.Error())
			} else if id != "" {

				u := t.SubUsage
				_ = st.SetUsage(id, ai.UsageSummary{Total: u, Models: t.SubModelUsage, Subagents: t.SubSubUsage})
			}
		}
	}
	m.agent.Tasks().SetSessionID(m.sessionID)
	m.agent.SetSessionID(m.sessionID)
	m.fgReg().clearSettled()
	m.subAct, m.subStop = nil, nil
	m.wireProgram()

	if m.mcpMgr != nil {
		m.agent.SetMCPTools(m.mcpMgr.Tools())
	}
}

// wireProgram connects the agent's asynchronous callbacks to the running
// program. The first agent is built before the program exists, so Run calls it
// again once the program is up.
func (m *model) wireProgram() {
	m.wireWaits()
	if m.prog == nil {
		return
	}
	m.agent.OnOrphanedSteer = func(text string) {

		go m.prog.Send(orphanSteerMsg(text))
	}
	m.agent.Tasks().OnChange = func(*agent.BackgroundTask) {

		go m.prog.Send(taskUpdateMsg{})
	}
	p := m.prog
	m.agent.Tasks().OnEvents = func(id string) agent.Events {
		return agent.Events{
			OnToolStart: func(_, n, a string) { p.Send(subProgressMsg{id: id, tool: subToolLabel(n, a)}) },
			OnUsage:     func(u ai.Usage) { p.Send(subProgressMsg{id: id, usage: u}) },
		}
	}
	m.fgReg().setSend(func(msg tea.Msg) { p.Send(msg) })
}

func (m *model) wireWaits() {
	if m.prog == nil {
		return
	}
	m.agent.Waits().OnWake = func(text string) {
		go m.prog.Send(waitWakeMsg(text))
	}
}

func (m *model) runningTasks() int {
	n := 0
	for _, t := range m.agent.Tasks().List() {
		if t.Status == agent.TaskRunning {
			n++
		}
	}
	return n
}

func (m *model) tasksView() string {
	tasks := m.agent.Tasks().List()
	if len(tasks) == 0 {
		return dimStyle.Render("(no background subagents)")
	}
	var b strings.Builder
	b.WriteString(dimStyle.Render(fmt.Sprintf("background subagents (%d):", len(tasks))))
	for _, t := range tasks {
		icon := "⏳"
		switch t.Status {
		case agent.TaskDone:
			icon = "✓"
		case agent.TaskError, agent.TaskCancelled:
			icon = "✗"
		}
		line := fmt.Sprintf("  %s %s  %s", icon, t.ID, t.Description)
		if t.Restored {
			line += dimStyle.Render("  (restored)")
		}
		if t.Status == agent.TaskRunning {
			line += dimStyle.Render(fmt.Sprintf("  (%ds)", int(time.Since(t.StartedAt).Seconds())))
		}
		b.WriteString("\n" + toolStyle.Render(line))
		if t.Status != agent.TaskRunning {
			report := t.Report
			if len(report) > 200 {
				report = report[:200] + "…"
			}
			b.WriteString("\n" + dimStyle.Render("      "+strings.ReplaceAll(report, "\n", " ")))
		}
	}
	return b.String()
}

func (m *model) switchModel(name, prov string, persist bool) {
	if err := m.selectModel(name, prov); err != nil {
		m.append(errStyle.Render(err.Error()))
		return
	}
	if !slices.Contains(m.effortsFor(), m.agent.Effort) {
		m.resetEffort("")
	}
	mn, pn := m.modelName, m.provName
	if persist {
		m.cfg.DefaultModel, m.cfg.DefaultProvider = mn, pn
		if err := m.cfg.Save(); err != nil {
			m.append(errStyle.Render("config save failed: " + err.Error()))
		}
		m.append(dimStyle.Render("→ " + mn + "@" + pn))
	} else {
		m.append(dimStyle.Render("→ " + mn + "@" + pn + " (this session only)"))
	}
}

func wrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	return ansi.Wrap(s, width, " ")
}

func truncLine(s string, width int) string {
	if width > 0 && len(s) > width {
		return s[:width-1] + "…"
	}
	return s
}
