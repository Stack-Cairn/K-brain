package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestThinkingAndResponseScrollFullyOnSmallTerminal(t *testing.T) {
	m := compactCmdModel()
	m.showThinking = true
	m.busy = true
	m.Update(mkWinSize(80, 12))

	for range 15 {
		um, _ := m.Update(thinkMsg("reasoning trace line about the plan\n"))
		m = um.(*model)
	}

	for i := range 10 {
		um, _ := m.Update(textMsg("response line " + string(rune('a'+i)) + " of the actual answer\n"))
		m = um.(*model)
	}

	m.flushThink()
	m.flushCurrent()
	m.busy = false
	m.layout()

	if h := lipgloss.Height(m.View()); h > m.height {
		t.Errorf("frame height %d exceeds terminal height %d — top rows scrolled off-screen", h, m.height)
	}

	m.vp.GotoTop()
	seen := map[string]bool{}
	for i := range 10 {
		seen[string(rune('a'+i))] = false
	}
	for {
		v := m.viewportView()
		for i := range 10 {
			if strings.Contains(v, "response line "+string(rune('a'+i))) {
				seen[string(rune('a'+i))] = true
			}
		}
		if m.vp.AtBottom() {
			break
		}
		m.vp.ScrollDown(1)
	}
	var missing []string
	for i := range 10 {
		k := string(rune('a' + i))
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Errorf("response lines never visible at any scroll position: %v", missing)
	}
}

func TestFrameFitsWhileReasoningStreams(t *testing.T) {
	m := compactCmdModel()
	m.showThinking = true
	m.busy = true
	m.Update(mkWinSize(80, 12))

	for range 30 {
		um, _ := m.Update(thinkMsg("a long reasoning trace line that keeps coming\n"))
		m = um.(*model)
	}
	um, _ := m.Update(textMsg("the response begins"))
	m = um.(*model)
	m.layout()

	if h := lipgloss.Height(m.View()); h > m.height {
		t.Errorf("streaming frame height %d exceeds terminal height %d", h, m.height)
	}

	m.vp.GotoTop()
	top := m.viewportView()
	if !strings.Contains(top, "reasoning trace") {
		t.Errorf("top of transcript doesn't show reasoning:\n%s", top)
	}
}

func TestFrameHeightDuringLongInflightResponse(t *testing.T) {
	newStreamingModel := func(height int) *model {
		m := compactCmdModel()
		m.showThinking = true
		m.busy = true
		m.Update(mkWinSize(80, height))
		for i := range 5 {
			um, _ := m.Update(thinkMsg("thinking " + string(rune('0'+i)) + "\n"))
			m = um.(*model)
		}

		m.current = "answer one\nanswer two\nanswer three\nanswer four\nanswer five\nanswer six\nanswer seven\nanswer eight"
		m.layout()
		return m
	}

	t.Run("small terminal keeps the transcript floor", func(t *testing.T) {
		m := newStreamingModel(20)
		if h := lipgloss.Height(m.View()); h > m.height {
			t.Errorf("frame %d rows > terminal %d rows:\n%s", h, m.height, m.View())
		}
		if m.vp.Height < minTranscriptRows {
			t.Errorf("vp.Height=%d < minTranscriptRows=%d — the live area ate the transcript floor", m.vp.Height, minTranscriptRows)
		}

		if cv := m.currentViewCapped(); !strings.Contains(cv, "answer") {
			t.Errorf("live area lost its tail entirely: %q", cv)
		}
	})

	t.Run("tiny terminal fits with a fair split", func(t *testing.T) {
		m := newStreamingModel(12)
		if h := lipgloss.Height(m.View()); h > m.height {
			t.Errorf("frame %d rows > terminal %d rows:\n%s", h, m.height, m.View())
		}
		live := lipgloss.Height(m.currentViewCapped())
		if live > m.vp.Height {
			t.Errorf("live area %d rows > transcript %d rows on a %d-row terminal", live, m.vp.Height, m.height)
		}
	})
}

func TestScrollPositionStableWhileResponseStreams(t *testing.T) {
	m := compactCmdModel()
	m.showThinking = true
	m.busy = true
	m.Update(mkWinSize(80, 12))

	for i := range 10 {
		um, _ := m.Update(thinkMsg("reasoning line " + string(rune('0'+i)) + "\n"))
		m = um.(*model)
	}
	m.layout()
	m.vp.GotoTop()
	m.follow = false
	before := m.viewportView()
	if !strings.Contains(before, "reasoning line 0") {
		t.Fatalf("setup: top should show first reasoning line:\n%s", before)
	}

	um, _ := m.Update(textMsg("response begins\n"))
	m = um.(*model)
	m.layout()

	after := m.viewportView()
	if m.follow {
		t.Error("streamed response re-engaged follow mode while scrolled up")
	}
	if !strings.Contains(after, "reasoning line 0") {
		t.Errorf("response streaming shifted the scrolled view:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestPgUpMovesThroughWholeTranscript(t *testing.T) {
	m := compactCmdModel()
	m.showThinking = true
	m.Update(mkWinSize(80, 12))

	for range 8 {
		um, _ := m.Update(thinkMsg("thinking block line\n"))
		m = um.(*model)
	}
	for i := range 20 {
		um, _ := m.Update(textMsg("answer part " + string(rune('a'+i)) + "\n"))
		m = um.(*model)
	}
	m.flushThink()
	m.flushCurrent()
	m.layout()
	m.vp.GotoBottom()

	um, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = um.(*model)
	v := m.viewportView()
	if !strings.Contains(v, "answer part") {
		t.Errorf("after one PgUp the answer is gone — jumped straight past it:\n%s", v)
	}
}
