package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func seedCall(id, name, args string) ai.ToolCall {
	var tc ai.ToolCall
	tc.ID, tc.Function.Name, tc.Function.Arguments = id, name, args
	return tc
}

func blockRows(m *model) string {
	var rows []string
	for _, b := range m.blocks {
		rows = append(rows, ansi.Strip(b.render(m.width)))
	}
	return strings.Join(rows, "\n")
}

// A resumed session draws its tool calls as the live turn finished them: a
// header with outcome and duration, and the result directly under its own
// header even when a batch's results arrive after all of its calls.
func TestSeedRebuildsToolBlocks(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))

	ok := seedCall("c1", "bash", `{"command":"go test ./..."}`)
	ok.DurationMs = 2400
	zero := 0
	ok.ExitStatus = &zero
	bad := seedCall("c2", "bash", `{"command":"go vet ./..."}`)
	bad.ExitCode, bad.DurationMs = 1, 1100
	one := 1
	bad.ExitStatus = &one
	slow := seedCall("c3", "bash", `{"command":"sleep 999"}`)
	slow.TimedOut, slow.ExitCode = true, 1
	read := seedCall("c4", "read", `{"path":"x.go"}`)

	m.seedTranscript([]ai.Message{
		{Role: "user", Content: "run the checks", Authored: true},
		{Role: "assistant", Content: "Running them.", ToolCalls: []ai.ToolCall{ok, bad, slow, read}},
		{Role: "tool", ToolCallID: "c1", Name: "bash", Content: "ok  \tpkg\t2.1s"},
		{Role: "tool", ToolCallID: "c2", Name: "bash", Content: "vet: x.go:3: unused\n(exit: exit status 1)"},
		{Role: "tool", ToolCallID: "c3", Name: "bash", Content: "Error: command timed out"},
		{Role: "tool", ToolCallID: "c4", Name: "read", Content: "package x\n\nfunc f() {}\n"},
		{Role: "assistant", Content: "Done."},
	}, 1)

	var kinds []blockKind
	for _, b := range m.blocks {
		kinds = append(kinds, b.kind)
	}
	want := []blockKind{blockUser, blockAssistant,
		blockToolRun, blockTool, blockToolRun, blockTool, blockToolRun, blockTool, blockToolRun, blockTool,
		blockAssistant}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v\n%s", kinds, want, blockRows(m))
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v\n%s", kinds, want, blockRows(m))
		}
	}
	for i, id := range []string{"c1", "c2", "c3", "c4"} {
		hdr := m.blocks[2+2*i]
		if hdr.toolID != id || hdr.toolRunning {
			t.Fatalf("header %d = %+v", i, hdr)
		}
		if res := m.blocks[3+2*i]; res.toolName != hdr.toolName {
			t.Fatalf("result %d sits under the wrong header: %q under %q", i, res.toolName, hdr.toolName)
		}
	}
	if m.blocks[2].toolFailed || !m.blocks[4].toolFailed || !m.blocks[6].toolFailed || m.blocks[8].toolFailed {
		t.Fatalf("failed flags wrong: %v %v %v %v", m.blocks[2].toolFailed, m.blocks[4].toolFailed, m.blocks[6].toolFailed, m.blocks[8].toolFailed)
	}
	if r := m.blocks[2].run; r == nil || r.exitCode == nil || *r.exitCode != 0 || r.duration.Milliseconds() != 2400 {
		t.Fatalf("run meta of c1 = %+v", r)
	}
	if r := m.blocks[6].run; r == nil || !r.timedOut {
		t.Fatalf("run meta of c3 = %+v", r)
	}

	view := blockRows(m)
	for _, s := range []string{"2.4s", "exit 1 · 1.1s", "timed out", "pkg    2.1s", "unused"} {
		if !strings.Contains(view, s) {
			t.Errorf("resumed transcript is missing %q:\n%s", s, view)
		}
	}
	// msgBlock still points at the message blocks, not at shifted results.
	if b := m.blocks[m.msgBlock[7]]; b.kind != blockAssistant || b.text != "Done." {
		t.Fatalf("msgBlock[7] -> %+v", b)
	}
	if b := m.blocks[m.msgBlock[1]]; b.kind != blockUser {
		t.Fatalf("msgBlock[1] -> %+v", b)
	}
}

func TestSeedMarksCancelledTool(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	m.seedTranscript([]ai.Message{
		{Role: "assistant", ToolCalls: []ai.ToolCall{seedCall("c1", "bash", `{"command":"make"}`)}},
		{Role: "tool", ToolCallID: "c1", Name: "bash", Content: "partial\n(exit: cancelled)"},
	}, 1)
	hdr := m.blocks[0]
	if hdr.kind != blockToolRun || hdr.run == nil || !hdr.run.cancelled || hdr.toolFailed {
		t.Fatalf("cancelled call = %+v run=%+v", hdr, hdr.run)
	}
}

// Thinking comes back with the reply it preceded while thinking is shown,
// and stays hidden when it is not.
func TestSeedRebuildsThinking(t *testing.T) {
	msgs := []ai.Message{
		{Role: "user", Content: "why?", Authored: true},
		{Role: "assistant", Reasoning: "first I check the logs\nthen the config", Content: "Because of the cache."},
	}
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	m.showThinking = true
	m.seedTranscript(msgs, 1)
	if len(m.blocks) != 3 || m.blocks[1].kind != blockThinking || m.blocks[2].kind != blockAssistant {
		t.Fatalf("thinking block missing or out of order:\n%s", blockRows(m))
	}
	if got := ansi.Strip(m.blocks[1].render(100)); got != "◌ Thought · 2 lines" {
		t.Fatalf("restored thinking = %q, want it folded", got)
	}
	m.blocks[1].toggle()
	if got := ansi.Strip(m.blocks[1].render(100)); !strings.Contains(got, "first I check the logs") || !strings.Contains(got, "then the config") {
		t.Fatalf("expanded thinking = %q", got)
	}
	if m.msgBlock[2] != 2 {
		t.Fatalf("msgBlock of the reply = %d, want 2", m.msgBlock[2])
	}

	m = compactCmdModel()
	m.Update(mkWinSize(100, 30))
	m.showThinking = false
	m.seedTranscript(msgs, 1)
	for _, b := range m.blocks {
		if b.kind == blockThinking {
			t.Fatalf("thinking shown while thinking is off:\n%s", blockRows(m))
		}
	}
}

// A background subagent's spawn result was written for the model; it is not
// shown on resume, as the live row replaced it with the task's state.
func TestSeedSkipsBackgroundSpawnResult(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	m.seedTranscript([]ai.Message{
		{Role: "assistant", ToolCalls: []ai.ToolCall{seedCall("s1", "subagent", `{"description":"survey","background":true}`)}},
		{Role: "tool", ToolCallID: "s1", Name: "subagent", Content: "Started background subagent bg-1: survey"},
	}, 1)
	for _, b := range m.blocks {
		if b.kind == blockTool {
			t.Fatalf("spawn result shown:\n%s", blockRows(m))
		}
	}
}
