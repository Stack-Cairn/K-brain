package tui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type outputLimitHarness struct {
	*historyHarness
	previews int
	starts   int
}

func (h *outputLimitHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case toolCallMsg:
		h.previews++
	case toolStartMsg:
		h.starts++
	}
	_, cmd := h.historyHarness.Update(msg)
	return h, cmd
}

func TestTUIOutputLimitRendersAndPersists(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"type":"response.output_text.delta","delta":"partial response"}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"type":"response.output_item.added","item":{"type":"function_call","id":"item-1","call_id":"call-1","name":"read","arguments":""}}`+"\n\n")
		for _, delta := range []string{`{`, `"path":`, `"file.txt"}`} {
			fmt.Fprintf(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"item-1\",\"delta\":%q}\n\n", delta)
		}
		fmt.Fprint(w, "data: "+`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":17,"output_tokens":3}}}`+"\n\n")
	}))
	defer srv.Close()
	m := forkModel(t)
	messages := m.agent.MessagesSnapshot()
	m.agent = agent.New(ai.NewResponses(srv.URL, "key"), "m", 100, "system")
	m.agent.Provider = "p"
	m.agent.Messages = messages
	m.titled = true
	h := &outputLimitHarness{historyHarness: &historyHarness{model: m}}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	p := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler())
	m.prog = p
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	if h.previews != 4 || h.starts != 0 {
		t.Fatalf("tool events: previews=%d starts=%d", h.previews, h.starts)
	}
	check := func(stage string) {
		t.Helper()
		var transcript strings.Builder
		for _, block := range m.blocks {
			if block.kind == blockToolQueued || block.kind == blockToolRun {
				t.Fatalf("%s retained discarded tool: %+v", stage, block)
			}
			transcript.WriteString(block.text)
		}
		if !strings.Contains(transcript.String(), "partial response") || !strings.Contains(transcript.String(), "truncated by output limit") || strings.Contains(transcript.String(), "error:") {
			t.Fatalf("%s transcript: %s", stage, transcript.String())
		}
		if m.agent.LastStopReason() != ai.StopReasonLength {
			t.Fatalf("%s stop reason lost", stage)
		}
	}
	check("streamed")
	if err := m.resume(m.sessionID); err != nil {
		t.Fatal(err)
	}
	check("restored")
	u := m.agent.UsageSummary().Total
	if u.PromptTokens != 17 || u.CompletionTokens != 3 {
		t.Fatalf("restored usage: %+v", u)
	}
}

func TestClearQueuedToolsPreservesHistoryMapping(t *testing.T) {
	m := compactCmdModel()
	m.blocks = []block{
		{kind: blockToolQueued, toolID: "a"},
		{kind: blockUser, text: "first"},
		{kind: blockToolQueued, toolID: "b"},
		{kind: blockToolRun, toolID: "completed"},
		{kind: blockUser, text: "steered"},
		{kind: blockToolQueued, toolID: "c"},
		{kind: blockAssistant, text: "partial"},
	}
	m.msgBlock = []int{-1, 1, 3, 4, 6, 2}
	m.clearQueuedTools()
	if len(m.blocks) != 4 || m.blocks[0].text != "first" || m.blocks[1].toolID != "completed" || m.blocks[2].text != "steered" || m.blocks[3].text != "partial" {
		t.Fatalf("retained blocks: %+v", m.blocks)
	}
	if want := []int{-1, 0, 1, 2, 3, -1}; !slices.Equal(m.msgBlock, want) {
		t.Fatalf("history mapping: got %v want %v", m.msgBlock, want)
	}
	m.clearQueuedTools()
	if len(m.blocks) != 4 || m.msgBlock[4] != 3 {
		t.Fatal("cleanup changed history without pending tools")
	}
}
