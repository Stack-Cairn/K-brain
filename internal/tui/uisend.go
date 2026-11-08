package tui

import (
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// (*tea.Program).Send blocks until the event loop takes the message. Code off
// the UI goroutine (turns, tools, hooks, subagents) must not wait on the UI:
// when the UI in turn waits on that code — a lock it holds, a result it owes —
// the program freezes for good, and even without that a turn stalls for as
// long as the UI is busy (an external editor, a slow frame). A uiMailbox
// delivers messages to the program in the order they were posted, from its
// own goroutine, and posting never blocks.
type uiMailbox struct {
	send func(tea.Msg)

	mu       sync.Mutex
	queue    []tea.Msg
	draining bool
}

// coalescer is a message that supersedes the queued message before it (say,
// a tool's output so far): the mailbox keeps only the newer one, so a UI that
// falls behind does not pile up stale copies.
type coalescer interface {
	supersedes(prev tea.Msg) bool
}

func newUIMailbox(send func(tea.Msg)) *uiMailbox { return &uiMailbox{send: send} }

func (b *uiMailbox) post(msg tea.Msg) {
	b.mu.Lock()
	if c, ok := msg.(coalescer); ok && len(b.queue) > 0 && c.supersedes(b.queue[len(b.queue)-1]) {
		b.queue[len(b.queue)-1] = msg
	} else {
		b.queue = append(b.queue, msg)
	}
	if b.draining {
		b.mu.Unlock()
		return
	}
	b.draining = true
	b.mu.Unlock()
	go b.drain()
}

func (b *uiMailbox) drain() {
	for {
		b.mu.Lock()
		if len(b.queue) == 0 {
			b.draining = false
			b.queue = nil
			b.mu.Unlock()
			return
		}
		msg := b.queue[0]
		b.queue[0] = nil
		b.queue = b.queue[1:]
		b.mu.Unlock()
		b.send(msg)
	}
}

// pending is how many messages wait for the event loop.
func (b *uiMailbox) pending() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queue)
}

var uiMailboxes struct {
	sync.Mutex
	byProgram map[*tea.Program]*uiMailbox
}

// uiPoster returns the non-blocking, ordered sender for p, or nil for no
// program. Every caller for the same program shares one mailbox, so their
// messages keep their relative order.
func uiPoster(p *tea.Program) func(tea.Msg) {
	if p == nil {
		return nil
	}
	uiMailboxes.Lock()
	defer uiMailboxes.Unlock()
	if uiMailboxes.byProgram == nil {
		uiMailboxes.byProgram = map[*tea.Program]*uiMailbox{}
	}
	b := uiMailboxes.byProgram[p]
	if b == nil {
		// The mailbox calls Send only from its own drain goroutine.
		b = newUIMailbox(p.Send) //nolint:uilock
		uiMailboxes.byProgram[p] = b
	}
	return b.post
}

// forgetUIPoster drops p's mailbox once the program has ended.
func forgetUIPoster(p *tea.Program) {
	uiMailboxes.Lock()
	delete(uiMailboxes.byProgram, p)
	uiMailboxes.Unlock()
}

// post delivers msg to the running program without waiting for the event
// loop; it does nothing without a program.
func (m *model) post(msg tea.Msg) {
	if send := uiPoster(m.prog); send != nil {
		send(msg)
	}
}

// poster is post bound to the current program, for callbacks that outlive
// this call; nil without a program.
func (m *model) poster() func(tea.Msg) { return uiPoster(m.prog) }

func (msg toolOutputMsg) supersedes(prev tea.Msg) bool {
	p, ok := prev.(toolOutputMsg)
	return ok && p.id == msg.id
}
