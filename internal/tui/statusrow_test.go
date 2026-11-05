package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// stClock is a model whose clock the test moves by hand.
func stClock(t *testing.T) (*model, *time.Time) {
	t.Helper()
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m := compactCmdModel()
	m.now = func() time.Time { return clock }
	prev := toolTimer.now
	toolTimer.now = func() time.Time { return clock }
	t.Cleanup(func() { toolTimer.now = prev })
	return m, &clock
}

// stBegin starts a turn the way submit does: busy, a turn clock, and an
// Update pass so the status area sees the new turn.
func stBegin(m *model, clock *time.Time) {
	m.busy = true
	m.turnStart = *clock
	m.Update(nil)
}

// stTool is a tool id no other test has left a running span under.
func stTool(id string) string {
	id = "st-" + id
	toolTimer.mu.Lock()
	delete(toolTimer.spans, id)
	toolTimer.mu.Unlock()
	return id
}

func stRow(m *model) string { return ansi.Strip(m.busyRow()) }

func TestStatusRowPhases(t *testing.T) {
	m, clock := stClock(t)
	stBegin(m, clock)
	if got := stRow(m); !strings.Contains(got, "Preparing…") || !strings.Contains(got, "Esc to interrupt") {
		t.Fatalf("turn start: %q", got)
	}
	m.Update(phaseMsg(phaseWaiting))
	*clock = clock.Add(3 * time.Second)
	if got := stRow(m); !strings.Contains(got, "Waiting for the model…  3s") {
		t.Fatalf("waiting: %q", got)
	}
	m.Update(thinkMsg(strings.Repeat("x", 400)))
	if got := stRow(m); !strings.Contains(got, "Thinking…") || !strings.Contains(got, "⇣100") {
		t.Fatalf("thinking: %q", got)
	}
	m.Update(textMsg("hello"))
	m.Update(usageMsg(ai.Usage{CompletionTokens: 1200}))
	if got := stRow(m); !strings.Contains(got, "Writing…") || !strings.Contains(got, "⇣1.2k") {
		t.Fatalf("writing: %q", got)
	}
	m.Update(toolStartMsg{id: stTool("r1"), name: "read", args: `{"path":"cmd/kn/status.go"}`})
	*clock = clock.Add(2 * time.Second)
	if got := stRow(m); !strings.Contains(got, "status.go") || !strings.Contains(got, "2.0s") {
		t.Fatalf("tool timer: %q", got)
	}
}

func TestStatusRowRetry(t *testing.T) {
	m, clock := stClock(t)
	stBegin(m, clock)
	m.Update(retryMsg{Attempt: 2, Max: 8, Delay: 4 * time.Second,
		Err: &ai.HTTPError{Status: "429 Too Many Requests", StatusCode: 429, Body: `{"error":{"message":"slow down"}}`}})
	if got := stRow(m); !strings.Contains(got, "Retrying in 4s (3/8) · rate limited 429") {
		t.Fatalf("retry countdown: %q", got)
	}
	*clock = clock.Add(5 * time.Second)
	if got := stRow(m); !strings.Contains(got, "Retrying… (3/8)") {
		t.Fatalf("retry attempt: %q", got)
	}
	m.Update(textMsg("back"))
	if got := stRow(m); strings.Contains(got, "Retrying") {
		t.Fatalf("output clears the retry: %q", got)
	}
	for _, b := range m.blocks {
		if strings.Contains(b.text, "retrying") {
			t.Fatalf("a retry is a status, not a transcript row: %q", b.text)
		}
	}
}

func TestStatusRowCompactingAndWaits(t *testing.T) {
	m, clock := stClock(t)
	stBegin(m, clock)
	m.Update(compactStartMsg{took: 18, est: 300})
	*clock = clock.Add(7 * time.Second)
	if got := stRow(m); !strings.Contains(got, "Compacting context…  7s") {
		t.Fatalf("compacting: %q", got)
	}
	m.Update(compactMsg{took: 8, kept: 10, summary: "sum", before: []ai.Message{{Role: "user", Content: strings.Repeat("a", 4000)}},
		after: []ai.Message{{Role: "user", Content: strings.Repeat("a", 400)}}})
	if got := stRow(m); strings.Contains(got, "Compacting") {
		t.Fatalf("compaction over: %q", got)
	}
	last := ansi.Strip(lastBlock(m))
	if !strings.Contains(last, "summarized 8 msgs, 10 kept") || !strings.Contains(last, "→") || !strings.Contains(last, "· 7s") {
		t.Fatalf("compaction result: %q", last)
	}

	m.receivePermission(permRequest{req: tools.GateRequest{Tool: "bash", Command: "make"}, reply: make(chan permAnswer, 1)})
	if got := stRow(m); !strings.Contains(got, "Waiting for your approval") || strings.Contains(got, "Esc to interrupt") {
		t.Fatalf("approval: %q", got)
	}
}

