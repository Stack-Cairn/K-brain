package tui

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
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
	"github.com/muesli/termenv"
)

var (
	youStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "21", Dark: "12"}).Bold(true)
	botStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "90", Dark: "13"}).Bold(true)
	toolStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "136", Dark: "11"})
	dimStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "240", Dark: "245"})
	errStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "124", Dark: "9"})
	growStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "10"})

	thinkingStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "240", Dark: "245"}).Italic(true)
	chromeStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "239", Dark: "245"})
	accentStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "33", Dark: "75"}).Bold(true)
	userPanel     = lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "255", Dark: "235"}).Padding(0, 1)
	shortcutStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "242", Dark: "245"})
)

var (
	glyphUser      = "❯ "
	glyphAssistant = "● "
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
}

type compactStartMsg struct {
	took, est int
}
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

type menu struct {
	head   string
	cands  []cand
	idx    int
	base   string
	cyc    bool
	cycled bool
	frozen []cand
}

type model struct {
	promptCatalog promptCatalog

	cfg           *config.Config
	agent         *agent.Agent
	modelName     string
	provName      string
	sysPrompt     string
	sandboxPolicy *sandbox.Policy
	pluginMgr     *plugins.Manager

	cfgExtra map[string]string
	cfgMod   time.Time

	input       textarea.Model
	spin        spinner.Model
	vp          viewport.Model
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
	pasteBuf string
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

	inputBodyOff     int
	viewportTop      int
	viewportRows     int
	inputLeft        int
	inputTop         int
	inputLines       []string
	vpLead           int
	viewTop          int
	viewH            int
	frameTop         int
	frameH           int
	themeHow         string
	sessTitle        string
	transientNotice  string
	noticeGeneration uint64
	compactModel     string
	compactProv      string

	updateLatest string
	effortX      int
	catalogs     map[string]config.Catalog
	mcpMgr       *mcp.Manager
	mcpSeen      map[string]bool
	lspMgr       *lsp.Manager

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

	rew    *rewindState
	esc1   bool
	escClr bool
	future []ai.Message

	namePrompt *namePrompt

	initialPrompt string
}

type initialPromptMsg struct{}

type picker struct {
	metas    []session.Meta
	idx      int
	previews map[string][2]string
}

func newInput() textarea.Model {
	ti := textarea.New()
	ti.Placeholder = inputPlaceholder
	ti.Prompt = "┃ "
	ti.SetHeight(1)
	ti.MaxHeight = 24
	ti.ShowLineNumbers = false
	ti.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("ctrl+j", "shift+enter", "alt+enter"),
		key.WithHelp("ctrl+j", "newline"),
	)

	ti.KeyMap.DeleteAfterCursor = key.NewBinding()

	ti.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ti.FocusedStyle.Placeholder = dimStyle
	ti.BlurredStyle.Placeholder = dimStyle
	ti.FocusedStyle.Prompt = botStyle
	ti.BlurredStyle.Prompt = dimStyle
	ti.Focus()
	return ti
}

var (
	tuiRunning bool
	bgCache    bgResult
)

func Run(cfg *config.Config, modelName, provName, sysPrompt, resumeID string, cautious, firstRun bool, initialPrompt string, continueMode, browseMode bool) (string, error) {

	stdinR := bufio.NewReader(os.Stdin)

	if ok, err := checkTrust(stdinR); err != nil {
		return "", err
	} else if !ok {
		return "", errors.New("folder not trusted")
	}

	if firstRun {
		if err := setupWizard(cfg, stdinR); err != nil {
			return "", err
		}
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
		cfg: cfg, agent: ag, modelName: mn, provName: pn, sysPrompt: sysPrompt,
		sandboxPolicy: cfg.Sandbox.Policy(cwd()),
		input:         ti, spin: spinner.New(spinner.WithSpinner(spinner.Dot)), follow: true, saved: 1,
		catalogs: config.LoadCatalogs(), mouseOn: mouseOn, now: time.Now, showThinking: showThinking,

		compactModel: cfg.CompactModel, compactProv: cfg.CompactProvider,
		skillScan:     func() []skills.Skill { return skills.Scan(skills.DefaultDirs()...) },
		initialPrompt: initialPrompt,
	}
	ag.SandboxPolicy = m.sandboxPolicy
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
			m.mcpMgr.SetOnChange(m.mcpOnChange())
			m.mcpMgr.Start(context.Background())
			ag.SetMCPTools(m.mcpMgr.Tools())
		}

		m.lspMgr = lsp.NewManager(lsp.FromConfigMap(cfg.LSPServers))
		tools.LSP = m.lspMgr
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

	m.themeHow = m.applyTheme(cfg.Theme)

	m.applyAppearance()

	tmuxEnableExtendedKeys()
	m.startupReport()

	opts := []tea.ProgramOption{tea.WithAltScreen()}

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

	m.irunner = newInteractiveRunner(p)
	tools.InteractiveBash = m.irunner
	go m.fetchCatalogs(false)
	go func() { p.Send(cfgSyncTick{}) }()
	go func() { p.Send(scheduleTickMsg{}) }()

	tuiRunning = true
	_, err = p.Run()
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

func (m *model) startupReport() {

	if inMoshEnv() {

		m.append(dimStyle.Render("◐ shift+enter unavailable over mosh — mosh collapses it to enter (no keyboard-protocol support); use ctrl+j or alt+enter for a newline"))
	} else if inTmuxEnv() && !tmuxExtKeysCheck() {

		m.append(dimStyle.Render("◐ shift+enter needs tmux extended-keys on — k-brain couldn't set it; add `set -s extended-keys on` to ~/.tmux.conf (meanwhile ctrl+j / alt+enter insert newlines)"))
	}
	sk, problems := skills.ScanDetailed(skills.DefaultDirs()...)
	var b strings.Builder
	var warned bool

	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}
	if len(sk) > 0 {
		line("skills: %d loaded", len(sk))
	}
	for _, s := range sk {
		if s.Warning != "" {
			line("  ⚠ %s: %s", s.Name, s.Warning)
			warned = true
		}
	}
	for _, p := range problems {
		line("  ⚠ %s: %s", p.Path, p.Err)
		warned = true
	}
	if m.mcpMgr != nil {
		sts := m.mcpMgr.Statuses()
		var parts []string
		for _, st := range sts {
			switch st.Status {
			case mcp.StatusReady:
				parts = append(parts, fmt.Sprintf("%s ✓ (%d tools)", st.Name, st.Tools))
			case mcp.StatusFailed:
				parts = append(parts, st.Name+" ✗")
				warned = true
			case mcp.StatusDisabled:
				parts = append(parts, st.Name+" ○")
			default:
				parts = append(parts, st.Name+" ◌")
			}
		}
		if len(parts) > 0 {
			line("mcp: %s", strings.Join(parts, " · "))
		}
	}
	if m.updateLatest != "" {
		line("update available: %s (run: kn update)", m.updateLatest)
		warned = true
	}
	if b.Len() == 0 {
		return
	}
	out := strings.TrimRight(b.String(), "\n")
	if warned {
		m.append(errStyle.Render(out))
	} else {
		m.append(dimStyle.Render(out))
	}
}

func enableClickWheelMouse(w *os.File) {
	fmt.Fprint(w, "\x1b[?1006h\x1b[?1002h")
}

func disableClickWheelMouse(w *os.File) {
	fmt.Fprint(w, "\x1b[?1003l\x1b[?1002l\x1b[?1000l\x1b[?1006l")
}

func enableKeyboardEnhancement(w *os.File) {
	fmt.Fprint(w, tmuxPassthrough("\x1b[>1u"))
	if inTmuxEnv() {
		fmt.Fprint(w, "\x1b[>4;1m")
	}
}

func disableKeyboardEnhancement(w *os.File) {
	fmt.Fprint(w, tmuxPassthrough("\x1b[<u"))
	if inTmuxEnv() {
		fmt.Fprint(w, "\x1b[>4;0m")
	}
}

func tmuxEnableExtendedKeys() {
	if tmuxExtendedKeysReady() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "tmux", "set", "-s", "extended-keys", "on").Run()
}

func tmuxPassthrough(seq string) string {
	if !inTmuxEnv() {
		return seq
	}
	return "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
}

