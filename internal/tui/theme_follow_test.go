package tui

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// inputMsgs runs raw terminal input through bubbletea's own input reader and
// returns the messages it produces, so the tests see a reply exactly as a
// running kn would.
func inputMsgs(t *testing.T, raw string) []tea.Msg {
	t.Helper()
	c := &msgCollector{}
	p := tea.NewProgram(c,
		tea.WithInput(strings.NewReader(raw+"\x03")),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithoutSignalHandler(),
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := p.Run(); err != nil {
			t.Errorf("program: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		p.Kill()
		t.Fatal("input reader did not finish")
	}
	return c.msgs
}

type msgCollector struct{ msgs []tea.Msg }

func (c *msgCollector) Init() tea.Cmd { return nil }
func (c *msgCollector) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.Type == tea.KeyCtrlC {
		return c, tea.Quit
	}
	switch msg.(type) {
	case tea.KeyMsg, tea.FocusMsg, tea.BlurMsg:
		c.msgs = append(c.msgs, msg)
	default:
		// Unknown sequences arrive as unexported types; keep them too.
		if s, ok := msg.(interface{ String() string }); ok && strings.HasPrefix(s.String(), "?CSI") {
			c.msgs = append(c.msgs, msg)
		}
	}
	return c, nil
}
func (c *msgCollector) View() string { return "" }

// followModel is a model in a running session whose saved theme follows
// the terminal, with the terminal writes captured.
func followModel(t *testing.T) (*model, *bytes.Buffer, *time.Time) {
	t.Helper()
	t.Setenv("K_BRAIN_THEME", "dark")
	m := surfaceModel(t)
	m.setTheme("auto")
	t.Cleanup(func() { m.setTheme("dark"); setSchemeOverride(""); SetLightTheme(false) })
	SetLightTheme(false)
	var out bytes.Buffer
	m.status().note.out = &out
	prev := tuiRunning
	tuiRunning = true
	t.Cleanup(func() { tuiRunning = prev })
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m.now = func() time.Time { return clock }
	return m, &out, &clock
}

// feedAll delivers msgs to m back to back, as the input reader would, then
// runs what they returned until it settles, passing on the theme switch and
// flush messages the guard produces.
func feedAll(m *model, msgs ...tea.Msg) {
	var cmds []tea.Cmd
	for _, msg := range msgs {
		_, cmd := m.Update(msg)
		cmds = append(cmds, cmd)
	}
	for _, cmd := range cmds {
		for _, next := range runCmd(cmd) {
			feedAll(m, next)
		}
	}
}

func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(time.Second):
		return nil
	}
	switch msg := msg.(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, runCmd(c)...)
		}
		return out
	case themeSyncMsg, bgReplyFlush:
		return []tea.Msg{msg}
	}
	return nil
}

// Every form of the reply switches the variant and leaves nothing in the
// composer: OSC 11 ended by ST or BEL, with 4- or 2-digit components, and
// the mode-2031 / DSR 996 report.
func TestAppearanceReplyNeverReachesComposer(t *testing.T) {
	for _, tc := range []struct{ name, reply string }{
		{"osc11-st", "\x1b]11;rgb:ffff/ffff/ffff\x1b\\"},
		{"osc11-bel", "\x1b]11;rgb:fa/fa/fa\x07"},
		{"dsr997", "\x1b[?997;2n"},
		{"both", "\x1b]11;rgb:f0f0/f0f0/f0f0\x1b\\\x1b[?997;2n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, out, _ := followModel(t)
			feedAll(m, tea.FocusMsg{})
			if got := out.String(); got != bgRuntimeQuery {
				t.Fatalf("focus-in should query the background, wrote %q", got)
			}
			msgs := inputMsgs(t, tc.reply)
			if len(msgs) == 0 {
				t.Fatal("the input reader produced nothing")
			}
			feedAll(m, msgs...)
			if v := m.input.Value(); v != "" {
				t.Errorf("the reply leaked into the composer: %q (messages %#v)", v, msgs)
			}
			if CurrentTheme() != "light" {
				t.Errorf("a light background should switch to the light variant, got %q", CurrentTheme())
			}
			if v := surfScreen(m); !strings.Contains(v, "theme: auto → light") {
				t.Errorf("the switch should be noted in the transcript:\n%s", v)
			}
		})
	}
}

