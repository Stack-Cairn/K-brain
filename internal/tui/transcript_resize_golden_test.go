package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// resizeSession is a resumed session long enough to scroll: turns with a
// markdown reply and finished tool calls, as history records them.
func resizeSession() []ai.Message {
	var msgs []ai.Message
	for i := range 6 {
		call := seedCall(fmt.Sprintf("c%d", i), "bash", fmt.Sprintf(`{"command":"go test ./pkg/stage%d/..."}`, i))
		call.DurationMs = int64(800 + 300*i)
		if i == 3 {
			call.ExitCode = 1
		}
		result := fmt.Sprintf("ok  \tgithub.com/acme/kn/pkg/stage%d\t0.%ds", i, i+1)
		if i == 3 {
			result = "--- FAIL: TestStage3 (0.00s)\n    stage3_test.go:12: want 2, got 3\n(exit: exit status 1)"
		}
		msgs = append(msgs,
			ai.Message{Role: "user", Content: fmt.Sprintf("Stage %d: make the pipeline step idempotent and rerun its tests.", i), Authored: true},
			ai.Message{Role: "assistant", Content: fmt.Sprintf("Stage %d keeps a **checkpoint** per input, so a rerun skips work it has already done:\n\n- read the checkpoint\n- skip inputs it lists\n\n```go\nif done[in.ID] {\n\tcontinue\n}\n```", i), ToolCalls: []ai.ToolCall{call}},
			ai.Message{Role: "tool", ToolCallID: call.ID, Name: "bash", Content: result},
		)
	}
	return msgs
}

// TestGoldenTranscriptResize pins a scrolled-back transcript through a run of
// resizes. The window is anchored to the second turn's reply, so every frame
// starts on that reply however its rows reflow.
func TestGoldenTranscriptResize(t *testing.T) {
	t.Chdir(t.TempDir())
	prevNow := toolTimer.now
	defer func() { toolTimer.now = prevNow }()
	defer SetLightTheme(false)

	steps := [][2]int{{120, 40}, {80, 24}, {40, 24}, {120, 40}}
	for _, mode := range goldenModes {
		t.Run(mode.name, func(t *testing.T) {
			restore := useProfile(t, mode.profile, mode.dark)
			defer restore()
			SetLightTheme(!mode.dark)
			if mode.mono {
				setMonoMode(true, true)
			}
			setASCIIGlyphs(mode.ascii)
			clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			toolTimer.now = func() time.Time { return clock }

			m := compactCmdModel()
			m.now = func() time.Time { return clock }
			m.applyAppearance()
			m.Update(mkWinSize(steps[0][0], steps[0][1]))
			m.seedTranscript(resizeSession(), 1)
			anchor := m.msgBlock[5] // the second turn's reply
			m.follow = false
			m.vp.SetYOffset(m.blocks[anchor].y0)

			for i, size := range steps {
				m.Update(mkWinSize(size[0], size[1]))
				m.layout()
				view := m.View()
				for r, l := range strings.Split(view, "\n") {
					if w := lipgloss.Width(l); w > size[0] {
						t.Errorf("step %d row %d is %d cells wide on a %d-column screen", i, r, w, size[0])
					}
				}
				if m.vp.YOffset != m.blocks[anchor].y0 {
					t.Errorf("step %d (%dx%d): window starts on row %d, want the anchored reply's row %d", i, size[0], size[1], m.vp.YOffset, m.blocks[anchor].y0)
				}
				name := fmt.Sprintf("resize_%s_step%d_%dx%d", mode.name, i, size[0], size[1])
				checkGolden(t, name+".ansi", view)
				checkGolden(t, name+".txt", stripView(view))
			}
		})
	}
}
