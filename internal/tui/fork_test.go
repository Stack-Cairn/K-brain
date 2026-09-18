package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func forkModel(t *testing.T) *model {
	t.Helper()
	st, err := session.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := &model{
		input:    newInput(),
		agent:    &agent.Agent{},
		store:    st,
		queueSel: -1,

		cfg: &config.Config{
			DefaultModel:    "m",
			DefaultProvider: "p",
			Providers:       map[string]config.Provider{"p": {BaseURL: "https://x", APIKey: "k"}},
			Models:          map[string]config.Model{"m": {Providers: []string{"p"}}},
		},
		modelName: "m",
		provName:  "p",
	}
	m.width = 80
	m.input.SetWidth(m.width - 2)
	m.agent.Messages = []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q1", Authored: true},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2", Authored: true},
		{Role: "assistant", Content: "a2"},
	}
	id, err := st.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	m.sessionID = id
	m.saved = len(m.agent.Messages)
	if err := st.Save(id, 1, m.agent.Messages, "m", "p"); err != nil {
		t.Fatal(err)
	}
	m.rebuildTranscript()
	return m
}

func tailBlock(m *model) string { return m.blocks[len(m.blocks)-1].text }

func TestForkWithArg(t *testing.T) {
	m := forkModel(t)
	m.command("/fork experiment")

	if m.sessionID == "" || strings.Contains(tailBlock(m), "fork failed") {
		t.Fatalf("fork failed: %q", tailBlock(m))
	}
	meta, msgs, err := m.store.Load(m.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "experiment" || len(msgs) != 4 {
		t.Fatalf("fork: %+v (%d msgs)", meta, len(msgs))
	}
	if !strings.Contains(tailBlock(m), "⑂ forked") {
		t.Fatalf("confirmation: %q", tailBlock(m))
	}

	recent, err := m.store.Recent(10)
	if err != nil || len(recent) != 2 {
		t.Fatalf("recent: %v %+v", err, recent)
	}
}

func TestForkBareOpensNamePrompt(t *testing.T) {
	m := forkModel(t)
	m.command("/fork")

	if m.namePrompt == nil || m.input.Value() != "q1 (fork #1)" {
		t.Fatalf("prompt: %+v input=%q", m.namePrompt, m.input.Value())
	}
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	meta, _, err := m.store.Load(m.sessionID)
	if err != nil || meta.Title != "q1 (fork #1)" {
		t.Fatalf("fork title: %+v %v", meta, err)
	}

	m.command("/fork")
	if m.input.Value() != "q1 (fork #2)" {
		t.Fatalf("suggestion: %q", m.input.Value())
	}

	press(t, m, esc(m))
	if m.namePrompt != nil {
		t.Fatal("esc should cancel the prompt")
	}
	if recent, _ := m.store.Recent(10); len(recent) != 2 {
		t.Fatalf("cancelled fork created a session: %+v", recent)
	}
}

func TestForkFromRewindPicker(t *testing.T) {
	m := forkModel(t)
	press(t, m, esc(m))
	press(t, m, esc(m))
	press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})

	if m.rew != nil || m.namePrompt == nil {
		t.Fatalf("f should swap picker for name prompt: rew=%v np=%v", m.rew, m.namePrompt)
	}
	m.input.SetValue("at-q2")
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	meta, msgs, err := m.store.Load(m.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "at-q2" {
		t.Fatalf("title: %+v", meta)
	}

	if len(msgs) != 3 || msgs[2].Content != "q2" {
		t.Fatalf("prefix: %+v", msgs)
	}
	if len(m.agent.Messages) != 4 {
		t.Fatalf("live messages: %+v", m.agent.Messages)
	}
}

func TestForkWhileRewoundIntoFuture(t *testing.T) {

	m := forkModel(t)
	press(t, m, esc(m))
	press(t, m, esc(m))
	press(t, m, tea.KeyMsg{Type: tea.KeyUp})
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m.input.Reset()
	if len(m.agent.Messages) != 1 || len(m.future) != 4 {
		t.Fatalf("rewound: msgs=%d future=%d", len(m.agent.Messages), len(m.future))
	}

	press(t, m, esc(m))
	press(t, m, esc(m))
	if len(m.rew.entries) != 2 {
		t.Fatalf("entries: %+v", m.rew.entries)
	}

	press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m.input.SetValue("redo-fork")
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(m.agent.Messages) != 4 || m.agent.Messages[3].Content != "q2" {
		t.Fatalf("fork through future: %+v", m.agent.Messages)
	}
	_, msgs, err := m.store.Load(m.sessionID)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("stored: %v %+v", err, msgs)
	}

	if len(m.future) != 0 {
		t.Fatalf("remaining future: %+v", m.future)
	}
}

