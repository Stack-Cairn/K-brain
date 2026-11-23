package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// An async notice must be seen: at the bottom it scrolls into view, and
// scrolled up (where the transcript end is out of sight) it also shows in
// the footer.
func TestAsyncNoticeIsVisible(t *testing.T) {
	m, _ := stClock(t)
	m.Update(mkWinSize(100, 30))
	for i := range 80 {
		m.append(fmt.Sprintf("line %d", i))
	}
	m.View()

	tm, _ := m.Update(noticeMsg("Reply copied to clipboard (verified)"))
	m = tm.(*model)
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Reply copied to clipboard") {
		t.Fatalf("at the bottom the notice should be in view:\n%s", v)
	}

	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = tm.(*model)
	m.View()
	if !m.scrolledUp() {
		t.Fatal("PgUp should scroll up")
	}
	tm, cmd := m.Update(noticeMsg("Reply saved to /x/reply.md"))
	m = tm.(*model)
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Reply saved to /x/reply.md") {
		t.Fatalf("scrolled up, the notice should show in the footer:\n%s", v)
	}
	if cmd == nil {
		t.Fatal("the footer notice needs its expiry timer")
	}
	if !m.scrolledUp() {
		t.Fatal("a notice must not yank the view down")
	}
}