func TestManualCompactShowsCompacting(t *testing.T) {
	m, _ := stClock(t)
	m.agent.Messages = append(m.agent.Messages, ai.Message{Role: "user", Content: "a"}, ai.Message{Role: "assistant", Content: "b"},
		ai.Message{Role: "user", Content: "c"}, ai.Message{Role: "assistant", Content: "d"})
	m.command("/compact")
	if !m.busy {
		t.Skip("compaction refused in this setup")
	}
	if got := stRow(m); !strings.Contains(got, "Compacting context…") {
		t.Fatalf("manual compact: %q", got)
	}
	if m.cancel != nil {
		m.cancel()
	}
}

func TestStatusRowStaysOneRow(t *testing.T) {
	builds := map[string]func(m *model, clock *time.Time){
		"thinking": func(m *model, c *time.Time) { m.Update(thinkMsg("hmm")) },
		"tool": func(m *model, c *time.Time) {
			m.Update(toolStartMsg{id: stTool("b1"), name: "bash", args: `{"command":"go test ./internal/tui/... -run TestSomethingLong -count=1"}`})
		},
		"retry": func(m *model, c *time.Time) {
			m.Update(retryMsg{Attempt: 1, Max: 8, Delay: 30 * time.Second, Err: errors.New("503 Service Unavailable")})
		},
		"compacting": func(m *model, c *time.Time) { m.Update(compactStartMsg{}) },
		"approval": func(m *model, c *time.Time) {
			m.receivePermission(permRequest{req: tools.GateRequest{Tool: "bash", Command: "make"}, reply: make(chan permAnswer, 1)})
		},
	}
	for name, build := range builds {
		for _, w := range []int{40, 80, 120} {
			m, clock := stClock(t)
			m.Update(mkWinSize(w, 24))
			stBegin(m, clock)
			*clock = clock.Add(75 * time.Second)
			m.Update(usageMsg(ai.Usage{CompletionTokens: 23456}))
			build(m, clock)
			row := m.busyRow()
			if strings.Contains(row, "\n") || lipgloss.Width(row) > w {
				t.Errorf("%s at %d: %d cells, %q", name, w, lipgloss.Width(row), ansi.Strip(row))
			}
			if !strings.Contains(ansi.Strip(row), "1:15") {
				t.Errorf("%s at %d lost the turn clock: %q", name, w, ansi.Strip(row))
			}
			if w == 120 && name != "approval" && !strings.Contains(ansi.Strip(row), "Esc to interrupt") {
				t.Errorf("%s at 120 lost the stop hint: %q", name, ansi.Strip(row))
			}
		}
	}
}

