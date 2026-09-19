package tui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestTUIStreamPreservesTextThinkingOrder(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"response.output_text.delta","delta":"first-visible-text"}`,
			`{"type":"response.reasoning_summary_text.delta","delta":"middle-thinking"}`,
			`{"type":"response.output_text.delta","delta":"last-visible-text"}`,
			`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":8,"output_tokens":3}}}`,
		} {
			fmt.Fprint(w, "data: "+event+"\n\n")
		}
	}))
	defer srv.Close()
	m := forkModel(t)
	messages := m.agent.MessagesSnapshot()
	m.agent = agent.New(ai.NewResponses(srv.URL, "key"), "m", 100, "system")
	m.agent.Messages = messages
	m.titled, m.showThinking = true, true
	h := &historyHarness{model: m}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	p := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler())
	m.prog = p
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	var transcript strings.Builder
	for _, b := range m.blocks {
		transcript.WriteString(b.text)
	}
	got := transcript.String()
	first, middle, last := strings.Index(got, "first-visible-text"), strings.Index(got, "middle-thinking"), strings.Index(got, "last-visible-text")
	if first < 0 || middle <= first || last <= middle {
		t.Fatalf("stream content reordered: %q", got)
	}
	if strings.Contains(got, "session save failed") {
		t.Fatalf("could not persist ordered response: %q", got)
	}
	if m.current != "" || m.curThink != "" || m.busy {
		t.Fatal("turn ended before all pending content was rendered")
	}
}