func tmuxExtendedKeysReady() bool {
	if !inTmuxEnv() {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{extended-keys}").Output()
	if err != nil {
		return true
	}
	v := strings.TrimSpace(string(out))
	return v == "on" || v == "always"
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
	if ag, mn, pn, err := buildAgent(m.cfg, meta.Model, meta.Provider, m.sysPrompt); err == nil {
		m.agent, m.modelName, m.provName = ag, mn, pn
	} else {
		m.agent = agent.New(m.agent.Client, m.agent.Model, m.agent.MaxTokens, m.sysPrompt, agent.WithExperimental(m.agent.Experimental()))
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
	m.blocks = nil
	m.msgBlock = nil
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

func (m *model) seedTranscript(msgs []ai.Message, base int) {
	for i, msg := range msgs {
		bi := -1
		switch msg.Role {
		case "user":
			bi = len(m.blocks)
			m.blocks = append(m.blocks, block{kind: blockUser, text: linkifyFilePaths(msg.TextContent(), realFileExists)})
		case "assistant":
			if strings.TrimSpace(msg.TextContent()) != "" {
				bi = len(m.blocks)
				m.blocks = append(m.blocks, block{kind: blockAssistant, text: strings.TrimRight(msg.TextContent(), "\n")})
			}
			for _, tc := range msg.ToolCalls {
				m.blocks = append(m.blocks, block{kind: blockText, text: toolHeaderRow(tc.Function.Name, tc.Function.Arguments, false)})
			}
		case "tool":

			switch {
			case strings.HasPrefix(msg.Content, "Error: tool call interrupted"):
				m.blocks = append(m.blocks, block{kind: blockText, text: errStyle.Render("⚒ "+msg.Name+" ") + dimStyle.Render("— interrupted: session ended before a result was recorded")})
			default:
				if diff, _ := extractDiff(strings.TrimRight(msg.Content, "\n")); diff != "" {
					m.blocks = append(m.blocks, block{kind: blockTool, text: msg.Content})
				}
			}
		}
		for len(m.msgBlock) <= base+i {
			m.msgBlock = append(m.msgBlock, -1)
		}
		m.msgBlock[base+i] = bi
	}
	m.follow = true
	m.refreshVP()
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
		m.append(dimStyle.Render(fmt.Sprintf("◐ theme: %s (auto: %s)", CurrentTheme(), how)))
	} else {
		m.append(dimStyle.Render("◐ theme: " + CurrentTheme()))
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
		_ = m.store.SetGoal(m.sessionID, goal)
	}
}

func buildAgent(cfg *config.Config, modelName, provName, sysPrompt string) (*agent.Agent, string, string, error) {
	route, err := routing.ResolveRoute(cfg, modelName, provName, true)
	if err != nil {
		return nil, "", "", err
	}

	ag := agent.New(route.Client, route.APIModel, route.MaxOutput, sysPrompt, agent.WithExperimental(cfg.Experimental))
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

type blockKind int

const (
	blockText blockKind = iota
	blockAssistant
	blockTool
	blockToolRun
	blockToolQueued
	blockUser
)

const toolPreviewLines = 5

const minRenderWidth = 8

type block struct {
	kind     blockKind
	text     string
	expanded bool

	toolID      string
	toolRunning bool
	toolFailed  bool

	toolName string
	toolArgs string

	live string

	y0, y1 int

	hover bool

	rendered string
	lines    int
	width    int
	stale    bool
	ancient  bool
}

func (b *block) renderAt(width int) string {
	return b.renderAtMode(width, false)
}

func (b *block) renderAtMode(width int, ancient bool) string {
	if !b.stale && b.width == width && b.ancient == ancient {
		return b.rendered
	}
	if ancient && (b.kind == blockAssistant || b.kind == blockUser) {
		b.rendered = accentStyle.Render(renderAncientText(ansi.Strip(b.render(width)), width))
	} else {
		b.rendered = b.render(width)
	}
	b.lines = lipgloss.Height(b.rendered)
	b.width, b.ancient, b.stale = width, ancient, false
	return b.rendered
}

func (b block) render(width int) string {
	switch b.kind {
	case blockUser:
		return userPanel.Width(max(width-2, 1)).Render(wrap(youStyle.Render(glyphUser)+b.text, max(width-4, 1)))
	case blockAssistant:

		w := width - 2
		if w <= 0 {
			w = 80
		}
		body := indentLines(renderMarkdown(b.text, w), 2)
		return accentStyle.Render(glyphAssistant) + strings.TrimPrefix(body, "  ")
	case blockTool:

		if diff, rest := extractDiff(strings.TrimRight(b.text, "\n")); diff != "" {
			return renderDiffResult(diff, rest, b.expanded, width)
		}
		lines := strings.Split(strings.TrimRight(b.text, "\n"), "\n")

		lines[0] = "⎿ " + lines[0]
		style := dimStyle
		if strings.HasPrefix(b.text, "Error") {
			style = errStyle
		}
		if b.expanded || len(lines) <= toolPreviewLines {
			return wrap(style.Render("  "+strings.Join(lines, "\n  ")), width)
		}
		preview := lines[:toolPreviewLines]
		out := style.Render("  " + strings.Join(preview, "\n  "))
		hint := fmt.Sprintf("\n  … +%d lines (ctrl+e or click to expand)", len(lines)-toolPreviewLines)
		return wrap(out+dimStyle.Render(hint), width)
	case blockToolRun:

		if b.toolRunning || b.expanded {
			if b.live != "" && b.toolRunning {
				return wrap(b.text, width) + "\n" + wrap(dimStyle.Render("  "+b.live), width)
			}
			return wrap(b.text, width)
		}
		return ansi.Truncate(b.text, width, "…")
	default:
		return wrap(b.text, width)
	}
}

func (b *block) toggle() bool {
	if b.kind != blockTool && b.kind != blockToolRun {
		return false
	}
	b.expanded = !b.expanded
	b.stale = true
	return true
}

func (m *model) append(blocks ...string) {
	for _, s := range blocks {
		m.appendRaw(blockText, s)
	}
}

func (m *model) appendAssistantBlock(s string) {
	m.appendRaw(blockAssistant, s)
}

func (m *model) wantFollow() bool {
	return m.follow || m.vp.AtBottom()
}

func (m *model) keepFollow() {
	if m.wantFollow() {
		m.follow = true
	}
}

func (m *model) appendRaw(kind blockKind, text string) {
	m.keepFollow()
	m.blocks = append(m.blocks, block{kind: kind, text: text})
	m.refreshVP()
}

func (m *model) refreshVP() {
	if m.width == 0 {
		return
	}

	width := max(m.width, minRenderWidth)
	var b strings.Builder
	if n := len(m.blocks); n > 0 {
		b.Grow(n*24 + 1<<20)
	}
	line := 0
	for i := range m.blocks {
		if i > 0 {

			b.WriteString("\n\n")
			line++
		}
		r := m.blocks[i].renderAtMode(width, m.ancientMode)
		m.blocks[i].y0 = line
		m.blocks[i].y1 = line + m.blocks[i].lines - 1
		b.WriteString(r)
		line = m.blocks[i].y1 + 1
	}
	content := b.String()
	if pad := m.contentPad(); pad > 0 {
		content = strings.Repeat("\n", pad) + content
	}
	m.vp.SetContent(content)
	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m *model) contentPad() int {
	if len(m.blocks) == 0 {
		return m.vp.Height
	}
	h := m.blocks[len(m.blocks)-1].y1 + 1
	return max(m.vp.Height-h, 0)
}

func (m *model) viewportView() string {
	s := sanitizeView(m.vp.View())
	if m.sel != nil {
		s = m.highlightSelection(s)
	}

	lines := strings.Split(s, "\n")

	drop := max(min(m.contentPad()-m.vp.YOffset, len(lines)), 0)

	first := 0
	for first < drop && strings.TrimSpace(ansi.Strip(lines[first])) == "" {
		first++
	}
	m.vpLead = first
	lines = lines[first:]

	last := len(lines) - 1
	for last >= 0 && strings.TrimSpace(ansi.Strip(lines[last])) == "" {
		last--
	}
	return strings.Join(lines[:last+1], "\n")
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{textarea.Blink}

	if inTmuxEnv() {

		cmds = append(cmds, themePollTick())
	}
	if m.initialPrompt != "" {

		cmds = append(cmds, func() tea.Msg { return initialPromptMsg{} })
	}
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

type bgResult struct {
	light, valid bool
	r, g, b      int
	hasRGB       bool
}

func inTmuxEnv() bool {
	return os.Getenv("TMUX") != "" ||
		strings.HasPrefix(os.Getenv("TERM"), "screen") ||
		strings.HasPrefix(os.Getenv("TERM"), "tmux")
}

func procHasAncestor(pid int, want string) bool {
	for i := 0; i < 64 && pid > 1; i++ {
		comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		if err != nil {
			return false
		}
		if strings.TrimSpace(string(comm)) == want {
			return true
		}
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return false
		}

		idx := bytes.LastIndexByte(stat, ')')
		if idx < 0 {
			return false
		}
		fields := strings.Fields(string(stat[idx+1:]))
		if len(fields) < 2 {
			return false
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid == pid || ppid <= 1 {
			return false
		}
		pid = ppid
	}
	return false
}

var moshDetect = detectMosh

var tmuxExtKeysCheck = tmuxExtendedKeysReady

func inMoshEnv() bool { return moshDetect() }

func detectMosh() bool {
	if procHasAncestor(os.Getpid(), "mosh-server") {
		return true
	}
	if inTmuxEnv() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{client_pid}").Output()
		if err == nil {
			if cpid, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && cpid > 1 {
				return procHasAncestor(cpid, "mosh-server")
			}
		}
	}
	return false
}

func detectColorScheme() string {
	setScheme := func(light bool) {
		SetLightTheme(light)
		lipgloss.SetHasDarkBackground(!light)
	}
	themeEnv := os.Getenv("K_BRAIN_THEME")
	themeSource := "K_BRAIN_THEME"
	switch strings.ToLower(themeEnv) {
	case "light":
		setScheme(true)
		return themeSource
	case "dark":
		setScheme(false)
		return themeSource
	}

	if tuiRunning {
		if bgCache.valid {
			setScheme(bgCache.light)
			return "terminal query (cached from startup)"
		}
		light, ok, how := fallbackScheme(inTmuxEnv(), os.Getenv("COLORFGBG"))
		if ok {
			setScheme(light)
		} else {
			SetUnknownTheme()
		}
		return how
	}

	inTmux := inTmuxEnv()
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err == nil {
		if r := queryTerminalBackground(tty, inTmux); r.valid {
			_ = tty.Close()
			setScheme(r.light)
			bgCache = r
			if inTmux {
				return "terminal query (inside tmux)"
			}
			return "terminal query"
		}

		if !inTmux {
			type result struct{ light bool }
			done := make(chan result, 1)
			go func() {
				o := termenv.NewOutput(tty)
				done <- result{light: !o.HasDarkBackground()}
			}()
			select {
			case r := <-done:
				_ = tty.Close()
				setScheme(r.light)
				bgCache = bgResult{light: r.light, valid: true}
				return "terminal query"
			case <-time.After(300 * time.Millisecond):
			}
		}
		_ = tty.Close()
	}
	light, ok, how := fallbackScheme(inTmux, os.Getenv("COLORFGBG"))
	if ok {
		setScheme(light)
	} else {

		SetUnknownTheme()
	}
	return how
}

func fallbackScheme(inTmux bool, colorfgbg string) (light, ok bool, how string) {
	if i := strings.LastIndex(colorfgbg, ";"); i >= 0 {
		var bg int
		if _, err := fmt.Sscanf(colorfgbg[i+1:], "%d", &bg); err == nil {

			return bg == 7 || bg >= 8, true, "COLORFGBG (query failed)"
		}
	}
	if inTmux {
		return false, false, "undetermined (no OSC 11 reply through tmux — outer terminal doesn't answer it (e.g. mosh), or tmux <3.4 without `allow-passthrough on`) — neutral default"
	}
	return false, false, "undetermined (query timed out) — neutral default"
}

func (m *model) inputContentHeight() int {
	contentWidth := max(

		m.input.Width()-2, 1,
	)
	h := 0
	for line := range strings.SplitSeq(m.input.Value(), "\n") {
		h += max(1, (lipgloss.Width(line)+contentWidth-1)/contentWidth)
	}
	return h
}

func (m *model) growInput() {
	if m.width <= 0 {
		return
	}
	h := max(1, min(m.inputContentHeight(), m.input.MaxHeight))
	if h == m.input.Height() {
		return
	}
	if h < m.input.Height() {
		m.input.SetHeight(h)
		return
	}
	val := m.input.Value()
	ti := newInput()

	ti.Prompt = m.input.Prompt
	ti.Placeholder = m.input.Placeholder
	ti.FocusedStyle = m.input.FocusedStyle
	ti.BlurredStyle = m.input.BlurredStyle
	ti.SetWidth(m.input.Width() + lipgloss.Width(ti.Prompt))
	ti.SetHeight(h)
	ti.SetValue(val)
	ti.CursorEnd()
	m.input = ti
	m.input.Focus()
}

func (m *model) layout() {
	m.growInput()

	chrome := 8 + m.input.Height()

	if m.iactive != nil {

		chrome -= m.input.Height()
	}
	if m.busy {
		chrome += 2
	}

	if m.iactive != nil {
		chrome += lipgloss.Height(m.interactiveView()) + 1
	}
	if m.askDialog != nil {
		chrome += lipgloss.Height(m.askView()) + 1
	}
	if m.permDialog != nil {
		chrome += lipgloss.Height(m.permView()) + 1
	}
	if m.menu != nil {

		chrome += lipgloss.Height(m.menuView()) + 1
	}
	if len(m.queue) > 0 {
		chrome += len(m.queue) + 1
	}
	if m.rew != nil {
		chrome += lipgloss.Height(m.rewindView()) + 1
	}
	if m.quit1 {
		chrome++
	}
	if m.escClr || (m.esc1 && m.rew == nil && m.namePrompt == nil) {
		chrome++
	}
	if m.taskVP != nil {
		m.refreshTaskVP()
	}
	m.dockRows = 0
	if dock := m.tasksDock(); dock != "" {
		m.dockRows = lipgloss.Height(dock)

		m.dockSkip = 0
		if m.tasksFocus {
			m.dockSkip++
		}
		chrome += m.dockRows
	}

	if liveRows := m.liveAreaRows(); liveRows > 0 {
		chrome += liveRows + 1
	}

	w, h := max(m.width, minRenderWidth), max(m.height-chrome, 1)
	if m.vp.Width != w || m.vp.Height != h {
		m.vp.Width, m.vp.Height = w, h
		m.refreshVP()
	}
}

func (m *model) streamCap(fixedChrome int) int {
	avail := m.height - fixedChrome - 1
	if avail < 2 {
		return 0
	}
	floor := min(minTranscriptRows, avail-1)
	return avail - floor
}

func (m *model) liveAreaRows() int {

	switch {
	case m.curThink != "":
		if cv := m.thinkViewCapped(); cv != "" {
			return lipgloss.Height(cv)
		}
	case m.current != "":
		if cv := m.currentViewCapped(); cv != "" {
			return lipgloss.Height(cv)
		}
	}
	return 0
}

func (m *model) dockTop() int {
	bottom := m.height
	if m.viewH > 0 {
		bottom = m.viewTop + m.viewH
	}
	return bottom - 2 - m.dockRows + m.dockSkip
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	defer m.layout()

	if vp, ok := msg.(viewProbe); ok {
		vp.fn(m)
		return m, nil
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

		m.viewTop, m.frameTop = 1<<30, 1<<30
		m.frameH = 0
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
		m.askDialog = &askDialog{req: msg.req, reply: msg.reply, picked: map[int]bool{}}
		return m, nil

	case askClose:
		if m.askDialog != nil && m.askDialog.reply == msg.reply {
			m.askDialog = nil
		}
		return m, nil

	case selScrollTick:

		return m, m.selEdgeScroll()

	case tea.KeyMsg:
		m.sel = nil
		return m.key(msg)

	case tea.MouseMsg:

		if msg.Shift {
			return m, nil
		}
		if handled, cmd := m.handleMouseSelect(msg); handled {
			return m, cmd
		}

		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft &&
			msg.Y == m.viewTop && msg.X >= m.effortX {
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
				msg.Y-m.viewTop > 1 && m.palette == nil {
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
		m.flushThink()
		m.current += string(msg)

		if i := strings.LastIndexByte(m.current, '\n'); i >= 0 {
			done := m.current[:i]
			m.current = m.current[i+1:]
			m.appendAssistant(done)
		}
		return m, nil

	case thinkMsg:
		if m.showThinking {

			m.flushCurrent()
			m.curThink += string(msg)
			if i := strings.LastIndexByte(m.curThink, '\n'); i >= 0 {
				done := m.curThink[:i]
				m.curThink = m.curThink[i+1:]
				m.appendThink(done)
			}
		}
		return m, nil

	case toolCallMsg:
		m.flushThink()
		m.flushCurrent()
		row := dimStyle.Render("⋯ " + msg.name + m.batchSuffix(msg.name, msg.id) + " " + queuedSubject(msg.name, msg.args))

		for i := len(m.blocks) - 1; i >= 0; i-- {
			if m.blocks[i].kind == blockToolQueued && m.blocks[i].toolID == msg.id {
				m.blocks[i].text, m.blocks[i].stale = row, true
				m.refreshVP()
				return m, nil
			}
		}
		m.blocks = append(m.blocks, block{kind: blockToolQueued, text: row, toolID: msg.id, toolName: msg.name, toolArgs: msg.args})

		for i := range m.blocks {
			if b := &m.blocks[i]; b.kind == blockToolQueued && b.toolName == msg.name && b.toolID != msg.id {
				b.text = dimStyle.Render("⋯ " + b.toolName + m.batchSuffix(b.toolName, b.toolID) + " " + queuedSubject(b.toolName, b.toolArgs))

				b.stale = true
			}
		}
		m.refreshVP()
		return m, nil

	case toolStartMsg:
		m.flushThink()
		m.flushCurrent()

		suffix := m.batchSuffix(msg.name, msg.id)

		for i := range slices.Backward(m.blocks) {
			if m.blocks[i].kind == blockToolQueued && m.blocks[i].toolID == msg.id {
				m.blocks = slices.Delete(m.blocks, i, i+1)
				break
			}
		}
		args := msg.args
		switch msg.name {
		case "browser_exec", "computer_exec":

			if label := browserStepLabel(msg.args); label != "" {
				args = label
			}
		case "subagent":

			args = toolSubject("subagent", msg.args)
		}

		row := toolStyle.Render("⚒ "+toolVerb(msg.name)+suffix+" ") + dimStyle.Render(args)

		m.blocks = append(m.blocks, block{kind: blockToolRun, text: row, toolID: msg.id, toolRunning: true, toolName: msg.name, toolArgs: msg.args})
		m.refreshVP()
		return m, nil

	case toolOutputMsg:

		for i := len(m.blocks) - 1; i >= 0; i-- {
			b := &m.blocks[i]
			if b.kind == blockToolRun && b.toolRunning && b.toolID == msg.id {
				if tail := lastLines(msg.text, 3); tail != "" {
					b.live = tail
					b.stale = true
					m.refreshVP()
				}
				break
			}
		}
		return m, nil

	case toolEndMsg:

		hdr := -1
		for i := len(m.blocks) - 1; i >= 0; i-- {
			b := &m.blocks[i]
			if b.kind == blockToolRun && b.toolRunning && b.toolID == msg.id {
				b.toolRunning = false
				b.toolFailed = strings.HasPrefix(msg.result, "Error:")
				b.live = ""

				b.text = toolHeaderRow(msg.name, b.toolArgs, b.toolFailed)
				b.stale = true
				hdr = i
				break
			}
		}

		result := block{kind: blockTool, text: msg.result}
		if hdr >= 0 && hdr+1 < len(m.blocks) {
			m.blocks = append(m.blocks[:hdr+1], append([]block{result}, m.blocks[hdr+1:]...)...)

			for i := range m.msgBlock {
				if m.msgBlock[i] > hdr {
					m.msgBlock[i]++
				}
			}
		} else {
			m.blocks = append(m.blocks, result)
		}
		m.keepFollow()
		m.refreshVP()
		return m, nil

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
		m.flushThink()
		m.flushCurrent()
		m.append(youStyle.Render(glyphUser) + linkifyFilePaths(string(msg), realFileExists) + dimStyle.Render("  (steered)"))
		return m, nil

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
		m.flushThink()
		m.flushCurrent()
		m.append(dimStyle.Render(fmt.Sprintf("◎ compacting %d msgs (est. %s) with %s…",
			msg.took, fmtTok(msg.est), m.compactModelLabel())))
		return m, nil

	case compactMsg:

		m.flushThink()
		m.flushCurrent()
		switch {
		case msg.err != nil:
			m.append(errStyle.Render("compact failed: " + msg.err.Error()))
		case msg.summary == "":

		default:
			if m.store == nil {
				snapshots := map[int]string{}
				for index, ref := range m.snapshots {
					if index >= msg.cutoff {
						snapshots[2+index-msg.cutoff] = ref
					}
				}
				m.snapshots = snapshots
			}
			if m.store != nil && msg.before != nil {
				m.historyEvents = append(m.historyEvents, msg)
				if err := m.saveHistory(msg.after); err != nil {
					m.append(errStyle.Render("compaction save failed: " + err.Error()))
				} else {
					msg.preserved = true
				}
			}
			m.future = nil
			m.msgBlock = nil
			m.append(m.compactResultLine(msg))
		}
		return m, nil

	case turnDoneMsg:
		m.flushThink()
		m.flushCurrent()
		m.clearQueuedTools()
		m.busy = false
		m.cancel = nil
		m.interrupt1 = false

		m.turnStart = time.Time{}

		canceled := errors.Is(msg.err, context.Canceled)
		if msg.err != nil && !canceled {
			m.append(errStyle.Render("error: " + msg.err.Error()))
		} else if canceled {
			m.append(dimStyle.Render("(interrupted — any running tool calls will be recorded as interrupted; k-brain can retry them next turn)"))
		}
		m.persist()
		m.maybeTitle()
		m.recordTurnSnapshot(msg)

		if m.pendingForkID != "" {
			m.switchToForked(m.pendingForkID)
			m.pendingForkID = ""
			return m, nil
		}

		for len(m.queue) > 0 && (msg.err == nil || canceled) {
			next := m.queue[0]
			if strings.HasPrefix(next, "!") {
				m.queue = m.queue[1:]
				m.queueSel = -1
				m.runShellQueued(next)
				continue
			}
			return m.drainQueueHead()
		}

		if m.goal != "" && msg.err == nil {
			if msg.stopReason != ai.StopReasonLength && goalMet(msg.final) {
				m.append(dimStyle.Render("◎ goal met after " + strconv.Itoa(m.goalRounds) + " round(s)"))
				m.setGoal("")
				return m, nil
			}
			if m.goalRounds >= m.goalMaxRounds() {
				m.append(errStyle.Render(fmt.Sprintf("◎ goal paused after %d rounds — /goal resume to continue, /goal clear to drop", m.goalRounds)))
				return m, nil
			}
			m.goalRounds++
			return m.submitGoal(goalContinuePrompt(m.goal))
		}
		return m, nil

	case catalogsMsg:
		m.updateCatalogs(msg)
		return m, nil

	case noticeMsg:
		m.append(dimStyle.Render(string(msg)))
		return m, nil

	case noticeExpiredMsg:
		if uint64(msg) == m.noticeGeneration {
			m.transientNotice = ""
		}
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

	case taskUpdateMsg:

		if m.taskVP != nil {
			if t, ok := m.agent.Tasks().Get(m.taskVP.id); ok && t.Status != agent.TaskRunning && !t.FollowingUp && m.taskVP.live {
				m.openTask(m.taskVP.id)
			} else {
				m.refreshTaskVP()
			}
		}
		return m, nil

	case orphanSteerMsg:

		if !m.busy {
			return m.submitTurn(string(msg), true)
		}
		return m, nil

	case waitWakeMsg:

		m.append(dimStyle.Render("⏲ " + firstLine(string(msg))))
		if m.busy {
			m.agent.Steer(string(msg))
			return m, nil
		}
		return m.submitTurn(string(msg), false)

	case mcpStatusMsg:

		if m.mcpMgr != nil {
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

		if k, ok := csiUKey(s.String()); ok {
			m.sel = nil
			return m.key(k)
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {

	if m.iactive != nil {
		return m.iactiveKey(msg)
	}
	if m.askDialog != nil {
		m.askKey(msg)
		return m, nil
	}
	if m.permDialog != nil {
		m.permKey(msg)
		return m, nil
	}
	if m.palette != nil {
		return m.paletteKey(msg)
	}
	if m.rew != nil {
		return m.rewindKey(msg)
	}
	if m.picker != nil {
		return m.pickerKey(msg)
	}
	if m.mpicker != nil {
		return m.modelPickerKey(msg)
	}

	if msg.Type == tea.KeyCtrlJ ||
		(msg.Type == tea.KeyEnter && msg.Alt) ||
		(msg.Type == tea.KeyRunes && msg.Alt && string(msg.Runes) == "\r") ||
		isShiftEnterSeq(msg) {
		return m.insertNewline()
	}

	if k := msg.String(); k == "alt+b" || k == "alt+left" {
		if m.wordLeftFromBlank() {
			return m, nil
		}
	}

	if m.taskVP != nil {
		return m.taskViewKey(msg)
	}

	if msg.Paste {
		if path, ok := pastedImagePath(string(msg.Runes)); ok {

			return m, func() tea.Msg { return pasteImageFileCmd(path) }
		}
	}

	if !msg.Paste && msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
		if path, ok := pastedImagePath(string(msg.Runes)); ok {
			return m, func() tea.Msg { return pasteImageFileCmd(path) }
		}
	}
	if msg.Paste && m.cfg != nil && m.cfg.CollapsePaste != nil && *m.cfg.CollapsePaste {
		if n := strings.Count(string(msg.Runes), "\n"); n >= 2 {
			m.pasteBuf = string(msg.Runes)
			m.input.SetValue(m.input.Value() + fmt.Sprintf("[Pasted ~%d lines]", n+1))
			m.input.CursorEnd()
			m.growInput()
			return m, nil
		}
	}

	switch msg.Type {
	case tea.KeyCtrlG:
		return m, m.openPromptEditor()
	case tea.KeyCtrlT:

		if len(m.dockTasks()) == 0 {
			return m, nil
		}
		m.tasksFocus = !m.tasksFocus
		m.taskExpanded = false
		m.clampTaskSel()
		return m, nil
	case tea.KeyCtrlC:
		if m.busy && m.cancel != nil {

			if !m.interrupt1 {
				m.interrupt1 = true
				return m, nil
			}
			m.cancel()
			return m, nil
		}

		if m.quit1 {
			m.quit1 = false
			return m, tea.Quit
		}
		m.quit1 = true
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return quitArmMsg{} })

	case tea.KeyPgUp, tea.KeyPgDown:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		m.follow = m.vp.AtBottom()
		return m, cmd

	case tea.KeyEsc:

		if m.busy && m.cancel != nil && strings.TrimSpace(m.input.Value()) == "" {
			m.cancel()
			return m, nil
		}

		dismissed := true
		switch {
		case m.namePrompt != nil:
			masked := m.namePrompt.mask
			m.closeNamePrompt()
			if masked {
				m.escClr = false
				return m, nil
			}
		case m.menu != nil:
			if m.menu.cyc {
				m.input.SetValue(m.menu.base)
			}
			m.menu = nil
		case m.queueSel >= 0:
			m.queueSel = -1

		default:
			dismissed = false
		}
		if !dismissed {

			if strings.TrimSpace(m.input.Value()) != "" {
				if m.escClr {
					m.escClr = false
					m.hist = append(m.hist, strings.TrimSpace(m.input.Value()))
					m.histIdx = len(m.hist)
					m.input.Reset()
					m.append(dimStyle.Render("draft cleared — ↑ recalls it"))
					return m, nil
				}
				m.escClr = true
				return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return escArmMsg{} })
			}

			if m.esc1 {
				m.esc1 = false
				m.openRewind()
				return m, nil
			}
			m.esc1 = true
			return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return escArmMsg{} })
		}
		m.esc1 = false
		m.escClr = false
		return m, nil

	case tea.KeyCtrlV:

		return m, pasteImageCmd

	case tea.KeyCtrlE:

		for i := len(m.blocks) - 1; i >= 0; i-- {
			if m.blocks[i].kind == blockTool {
				m.blocks[i].toggle()
				m.refreshVP()
				return m, nil
			}
		}
		m.tasksFocus = false
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.refreshMenu()
		return m, cmd

	case tea.KeyCtrlO:

		m.toggleThinking()
		return m, nil

	case tea.KeyCtrlK:

		return m.command("/clear")

	case tea.KeyTab:

		if m.menu != nil {
			m.menuCycle(1)
			return m, nil
		}
		m.openMenu()
		return m, nil

	case tea.KeyDown, tea.KeyCtrlN:
		if m.menu != nil {
			m.menu.idx = (m.menu.idx + 1) % len(m.menu.cands)
			return m, nil
		}
		if m.tasksFocus {
			m.taskSel = min(m.taskSel+1, len(m.dockTasks())-1)
			m.taskExpanded = false
			return m, nil
		}

		if m.busy && len(m.queue) > 0 && m.input.Value() == "" {
			if m.queueSel >= 0 {
				m.queueSel++
				if m.queueSel >= len(m.queue) {
					m.queueSel = -1
				}
			}
			return m, nil
		}

		if m.input.Value() == "" && len(m.dockTasks()) > 0 {
			m.tasksFocus = true
			m.taskSel = 0
			return m, nil
		}

		if !m.cursorOnLastLine() {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		m.histNext()
		return m, nil

	case tea.KeyShiftTab:
		if m.menu != nil {
			m.menuCycle(-1)
			return m, nil
		}
		m.cyclePermissionMode()
		return m, nil

	case tea.KeySpace:
		if m.tasksFocus && m.menu == nil {

			m.taskExpanded = !m.taskExpanded
			return m, nil
		}

		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.refreshMenu()
		return m, cmd

	case tea.KeyUp, tea.KeyCtrlP:
		if m.menu != nil {
			m.menu.idx = (m.menu.idx + len(m.menu.cands) - 1) % len(m.menu.cands)
			return m, nil
		}
		if m.tasksFocus {
			if m.taskSel == 0 {
				m.tasksFocus = false
				m.taskExpanded = false
				return m, nil
			}
			m.taskSel--
			m.taskExpanded = false
			return m, nil
		}

		if m.busy && len(m.queue) > 0 && m.input.Value() == "" &&
			(msg.Type == tea.KeyUp || msg.Type == tea.KeyShiftTab) {
			if m.queueSel < 0 {
				m.queueSel = len(m.queue) - 1
			} else if m.queueSel > 0 {
				m.queueSel--
			}
			return m, nil
		}

		if msg.Type == tea.KeyUp && !m.cursorOnFirstLine() {
			m.lastUp = m.nowFn()
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		if msg.Type == tea.KeyUp && m.nowFn().Sub(m.lastUp) < 300*time.Millisecond {
			m.lastUp = m.nowFn()
			return m, nil
		}
		m.lastUp = m.nowFn()
		if msg.Type == tea.KeyCtrlP {
			m.openPalette()
			return m, nil
		}
		m.histPrev()
		return m, nil

	case tea.KeyDelete, tea.KeyBackspace:

		if m.busy && m.queueSel >= 0 && m.queueSel < len(m.queue) {
			m.queue = append(m.queue[:m.queueSel], m.queue[m.queueSel+1:]...)
			if m.queueSel >= len(m.queue) {
				m.queueSel = len(m.queue) - 1
			}
			if len(m.queue) == 0 {
				m.queueSel = -1
			}
			return m, nil
		}

		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.refreshMenu()
		return m, cmd

	case tea.KeyEnter:
		if m.namePrompt != nil {
			onOK := m.namePrompt.onOK
			value := strings.TrimSpace(m.input.Value())
			m.closeNamePrompt()
			onOK(value)
			return m, nil
		}
		if m.menu != nil {
			c := m.menu.cands[m.menu.idx]

			if m.menu.cyc && m.menu.head == "" && execNow[c.Text] {
				m.menu = nil
				m.input.Reset()
				return m.command(c.Text)
			}

			if m.menu.cyc {
				m.acceptPreview()
				return m, nil
			}

			if m.menu.head == "" && execNow[c.Text] {
				m.menu = nil
				m.input.Reset()
				return m.command(c.Text)
			}
			if m.accept() {
				return m, nil
			}

		}
		if m.tasksFocus {
			m.tasksFocus = false

			if tasks := m.dockTasks(); len(tasks) > 0 {
				m.openTask(tasks[min(m.taskSel, len(tasks)-1)].ID)
			}
			return m, nil
		}
		text := strings.TrimSpace(m.input.Value())

		if m.pasteBuf != "" {
			text = strings.Replace(text, strings.TrimSpace(fmt.Sprintf("[Pasted ~%d lines]", strings.Count(m.pasteBuf, "\n")+1)), strings.TrimSpace(m.pasteBuf), 1)
			m.pasteBuf = ""
		}
		if m.busy {
			switch {

			case text != "" && (busyCmd(text) || m.isPromptCommand(text)):
				if !strings.HasPrefix(text, "/auth ") {
					m.hist = append(m.hist, text)
					m.histIdx = len(m.hist)
				}
				m.input.Reset()
				m.menu = nil
				return m.command(text)
			case strings.HasPrefix(text, "!"):
				m.hist = append(m.hist, text)
				m.histIdx = len(m.hist)
				m.input.Reset()
				m.menu = nil
				m.runShell(text)
			case text != "" && m.agent.WaitingOnSubagents():

				m.hist = append(m.hist, text)
				m.histIdx = len(m.hist)
				m.input.Reset()
				m.menu = nil

				steerText := m.expandImageChips(text)
				if m.supportsVision() {
					if parts, note := imageParts(steerText); len(parts) > 0 {
						m.agent.SteerImages(steerText+note, parts)
					} else {
						m.agent.Steer(steerText)
					}
				} else {
					m.agent.Steer(steerText)
				}
				m.append(youStyle.Render("❯ ") + linkifyFilePaths(text, realFileExists) + dimStyle.Render("  (steered)"))
			case text != "":
				m.queue = append(m.queue, text)
				m.hist = append(m.hist, text)
				m.histIdx = len(m.hist)
				m.input.Reset()
				m.menu = nil
			case len(m.queue) > 0:

				if m.cancel != nil {
					m.cancel()
				}
			}
			return m, nil
		}
		if text == "" && len(m.queue) > 0 {

			return m.drainQueueHead()
		}
		if text == "" {
			return m, nil
		}
		m.input.Reset()
		m.menu = nil

		if !strings.HasPrefix(text, "/auth ") {
			m.hist = append(m.hist, text)
			m.histIdx = len(m.hist)
		}
		m.draft = ""
		if strings.HasPrefix(text, "/") {
			return m.command(text)
		}
		if strings.HasPrefix(text, "!") {
			m.runShell(text)
			return m, nil
		}
		return m.submit(text)
	}

	m.tasksFocus = false
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.refreshMenu()
	return m, cmd
}

var shiftEnterRe = regexp.MustCompile(
	`\[49 51 59 50 117\]` +
		`|\[50 55 59 50 59 49 51 126\]` +
		`|\[53 55 52 52 49 117\]` +
		`|'\[', '1', '3', ';', '2', 'u'` +
		`|'\[', '2', '7', ';', '2', ';', '1', '3', '~'` +
		`|'\[', 'five', 'seven', 'four', 'four', 'one', 'u'`,
)

func isShiftEnterSeq(msg tea.KeyMsg) bool {
	return isShiftEnterString(msg.String())
}

func isShiftEnterString(s string) bool {
	return (strings.HasPrefix(s, "?CSI[") || strings.HasPrefix(s, "unknown csi sequence:")) && shiftEnterRe.MatchString(s)
}

var csiURe = regexp.MustCompile(`^\?CSI\[([0-9 ]+)\]\?$`)

func csiUKey(rendered string) (tea.KeyMsg, bool) {
	mm := csiURe.FindStringSubmatch(rendered)
	if mm == nil {
		return tea.KeyMsg{}, false
	}
	var b []byte
	for f := range strings.FieldsSeq(mm[1]) {
		n, err := strconv.ParseUint(f, 10, 8)
		if err != nil {
			return tea.KeyMsg{}, false
		}
		b = append(b, byte(n))
	}
	if len(b) == 0 || b[len(b)-1] != 'u' {
		return tea.KeyMsg{}, false
	}
	codeStr, modStr, _ := strings.Cut(string(b[:len(b)-1]), ";")
	codeStr, _, _ = strings.Cut(codeStr, ":")
	code, err := strconv.Atoi(codeStr)
	if err != nil {
		return tea.KeyMsg{}, false
	}
	mods := 1
	if modStr != "" {
		modStr, _, _ = strings.Cut(modStr, ":")
		if mods, err = strconv.Atoi(modStr); err != nil {
			return tea.KeyMsg{}, false
		}
	}
	mods--
	shift, alt, ctrl := mods&1 != 0, mods&2 != 0, mods&4 != 0
	switch {
	case code == 27:
		return tea.KeyMsg{Type: tea.KeyEsc, Alt: alt}, true
	case code == 13 && !ctrl:
		return tea.KeyMsg{Type: tea.KeyEnter, Alt: alt}, true
	case code == 9 && !ctrl:
		if shift {
			return tea.KeyMsg{Type: tea.KeyShiftTab, Alt: alt}, true
		}
		return tea.KeyMsg{Type: tea.KeyTab, Alt: alt}, true
	case code == 127 && !ctrl:
		return tea.KeyMsg{Type: tea.KeyBackspace, Alt: alt}, true
	case code < 32 || code > 126:
		return tea.KeyMsg{}, false
	}
	r := rune(code)
	if ctrl {
		if r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		if r < '@' || r > '_' {
			return tea.KeyMsg{}, false
		}

		return tea.KeyMsg{Type: tea.KeyType(r & 0x1f), Alt: alt}, true
	}
	if shift && r >= 'a' && r <= 'z' {
		r -= 'a' - 'A'
	}
	if r == ' ' {
		return tea.KeyMsg{Type: tea.KeySpace, Alt: alt}, true
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: alt}, true
}

func (m *model) wordLeftFromBlank() bool {
	lines := strings.Split(m.input.Value(), "\n")
	row := m.input.Line()
	if row < 0 || row >= len(lines) {
		return false
	}
	li := m.input.LineInfo()
	col := min(li.StartColumn+li.CharOffset, len([]rune(lines[row])))
	if strings.TrimSpace(string([]rune(lines[row])[:col])) != "" {
		return false
	}
	if row == 0 {
		m.input.CursorStart()
	} else {
		m.input.CursorUp()
		m.input.CursorEnd()
	}
	return true
}

func (m *model) insertNewline() (tea.Model, tea.Cmd) {
	maxHeight := m.input.MaxHeight
	m.input.MaxHeight = 0
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m.input.MaxHeight = maxHeight
	m.input.SetHeight(maxHeight)

	v := m.input.Value()
	m.input.SetValue(v)
	m.input.CursorEnd()
	m.refreshMenu()
	return m, cmd
}

func (m *model) nowFn() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (m *model) busyStats() string {
	if m.turnStart.IsZero() {
		return ""
	}
	d := max(m.nowFn().Sub(m.turnStart), 0)
	elapsed := d.Round(time.Second)
	stats := fmt.Sprintf(" %d:%02d", int(elapsed.Minutes()), int(elapsed.Seconds())%60)
	if u := m.agent.TotalUsage(); u.PromptTokens > 0 || u.CompletionTokens > 0 {
		stats += fmt.Sprintf(" · %s tok", fmtTok(u.PromptTokens+u.CompletionTokens))
	}
	if m.agent.ContextLimit > 0 {
		stats += fmt.Sprintf(" · %d%%", agent.EstimateTokens(m.agent.Messages)*100/m.agent.ContextLimit)
	}
	return stats
}

func (m *model) histPrev() {
	if len(m.hist) == 0 || m.histIdx == 0 {
		return
	}
	if m.histIdx == len(m.hist) {
		m.draft = m.input.Value()
	}
	m.histIdx--
	m.input.SetValue(m.hist[m.histIdx])
}

func (m *model) histNext() {
	if m.histIdx >= len(m.hist) {
		return
	}
	m.histIdx++
	if m.histIdx == len(m.hist) {
		m.input.SetValue(m.draft)
	} else {
		m.input.SetValue(m.hist[m.histIdx])
	}
}

func (m *model) cursorOnFirstLine() bool {
	if m.input.Line() != 0 {
		return false
	}
	return m.input.LineInfo().RowOffset == 0
}

func (m *model) cursorOnLastLine() bool {
	if m.input.Line() != m.input.LineCount()-1 {
		return false
	}
	li := m.input.LineInfo()
	return li.RowOffset >= li.Height-1
}

func (m *model) contextLimitFor(provName, apiID string) int {
	mdl := config.Model{}
	if m.cfg != nil {
		mdl = m.cfg.Models[apiID]
		if selected, ok := m.cfg.Models[m.modelName]; ok {
			id := selected.ID
			if id == "" {
				id = m.modelName
			}
			if id == apiID && provName == m.provName {
				mdl = selected
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
		if m.compactModel != "" {
			m.append(errStyle.Render("compaction model: " + err.Error() + " — using current model"))
		}
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
	} else if m.compactModel != "" {
		m.append(errStyle.Render("compaction model: " + err.Error() + " — using current model"))
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

	if m.mcpMgr != nil {
		m.agent.SetMCPTools(m.mcpMgr.Tools())
	}
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
		m.append(dimStyle.Render("→ " + mn + " @ " + pn))
	} else {
		m.append(dimStyle.Render("→ " + mn + " @ " + pn + " (this session only)"))
	}
}

func (m *model) pickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.picker
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.picker = nil
	case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
		if p.idx < len(p.metas)-1 {
			p.idx++
			p.loadPreview(m.store)
		}
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		if p.idx > 0 {
			p.idx--
			p.loadPreview(m.store)
		}
	case tea.KeyEnter:
		id := p.metas[p.idx].ID
		m.picker = nil
		if err := m.resume(id); err != nil {
			m.append(errStyle.Render(err.Error()))
		}
	}
	return m, nil
}

func (p *picker) loadPreview(store *session.Store) {
	id := p.metas[p.idx].ID
	if _, ok := p.previews[id]; !ok {
		u, a := store.LastExchange(id)
		p.previews[id] = [2]string{u, a}
	}
}

func (m *model) openPicker() {
	if m.store == nil {
		m.append(errStyle.Render("session store unavailable"))
		return
	}
	metas, err := m.store.Recent(50)
	if err != nil {
		m.append(errStyle.Render(err.Error()))
		return
	}
	if len(metas) == 0 {
		m.append(dimStyle.Render("(no previous sessions)"))
		return
	}
	m.picker = &picker{metas: metas, previews: map[string][2]string{}}
	m.picker.loadPreview(m.store)
}

func (m *model) openMenu() {
	head, cands := m.promptCompletions(m.input.Value())
	if len(cands) == 0 {
		return
	}
	m.menu = &menu{head: head, cands: cands}
	m.menuCycle(0)
}

func (m *model) refreshMenu() {
	if m.menu != nil && m.menu.cyc && m.menu.frozen != nil && m.menu.idx < len(m.menu.frozen) &&
		m.input.Value() == m.menu.head+m.menu.frozen[m.menu.idx].Text {
		return
	}
	val := m.input.Value()
	token := val[strings.LastIndexAny(val, " \n")+1:]
	if strings.HasPrefix(val, "/") || strings.HasPrefix(token, "@") || strings.HasPrefix(token, "$") {
		head, cands := m.promptCompletions(val)
		if len(cands) > 0 {
			idx := 0
			if m.menu != nil && m.menu.idx < len(cands) && m.menu.frozen == nil {
				idx = m.menu.idx
			}
			m.menu = &menu{head: head, cands: cands, idx: idx}
			return
		}
	}
	m.menu = nil
}

func (m *model) previewCand() {
	m.input.SetValue(m.menu.head + m.menu.cands[m.menu.idx].Text)
	m.refreshMenu()
}

func (m *model) acceptPreview() {
	m.menu.cyc, m.menu.frozen = false, nil
	v := m.input.Value()
	if !strings.HasSuffix(v, "/") {
		m.input.SetValue(v + " ")
	}
	m.refreshMenu()
}

func (m *model) menuCycle(delta int) {
	mu := m.menu
	if mu.frozen == nil {
		mu.cyc, mu.frozen = true, mu.cands
		mu.base = mu.head + mu.frozen[mu.idx].Text
	}
	if mu.cycled {
		mu.idx = (mu.idx + delta + len(mu.cands)) % len(mu.cands)
	} else {
		mu.cycled = true
	}
	m.previewCand()
}

func (m *model) accept() bool {
	c := m.menu.cands[m.menu.idx]
	v := m.menu.head + c.Text
	if !strings.HasSuffix(c.Text, "/") {
		v += " "
	}
	if strings.TrimRight(m.input.Value(), " ") == strings.TrimRight(v, " ") {
		m.menu = nil
		return false
	}
	m.input.SetValue(v)
	m.menu = nil
	m.refreshMenu()
	return true
}

func (m *model) modelCands() []cand {
	out := make([]cand, 0, len(m.cfg.Models))
	for name, mdl := range m.cfg.Models {
		out = append(out, cand{name, "via " + strings.Join(mdl.Providers, ", ")})
	}

	for _, it := range buildModelItems(m.cfg) {
		if it.fromCatalog {
			out = append(out, cand{it.model, "via " + it.provider + " (catalog)"})
		}
	}
	return out
}

func (m *model) providerCands() []cand {
	out := make([]cand, 0, len(m.cfg.Providers))
	for name, p := range m.cfg.Providers {
		out = append(out, cand{name, p.BaseURL})
	}
	return out
}

func (m *model) skillCands() []cand {
	sk := skills.Scan(skills.DefaultDirs()...)
	out := make([]cand, 0, len(sk))
	for _, s := range sk {
		d := s.Description
		if len(d) > 80 {
			d = d[:80] + "…"
		}
		out = append(out, cand{"$" + s.Name, d})
	}
	return out
}

func (m *model) prepareTurn(text string) (string, []ai.ContentPart) {
	sk := skills.Scan(skills.DefaultDirs()...)
	sys := m.sysPrompt + skills.PromptBlock(sk)
	if m.pluginMgr != nil {
		sys += m.pluginMgr.PromptBlock()
	}
	if m.mcpMgr != nil {
		sys += m.mcpMgr.InstructionsBlock()
	}
	m.agent.Messages[0].Content = sys
	m.agent.RefreshMemory()

	text = m.expandImageChips(text)
	expanded := expandMentions(expandSkills(text, sk))
	if !m.supportsVision() {

		return expanded, nil
	}
	parts, withNote := imageParts(text)
	return expanded + withNote, parts
}

func (m *model) supportsVision() bool {
	return modelSupportsVision(m.cfg, m.modelName, m.agent.Model, m.catalogs, m.provName)
}

func modelSupportsVision(cfg *config.Config, modelName, modelID string, catalogs map[string]config.Catalog, provName string) bool {
	return routing.SupportsVision(cfg, modelName, modelID, catalogs, provName)
}

func (m *model) appendAssistant(s string) {
	if m.inMsg && len(m.blocks) > 0 && m.blocks[len(m.blocks)-1].kind == blockAssistant {
		m.blocks[len(m.blocks)-1].text += "\n\n" + s
		m.blocks[len(m.blocks)-1].stale = true
		m.keepFollow()
		m.refreshVP()
		return
	}
	m.appendAssistantBlock(s)
	m.inMsg = true
}

func indentLines(s string, n int) string {
	const docMargin = 2
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(ansi.Strip(l)) == "" {
			lines[i] = ""
			continue
		}
		lead := len(l) - len(strings.TrimLeft(l, " "))
		shift := max(n+lead-docMargin, 0)
		lines[i] = strings.Repeat(" ", shift) + strings.TrimLeft(l, " ")
	}
	return strings.Join(lines, "\n")
}

func (m *model) appendThink(s string) {
	if !m.inThink {
		s = "◌ " + s
		m.inThink = true
	}
	m.append(thinkingStyle.Render(s))
}

func (m *model) toggleThinking() {
	m.setThinking(!m.showThinking)
	m.append(dimStyle.Render("◌ thinking tokens: " + onOff(m.showThinking)))
}

func (m *model) setThinking(on bool) {
	m.showThinking = on
	if !on {
		m.flushThink()
	}
	b := on
	m.cfg.Thinking = &b
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
	}
}

func (m *model) flushThink() {

	cur := strings.TrimRight(m.curThink, " \n")
	m.curThink = ""
	if cur != "" {
		m.appendThink(cur)
	}
	m.inThink = false
}

func (m *model) thinkView() string {
	s := m.curThink
	if !m.inThink {
		s = "◌ " + s
	}
	return thinkingStyle.Render(wrap(s, m.width))
}

func (m *model) flushCurrent() {
	cur := strings.TrimRight(m.current, " \n")
	m.current = ""
	if cur != "" {
		m.appendAssistant(cur)
	}
	m.inMsg = false
}

func (m *model) drainQueueHead() (tea.Model, tea.Cmd) {
	next := m.queue[0]
	m.queue = m.queue[1:]
	m.queueSel = -1
	m.hist = append(m.hist, next)
	m.histIdx = len(m.hist)
	return m.submit(next)
}

func (m *model) submit(text string) (tea.Model, tea.Cmd) {
	return m.submitTurn(text, true)
}

func (m *model) submitGoal(text string) (tea.Model, tea.Cmd) {
	return m.submitTurn(text, false)
}

func (m *model) submitTurn(text string, authored bool) (tea.Model, tea.Cmd) {
	m.cancelGoalFromContext()
	m.prepareHistory()
	m.turnSnapshotSeq = nil
	m.busy = true
	m.turnStart = m.nowFn()
	prepared, parts := m.prepareTurn(text)
	userMsgIdx := len(m.agent.Messages)

	preSnap := snapshotWorkspace()

	rewoundFrom := ""
	if authored && len(m.future) > 0 {
		for _, fm := range m.future {
			if fm.Role == "user" && fm.Authored {
				rewoundFrom = oneLine(fm.Content)
				break
			}
		}
	}
	m.discardFuture()

	if authored && m.agent != nil {
		var keep []string
		if m.taskVP != nil {
			keep = append(keep, m.taskVP.id)
		}
		m.agent.Tasks().ClearSettled(keep...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = sandbox.WithPolicy(ctx, m.sandboxPolicy)
	m.cancel = cancel
	p := m.prog

	stream := newTurnStream(func(msg tea.Msg) {
		if p != nil {
			p.Send(msg)
		}
	})
	send := stream.emit

	go func() {
		var compactTook, compactKept int
		var compactBefore []ai.Message
		turnAt := userMsgIdx
		events := agent.Events{
			OnText:  stream.text,
			OnThink: stream.think,
			OnToolStart: func(id, n, a string) {
				send(toolStartMsg{id, n, a})
			},
			OnToolEnd: func(id, n, r string) { send(toolEndMsg{id, n, r}) },

			OnToolCall: func(id, n, a string) { send(toolCallMsg{id, n, a}) },

			OnToolOutput: func(id, soFar string) { send(toolOutputMsg{id, soFar}) },
			OnSteer: func(s string) {
				send(steeredMsg(s))
			},
			OnCompactStart: func(took, est int) {
				if rewoundFrom != "" && turnAt > 0 && turnAt < len(m.agent.Messages) {
					m.agent.Messages[turnAt].RewoundFrom = rewoundFrom
				}
				compactBefore = m.agent.MessagesSnapshot()
				send(compactStartMsg{took, est})
			},

			OnCompact: func(took, kept int) { compactTook, compactKept = took, kept },
			OnCompacted: func(sum string, cutoff int, info agent.CompactInfo) {
				at := turnAt
				send(compactMsg{took: compactTook, kept: compactKept, summary: sum, cutoff: cutoff, info: info,
					before: compactBefore, after: m.agent.MessagesSnapshot(), turnAt: &at})
				if turnAt >= cutoff {
					turnAt = 2 + turnAt - cutoff
				} else {
					turnAt = -1
				}
			},
			OnUsage: func(u ai.Usage) { send(usageMsg(u)) },

			OnRetry: func(ev ai.RetryEvent) {
				send(noticeMsg(fmt.Sprintf("⚠ request failed (%s) — retrying in %s (attempt %d/%d)",
					ev.Err, ev.Delay.Round(time.Millisecond), ev.Attempt+1, ev.Max)))
			},
		}
		var final string
		var err error
		switch {
		case len(parts) > 0:
			final, err = m.agent.TurnWithImages(ctx, prepared, parts, events)
		case authored:
			final, err = m.agent.TurnAuthored(ctx, prepared, events)
		default:
			final, err = m.agent.Turn(ctx, prepared, events)
		}
		if rewoundFrom != "" && turnAt > 0 && turnAt < len(m.agent.Messages) {
			m.agent.Messages[turnAt].RewoundFrom = rewoundFrom
		}
		stream.finish(turnDoneMsg{final: final, stopReason: m.agent.LastStopReason(), err: err, at: turnAt, snap: preSnap, clean: workspaceClean()})
	}()
	m.appendRaw(blockUser, linkifyFilePaths(text, realFileExists))
	if authored {

		for len(m.msgBlock) <= userMsgIdx {
			m.msgBlock = append(m.msgBlock, -1)
		}
		m.msgBlock[userMsgIdx] = len(m.blocks) - 1
	}
	return m, m.spin.Tick
}

func busyCmd(text string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "/permissions", "/language", "/editor", "/copy", "/diff", "/prompts", "/help", "/theme", "/mouse", "/effort", "/subagents", "/tasks", "/subagent", "/cd", "/pwd", "/report", "/export", "/import", "/fork", "/forks", "/context", "/context-doctor", "/doctor", "/info", "/mcps", "/mcp", "/plugins", "/new", "/plan", "/session-info", "/status", "/title", "/undo", "/rewind", "/view-plan", "/privacy", "/ancient":
		return true
	case "/auth":
		return true
	case "/goal":
		return len(fields) == 1 || fields[1] == "clear" || fields[1] == "rounds"
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
		m.agent.Messages = m.agent.Messages[:1]
		m.agent.ResetUsage()
		m.lastResp = ai.Usage{}
		m.blocks = nil
		m.msgBlock = nil
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
		m.append(dimStyle.Render("(conversation cleared)"))
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
	case "/compact":
		if len(fields) > 1 {
			switch fields[1] {
			case "retry":
				m.compactRetry()
				return m, nil
			case "log":
				m.compactLog()
				return m, nil
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
		took := len(m.agent.Messages)
		m.append(dimStyle.Render(fmt.Sprintf("◎ compacting %d msgs (est. %s) with %s…",
			took, fmtTok(agent.EstimateTokens(m.agent.Messages)), m.compactModelLabel())))
		p := m.prog
		ag := m.agent
		ctx, cancel := context.WithCancel(context.Background())
		ctx = sandbox.WithPolicy(ctx, m.sandboxPolicy)
		m.cancel = cancel
		go func() {
			before := ag.MessagesSnapshot()
			var summary string
			var cutoff int
			var info agent.CompactInfo
			err := ag.ManualCompact(ctx, agent.Events{
				OnCompacted: func(s string, c int, ci agent.CompactInfo) { summary, cutoff, info = s, c, ci },
			})
			if p != nil {
				p.Send(compactMsg{took: took - len(ag.Messages), kept: len(ag.Messages), summary: summary, cutoff: cutoff, info: info, err: err,
					before: before, after: ag.MessagesSnapshot()})
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
		return m, nil
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
		if len(fields) > 1 {
			switch fields[1] {
			case "light", "dark", "auto":
				m.setTheme(fields[1])
			default:
				m.append(errStyle.Render("usage: /theme light|dark|auto"))
			}
		} else {
			m.openPaletteOn("theme")
		}
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
			m.append(accentStyle.Render("✦ effort: " + effortLabel(m.agent.Effort) + "  ·  " + effortDescription(m.agent.Effort)))
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
			m.append(dimStyle.Render("(goal cleared)"))
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
		m.append(dimStyle.Render(helpTextFor(m.language())))
	case "/auth":
		m.append(errStyle.Render("auth was removed; configure baseUrl and apiKey in ~/.k-brain/config.json"))
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
		note += " @ " + prov
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

const menuRows = 8

func (m *model) currentView() string {
	s := m.current

	if m.busy {
		w := max(m.width-2, 1)
		body := indentPlain(wrap(s, w), 2)
		return botStyle.Render(glyphAssistant) + strings.TrimPrefix(body, "  ")
	}
	return wrap(s, m.width)
}

func indentPlain(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(ansi.Strip(l)) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

const minTranscriptRows = 5

func streamTail(s string, n int) string {
	if n < 1 {
		n = 1
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func (m *model) fixedChrome() int {
	chrome := 8 + m.input.Height()

	if m.iactive != nil {
		chrome -= m.input.Height()
	}
	if m.busy {
		chrome += 2
	}

	if m.iactive != nil {
		chrome += lipgloss.Height(m.interactiveView()) + 1
	}
	if m.permDialog != nil {
		chrome += lipgloss.Height(m.permView()) + 1
	}
	if m.menu != nil {
		chrome += lipgloss.Height(m.menuView()) + 1
	}
	if len(m.queue) > 0 {
		chrome += len(m.queue) + 1
	}
	if m.rew != nil {
		chrome += lipgloss.Height(m.rewindView()) + 1
	}
	if m.quit1 {
		chrome++
	}
	if m.escClr || (m.esc1 && m.rew == nil && m.namePrompt == nil) {
		chrome++
	}
	return chrome + m.dockRows
}

func (m *model) currentViewCapped() string {
	liveCap := m.streamCap(m.fixedChrome())
	if liveCap == 0 {
		return ""
	}
	return streamTail(m.currentView(), liveCap)
}

func (m *model) thinkViewCapped() string {
	liveCap := m.streamCap(m.fixedChrome())
	if liveCap == 0 {
		return ""
	}
	return streamTail(m.thinkView(), liveCap)
}

func (m *model) View() string {
	m.syncInputPlaceholder()
	v := m.viewBody()

	if m.height > 0 {
		m.viewH = lipgloss.Height(v)

		m.frameH, m.frameTop = m.height, 0
		if dropped := m.viewH - m.height; dropped > 0 {
			v = strings.Join(strings.Split(v, "\n")[dropped:], "\n")
			m.inputBodyOff -= dropped
			m.viewportTop -= dropped
			m.vpLead += dropped
			m.viewH = m.height
		}
		m.viewTop = m.height - m.viewH
		if m.viewTop > 0 {
			v = strings.Repeat("\n", m.viewTop) + v
		}
	}
	m.viewportTop += m.viewTop

	if m.iactive != nil || m.height == 0 || m.palette != nil || m.picker != nil || m.mpicker != nil || m.taskVP != nil {
		m.inputTop = -1
		m.inputLines = nil
	} else {
		m.inputTop = m.viewTop + m.inputBodyOff

		iv := m.input.View()
		if m.namePrompt != nil && m.namePrompt.mask {
			iv = m.namePrompt.label + " ┃ " + m.namePrompt.maskedValue(m.input.Value())
		}
		if m.ancientInput() {
			iv = m.ancientize(iv)
		}
		raw := strings.Split(iv, "\n")
		m.inputLines = make([]string, len(raw))
		for i, ln := range raw {
			m.inputLines[i] = strings.TrimRight(ansi.Strip(ln), " \t")
		}
	}
	return v
}

func (m *model) viewBody() string {
	var b strings.Builder
	m.viewportTop, m.viewportRows = 0, 0
	left := fmt.Sprintf(" k-brain · %s @ %s", m.modelName, m.provName)
	if m.goal != "" {
		left += " · ◎ " + truncLine(m.goal, 40)
	}
	if !m.follow {
		left += fmt.Sprintf(" · ↑ %d%%", int(m.vp.ScrollPercent()*100))
	}

	u := m.agent.TotalUsage()
	if u.PromptTokens > 0 || u.CompletionTokens > 0 {
		left += fmt.Sprintf(" · ⣿ %s in", fmtTok(u.PromptTokens))
		if c := u.Cached(); c > 0 {
			left += fmt.Sprintf(" (%s cached)", fmtTok(c))
		}
		left += fmt.Sprintf(" · %s out", fmtTok(u.CompletionTokens))
	}
	if m.agent.ContextLimit > 0 {
		left += fmt.Sprintf(" · %d%% ctx", agent.EstimateTokens(m.agent.Messages)*100/m.agent.ContextLimit)
	}

	if n := m.runningTasks(); n > 0 {
		left += fmt.Sprintf(" · ⚙ %d sub", n)
	}

	right := "✦ " + m.tr(effortLabel(m.agent.Effort))
	if m.showThinking {
		right = m.tr("◌ thinking  ·  ") + right
	}
	m.effortX = max(m.width-lipgloss.Width(right)-2, 0)
	left = truncLine(left, max(m.width-lipgloss.Width(right)-4, 0))
	b.WriteString(kbrainHeaderLabel(m.width, left, right, m.tr("commands")) + "\n")
	if m.palette != nil {

		b.WriteString(m.paletteView())
		return b.String()
	}
	if m.picker != nil {
		b.WriteString(m.pickerView())
		return b.String()
	}

	if m.mpicker != nil {
		b.WriteString(m.modelPickerView())
		return b.String()
	}
	if m.taskVP != nil {
		b.WriteString(m.taskViewView())
		return b.String()
	}

	viewport := m.viewportView()
	m.viewportTop = strings.Count(b.String(), "\n")
	if viewport != "" {
		m.viewportRows = lipgloss.Height(viewport)
	}
	b.WriteString(viewport + "\n")

	if cv := m.thinkViewCapped(); m.curThink != "" && cv != "" {
		b.WriteString("\n" + m.ancientize(cv) + "\n")
	}

	if cv := m.currentViewCapped(); m.current != "" && cv != "" {
		b.WriteString("\n" + m.ancientize(cv) + "\n")
	}
	if m.iactive != nil {
		b.WriteString("\n" + m.interactiveView() + "\n")
	}
	if m.permDialog != nil {
		b.WriteString("\n" + m.permView() + "\n")
	}
	if m.askDialog != nil {
		b.WriteString("\n" + m.askView() + "\n")
	}
	if m.busy {
		hint := " thinking… (enter queues · /theme /mouse /effort run now · esc interrupts · ctrl+c ctrl+c interrupts)"
		if m.iactive != nil {
			hint = " bash (interactive) — type to respond · ctrl+c ctrl+c to cancel"
		} else if m.interrupt1 {
			hint = " thinking… (esc or ctrl+c again to interrupt)"
		}
		b.WriteString("\n" + m.spin.View() + dimStyle.Render(m.busyStats()+hint) + "\n")
	}
	if len(m.queue) > 0 {
		nav := ""
		if m.busy && m.input.Value() == "" {
			nav = " · ↑/↓ select · del removes"
		}
		b.WriteString(dimStyle.Render(fmt.Sprintf("queued (%d) — enter on empty input to steer into this turn%s", len(m.queue), nav)) + "\n")
		for i, q := range m.queue {

			line := ansi.Truncate(youStyle.Render(" "+glyphUser)+q, m.width, "…")
			if i == m.queueSel {
				line = ansi.Truncate(botStyle.Render(" → ")+q+dimStyle.Render("  (del to remove)"), m.width, "…")
			}
			b.WriteString(line + "\n")
		}
	}
	b.WriteString("\n")
	if m.rew != nil {
		b.WriteString(m.rewindView() + "\n\n")
	}

	m.inputBodyOff = strings.Count(b.String(), "\n")
	if m.iactive == nil {
		var inputView string
		if m.namePrompt != nil {
			if m.namePrompt.mask {
				inputView = m.highlightInput(sanitizeInputView("┃ " + m.namePrompt.maskedValue(m.input.Value())))
			} else {
				inputView = m.highlightInput(sanitizeInputView(m.input.View()))
			}
		} else {
			inputView = m.inputArgumentView(m.highlightInput(sanitizeInputView(m.input.View())))
		}
		frameWidth := max(m.width-2, 1)
		m.inputLeft = 0
		if m.ancientInput() {
			inputView = m.ancientize(inputView)
			b.WriteString(inputView)
		} else {
			frame := kbrainPromptFrame(m.height).Width(frameWidth)
			m.inputBodyOff += frame.GetBorderTopSize() + frame.GetPaddingTop()
			m.inputLeft = frame.GetBorderLeftSize() + frame.GetPaddingLeft()
			b.WriteString(frame.Render(inputView))
		}
	}
	if m.quit1 {

		b.WriteString("\n" + errStyle.Render("press ctrl+c again to quit"))
	}
	if m.escClr {
		b.WriteString("\n" + errStyle.Render("esc again: clear the input (↑ recalls it)"))
	} else if m.esc1 && m.rew == nil && m.namePrompt == nil {
		b.WriteString("\n" + dimStyle.Render("esc again: rewind the conversation"))
	}
	if m.menu != nil {

		b.WriteString("\n" + m.menuView())
	}

	if dock := m.tasksDock(); dock != "" {
		b.WriteString("\n" + dock)
	}
	notice := dimStyle.Render(ansi.Truncate(m.tr(m.transientNotice), max(m.width, 0), "…"))
	footer := accentStyle.Render(m.permissionModeLabel()) + "  ·  " + shortcutStyle.Render(m.tr("shift+tab mode  ·  ctrl+c cancel  ·  ctrl+p menu")) + "\n" + notice + "\n" + m.statusView()
	b.WriteString("\n" + footer)
	return b.String()
}

func (m *model) ancientInput() bool {
	return m.ancientMode && m.namePrompt == nil && strings.TrimSpace(m.input.Value()) != "" && !strings.HasPrefix(strings.TrimSpace(m.input.Value()), "/")
}

func (m *model) ancientize(s string) string {
	if !m.ancientMode {
		return s
	}
	w := max(m.width-2, 8)
	return renderAncientText(ansi.Strip(s), w)
}

const inputPlaceholder = "Ask k-brain anything… (/ for commands, tab completes)"

func (m *model) syncInputPlaceholder() {
	if m.input.Value() != "" {
		return
	}
	switch {
	case !m.busy:
		m.input.Placeholder = m.tr(inputPlaceholder)
	case m.agent != nil && m.agent.WaitingOnSubagents():
		m.input.Placeholder = m.tr("waiting on subagents — type to steer this turn")
	default:
		m.input.Placeholder = m.tr("busy — type to queue (sent when the turn ends)")
	}
}

func (m *model) statusView() string {

	model := m.modelName
	if e := effortLabel(m.agent.Effort); e != "off" {
		model += " (" + m.tr(e) + ")"
	}
	u := m.agent.TotalUsage()
	spend := fmtUsage(u)
	if cost, ok := m.sessionCost(); ok {
		spend += " · " + fmtCost(cost)
	}

	if last := m.lastResp; last.PromptTokens > 0 || last.CompletionTokens > 0 {
		spend += m.tr(" · last ") + fmtUsage(last)
	}

	const lead = " "
	right := fmt.Sprintf("   %s   %s   %s", model, m.provName, spend)
	titleSuffix := ""
	if title := strings.Join(strings.Fields(m.sessTitle), " "); title != "" && m.width > 0 {
		titleWidth := min(lipgloss.Width(title), max(m.width/2, 1))
		title = ansi.Truncate(title, titleWidth, "…")
		separator := strings.Repeat(" ", min(3, max(m.width-lipgloss.Width(title), 0)))
		titleSuffix = separator + title
		right = ansi.Truncate(right, max(m.width-lipgloss.Width(lead)-lipgloss.Width(titleSuffix), 0), "")
	}
	dir := shortCWD()
	budget := max(m.width, 0) - lipgloss.Width(lead) - lipgloss.Width(right) - lipgloss.Width(titleSuffix)
	switch {
	case lipgloss.Width(dir) <= budget:

	case budget > 1:

		keep := budget - 1
		if drop := lipgloss.Width(dir) - keep; drop > 0 {
			dir = "…" + ansi.TruncateLeft(dir, drop, "")
		}
	default:
		dir = ""
	}
	line := lead + dir + right
	if titleSuffix != "" {
		if pad := m.width - lipgloss.Width(line) - lipgloss.Width(titleSuffix); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		line += titleSuffix
	}
	return dimStyle.Render(ansi.Truncate(line, max(m.width, 0), ""))
}

func shortCWD() string {
	dir := cwd()
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(dir, home) {
		dir = "~" + strings.TrimPrefix(dir, home)
	}
	parts := strings.Split(strings.Trim(dir, "/"), "/")
	if len(parts) > 3 {
		return "…/" + strings.Join(parts[len(parts)-3:], "/")
	}
	return dir
}

const previewLines = 5

func (m *model) pickerView() string {
	p := m.picker
	rows := []string{}
	expanded := 3 + 2*previewLines

	budget := max(m.height-2-expanded-1, 2)
	lo := max(p.idx-budget/2, 0)
	hi := min(lo+budget+1, len(p.metas))

	for i := hi - 1; i >= lo; i-- {
		meta := p.metas[i]
		title := meta.Title
		if title == "" {
			title = m.tr("(untitled)")
		}
		line := fmt.Sprintf("%s  %s · %s · %s @ %s", meta.ID, title, ago(meta.UpdatedAt), meta.Model, meta.Provider)
		if i != p.idx {
			rows = append(rows, wrap("    "+line, m.width))
			continue
		}
		rows = append(rows, wrap(botStyle.Render("  → ")+line, m.width))
		prev := p.previews[meta.ID]
		rows = append(rows, previewBlock(youStyle.Render(glyphUser), prev[0], m.width)...)
		rows = append(rows, previewBlock(botStyle.Render(glyphAssistant), prev[1], m.width)...)
	}
	rows = append(rows, dimStyle.Render(fmt.Sprintf(m.tr("  (%d/%d) ↑ older · ↓ newer · enter resume · esc cancel"), p.idx+1, len(p.metas))))

	for len(rows) < m.height-1 {
		rows = append(rows, "")
	}
	return strings.Join(rows, "\n")
}

func previewBlock(prefix, text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	w := max(width-8, 8)
	var lines []string
	for i, l := range strings.Split(text, "\n") {
		wrapped := strings.Split(ansi.Hardwrap(l, w, true), "\n")
		for j, wl := range wrapped {
			if i == 0 && j == 0 {
				lines = append(lines, "      "+prefix+wl)
			} else {
				lines = append(lines, "        "+wl)
			}
		}
	}
	if len(lines) > previewLines {
		lines = append(lines[:previewLines],
			dimStyle.Render(fmt.Sprintf("        … +%d lines (full text after resume)", len(lines)-previewLines)))
	}
	return lines
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
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