func TestTurnSummaryLine(t *testing.T) {
	m, clock := stClock(t)
	stBegin(m, clock)
	m.Update(toolStartMsg{id: "a", name: "bash", args: `{"command":"true"}`})
	m.Update(toolStartMsg{id: "b", name: "bash", args: `{"command":"false"}`})
	m.Update(toolOutcomeMsg{id: "a"})
	m.Update(toolOutcomeMsg{id: "b", failed: true})
	m.Update(usageMsg(ai.Usage{CompletionTokens: 3400}))
	*clock = clock.Add(12 * time.Second)
	m.Update(turnDoneMsg{})
	got := ansi.Strip(lastBlock(m))
	if !strings.Contains(got, "Done in 12s · 2 tool calls (1 failed) · ⇣3.4k tok") {
		t.Fatalf("summary: %q", got)
	}

	// A short chat reply earns no summary.
	stBegin(m, clock)
	n := len(m.blocks)
	*clock = clock.Add(2 * time.Second)
	m.Update(turnDoneMsg{})
	if len(m.blocks) != n {
		t.Fatalf("short turn summarized: %q", ansi.Strip(lastBlock(m)))
	}

	// A failed turn gets the banner and no summary.
	stBegin(m, clock)
	*clock = clock.Add(20 * time.Second)
	m.Update(turnDoneMsg{err: &ai.HTTPError{Status: "401 Unauthorized", StatusCode: 401, Body: `{"error":{"message":"invalid api key"}}`}})
	m.Update(mkWinSize(80, 40))
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Authentication failed · 401") || strings.Contains(view, "Done in 20s") {
		t.Fatalf("failed turn: %q", view)
	}
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		err    error
		kind   errKind
		status int
	}{
		{&ai.HTTPError{Status: "401 Unauthorized", StatusCode: 401, Body: `{"error":{"message":"Incorrect API key"}}`}, errAuth, 401},
		{&ai.HTTPError{Status: "429 Too Many Requests", Body: `{"error":{"message":"rate limit"}}`}, errRateLimit, 429},
		{&ai.HTTPError{Status: "429", StatusCode: 429, Body: `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`}, errQuota, 429},
		{&ai.HTTPError{Status: "400 Bad Request", StatusCode: 400, Body: `{"error":{"message":"This model's maximum context length is 128000 tokens"}}`}, errContext, 400},
		{&ai.HTTPError{Status: "503 Service Unavailable", StatusCode: 503}, errServer, 503},
		{&ai.HTTPError{Status: "529", StatusCode: 529, Body: "overloaded"}, errOverloaded, 529},
		{&ai.HTTPError{Status: "404 Not Found", StatusCode: 404}, errModel, 404},
		{fmt.Errorf("post: %w", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}), errNetwork, 0},
		{errors.New("dial tcp 10.0.0.1:443: connect: connection refused"), errNetwork, 0},
		{context.DeadlineExceeded, errNetwork, 0},
		{errors.New("provider returned 529: overloaded"), errOverloaded, 529},
		{errors.New("something odd at line 404"), errModel, 404},
		{errors.New("tool loop exploded"), errOther, 0},
	}
	for _, c := range cases {
		got := classifyError(c.err)
		if got.kind != c.kind || got.status != c.status {
			t.Errorf("%v: got %s/%d, want %s/%d", c.err, got.kind, got.status, c.kind, c.status)
		}
	}
	if got := classifyError(cases[0].err).cause; got != "Incorrect API key" {
		t.Errorf("provider message not extracted: %q", got)
	}
}

func TestErrorBannerRendering(t *testing.T) {
	for _, w := range []int{40, 80, 120} {
		m, _ := stClock(t)
		m.Update(mkWinSize(w, 30))
		m.appendTurnError(&ai.HTTPError{Status: "429 Too Many Requests", StatusCode: 429,
			Body: `{"error":{"message":"Rate limit reached for requests per minute. Please try again in 20s."}}`})
		view := m.View()
		plain := ansi.Strip(view)
		for _, want := range []string{"Rate limited · 429", "Rate limit reached", "Wait a moment"} {
			if !strings.Contains(plain, want) {
				t.Errorf("width %d: missing %q in\n%s", w, want, plain)
			}
		}
		for i, l := range strings.Split(view, "\n") {
			if lipgloss.Width(l) > w {
				t.Errorf("width %d: row %d is %d cells", w, i, lipgloss.Width(l))
			}
		}
	}
	// Untagged error rows keep their old look.
	if out, ok := renderErrorBanner("error: boom", 80); ok || out != "" {
		t.Fatal("plain error text must not become a typed banner")
	}
}

func TestContextBarInFooter(t *testing.T) {
	m, _ := stClock(t)
	m.agent.ContextLimit = 1000
	m.agent.Messages = append(m.agent.Messages, ai.Message{Role: "user", Content: strings.Repeat("word ", 600)})
	m.Update(mkWinSize(120, 30))
	footer := ansi.Strip(m.footerHints())
	if !strings.Contains(footer, "% ctx") || !strings.Contains(footer, "▰") {
		t.Fatalf("wide footer: %q", footer)
	}
	m.Update(mkWinSize(60, 30))
	if footer := ansi.Strip(m.footerHints()); strings.Contains(footer, "▰") || lipgloss.Width(footer) > 60 {
		t.Fatalf("narrow footer keeps only the percentage: %q", footer)
	}
	if got := ansi.Strip(contextBar(75, 4)); got != "▰▰▰▱" {
		t.Fatalf("bar: %q", got)
	}
}

