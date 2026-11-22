package tui

// The configurable status line. statusLine.items picks built-in readouts
// (model, mode, cwd, branch, context, tokens, cost, time) and
// statusLine.command runs a script with the session as JSON on stdin. Their
// lines, items first, replace the git and goal readout on the left of the
// footer (after the mode chip); lines past the first go on rows of their own
// under the footer, up to statusLine.maxLines of script output. With neither
// set the footer is unchanged.
//
// The script runs as a tea.Cmd, never on the UI goroutine, at most once per
// interval and only when the session changed or its output is older than
// the refresh interval, and each run is killed at its timeout.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

const (
	statusLineInterval    = 2 * time.Second
	statusLineMinInterval = 500 * time.Millisecond
	statusLineTimeout     = time.Second
	// statusLineStale reruns an unchanged session this often by default,
	// for scripts that show something of their own (a clock, CI state).
	statusLineStale  = 15 * time.Second
	statusLineMaxOut = 4 << 10
	// statusLineMaxLines caps the script lines shown.
	statusLineMaxLines = 4
)

// statusItemNames are the built-in readouts statusLine.items can list.
var statusItemNames = []string{"model", "mode", "cwd", "branch", "context", "tokens", "cost", "time"}

type statusLine struct {
	running bool
	seq     int
	lastRun time.Time
	checked time.Time
	input   string // the JSON the last run got
	text    string
	err     string
}

type statusLineMsg struct {
	seq  int
	text string
	err  error
}

// statusLineInput is what the command reads on stdin.
type statusLineInput struct {
	SessionID string   `json:"session_id"`
	Title     string   `json:"session_title"`
	Cwd       string   `json:"cwd"`
	Model     slModel  `json:"model"`
	Effort    string   `json:"effort,omitempty"`
	Mode      string   `json:"permission_mode"`
	Busy      bool     `json:"busy"`
	Context   slCtx    `json:"context"`
	Usage     slUsage  `json:"usage"`
	CostUSD   *float64 `json:"cost_usd"`
	Git       *slGit   `json:"git,omitempty"`
	Goal      string   `json:"goal,omitempty"`
	Version   string   `json:"version"`
}

type slModel struct {
	Name     string `json:"name"`
	ID       string `json:"id"`
	Provider string `json:"provider"`
}

type slCtx struct {
	Tokens  int `json:"tokens"`
	Limit   int `json:"limit"`
	Percent int `json:"percent"`
}

type slUsage struct {
	Input  int `json:"input_tokens"`
	Cached int `json:"cached_tokens"`
	Output int `json:"output_tokens"`
}

type slGit struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
}

func (m *model) statusLineInput() statusLineInput {
	in := statusLineInput{
		SessionID: m.sessionID, Title: m.sessTitle, Cwd: cwd(),
		Model: slModel{Name: m.modelName, Provider: m.provName},
		Mode:  m.permissionMode, Busy: m.busy, Goal: m.goal, Version: Version,
	}
	if m.agent != nil {
		in.Model.ID = m.agent.Model
		in.Effort = m.agent.Effort
		in.Context.Tokens, in.Context.Limit = m.agent.ContextTokensNow(), m.agent.ContextLimit
		if in.Context.Limit > 0 {
			in.Context.Percent = in.Context.Tokens * 100 / in.Context.Limit
		}
		u := m.agent.TotalUsage()
		in.Usage = slUsage{Input: u.PromptTokens, Cached: u.Cached(), Output: u.CompletionTokens}
		if c, ok := m.sessionCost(); ok {
			in.CostUSD = &c
		}
	}
	if m.git.Repo != "" {
		in.Git = &slGit{Repo: m.git.Repo, Branch: m.git.Branch}
	}
	return in
}

// statusLineOpts is statusLine from config.json with defaults filled in.
type statusLineOpts struct {
	command                    string
	interval, timeout, refresh time.Duration
	maxLines                   int
}

func statusLineSettings(cfg *config.Config) statusLineOpts {
	if cfg == nil {
		return statusLineOpts{}
	}
	sl := cfg.StatusLine
	o := statusLineOpts{
		command:  strings.TrimSpace(sl.Command),
		interval: statusLineInterval, timeout: statusLineTimeout, refresh: statusLineStale,
		maxLines: 1,
	}
	if sl.IntervalMs > 0 {
		o.interval = max(time.Duration(sl.IntervalMs)*time.Millisecond, statusLineMinInterval)
	}
	if sl.TimeoutMs > 0 {
		o.timeout = time.Duration(sl.TimeoutMs) * time.Millisecond
	}
	if sl.RefreshMs > 0 {
		o.refresh = max(time.Duration(sl.RefreshMs)*time.Millisecond, o.interval)
	}
	if sl.MaxLines > 0 {
		o.maxLines = min(sl.MaxLines, statusLineMaxLines)
	}
	return o
}

