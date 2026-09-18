package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestInitialPromptSubmitsFirstTurn(t *testing.T) {
	m := busyQueueModel()
	m.busy = false
	m.cancel = nil
	m.agent = &agent.Agent{Client: stubLLM()}

	m.agent.Messages = []ai.Message{{Role: "system", Content: "sys"}}
	m.initialPrompt = "fix the flaky test"

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init with an initial prompt must return the kickoff cmd")
	}

	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("Init should batch blink + kickoff, got %T", cmd())
	}
	var msg tea.Msg
	for _, c := range batch {
		if got := c(); got != nil {
			if _, is := got.(initialPromptMsg); is {
				msg = got
			}
		}
	}
	if msg == nil {
		t.Fatal("the batched cmds should include the initialPromptMsg kickoff")
	}

	tm, _ := m.Update(msg)
	m = tm.(*model)

	if !m.busy {
		t.Fatal("the initial prompt should start a turn (busy)")
	}
	if m.initialPrompt != "" {
		t.Fatal("the kickoff is one-shot; initialPrompt should be consumed")
	}
	if len(m.hist) != 1 || m.hist[0] != "fix the flaky test" {
		t.Fatalf("the prompt belongs in up-arrow history, got %v", m.hist)
	}
	if !hasUserMsg(t, m, "fix the flaky test") {
		t.Fatalf("the prompt should reach the model as a user message, got %+v", m.agent.MessagesSnapshot())
	}
}

func TestNoInitialPromptNoKickoff(t *testing.T) {
	m := busyQueueModel()
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init always returns at least textarea.Blink")
	}
	if msg := cmd(); msg != nil {
		if _, is := msg.(initialPromptMsg); is {
			t.Fatal("without an initial prompt Init must not emit the kickoff")
		}
		if batch, is := msg.(tea.BatchMsg); is {
			for _, c := range batch {
				if _, is := c().(initialPromptMsg); is {
					t.Fatal("without an initial prompt the batch must not carry the kickoff")
				}
			}
		}
	}

	m.initialPrompt = ""
	tm, _ := m.Update(initialPromptMsg{})
	m = tm.(*model)
	if len(m.agent.MessagesSnapshot()) != 0 {
		t.Fatal("an empty initialPrompt must not submit")
	}
}

func TestInitialPromptMsgIgnoredWhileBusy(t *testing.T) {
	m := busyQueueModel()
	m.initialPrompt = "queued behind a turn"
	tm, _ := m.Update(initialPromptMsg{})
	m = tm.(*model)
	if len(m.agent.MessagesSnapshot()) != 0 {
		t.Fatal("a busy model must not submit the initial prompt")
	}
	if m.initialPrompt != "queued behind a turn" {
		t.Fatal("a swallowed kickoff should leave the prompt untouched")
	}
}