func TestScheduleNextNote(t *testing.T) {
	m, clock := stClock(t)
	sc := session.Schedule{ID: 1, Schedule: "@every 5m", Prompt: "check CI", Anchor: clock.Add(-time.Minute), LastFire: clock.Add(-time.Minute)}
	if got := ansi.Strip(m.scheduleNextNote(sc)); got != " · next in 4m" {
		t.Fatalf("note: %q", got)
	}
	sc.LastFire = clock.Add(-10 * time.Minute)
	sc.Anchor = clock.Add(-10 * time.Minute)
	if got := ansi.Strip(m.scheduleNextNote(sc)); got != " · due now" {
		t.Fatalf("overdue: %q", got)
	}
	for d, want := range map[time.Duration]string{45 * time.Second: "45s", 2 * time.Minute: "2m", 65 * time.Minute: "1h05m", 50 * time.Hour: "2d"} {
		if got := fmtUntil(d); got != want {
			t.Errorf("fmtUntil(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestUsageCard(t *testing.T) {
	m, _ := stClock(t)
	m.agent.ContextLimit = 131072
	m.agent.Messages = append(m.agent.Messages, ai.Message{Role: "user", Content: strings.Repeat("word ", 4000)})
	m.agent.AddUsage(ai.Usage{PromptTokens: 48000, PromptCacheHitTokens: 36000, CompletionTokens: 2100})
	m.agent.AddSubUsage("glm-5.2-fast @ inference", ai.Usage{PromptTokens: 9000, CompletionTokens: 700})
	for _, w := range []int{40, 80, 120} {
		m.Update(mkWinSize(w, 40))
		m.command("/usage")
		view := m.View()
		plain := ansi.Strip(view)
		for _, want := range []string{"Usage", "Context", "131.1k", "Session", "Cache", "Cost", "By model", "subagent glm-5.2-fast"} {
			if !strings.Contains(plain, want) {
				t.Errorf("width %d: /usage lacks %q:\n%s", w, want, plain)
			}
		}
		for i, l := range strings.Split(view, "\n") {
			if lipgloss.Width(l) > w {
				t.Errorf("width %d: row %d is %d cells: %q", w, i, lipgloss.Width(l), ansi.Strip(l))
			}
		}
		m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
		if !m.usageOpen() || m.input.Value() != "" {
			t.Fatal("keys reach the composer under /usage")
		}
		m.key(tea.KeyMsg{Type: tea.KeyEsc})
		if m.usageOpen() || strings.Contains(ansi.Strip(m.View()), "By model") {
			t.Fatal("Esc does not close /usage")
		}
	}
	if !busyCmd("/usage") || !execNow["/usage"] {
		t.Fatal("/usage must run at once and while busy")
	}
}

// ── notifications and title ─────────────────────────────────────────────

func stNotifier(t *testing.T, mode string) (*model, *time.Time, *bytes.Buffer) {
	t.Helper()
	for _, k := range []string{"TERM_PROGRAM", "LC_TERMINAL", "KITTY_WINDOW_ID", "WT_SESSION", "VTE_VERSION", "TMUX"} {
		t.Setenv(k, "")
	}
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	m, clock := stClock(t)
	m.cfg.Notifications.Desktop = mode
	var out bytes.Buffer
	m.startStatus(&out)
	return m, clock, &out
}

func TestNotifyWhenUnfocused(t *testing.T) {
	m, clock, out := stNotifier(t, "")
	if !strings.HasPrefix(out.String(), "\x1b[22;0t") {
		t.Fatalf("title stack not pushed: %q", out.String())
	}
	m.Update(tea.BlurMsg{})
	stBegin(m, clock)
	*clock = clock.Add(30 * time.Second)
	out.Reset()
	m.Update(turnDoneMsg{})
	if !strings.Contains(out.String(), "\x1b]9;k-brain: Turn finished after 30s\x07") {
		t.Fatalf("no OSC 9 on turn end: %q", out.String())
	}

	m.Update(tea.FocusMsg{})
	stBegin(m, clock)
	out.Reset()
	m.Update(turnDoneMsg{})
	if strings.Contains(out.String(), "\x1b]9;") {
		t.Fatalf("notified while focused: %q", out.String())
	}

	m.Update(tea.BlurMsg{})
	stBegin(m, clock)
	out.Reset()
	m.receivePermission(permRequest{req: tools.GateRequest{Tool: "bash", Command: "rm -rf build"}, reply: make(chan permAnswer, 1)})
	m.Update(nil)
	if !strings.Contains(out.String(), "Approval needed: bash rm -rf build") {
		t.Fatalf("no approval notification: %q", out.String())
	}
}

func TestNotifyAutoNeedsFocusReports(t *testing.T) {
	m, clock, out := stNotifier(t, "auto")
	stBegin(m, clock)
	m.Update(turnDoneMsg{})
	if m.status().note.sent != 0 {
		t.Fatalf("auto mode notified without focus reports: %q", out.String())
	}
	m2, clock2, _ := stNotifier(t, "on")
	stBegin(m2, clock2)
	m2.Update(turnDoneMsg{})
	if m2.status().note.sent != 1 {
		t.Fatal("mode on must notify when focus is unknown")
	}
	m3, clock3, _ := stNotifier(t, "off")
	m3.Update(tea.BlurMsg{})
	stBegin(m3, clock3)
	m3.Update(turnDoneMsg{})
	if m3.status().note.sent != 0 {
		t.Fatal("mode off notified")
	}
}

func TestNotifyRoutes(t *testing.T) {
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	cases := []struct {
		mode string
		env  func(string) string
		want string
	}{
		{"auto", env("TERM_PROGRAM", "iTerm.app"), notifyOSC9},
		{"auto", env("TERM_PROGRAM", "WezTerm"), notifyOSC9},
		{"auto", env("TERM", "xterm-kitty"), notifyOSC9},
		{"auto", env("TERM_PROGRAM", "tmux", "LC_TERMINAL", "iTerm2"), notifyOSC9},
		{"auto", env("VTE_VERSION", "7200"), notifyOSC777},
		{"auto", env("TERM", "xterm-256color"), notifyBEL},
		{"osc777", env("TERM_PROGRAM", "iTerm.app"), notifyOSC777},
	}
	for _, c := range cases {
		if got := notifyRoute(c.mode, c.env); got != c.want {
			t.Errorf("%s: got %s, want %s", c.mode, got, c.want)
		}
	}

	var out bytes.Buffer
	n := notifier{out: &out, mode: "on", route: notifyOSC777, tmux: true}
	n.notify("k-brain", "done;\x1b[31mred\nline")
	got := out.String()
	if !strings.HasPrefix(got, "\x1bPtmux;\x1b\x1b]777;notify;k-brain;done;red line") || !strings.HasSuffix(got, "\x1b\\") {
		t.Fatalf("tmux passthrough: %q", got)
	}
	out.Reset()
	n = notifier{out: &out, mode: "on", route: notifyBEL}
	n.notify("t", "b")
	if out.String() != "\a" {
		t.Fatalf("bel: %q", out.String())
	}
}

func TestWindowTitleSpinner(t *testing.T) {
	m, clock, out := stNotifier(t, "off")
	m.sessTitle = "fix flags"
	title := func() string {
		cmd := m.statusAfter(nil)
		if cmd == nil {
			return ""
		}
		return fmt.Sprint(cmd())
	}
	if got := title(); got != "kn · fix flags" {
		t.Fatalf("idle title: %q", got)
	}
	if got := title(); got != "" {
		t.Fatalf("title set again without a change: %q", got)
	}
	stBegin(m, clock)
	first := m.windowTitle(*clock)
	*clock = clock.Add(titleFrame)
	if second := m.windowTitle(*clock); first == second || !strings.HasSuffix(second, "kn · fix flags") {
		t.Fatalf("spinner does not advance: %q %q", first, second)
	}
	m.receivePermission(permRequest{req: tools.GateRequest{Tool: "bash", Command: "make"}, reply: make(chan permAnswer, 1)})
	if got := m.windowTitle(*clock); !strings.HasPrefix(got, "◇ ") {
		t.Fatalf("waiting title: %q", got)
	}
	m.stopStatus()
	if !strings.HasSuffix(out.String(), "\x1b[23;0t") {
		t.Fatalf("title stack not popped: %q", out.String())
	}
}

func TestNoTerminalWritesWithoutWriter(t *testing.T) {
	m, clock := stClock(t)
	m.Update(tea.BlurMsg{})
	stBegin(m, clock)
	m.Update(turnDoneMsg{})
	if n := m.status().note; n.sent != 0 || n.lastTitle != "" {
		t.Fatal("a model without a terminal must not notify or set the title")
	}
}

// ── status line ─────────────────────────────────────────────────────────

func TestStatusLineCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	got, err := runStatusLine(`printf '\n  \033[1mbranch %s\033[0m\nsecond\n' "$(cat | sed -n 's/.*"session_id":"\([^"]*\)".*/\1/p')"`,
		[]byte(`{"session_id":"s-42"}`), 2*time.Second)
	if err != nil || got != "branch s-42" {
		t.Fatalf("got %q, %v", got, err)
	}
	start := time.Now()
	if _, err := runStatusLine("sleep 5", nil, 150*time.Millisecond); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("timeout not enforced: %v after %v", err, time.Since(start))
	}
}

func TestStatusLineFooterAndRateLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	m, clock := stClock(t)
	if cmd := m.statusLineCmd(*clock); cmd != nil {
		t.Fatal("status line runs while unset")
	}
	m.cfg.StatusLine = config.StatusLineConfig{Command: "echo 'ci: green'", IntervalMs: 1000}
	cmd := m.statusLineCmd(*clock)
	if cmd == nil {
		t.Fatal("no run when due")
	}
	if again := m.statusLineCmd(*clock); again != nil {
		t.Fatal("second run while the first is going")
	}
	m.Update(cmd())
	m.Update(mkWinSize(100, 30))
	if footer := ansi.Strip(m.footerHints()); !strings.Contains(footer, "ci: green") {
		t.Fatalf("footer: %q", footer)
	}
	*clock = clock.Add(500 * time.Millisecond)
	if m.statusLineCmd(*clock) != nil {
		t.Fatal("ran inside the interval")
	}
	*clock = clock.Add(time.Second)
	if m.statusLineCmd(*clock) != nil {
		t.Fatal("reran an unchanged session before it went stale")
	}
	m.goal = "ship it"
	*clock = clock.Add(time.Second)
	if m.statusLineCmd(*clock) == nil {
		t.Fatal("no rerun after the session changed")
	}
	stale := m.status().line.seq
	m.Update(statusLineMsg{seq: stale - 1, text: "old"})
	if m.status().line.text != "ci: green" {
		t.Fatal("a stale run replaced the text")
	}
}

