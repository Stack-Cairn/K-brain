package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestUIMailboxNeverBlocksAndKeepsOrder(t *testing.T) {
	release := make(chan struct{})
	got := make(chan tea.Msg, 1000)
	b := newUIMailbox(func(msg tea.Msg) {
		<-release // the event loop is busy
		got <- msg
	})
	posted := make(chan struct{})
	go func() {
		for i := range 500 {
			b.post(i)
		}
		close(posted)
	}()
	select {
	case <-posted:
	case <-time.After(5 * time.Second):
		t.Fatal("post waited for the event loop")
	}
	close(release)
	for want := range 500 {
		select {
		case msg := <-got:
			if msg != want {
				t.Fatalf("message %d delivered as %v", want, msg)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("message %d never delivered", want)
		}
	}
}

func TestUIMailboxCoalescesToolOutput(t *testing.T) {
	release := make(chan struct{})
	got := make(chan tea.Msg, 10)
	b := newUIMailbox(func(msg tea.Msg) {
		<-release
		got <- msg
	})
	b.post("first") // taken by the drain goroutine, held at release
	for b.pending() != 0 {
		time.Sleep(time.Millisecond)
	}
	b.post(toolOutputMsg{id: "a", text: "1"})
	b.post(toolOutputMsg{id: "a", text: "12"})
	b.post(toolOutputMsg{id: "b", text: "x"})
	b.post(toolOutputMsg{id: "a", text: "123"})
	close(release)
	want := []tea.Msg{"first", toolOutputMsg{id: "a", text: "12"}, toolOutputMsg{id: "b", text: "x"}, toolOutputMsg{id: "a", text: "123"}}
	for i, w := range want {
		select {
		case msg := <-got:
			if msg != w {
				t.Fatalf("message %d = %#v, want %#v", i, msg, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("message %d never delivered", i)
		}
	}
}

// A hook on a tool goroutine (permission, question, plan, elicitation) posts
// its request while the event loop is not reading — drawing, or suspended for
// an external editor. It must reach its own select (reply or cancellation)
// instead of blocking in Send.
func TestPostToIdleProgramDoesNotBlock(t *testing.T) {
	p := tea.NewProgram(nil) // never run: nothing reads its messages
	t.Cleanup(func() { forgetUIPoster(p) })
	m := &model{prog: p}
	done := make(chan struct{})
	go func() {
		m.post(noticeMsg("hello"))
		m.post(noticeMsg("again"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("post blocked on a program whose event loop is not reading")
	}
}
