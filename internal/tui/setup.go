package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func setupWizard(cfg *config.Config, r *bufio.Reader) error {

	st, statErr := os.Stdin.Stat()
	if statErr != nil || st.Mode()&os.ModeCharDevice == 0 {
		return nil
	}
	return runSetupWizard(cfg, r, os.Stderr)
}

func runSetupWizard(cfg *config.Config, stdin io.Reader, stderr io.Writer) error {
	r, ok := stdin.(*bufio.Reader)
	if !ok {
		r = bufio.NewReader(stdin)
	}
	w := stderr

	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Welcome to k-brain! First-run setup (Enter = skip/keep default).")
	fmt.Fprintln(w, "Every choice is reversible later: ctrl+p, ~/.liveagent/config.json.")
	fmt.Fprintln(w, "")

	fmt.Fprintln(w, "Configure your API provider baseUrl, apiKey, and model IDs in ~/.liveagent/config.json.")

	if !askYN(r, w, "Show thinking (reasoning) tokens in the transcript?", true) {
		off := false
		cfg.Thinking = &off
	}

	if err := cfg.Save(); err != nil {
		return fmt.Errorf("saving setup choices: %w", err)
	}
	config.MarkSetupDone()
	fmt.Fprintln(w, "Setup complete — starting k-brain.")
	fmt.Fprintln(w, "")
	return nil
}

func askYN(r *bufio.Reader, w io.Writer, question string, def bool) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	for attempt := 0; ; attempt++ {
		fmt.Fprintf(w, "%s %s ", question, hint)
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return def
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return def
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
		if attempt > 0 {
			return def
		}
		fmt.Fprintln(w, "  (answer y or n)")
	}
}

// ── Onboarding inside the TUI ──────────────────────────────────────────────
//
// On an interactive terminal the first-run welcome, the folder trust question
// and the setup choices are one small full-screen program that runs before
// the agent is built, so nothing the folder holds (plugins, MCP servers, LSP)
// is loaded until it is trusted. Without a terminal the stdin prompts and
// errors above stay as they were.

type onboardStep int

const (
	stepWelcome onboardStep = iota
	stepTrust
	stepThinking
)

// onboardModel is the onboarding program.
type onboardModel struct {
	steps         []onboardStep
	step          int
	sel           int
	wd            string
	configPath    string
	width, height int

	// trusted, thinking and done are the answers; quit is set when the user
	// declined trust or cancelled.
	trusted  bool
	thinking bool
	done     bool
	quit     bool
}

func newOnboardModel(wd, configPath string, needTrust, firstRun bool) *onboardModel {
	o := &onboardModel{wd: wd, configPath: configPath, thinking: true, trusted: !needTrust}
	if firstRun {
		o.steps = append(o.steps, stepWelcome)
	}
	if needTrust {
		o.steps = append(o.steps, stepTrust)
	}
	if firstRun {
		o.steps = append(o.steps, stepThinking)
	}
	return o
}

func (o *onboardModel) Init() tea.Cmd { return nil }

// options are the current step's choices.
func (o *onboardModel) options() []string {
	switch o.steps[o.step] {
	case stepTrust:
		return []string{"Yes, I trust this folder", "No, exit"}
	case stepThinking:
		return []string{"Yes, show thinking tokens", "No, hide them"}
	}
	return []string{"Continue"}
}

func (o *onboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		o.width, o.height = msg.Width, msg.Height
	case tea.KeyMsg:
		n := len(o.options())
		switch msg.Type {
		case tea.KeyCtrlC:
			o.quit = true
			return o, tea.Quit
		case tea.KeyEsc:
			if o.steps[o.step] == stepTrust {
				o.quit = true
				return o, tea.Quit
			}
			return o.choose(n - 1)
		case tea.KeyUp, tea.KeyShiftTab:
			o.sel = (o.sel - 1 + n) % n
		case tea.KeyDown, tea.KeyTab:
			o.sel = (o.sel + 1) % n
		case tea.KeyEnter:
			return o.choose(o.sel)
		case tea.KeyRunes:
			switch k := strings.ToLower(string(msg.Runes)); {
			case k == "y":
				return o.choose(0)
			case k == "n" && n > 1:
				return o.choose(1)
			case len(k) == 1 && k[0] >= '1' && int(k[0]-'1') < n:
				return o.choose(int(k[0] - '1'))
			}
		}
	}
	return o, nil
}

// choose answers the current step with option i and moves on.
func (o *onboardModel) choose(i int) (tea.Model, tea.Cmd) {
	switch o.steps[o.step] {
	case stepTrust:
		if i != 0 {
			o.quit = true
			return o, tea.Quit
		}
		o.trusted = true
	case stepThinking:
		o.thinking = i == 0
	}
	o.step++
	o.sel = 0
	if o.step >= len(o.steps) {
		o.done = true
		return o, tea.Quit
	}
	return o, nil
}