// ── goldens ─────────────────────────────────────────────────────────────

// statusGoldenScenes pin the status row in each of its states, the typed
// error banners and the /usage card. Regenerate with
//
//	go test ./internal/tui -run TestGoldenStatusViews -update-golden
var statusGoldenScenes = []struct {
	name  string
	build func(m *model, clock *time.Time) string
}{
	{"statusrows", goldenStatusRows},
	{"errors", func(m *model, clock *time.Time) string {
		m.appendRaw(blockUser, "run the tests")
		for _, err := range []error{
			&ai.HTTPError{Status: "401 Unauthorized", StatusCode: 401, Body: `{"error":{"message":"Incorrect API key provided: sk-…abcd."}}`},
			&ai.HTTPError{Status: "429 Too Many Requests", StatusCode: 429, Body: `{"error":{"message":"Rate limit reached for requests per minute."}}`},
			&ai.HTTPError{Status: "400 Bad Request", StatusCode: 400, Body: `{"error":{"message":"This model's maximum context length is 131072 tokens."}}`},
			fmt.Errorf("post https://api.example.com/v1/chat/completions: %w", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}),
			&ai.HTTPError{Status: "503 Service Unavailable", StatusCode: 503, Body: "upstream connect error"},
		} {
			m.appendTurnError(err)
		}
		m.layout()
		return m.View()
	}},
	{"usage", func(m *model, clock *time.Time) string {
		m.agent.ContextLimit = 131072
		m.agent.Messages = append(m.agent.Messages, ai.Message{Role: "user", Content: strings.Repeat("word ", 30000)})
		m.agent.AddUsage(ai.Usage{PromptTokens: 48210, PromptCacheHitTokens: 36000, CompletionTokens: 2140})
		m.agent.AddSubUsage("glm-5.2-fast @ inference", ai.Usage{PromptTokens: 9100, CompletionTokens: 730})
		m.lastResp = ai.Usage{PromptTokens: 24100, PromptCacheHitTokens: 20000, CompletionTokens: 410}
		m.command("/usage")
		m.layout()
		return m.View()
	}},
}

