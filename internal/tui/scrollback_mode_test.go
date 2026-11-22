package tui

import (
	"bytes"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// nudgeMsg is a message nothing handles: an Update that only runs the
// inline hook.
type nudgeMsg struct{}

func inlineTestModel(t *testing.T) *model {
	t.Helper()
	m := compactCmdModel()
	m.inl = newInlineState()
	m.mouseOn = false
	m.appendBanner()
	m.Update(mkWinSize(80, 24))
	return m
}

// takePrinted acknowledges every print in flight and returns what they
// printed, escapes stripped.
func takePrinted(m *model) string {
	var lines []string
	for len(m.inl.batches) > 0 {
		lines = append(lines, m.inl.batches[0].lines...)
		m.Update(inlinePrintedMsg{})
	}
	return ansi.Strip(strings.Join(lines, "\n"))
}

func TestInlineWanted(t *testing.T) {
	on, off := true, false
	defer func() { InlineOverride = nil }()
	if inlineWanted(&config.Config{}) {
		t.Fatal("inline mode is off by default")
	}
	if !inlineWanted(&config.Config{Inline: &on}) {
		t.Fatal("the inline key turns it on")
	}
	InlineOverride = &off
	if inlineWanted(&config.Config{Inline: &on}) {
		t.Fatal("--inline=false wins over the config")
	}
	InlineOverride = &on
	if !inlineWanted(nil) {
		t.Fatal("--inline turns it on")
	}
}

// A reply is printed once, when it stops streaming; until its print is
// acknowledged it stays in the live region, after that only the scrollback
// has it.
func TestInlinePrintsReplyWhenFinished(t *testing.T) {
	m := inlineTestModel(t)
	if got := takePrinted(m); got != "" {
		t.Fatalf("the startup card is never printed, got %q", got)
	}
	if !strings.Contains(ansi.Strip(m.View()), "k-brain") {
		t.Fatalf("the startup card shows in the live region:\n%s", ansi.Strip(m.View()))
	}

	m.busy = true
	m.appendRaw(blockUser, "inline question")
	m.Update(textMsg("first streamed line\nsecond str"))
	printed := takePrinted(m)
	if !strings.Contains(printed, "inline question") {
		t.Fatalf("the prompt should print at once, got %q", printed)
	}
	if strings.Contains(printed, "first streamed line") {
		t.Fatal("a streaming reply must not print before it ends")
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, "first streamed line") || !strings.Contains(v, "second str") || strings.Contains(v, "inline question") {
		t.Fatalf("the live region should hold the streaming reply and nothing printed:\n%s", v)
	}

	m.Update(textMsg("eam line\n"))
	m.flushCurrent()
	m.busy = false
	m.Update(nudgeMsg{})
	if len(m.inl.batches) != 1 {
		t.Fatalf("want one print in flight, got %d", len(m.inl.batches))
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, "second stream line") {
		t.Fatalf("an unacknowledged print stays on screen:\n%s", v)
	}
	printed = takePrinted(m)
	if !strings.Contains(printed, "first streamed line") || !strings.Contains(printed, "second stream line") {
		t.Fatalf("the finished reply should print, got %q", printed)
	}
	if strings.Count(printed, "second stream line") != 1 {
		t.Fatalf("printed twice: %q", printed)
	}
	if v := ansi.Strip(m.View()); strings.Contains(v, "second stream line") {
		t.Fatalf("a printed reply leaves the live region:\n%s", v)
	}
	m.Update(nudgeMsg{})
	if got := takePrinted(m); got != "" {
		t.Fatalf("nothing new to print, got %q", got)
	}
}

// A tool call prints when it is final: not while it runs, and in a busy turn
// not before its structured result has settled the header.
func TestInlineToolPrintsWhenSettled(t *testing.T) {
	m := inlineTestModel(t)
	m.busy = true
	m.appendRaw(blockUser, "run it")
	m.Update(nudgeMsg{})
	takePrinted(m)

	m.Update(toolStartMsg{id: "b1", name: "bash", args: `{"command":"go vet ./..."}`})
	if got := takePrinted(m); got != "" {
		t.Fatalf("a running call stays live, printed %q", got)
	}
	m.Update(toolEndMsg{id: "b1", name: "bash", result: "vet output line"})
	if got := takePrinted(m); got != "" {
		t.Fatalf("the header waits for its result, printed %q", got)
	}
	m.Update(toolResultMsg{id: "b1", name: "bash", result: tools.Result{}})
	got := takePrinted(m)
	if !strings.Contains(got, "go vet") || !strings.Contains(got, "vet output line") {
		t.Fatalf("the settled call should print with its output, got %q", got)
	}

	// Read-only calls may still fold into a group while the turn runs.
	m.Update(toolStartMsg{id: "r1", name: "read", args: `{"path":"a.go"}`})
	m.Update(toolEndMsg{id: "r1", name: "read", result: "package a"})
	m.Update(toolResultMsg{id: "r1", name: "read", result: tools.Result{}})
	m.Update(toolStartMsg{id: "r2", name: "read", args: `{"path":"b.go"}`})
	m.Update(toolEndMsg{id: "r2", name: "read", result: "package b"})
	m.Update(toolResultMsg{id: "r2", name: "read", result: tools.Result{}})
	if got := takePrinted(m); got != "" {
		t.Fatalf("a trailing run of reads stays live in a busy turn, printed %q", got)
	}
	m.busy = false
	m.Update(nudgeMsg{})
	got = takePrinted(m)
	if !strings.Contains(got, "a.go") || !strings.Contains(got, "b.go") {
		t.Fatalf("the reads print when the turn ends, got %q", got)
	}
}

// Printed blocks are separated as the transcript separates them, and a
// block printed earlier is printed again when Alt+O expands it.
func TestInlineGapsAndExpandedReprint(t *testing.T) {
	m := inlineTestModel(t)
	m.appendRaw(blockUser, "one")
	m.appendRaw(blockAssistant, "two")
	m.Update(nudgeMsg{})
	got := takePrinted(m)
	if !strings.Contains(got, "one\n\n") {
		t.Fatalf("a blank row separates unglued blocks, got %q", got)
	}

	m.blocks = append(m.blocks, block{kind: blockTool, toolName: "bash", text: strings.Repeat("output row\n", 12)})
	m.refreshVP()
	m.Update(nudgeMsg{})
	folded := takePrinted(m)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}, Alt: true})
	if !m.blocks[len(m.blocks)-1].expanded {
		t.Fatal("Alt+O should expand the last tool block")
	}
	expanded := takePrinted(m)
	if strings.Count(expanded, "output row") <= strings.Count(folded, "output row") {
		t.Fatalf("the expanded copy should show more rows:\nfolded %q\nexpanded %q", folded, expanded)
	}
}

