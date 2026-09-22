package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/tools"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

type interactive struct {
	keys    chan []byte
	output  string
	await   bool
	awaitcd int
}

type (
	interactiveStartMsg struct{ keys chan []byte }
	interactiveOutMsg   struct{ chunk string }
	interactiveAwaitMsg struct{ secsLeft int }
	interactiveDoneMsg  struct {
		output string
		exit   string
	}
)

type interactiveRunner struct {
	prog *tea.Program

	mu   sync.Mutex
	keys chan []byte
}

func newInteractiveRunner(prog *tea.Program) *interactiveRunner {
	return &interactiveRunner{prog: prog}
}

func (r *interactiveRunner) Run(ctx context.Context, command string, timeout time.Duration, _ <-chan []byte) string {
	keys := make(chan []byte, 16)
	r.mu.Lock()
	r.keys = keys
	r.mu.Unlock()
	r.prog.Send(interactiveStartMsg{keys: keys})

	res := bashrun.Run(ctx, bashrun.Options{
		Command:           command,
		Timeout:           timeout,
		Interactive:       true,
		InactivityTimeout: 15 * time.Second,
		OnOutput:          func(chunk string) { r.prog.Send(interactiveOutMsg{chunk}) },
		OnAwaitInput:      func(s int) { r.prog.Send(interactiveAwaitMsg{secsLeft: s}) },
		Keys:              keys,
	})

	r.mu.Lock()
	r.keys = nil
	r.mu.Unlock()

	r.prog.Send(interactiveDoneMsg{output: res.Output, exit: res.Exit})

	return interactiveOutput(res)
}

func interactiveOutput(res bashrun.Result) string {
	switch {
	case res.Output == "" && res.Exit == "":
		return "(no output)"
	case res.Exit == "":
		return res.Output
	default:
		return res.Output + "\n(" + res.Exit + ")"
	}
}

func (m *model) iactiveKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:

		if !m.interrupt1 {
			m.interrupt1 = true
			return m, nil
		}
		if m.cancel != nil {
			m.cancel()
		}
		return m, nil
	case tea.KeyEsc:

		m.sendKeys([]byte{0x1b})
		return m, nil
	case tea.KeyEnter:
		m.sendKeys([]byte("\r"))
		return m, nil
	case tea.KeyTab:
		m.sendKeys([]byte("\t"))
		return m, nil
	case tea.KeyBackspace, tea.KeyDelete:
		m.sendKeys([]byte{0x7f})
		return m, nil
	case tea.KeyUp, tea.KeyDown, tea.KeyLeft, tea.KeyRight:

		m.sendKeys([]byte(arrowBytes(msg.Type)))
		return m, nil
	case tea.KeyCtrlJ:

		m.sendKeys([]byte("\r"))
		return m, nil
	}

	if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
		var buf []byte
		if msg.Alt {
			buf = append(buf, 0x1b)
		}
		for _, r := range msg.Runes {
			var rb [4]byte
			n := utf8.EncodeRune(rb[:], r)
			buf = append(buf, rb[:n]...)
		}
		if len(buf) > 0 {
			m.sendKeys(buf)
		}
		return m, nil
	}

	return m, nil
}

func (m *model) sendKeys(b []byte) {
	if m.iactive == nil {
		return
	}
	select {
	case m.iactive.keys <- b:
	default:

	}
}

func arrowBytes(t tea.KeyType) string {
	switch t {
	case tea.KeyUp:
		return bashrun.KeyUp
	case tea.KeyDown:
		return bashrun.KeyDown
	case tea.KeyLeft:
		return bashrun.KeyLeft
	case tea.KeyRight:
		return bashrun.KeyRight
	}
	return ""
}

func (m *model) interactiveView() string {
	if m.iactive == nil {
		return ""
	}
	const maxLines = 12
	out := m.iactive.output
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	rendered := dimStyle.Render("  " + strings.Join(lines, "\n  "))

	header := toolStyle.Render("⚒ bash (interactive)")
	if m.iactive.await {
		header += errStyle.Render(fmt.Sprintf(
			"  ⏳ waiting for input — cancels in %ds", m.iactive.awaitcd,
		))
	} else {
		header += dimStyle.Render("  (type to respond; Ctrl+C Ctrl+C to cancel)")
	}
	return header + "\n" + rendered
}

var _ tools.InteractiveRunner = (*interactiveRunner)(nil)