// goldenStatusRows stacks the status row in each state, one model per state.
func goldenStatusRows(base *model, clock *time.Time) string {
	states := []struct {
		name  string
		build func(m *model)
	}{
		{"preparing", func(m *model) {}},
		{"waiting", func(m *model) { m.Update(phaseMsg(phaseWaiting)); *clock = clock.Add(2 * time.Second) }},
		{"thinking", func(m *model) { m.Update(thinkMsg(strings.Repeat("x", 900))); *clock = clock.Add(4 * time.Second) }},
		{"writing", func(m *model) {
			m.Update(usageMsg(ai.Usage{CompletionTokens: 3120}))
			m.Update(textMsg("ok"))
		}},
		{"tool", func(m *model) {
			m.Update(toolStartMsg{id: stTool("b1"), name: "bash", args: `{"command":"go test ./internal/tui/..."}`})
			*clock = clock.Add(3200 * time.Millisecond)
		}},
		{"retrying", func(m *model) {
			m.Update(retryMsg{Attempt: 2, Max: 8, Delay: 4 * time.Second, Err: &ai.HTTPError{Status: "529", StatusCode: 529, Body: "overloaded"}})
		}},
		{"compacting", func(m *model) { m.Update(compactStartMsg{took: 18, est: 48000}); *clock = clock.Add(9 * time.Second) }},
		{"approval", func(m *model) {
			m.receivePermission(permRequest{req: tools.GateRequest{Tool: "bash", Command: "make"}, reply: make(chan permAnswer, 1)})
		}},
		{"interrupt", func(m *model) { m.interrupt1 = true }},
	}
	start := *clock
	var rows []string
	for _, s := range states {
		*clock = start.Add(-42 * time.Second)
		m := compactCmdModel()
		m.now = base.now
		m.permissionMode = "normal"
		m.applyAppearance()
		m.Update(mkWinSize(base.width, base.height))
		stBegin(m, clock)
		*clock = start
		s.build(m)
		rows = append(rows, "# "+s.name, m.busyRow())
	}
	return strings.Join(rows, "\n")
}

