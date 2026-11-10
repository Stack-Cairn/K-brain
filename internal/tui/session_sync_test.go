package tui

import (
	"context"
	"encoding/json"
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
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// otherProcess 打开同一会话目录的第二个 Store，代表 LiveAgent 的 backend。
func otherProcess(t *testing.T, m *model) *session.Store {
	t.Helper()
	st, err := session.Open(m.store.SessionsDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestCheckpointEventsThrottleRoundStarts(t *testing.T) {
	now := time.Unix(100, 0)
	var sent []turnCheckpointMsg
	rounds := 0
	ev := checkpointEvents(agent.Events{OnLifecycle: func(string) { rounds++ }}, "s1",
		func() []ai.Message { return []ai.Message{{Role: "user", Content: fmt.Sprint(len(sent))}} },
		func(msg tea.Msg) { sent = append(sent, msg.(turnCheckpointMsg)) },
		func() time.Time { return now })
	ev.OnLifecycle("turn_start")
	ev.OnLifecycle("message_start")
	now = now.Add(time.Second)
	ev.OnLifecycle("turn_start")
	now = now.Add(1500 * time.Millisecond)
	ev.OnLifecycle("turn_start")
	if rounds != 4 {
		t.Fatalf("wrapped lifecycle saw %d events", rounds)
	}
	if len(sent) != 2 || sent[0].sessionID != "s1" || sent[1].msgs[0].Content != "1" {
		t.Fatalf("checkpoints = %+v", sent)
	}
}

type checkpointHarness struct {
	*model
	prompt string
}

func (h *checkpointHarness) Init() tea.Cmd { return func() tea.Msg { return historyTurnStart{} } }
func (h *checkpointHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(historyTurnStart); ok {
		_, cmd := h.model.submitTurn(h.prompt, true)
		return h, cmd
	}
	_, cmd := h.model.Update(msg)
	if _, ok := msg.(turnDoneMsg); ok {
		return h, tea.Quit
	}
	return h, cmd
}

// LiveAgent 在 kn 的回合进行中就能看到用户消息和运行状态，回合结束后看到回答。
func TestTUITurnIsVisibleToAnotherProcessWhileRunning(t *testing.T) {
	t.Chdir(t.TempDir())
	started, proceed := make(chan struct{}, 4), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if !req.Stream {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"title"}}]}`)
			return
		}
		started <- struct{}{}
		<-proceed
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer from kn\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	m := forkModel(t)
	msgs := m.agent.Messages
	m.agent = agent.New(ai.New(srv.URL, "key"), "m", 100, "sys")
	m.agent.Messages = msgs
	m.titled = true
	backend := otherProcess(t, m)
	h := &checkpointHarness{model: m, prompt: "question from kn"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler())
	m.prog = p
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()

	<-started
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, stored, err := backend.Load(m.sessionID)
		run, busy := backend.ExternalRun(m.sessionID)
		if err == nil && busy && run.Client == "kn" && len(stored) > 0 && stored[len(stored)-1].Content == "question from kn" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mid-turn state not visible: busy=%v run=%+v stored=%+v err=%v", busy, run, stored, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := backend.AcquireRun(m.sessionID, "backend"); err == nil {
		t.Fatal("the backend could start a run while kn runs")
	}
	close(proceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, busy := backend.ExternalRun(m.sessionID); busy || m.runLease != nil {
		t.Fatal("kn kept its run lease after the turn")
	}
	_, stored, err := backend.Load(m.sessionID)
	if err != nil || stored[len(stored)-1].Content != "answer from kn" || stored[len(stored)-2].Content != "question from kn" {
		t.Fatalf("final transcript = %+v, %v", stored, err)
	}
}

func TestTUIRefusesTurnWhileAnotherProcessRuns(t *testing.T) {
	m := forkModel(t)
	backend := otherProcess(t, m)
	lease, err := backend.AcquireRun(m.sessionID, "backend")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	before := len(m.agent.Messages)
	m.submitTurn("hello", true)
	if m.busy || m.cancel != nil || len(m.agent.Messages) != before {
		t.Fatal("turn started while LiveAgent runs the session")
	}
	if !strings.Contains(tailBlock(m), "running in LiveAgent") || m.input.Value() != "hello" {
		t.Fatalf("notice %q, composer %q", tailBlock(m), m.input.Value())
	}
	// 空闲时的保存不会覆盖或报错。
	if !m.persist() {
		t.Fatal(tailBlock(m))
	}
}

func TestTUIReloadsSessionChangedElsewhereBeforeTurn(t *testing.T) {
	m := forkModel(t)
	m.prepareHistory()
	backend := otherProcess(t, m)
	h, msgs, err := backend.History(m.sessionID, m.agent.Messages[:1])
	if err != nil {
		t.Fatal(err)
	}
	msgs = append(msgs, ai.Message{Role: "user", Content: "q from LiveAgent", Authored: true}, ai.Message{Role: "assistant", Content: "a from LiveAgent"})
	if err := backend.SaveHistory(m.sessionID, h, msgs, "m", "p"); err != nil {
		t.Fatal(err)
	}
	// 空闲保存跳过而不是覆盖 LiveAgent 的消息。
	if !m.persist() {
		t.Fatal(tailBlock(m))
	}
	if !m.beginSessionRun() {
		t.Fatal(tailBlock(m))
	}
	defer m.endSessionRun()
	got := m.agent.Messages
	if len(got) != 7 || got[len(got)-1].Content != "a from LiveAgent" {
		t.Fatalf("reloaded messages = %+v", got)
	}
	var sawNotice, sawAnswer bool
	for _, b := range m.blocks {
		sawNotice = sawNotice || strings.Contains(b.text, "changed in another window")
		sawAnswer = sawAnswer || strings.Contains(b.text, "a from LiveAgent")
	}
	if !sawNotice || !sawAnswer {
		t.Fatal("transcript was not redrawn from the reloaded session")
	}
	m.agent.Messages = append(m.agent.Messages, ai.Message{Role: "user", Content: "q3", Authored: true}, ai.Message{Role: "assistant", Content: "a3"})
	if err := m.saveHistory(m.agent.MessagesSnapshot()); err != nil {
		t.Fatal(err)
	}
	_, stored, err := backend.Load(m.sessionID)
	if err != nil || len(stored) != 8 || stored[5].Content != "a from LiveAgent" {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
}
