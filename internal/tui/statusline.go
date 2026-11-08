package tui

// The configurable status line: statusLine.command in config.json runs with
// the session as JSON on stdin, and the first line it prints takes the left
// side of the footer (after the mode chip) in place of the git and goal
// readout. It runs as a tea.Cmd, never on the UI goroutine, at most once per
// interval and only when the session changed or the output has gone stale,
// and each run is killed at its timeout.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	// statusLineStale reruns an unchanged session this often, for scripts
	// that show something of their own (a clock, CI state).
	statusLineStale  = 15 * time.Second
	statusLineMaxOut = 4 << 10
)

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

func statusLineSettings(cfg *config.Config) (command string, interval, timeout time.Duration) {
	if cfg == nil {
		return "", 0, 0
	}
	sl := cfg.StatusLine
	interval, timeout = statusLineInterval, statusLineTimeout
	if sl.IntervalMs > 0 {
		interval = max(time.Duration(sl.IntervalMs)*time.Millisecond, statusLineMinInterval)
	}
	if sl.TimeoutMs > 0 {
		timeout = time.Duration(sl.TimeoutMs) * time.Millisecond
	}
	return strings.TrimSpace(sl.Command), interval, timeout
}

// statusLineCmd starts a run when one is due, else returns nil.
func (m *model) statusLineCmd(now time.Time) tea.Cmd {
	command, interval, timeout := statusLineSettings(m.cfg)
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
	if string(data) == sl.input && now.Sub(sl.lastRun) < statusLineStale {
		return nil
	}
	sl.running, sl.lastRun, sl.input = true, now, string(data)
	sl.seq++
	seq := sl.seq
	return func() tea.Msg {
		text, err := runStatusLine(command, data, timeout)
		return statusLineMsg{seq: seq, text: text, err: err}
	}
}

// runStatusLine runs command through the shell with stdin as its input and
// returns the first non-empty line it printed, escapes removed.
func runStatusLine(command string, stdin []byte, timeout time.Duration) (string, error) {
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
	for line := range strings.SplitSeq(out.String(), "\n") {
		if line = strings.TrimSpace(notifyText(line)); line != "" {
			return line, nil
		}
	}
	return "", nil
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

// statusLineText is the footer text the command produced, if any.
func (m *model) statusLineText() string {
	if m.st == nil {
		return ""
	}
	return ansi.Strip(m.st.line.text)
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