// /clear and the like leave the old transcript in the scrollback, print a
// rule, and print the conversation as it now stands.
func TestInlineResetPrintsRuleAndNewTranscript(t *testing.T) {
	m := inlineTestModel(t)
	m.appendRaw(blockUser, "before the reset")
	m.Update(nudgeMsg{})
	takePrinted(m)

	m.resetTranscript()
	m.Update(nudgeMsg{})
	if got := takePrinted(m); !strings.Contains(got, "────") {
		t.Fatalf("a reset prints a rule, got %q", got)
	}
	m.appendRaw(blockUser, "before the reset")
	m.Update(nudgeMsg{})
	if got := takePrinted(m); !strings.Contains(got, "before the reset") {
		t.Fatalf("the new transcript prints in full, got %q", got)
	}
}

// Leaving kn clears the live region and prints what never left it.
func TestInlineFinishPrintsTheRest(t *testing.T) {
	m := inlineTestModel(t)
	m.busy = true
	m.appendRaw(blockUser, "question")
	m.Update(textMsg("cut short\n"))
	takePrinted(m)
	m.View()
	rows := m.inl.frameRows
	var out bytes.Buffer
	m.inlineFinish(&out)
	got := out.String()
	if !strings.HasPrefix(got, ansi.CursorUp(rows-1)+"\r"+ansi.EraseScreenBelow) {
		t.Fatalf("finish should first clear the %d-row live region, got %q", rows, got)
	}
	if !strings.Contains(got, "cut short") || strings.Contains(got, "question") {
		t.Fatalf("finish prints only what was never printed, got %q", got)
	}
}

// A full-screen view takes the alternate screen and gives it back; nothing
// prints while it is open, and the inline frame stays put until bubbletea
// has switched screens.
func TestInlineFullScreenViewSwitchesScreens(t *testing.T) {
	m := inlineTestModel(t)
	m.appendRaw(blockUser, "printed first")
	m.Update(nudgeMsg{})
	takePrinted(m)
	frame := m.View()

	m.openPalette()
	m.Update(nudgeMsg{})
	if !m.inl.entering || m.inl.altOn {
		t.Fatal("opening the palette should ask for the alternate screen")
	}
	if m.View() != frame {
		t.Fatal("the inline frame stays until the switch is acknowledged")
	}
	m.Update(inlineAltMsg{})
	if !m.inl.altOn || m.inlineLive() {
		t.Fatal("the switch is acknowledged")
	}
	m.appendRaw(blockUser, "while the palette is open")
	m.Update(nudgeMsg{})
	if len(m.inl.batches) != 0 {
		t.Fatal("nothing prints on the alternate screen")
	}

	m.palette = nil
	m.Update(nudgeMsg{})
	if m.inl.altOn || !m.inlineLive() {
		t.Fatal("closing the palette goes back to the normal screen")
	}
	if got := takePrinted(m); !strings.Contains(got, "while the palette is open") {
		t.Fatalf("what became final meanwhile prints on the way back, got %q", got)
	}
}

func TestDoctorReportsScreen(t *testing.T) {
	m := inlineTestModel(t)
	if !strings.Contains(m.doctorReport(), "screen               inline") {
		t.Fatalf("/doctor should say kn runs inline:\n%s", m.doctorReport())
	}
	m.inl = nil
	if !strings.Contains(m.doctorReport(), "screen               alternate screen") {
		t.Fatalf("/doctor should say kn runs on the alternate screen:\n%s", m.doctorReport())
	}
}