func (o *onboardModel) View() string {
	if o.done || o.quit {
		return ""
	}
	w := cardWidth(o.width)
	var rows []string
	var title string
	switch o.steps[o.step] {
	case stepWelcome:
		title = "Welcome to k-brain"
		rows = []string{
			"A coding agent in your terminal. This first-run setup takes a moment; every choice can be changed later with Ctrl+P or /settings.",
			"",
			textSecondaryStyle.Render("Provider: set baseUrl, apiKey and model IDs in"),
			accentFgStyle.Render(o.configPath),
		}
	case stepTrust:
		title = "Do you trust the files in this folder?"
		rows = []string{
			cardTitleStyle.Render(o.wd),
			"",
			"k-brain may read files in this folder. Reading untrusted files may lead k-brain to behave in unexpected ways.",
			"",
			"With your permission k-brain may execute files in this folder. Executing untrusted code is unsafe.",
		}
	case stepThinking:
		title = "Show thinking tokens?"
		rows = []string{
			"Show the model's reasoning in the transcript while it works.",
			textHintStyle.Render(chromeGlyphs("Ctrl+O toggles it at any time.")),
		}
	}
	rows = append(rows, "")
	for i, opt := range o.options() {
		rows = append(rows, cardOption(i == o.sel, strconv.Itoa(i+1), opt, w))
	}
	hint := "↑/↓ select · Enter confirm"
	if o.steps[o.step] == stepTrust {
		hint += " · Esc exit"
	}
	rows = append(rows, "", textHintStyle.Render(chromeGlyphs(hint)))
	progress := ""
	if len(o.steps) > 1 {
		progress = textHintStyle.Render(fmt.Sprintf("step %d of %d", o.step+1, len(o.steps)))
	}
	// The card cuts a long title itself; cut it first so ASCII mode keeps
	// its own ellipsis.
	title = ansi.Truncate(chromeGlyphs(title), max(w-6, 1), ellipsis())
	for i, r := range rows {
		rows[i] = chromeGlyphs(r)
	}
	card := renderCard(tokAccent, title, rows, w)
	out := strings.Split(card, "\n")
	if progress != "" {
		out = append(out, " "+progress)
	}
	// Centre the card vertically on the screen.
	if o.height > len(out) {
		pad := (o.height - len(out)) / 2
		out = append(make([]string, pad), out...)
		for len(out) < o.height {
			out = append(out, "")
		}
	}
	return strings.Join(out, "\n")
}

// interactiveTerminal reports whether stdin and stdout are both terminals,
// so a full-screen program can run.
func interactiveTerminal() bool {
	in, err := os.Stdin.Stat()
	if err != nil || in.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	return stdoutIsTTY()
}

// onboard runs the trust check and, on a first run, the setup before the
// agent is built. It reports false when the folder was not trusted.
func onboard(cfg *config.Config, firstRun bool, r *bufio.Reader) (bool, error) {
	wd, err := os.Getwd()
	if err != nil {
		return false, err
	}
	needTrust := !config.Trusted(wd)
	if !needTrust && !firstRun {
		return true, nil
	}
	if !interactiveTerminal() {
		return onboardPrompts(cfg, firstRun, r)
	}
	configPath := "~/.liveagent/config.json"
	if dir, err := config.Dir(); err == nil {
		configPath = filepath.Join(dir, "config.json")
	}
	setASCIIGlyphs(wantASCII(os.Getenv))
	setMonoMode(wantMono(os.Getenv, cfg.Theme), true)
	o := newOnboardModel(wd, configPath, needTrust, firstRun)
	if _, err := tea.NewProgram(o, tea.WithAltScreen()).Run(); err != nil {
		// The terminal would not run a program: ask on stdin instead.
		return onboardPrompts(cfg, firstRun, r)
	}
	return applyOnboarding(cfg, wd, o)
}

// onboardPrompts is the line-based path for when no full-screen program can
// run.
func onboardPrompts(cfg *config.Config, firstRun bool, r *bufio.Reader) (bool, error) {
	if ok, err := checkTrust(r); err != nil || !ok {
		return ok, err
	}
	if firstRun {
		if err := setupWizard(cfg, r); err != nil {
			return false, err
		}
	}
	return true, nil
}

// applyOnboarding stores what the onboarding program was told.
func applyOnboarding(cfg *config.Config, wd string, o *onboardModel) (bool, error) {
	if o.quit || !o.trusted {
		return false, nil
	}
	if slices.Contains(o.steps, stepTrust) {
		if err := config.Trust(wd); err != nil {
			return false, err
		}
	}
	if slices.Contains(o.steps, stepThinking) {
		if !o.thinking {
			off := false
			cfg.Thinking = &off
		}
		if err := cfg.Save(); err != nil {
			return false, fmt.Errorf("saving setup choices: %w", err)
		}
		config.MarkSetupDone()
	}
	return true, nil
}