// statusLineCmd starts a run when one is due, else returns nil.
func (m *model) statusLineCmd(now time.Time) tea.Cmd {
	o := statusLineSettings(m.cfg)
	command, interval, timeout := o.command, o.interval, o.timeout
	sl := &m.status().line
	if command == "" {
		sl.text, sl.err = "", ""
		return nil
	}
	if sl.running || (!sl.checked.IsZero() && now.Sub(sl.checked) < interval) {
		return nil
	}
	sl.checked = now
	data, err := json.Marshal(m.statusLineInput())
	if err != nil {
		return nil
	}
	if string(data) == sl.input && now.Sub(sl.lastRun) < o.refresh {
		return nil
	}
	sl.running, sl.lastRun, sl.input = true, now, string(data)
	sl.seq++
	seq := sl.seq
	return func() tea.Msg {
		text, err := runStatusLine(command, data, timeout, o.maxLines)
		return statusLineMsg{seq: seq, text: text, err: err}
	}
}

// runStatusLine runs command through the shell with stdin as its input and
// returns the first maxLines non-empty lines it printed, escapes removed.
func runStatusLine(command string, stdin []byte, timeout time.Duration, maxLines int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Stdin = bytes.NewReader(stdin)
	var out limitedBuffer
	out.max = statusLineMaxOut
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 100 * time.Millisecond
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", errors.New("timed out after " + timeout.String())
	}
	if err != nil {
		return "", err
	}
	var lines []string
	for line := range strings.SplitSeq(out.String(), "\n") {
		if line = strings.TrimSpace(notifyText(line)); line != "" {
			lines = append(lines, line)
			if len(lines) >= max(maxLines, 1) {
				break
			}
		}
	}
	return strings.Join(lines, "\n"), nil
}

// receive applies a finished run; a stale one is ignored.
func (sl *statusLine) receive(msg statusLineMsg, now time.Time) {
	if msg.seq != sl.seq {
		return
	}
	sl.running = false
	sl.lastRun = now
	if msg.err != nil {
		sl.text, sl.err = "", msg.err.Error()
		config.LogEvent("statusline", "FAILED: "+sl.err)
		return
	}
	sl.text, sl.err = msg.text, ""
}

// statusLineRows are the configured status lines, styled: the built-in
// items on one line, then the script's lines. Empty when neither is set or
// neither has anything to show.
func (m *model) statusLineRows() []string {
	var rows []string
	if line := m.statusItemsLine(); line != "" {
		rows = append(rows, line)
	}
	if m.st != nil && m.st.line.text != "" {
		for line := range strings.SplitSeq(ansi.Strip(m.st.line.text), "\n") {
			rows = append(rows, dimStyle.Render(line))
		}
	}
	return rows
}

// statusLineExtra is the status lines past the first, each a row under the
// footer.
func (m *model) statusLineExtra() []string {
	rows := m.statusLineRows()
	if len(rows) < 2 {
		return nil
	}
	rows = rows[1:]
	for i, r := range rows {
		rows[i] = chromeColumns(r, "", m.width)
	}
	return rows
}

func (m *model) statusLineExtraRows() int { return len(m.statusLineExtra()) }

// statusItemsLine renders statusLine.items in order; an item with nothing
// to say (no repo, no spend yet) or an unknown name is left out.
func (m *model) statusItemsLine() string {
	if m.cfg == nil {
		return ""
	}
	var parts []string
	for _, name := range m.cfg.StatusLine.Items {
		if s := m.statusItem(strings.ToLower(strings.TrimSpace(name))); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, dimStyle.Render(" · "))
}

func (m *model) statusItem(name string) string {
	switch name {
	case "model":
		return dimStyle.Render(m.modelName)
	case "mode":
		return dimStyle.Render(m.permissionModeLabel())
	case "cwd":
		return dimStyle.Render(shortCWD())
	case "branch":
		return m.git.render()
	case "context":
		if m.agent != nil && m.agent.ContextLimit > 0 {
			pct := contextPercent(m.agent.ContextTokensNow(), m.agent.ContextLimit)
			return dimStyle.Render(fmt.Sprintf(m.tr("%d%% ctx"), pct))
		}
	case "tokens":
		if m.agent != nil {
			if u := m.agent.TotalUsage(); u.PromptTokens+u.CompletionTokens > 0 {
				return dimStyle.Render(fmtTok(u.PromptTokens+u.CompletionTokens) + " tok")
			}
		}
	case "cost":
		if cost, ok := m.sessionCost(); ok && cost > 0 {
			return dimStyle.Render(fmtCost(cost))
		}
	case "time":
		return dimStyle.Render(m.nowFn().Format("15:04"))
	}
	return ""
}

// limitedBuffer keeps the first max bytes written and drops the rest.
type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