// A dark reply on a dark screen changes nothing.
func TestAppearanceReplySameVariantIsQuiet(t *testing.T) {
	m, _, _ := followModel(t)
	feedAll(m, tea.FocusMsg{})
	feedAll(m, inputMsgs(t, "\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\")...)
	if CurrentTheme() != "dark" || strings.Contains(surfScreen(m), "terminal appearance changed") {
		t.Errorf("an unchanged background should switch nothing")
	}
}

// Mode-2031 reports arrive without a query and switch back and forth.
func TestUnsolicitedSchemeReports(t *testing.T) {
	m, _, _ := followModel(t)
	feedAll(m, inputMsgs(t, "\x1b[?997;2n")...)
	if CurrentTheme() != "light" {
		t.Fatalf("light report: %q", CurrentTheme())
	}
	feedAll(m, inputMsgs(t, "\x1b[?997;1n")...)
	if CurrentTheme() != "dark" {
		t.Fatalf("dark report: %q", CurrentTheme())
	}
}

// Keys that only look like the start of a reply are typed as usual, in
// order; outside the window after a query nothing is held at all.
func TestAppearanceGuardReplaysTyping(t *testing.T) {
	m, _, clock := followModel(t)
	feedAll(m, tea.FocusMsg{})
	feedAll(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]"), Alt: true}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ab")})
	if v := m.input.Value(); !strings.HasSuffix(v, "ab") {
		t.Errorf("typing after a held Alt+] should come through: %q", v)
	}
	// A partial reply that stops is held until the gap runs out, then
	// replayed, since it never completed.
	m.input.SetValue("")
	feedAll(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]"), Alt: true}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("11")})
	if v := m.input.Value(); !strings.HasSuffix(v, "11") {
		t.Errorf("an unfinished reply should be replayed: %q", v)
	}

	*clock = clock.Add(bgReplyWindow + time.Second)
	m.input.SetValue("")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("11;rgb:ffff/ffff/ffff")})
	if cmd != nil && len(runCmd(cmd)) > 0 || m.input.Value() != "11;rgb:ffff/ffff/ffff" {
		t.Errorf("with no query outstanding, text is just text: %q", m.input.Value())
	}
}

// A saved theme that does not follow the terminal sends no query, and
// focus flapping is throttled.
func TestAppearanceQueryThrottleAndPinned(t *testing.T) {
	m, out, clock := followModel(t)
	feedAll(m, tea.FocusMsg{}, tea.BlurMsg{}, tea.FocusMsg{})
	if n := strings.Count(out.String(), "\x1b]11;?"); n != 1 {
		t.Errorf("queries within %s: %d, want 1", bgQueryMinGap, n)
	}
	*clock = clock.Add(bgQueryMinGap + time.Millisecond)
	feedAll(m, tea.FocusMsg{})
	if n := strings.Count(out.String(), "\x1b]11;?"); n != 2 {
		t.Errorf("queries after the gap: %d, want 2", n)
	}
	out.Reset()
	*clock = clock.Add(time.Minute)
	m.setTheme("dark")
	feedAll(m, tea.FocusMsg{}, themePollMsg{})
	if out.Len() != 0 {
		t.Errorf("a pinned theme should not query: %q", out.String())
	}
	m.setTheme("dusk")
	feedAll(m, inputMsgs(t, "\x1b[?997;2n")...)
	if CurrentTheme() != "dark" {
		t.Errorf("a dark-only theme ignores scheme reports, got %q", CurrentTheme())
	}
}

// A reply cut across reads arrives as different keys: an ESC on its own
// before "]11;…", or the ST's ESC and "\" apart. Both are still taken out.
func TestAppearanceReplyCutAcrossReads(t *testing.T) {
	runes := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
	for name, keys := range map[string][]tea.Msg{
		"esc-first": {tea.KeyMsg{Type: tea.KeyEsc}, runes("]11;rgb:ffff/ffff/ffff"), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(`\`), Alt: true}},
		"st-split":  {tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]"), Alt: true}, runes("11;rgb:ffff/"), runes("ffff/ffff"), tea.KeyMsg{Type: tea.KeyEsc}, runes(`\`)},
	} {
		t.Run(name, func(t *testing.T) {
			m, _, _ := followModel(t)
			feedAll(m, tea.FocusMsg{})
			feedAll(m, keys...)
			if v := m.input.Value(); v != "" {
				t.Errorf("leaked into the composer: %q", v)
			}
			if CurrentTheme() != "light" {
				t.Errorf("variant %q, want light", CurrentTheme())
			}
		})
	}
}