func TestGoldenStatusViews(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("K_BRAIN_HOME", filepath.Join(home, ".k-brain"))
	dir := filepath.Join(home, "src", "acme", "kn")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	prevNow := toolTimer.now
	defer func() { toolTimer.now = prevNow }()
	defer SetLightTheme(false)
	defer setASCIIGlyphs(false)

	for _, scene := range statusGoldenScenes {
		for _, mode := range goldenModes {
			for _, size := range goldenSizes {
				name := fmt.Sprintf("%s_%s_%dx%d", scene.name, mode.name, size[0], size[1])
				t.Run(name, func(t *testing.T) {
					restore := useProfile(t, mode.profile, mode.dark)
					defer restore()
					SetLightTheme(!mode.dark)
					if mode.mono {
						setMonoMode(true, true)
					}
					setASCIIGlyphs(mode.ascii)

					clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
					toolTimer.now = func() time.Time { return clock }

					m := compactCmdModel()
					m.now = func() time.Time { return clock }
					m.permissionMode = "always"
					m.applyAppearance()
					m.Update(mkWinSize(size[0], size[1]))
					view := scene.build(m, &clock)

					for i, l := range strings.Split(view, "\n") {
						if w := lipgloss.Width(l); w > size[0] {
							t.Errorf("row %d is %d cells wide on a %d-column screen: %q", i, w, size[0], ansi.Strip(l))
						}
					}
					if mode.mono && (strings.Contains(view, "\x1b[38;") || strings.Contains(view, "\x1b[48;")) {
						t.Errorf("no-colour view carries a colour escape")
					}
					if mode.ascii {
						for _, r := range ansi.Strip(view) {
							if r > 0x7e {
								t.Errorf("ascii view contains %q", r)
								break
							}
						}
					}
					checkGolden(t, name+".ansi", view)
					checkGolden(t, name+".txt", stripView(view))
				})
			}
		}
	}
}
