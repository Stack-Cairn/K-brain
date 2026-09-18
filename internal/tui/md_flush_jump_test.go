package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestMarkdownFlushDoesNotJump(t *testing.T) {
	for _, mode := range []string{"default"} {
		t.Run(mode, func(t *testing.T) {
			runMarkdownFlushJump(t)
		})
	}
}

func runMarkdownFlushJump(t *testing.T) {
	t.Helper()
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))
	m.busy = true
	m.turnStart = m.nowFn()
	m.layout()
	m.View()

	chunks := []string{
		"## Fixing the jump\n",
		"\n",
		"Here is a paragraph of assistant text that wraps a bit at width 80.\n",
		"\n",
		"- first item with some words\n",
		"- second item with more words in it\n",
		"- third\n",
		"\n",
		"```go\n",
		"func main() {\n",
		"\tfmt.Println(\"hi\")\n",
		"}\n",
		"```\n",
		"\n",
		"That is the plan.\n",
	}

	type snap struct {
		step             string
		viewTop, viewH   int
		frameTop, frameH int
		inputTop         int
		screenH          int
	}
	var snaps []snap
	snapIt := func(step string) {
		m.layout()
		v := m.View()
		s := snap{step: step, viewTop: m.viewTop, viewH: m.viewH, frameTop: m.frameTop, frameH: m.frameH, inputTop: m.inputTop, screenH: lipgloss.Height(v)}
		snaps = append(snaps, s)
	}

	snapIt("start")
	for _, c := range chunks {
		um, _ := m.Update(textMsg(c))
		m = um.(*model)
		snapIt("stream:" + strings.TrimSpace(c))
	}

	um, _ := m.Update(textMsg("final trailing bit"))
	m = um.(*model)
	snapIt("stream:tail")

	um, _ = m.Update(turnDoneMsg{final: "done"})
	m = um.(*model)
	snapIt("turnDone")

	m.layout()
	m.View()
	snapIt("idle")

	var pre, post snap
	for i, s := range snaps {
		if s.step == "turnDone" {
			pre, post = snaps[i-1], snaps[i]
			break
		}
	}
	t.Logf("stream:tail inputTop=%d viewTop=%d viewH=%d -> turnDone inputTop=%d viewTop=%d viewH=%d",
		pre.inputTop, pre.viewTop, pre.viewH, post.inputTop, post.viewTop, post.viewH)

	if pre.inputTop != post.inputTop {
		t.Errorf("input box jumped across final markdown flush: %d -> %d", pre.inputTop, post.inputTop)
	}

	if post.viewTop+post.viewH != m.height {
		t.Errorf("after flush, frame not bottom-anchored: bottom %d != terminal %d", post.viewTop+post.viewH, m.height)
	}

	for _, s := range snaps {
		if s.screenH > m.height {
			t.Errorf("step %q: frame %d rows exceeds terminal %d", s.step, s.screenH, m.height)
		}
	}
}
