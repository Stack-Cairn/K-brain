package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestCompactionVisibleInTranscript(t *testing.T) {
	var sawSummaryCall atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		json.NewDecoder(r.Body).Decode(&req)
		if !req.Stream {
			sawSummaryCall.Store(true)
			w.Write([]byte(`{"choices":[{"message":{"content":"folded summary"}}],` +
				`"usage":{"prompt_tokens":2000,"completion_tokens":100}}`))
			return
		}

		if !strings.Contains(req.Messages[1].Content, "Summary of the conversation") {
			t.Error("first stream request should run on compacted history")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"all done"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ag := agent.New(ai.New(srv.URL, "k"), "kimi-k3-fast", 100, "sys")
	ag.ContextLimit = 400
	ag.CompactThreshold = 0.1
	m := &model{
		input:   newInput(),
		mouseOn: true,
		agent:   ag,
		cfg: &config.Config{
			DefaultModel: "kimi-k3-fast",
			Providers:    map[string]config.Provider{"inference": {BaseURL: srv.URL, APIKey: "k"}},
			Models: map[string]config.Model{
				"kimi-k3-fast": {Providers: []string{"inference"}},
			},
		},
		modelName: "kimi-k3-fast",
		provName:  "inference",
		now:       time.Now,
		catalogs: map[string]config.Catalog{
			"inference": {Models: []config.ModelInfoLite{

				{ID: "kimi-k3-fast", ContextLength: 400, InPrice: 1e-6, OutPrice: 2e-6},
			}},
		},
	}
	m.width = 80
	m.input.SetWidth(78)

	for i := range 8 {
		m.agent.Messages = append(m.agent.Messages,
			ai.Message{Role: "user", Content: fmt.Sprintf("question %d about the migration", i)},
			ai.Message{Role: "assistant", Content: fmt.Sprintf("answer %d about the migration", i)},
		)
	}

	var mu sync.Mutex
	var msgs []any
	capture := func(x any) { mu.Lock(); msgs = append(msgs, x); mu.Unlock() }
	_, err := m.agent.Turn(context.Background(), "wrap it up", agent.Events{
		OnCompactStart: func(took, est int) { capture(compactStartMsg{took, est}) },
		OnCompacted: func(sum string, cutoff int, info agent.CompactInfo) {
			capture(compactMsg{summary: sum, cutoff: cutoff, info: info})
		},
	})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if !sawSummaryCall.Load() {
		t.Fatal("no summary call ran — the tiny limit should force a proactive compaction")
	}
	mu.Lock()
	for _, x := range msgs {
		upd, _ := m.Update(x)
		m = upd.(*model)
	}
	mu.Unlock()

	var lines []string
	for _, b := range m.blocks {
		lines = append(lines, ansi.Strip(b.text))
	}
	joined := strings.Join(lines, "\n")

	startIdx := strings.Index(joined, "compacting ")
	if startIdx < 0 {
		t.Fatalf("transcript should show the compaction starting, got:\n%s", joined)
	}
	if !strings.Contains(joined[startIdx:], "with kimi-k3-fast @ inference") {
		t.Fatalf("start note should name the summarizer route, got:\n%s", joined[startIdx:])
	}
	resultIdx := strings.Index(joined, "compacted — summarized ")
	if resultIdx < 0 {
		t.Fatalf("transcript should show the compaction result, got:\n%s", joined)
	}
	if resultIdx < startIdx {
		t.Fatalf("result note must land after the start note:\n%s", joined)
	}
	result := joined[resultIdx:]
	for _, want := range []string{"kimi-k3-fast", "$0.0022", "2.0k/100 tok"} {
		if !strings.Contains(result, want) {
			t.Fatalf("result note should contain %q, got:\n%s", want, result)
		}
	}
}

func TestCompactionNotesRenderInOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	m := &model{
		input:   newInput(),
		mouseOn: true,
		agent:   agent.New(ai.New(srv.URL, "k"), "kimi-k3-fast", 100, "sys"),
		cfg: &config.Config{
			DefaultModel: "kimi-k3-fast",
			Providers:    map[string]config.Provider{"inference": {BaseURL: srv.URL, APIKey: "k"}},
			Models: map[string]config.Model{
				"kimi-k3-fast": {Providers: []string{"inference"}},
			},
		},
		modelName: "kimi-k3-fast",
		provName:  "inference",
		now:       time.Now,
		catalogs: map[string]config.Catalog{
			"inference": {Models: []config.ModelInfoLite{
				{ID: "kimi-k3-fast", ContextLength: 400, InPrice: 1e-6, OutPrice: 2e-6},
			}},
		},
	}
	m.width = 80
	m.input.SetWidth(78)

	u := ai.Usage{PromptTokens: 2000, CompletionTokens: 100}
	upd, _ := m.Update(compactStartMsg{took: 18, est: 300})
	m = upd.(*model)
	upd, _ = m.Update(compactMsg{
		took: 8, kept: 10, summary: "folded summary", cutoff: 8,
		info: agent.CompactInfo{Model: "kimi-k3-fast", Usage: u},
	})
	m = upd.(*model)

	var lines []string
	for _, b := range m.blocks {
		lines = append(lines, ansi.Strip(b.text))
	}
	joined := strings.Join(lines, "\n")

	startIdx := strings.Index(joined, "compacting 18 msgs (est. 300)")
	if startIdx < 0 {
		t.Fatalf("transcript should show the compaction starting, got:\n%s", joined)
	}
	resultIdx := strings.Index(joined, "compacted — summarized 8 msgs, 10 kept")
	if resultIdx < 0 {
		t.Fatalf("transcript should show the compaction result, got:\n%s", joined)
	}
	if resultIdx < startIdx {
		t.Fatalf("result note must land after the start note:\n%s", joined)
	}
	result := joined[resultIdx:]
	for _, want := range []string{"kimi-k3-fast", "$0.0022", "(2.0k/100 tok)"} {
		if !strings.Contains(result, want) {
			t.Fatalf("result note should contain %q, got:\n%s", want, result)
		}
	}
}

func TestCompactionResultShowsRealCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	m := &model{
		input:     newInput(),
		mouseOn:   true,
		agent:     agent.New(ai.New(srv.URL, "k"), "kimi-k3-fast", 100, "sys"),
		cfg:       &config.Config{DefaultModel: "kimi-k3-fast"},
		modelName: "kimi-k3-fast",
		provName:  "inference",
		now:       time.Now,
		catalogs:  map[string]config.Catalog{},
	}
	m.width = 80
	m.input.SetWidth(78)

	upd, _ := m.Update(compactMsg{
		took: 14, kept: 7, summary: "folded", cutoff: 8,
		info: agent.CompactInfo{Model: "kimi-k3-fast", Usage: ai.Usage{PromptTokens: 900, CompletionTokens: 40}},
	})
	m = upd.(*model)
	got := ansi.Strip(m.blocks[len(m.blocks)-1].text)
	if !strings.Contains(got, "summarized 14 msgs, 7 kept") {
		t.Fatalf("result note should show real counts, got %q", got)
	}
	if strings.Contains(got, "0 msgs, 0 kept") {
		t.Fatalf("counts must come from OnCompact, got %q", got)
	}
}
