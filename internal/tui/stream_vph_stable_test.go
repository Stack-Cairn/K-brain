package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestStreamViewportHeightStable(t *testing.T) {
	for _, mode := range []string{"default"} {
		t.Run(mode, func(t *testing.T) {
			runStreamViewportStable(t)
		})
	}
}

func runStreamViewportStable(t *testing.T) {
	t.Helper()
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.busy = true
	m.turnStart = m.nowFn()

	for i := range 15 {
		m.appendAssistant("prior turn line " + itoa(i) + " with some words to be real")
	}
	m.layout()
	m.View()

	full := "## Root cause\n\n- item one with words here\n- item two with words\n- item three\n- item four\n\nThat is the plan.\n"
	tokens := tokenize(full)

	type snap struct {
		step string
		vpH  int
	}
	var snaps []snap
	snapIt := func(step string) {
		m.layout()
		m.View()
		snaps = append(snaps, snap{step: step, vpH: m.vp.Height})
	}

	snapIt("start")
	for i, tok := range tokens {
		um, _ := m.Update(textMsg(tok))
		m = um.(*model)
		snapIt("tok:" + itoa(i))
	}
	um, _ := m.Update(turnDoneMsg{final: full})
	m = um.(*model)
	snapIt("turnDone")

	for _, s := range snaps {
		t.Logf("%-14s vpH=%d", s.step, s.vpH)
	}

	streamSnaps := snaps[1 : len(snaps)-1]
	minH, maxH := streamSnaps[0].vpH, streamSnaps[0].vpH
	for _, s := range streamSnaps {
		if s.vpH < minH {
			minH = s.vpH
		}
		if s.vpH > maxH {
			maxH = s.vpH
		}
	}
	swing := maxH - minH
	if swing > 3 {
		t.Errorf("viewport height oscillated %d rows during streaming (maxH=%d minH=%d); want ≤3 (the partial line's height, not the full streamCap)", swing, maxH, minH)
	}

	if minH <= minTranscriptRows {
		t.Errorf("viewport collapsed to %d rows (≤ minTranscriptRows=%d) during streaming; the full chat was replaced by the tail", minH, minTranscriptRows)
	}

	for _, s := range snaps {
		if s.vpH > m.height {
			t.Errorf("%s: viewport %d rows exceeds terminal %d", s.step, s.vpH, m.height)
		}
	}
	_ = ansi.Strip
}

func TestStreamMarkerMatchesCommitted(t *testing.T) {
	for _, mode := range []string{"default"} {
		t.Run(mode, func(t *testing.T) {
			m := compactCmdModel()
			m.Update(mkWinSize(80, 30))
			m.busy = true
			m.turnStart = m.nowFn()
			m.layout()
			m.View()

			var dotsAt []string
			for i, tok := range tokenize("first line\nsecond line\nthird line\n") {
				um, _ := m.Update(textMsg(tok))
				m = um.(*model)
				m.layout()
				m.View()
				if strings.Contains(ansi.Strip(m.currentView()), "●") {
					dotsAt = append(dotsAt, itoa(i))
				}
			}
			blk := ""
			for _, b := range m.blocks {
				if b.kind == blockAssistant {
					blk = ansi.Strip(b.renderAt(76))
				}
			}

			if len(dotsAt) < 2 {
				t.Errorf("default live partial showed ● only %d time(s); want it to persist for the whole turn (matches committed block):\n%s", len(dotsAt), blk)
			}
			if !strings.Contains(blk, "●") {
				t.Errorf("default committed block lost its ● marker:\n%s", blk)
			}
		})
	}
}

func TestStreamHangingIndentMatchesCommitted(t *testing.T) {
	long := "this is a long streaming line that should wrap under the marker for sure"
	for _, mode := range []string{"default"} {
		t.Run(mode, func(t *testing.T) {
			m := compactCmdModel()
			m.Update(mkWinSize(40, 20))
			m.busy = true
			m.turnStart = m.nowFn()
			m.current = long
			m.inMsg = true

			live := ansi.Strip(m.currentView())
			b := block{kind: blockAssistant, text: long}
			committed := ansi.Strip(b.renderAt(40))

			liveLines := strings.Split(live, "\n")
			commLines := strings.Split(committed, "\n")
			n := min(len(liveLines), len(commLines))
			for i := 1; i < n; i++ {
				liveLead := len(liveLines[i]) - len(strings.TrimLeft(liveLines[i], " "))
				commLead := len(commLines[i]) - len(strings.TrimLeft(commLines[i], " "))
				if liveLead != commLead {
					t.Errorf("mode=%s continuation row %d: live lead=%d, committed lead=%d (hanging indent mismatch)\n  live:   %q\n  commit: %q",
						mode, i, liveLead, commLead, liveLines[i], commLines[i])
				}
			}
		})
	}
}

func tokenize(s string) []string {
	var toks []string
	for i := 0; i < len(s); {
		n := 2 + (i % 4)
		if i+n > len(s) {
			n = len(s) - i
		}
		toks = append(toks, s[i:i+n])
		i += n
	}
	return toks
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