func TestForkWhileBusyCopiesImmediatelyAndDefersSwitch(t *testing.T) {
	m := forkModel(t)
	m.busy = true
	origID := m.sessionID

	m.command("/fork experiment")

	if m.pendingForkID == "" {
		t.Fatalf("busy fork should mark a pending switch; blocks=%v", m.blocks)
	}
	meta, msgs, err := m.store.Load(m.pendingForkID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "experiment" || meta.ForkedFrom != origID || len(msgs) != 4 {
		t.Fatalf("copy: %+v (%d msgs)", meta, len(msgs))
	}

	if m.sessionID != origID || len(m.agent.Messages) != 5 {
		t.Fatalf("live session should be untouched until turn end: %s (%d msgs)", m.sessionID, len(m.agent.Messages))
	}
	if !strings.Contains(tailBlock(m), "kn --resume "+m.pendingForkID) {
		t.Fatalf("the confirmation should tell the user how to open the copy: %q", tailBlock(m))
	}

	m.command("/fork another")
	meta2, _, _ := m.store.Load(m.pendingForkID)
	if meta2.Title != "experiment" {
		t.Fatalf("second fork should not replace the pending one: %+v", meta2)
	}
	if recent, _ := m.store.Recent(10); len(recent) != 2 {
		t.Fatalf("the refused fork must not create a session: %+v", recent)
	}

	tm, _ := m.Update(turnDoneMsg{})
	m = tm.(*model)
	if m.busy {
		t.Fatal("turnDoneMsg should clear busy")
	}
	if m.pendingForkID != "" || m.sessionID == origID {
		t.Fatalf("switch should have happened: pending=%q session=%q", m.pendingForkID, m.sessionID)
	}
	meta, msgs, err = m.store.Load(m.sessionID)
	if err != nil || meta.Title != "experiment" {
		t.Fatalf("live session should be the fork: %+v %v", meta, err)
	}
	if len(m.agent.Messages) != 5 || len(msgs) != 4 {
		t.Fatalf("the fork's conversation should be live: msgs=%d stored=%d", len(m.agent.Messages), len(msgs))
	}

	if _, origMsgs, err := m.store.Load(origID); err != nil || len(origMsgs) != 4 {
		t.Fatalf("original should survive untouched: %v %d", err, len(origMsgs))
	}
	if !strings.Contains(tailBlock(m), "⑂ switched to the fork") {
		t.Fatalf("switch confirmation: %q", tailBlock(m))
	}
}

func TestForkWhileBusyBareOpensPrompt(t *testing.T) {
	m := forkModel(t)
	m.busy = true

	m.command("/fork")
	if m.namePrompt == nil || m.input.Value() != "q1 (fork #1)" {
		t.Fatalf("prompt: %+v input=%q", m.namePrompt, m.input.Value())
	}
	m.input.SetValue("mid-turn")
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.namePrompt != nil {
		t.Fatal("enter should close the prompt")
	}
	if m.pendingForkID == "" {
		t.Fatal("the committed name should fork immediately, even while busy")
	}
	meta, msgs, err := m.store.Load(m.pendingForkID)
	if err != nil || meta.Title != "mid-turn" || len(msgs) != 4 {
		t.Fatalf("copy: %+v %v (%d msgs)", meta, err, len(msgs))
	}
}

func TestForkWhileBusyWithoutStoredSession(t *testing.T) {
	m := forkModel(t)
	m.busy = true
	m.sessionID = ""

	m.command("/fork too-early")
	if m.pendingForkID != "" {
		t.Fatal("nothing stored yet — no fork should land")
	}
	if !strings.Contains(tailBlock(m), "nothing to fork yet") {
		t.Fatalf("explanation: %q", tailBlock(m))
	}
}

func TestEnterWhileBusyForksImmediately(t *testing.T) {
	m := forkModel(t)
	m.busy = true
	_, m.cancel = context.WithCancel(context.Background())
	m.queueSel = -1

	m.input.SetValue("/fork typed-mid-turn")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(m.queue) != 0 {
		t.Fatalf("/fork must never enter the chat queue: %v", m.queue)
	}
	if m.pendingForkID == "" {
		t.Fatal("enter on /fork while busy should fork immediately")
	}
	meta, _, err := m.store.Load(m.pendingForkID)
	if err != nil || meta.Title != "typed-mid-turn" {
		t.Fatalf("copy: %+v %v", meta, err)
	}
	if m.hist[len(m.hist)-1] != "/fork typed-mid-turn" {
		t.Fatalf("the command belongs in input recall: %v", m.hist)
	}
}

func TestForkSwitchClearsQueue(t *testing.T) {
	m := forkModel(t)
	m.busy = true
	m.queue = []string{"follow up for the old conversation"}
	m.queueSel = -1

	m.command("/fork clean-slate")
	if m.pendingForkID == "" {
		t.Fatal("fork should be pending")
	}

	tm, _ := m.Update(turnDoneMsg{})
	m = tm.(*model)
	if len(m.queue) != 0 {
		t.Fatalf("the switch must drop queued messages: %v", m.queue)
	}
	if m.busy {
		t.Fatal("nothing should submit after the switch — the turn is over")
	}
}

func TestForkSwitchKeepsTurnOnOriginal(t *testing.T) {
	m := forkModel(t)
	m.busy = true
	origID := m.sessionID
	m.command("/fork branch-point")

	m.busy = false
	m.agent.Messages = append(m.agent.Messages,
		ai.Message{Role: "assistant", Content: "finished answer"})
	m.busy = true
	tm, _ := m.Update(turnDoneMsg{})
	m = tm.(*model)

	if _, msgs, err := m.store.Load(origID); err != nil || len(msgs) != 5 {
		t.Fatalf("the original should have kept the finished turn: %v %d", err, len(msgs))
	}
	_, forkMsgs, err := m.store.Load(m.sessionID)
	if err != nil || len(forkMsgs) != 4 {
		t.Fatalf("the fork branched before the turn ended: %v %d", err, len(forkMsgs))
	}
	if len(m.agent.Messages) != 5 {
		t.Fatalf("the live window is the fork: %d", len(m.agent.Messages))
	}
}

func TestForkSwitchThenNextTurnLandsOnFork(t *testing.T) {
	m := forkModel(t)
	m.busy = true
	origID := m.sessionID
	m.command("/fork continuation")
	tm, _ := m.Update(turnDoneMsg{})
	m = tm.(*model)

	tm, _ = m.Update(turnDoneMsg{})
	m = tm.(*model)
	if m.sessionID == origID || m.pendingForkID != "" {
		t.Fatal("the switch is one-shot")
	}

	m.agent.Client = stubLLM()
	m.busy = false
	if _, err := m.agent.Turn(context.Background(), "next question", agent.Events{}); err != nil {
		t.Fatal(err)
	}
	m.persist()

	_, msgs, err := m.store.Load(m.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, msg := range msgs {
		if msg.Role == "user" && msg.Content == "next question" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the new turn should persist on the fork: %+v", msgs[len(msgs)-2:])
	}
	if _, origMsgs, _ := m.store.Load(origID); len(origMsgs) != 4 {
		t.Fatalf("the original must not grow after the switch: %d", len(origMsgs))
	}
}

func TestRename(t *testing.T) {
	m := forkModel(t)
	m.command("/rename better-name")
	meta, _, err := m.store.Load(m.sessionID)
	if err != nil || meta.Title != "better-name" {
		t.Fatalf("rename: %+v %v", meta, err)
	}

	m.command("/rename")
	if m.namePrompt == nil || m.input.Value() != "better-name" {
		t.Fatalf("prompt: %q", m.input.Value())
	}
	m.input.SetValue("final-name")
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	meta, _, _ = m.store.Load(m.sessionID)
	if meta.Title != "final-name" {
		t.Fatalf("prompt rename: %+v", meta)
	}
}
