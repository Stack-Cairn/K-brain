package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestHeaderChromeFitsTerminal(t *testing.T) {
	for _, width := range []int{1, 2, 3, 8, 24, 25, 40, 80, 120} {
		for _, label := range []string{"commands", "指令", strings.Repeat("指令", 40)} {
			out := kbrainHeaderLabel(width, " k-brain · "+strings.Repeat("模型名称", 40), "✦ high", label)
			lines := strings.Split(ansi.Strip(out), "\n")
			if len(lines) != 2 {
				t.Fatalf("header height = %d, want 2", len(lines))
			}
			for _, line := range lines {
				if got := lipgloss.Width(line); got > width {
					t.Fatalf("width %d: header overflow (%d): %q", width, got, line)
				}
			}
			if width >= 24 && !strings.HasSuffix(lines[0], "✦ high ") {
				t.Fatalf("long model hid effort at width %d: %q", width, lines[0])
			}
		}
	}
}

func TestChromeFooterFitsLanguages(t *testing.T) {
	m := compactCmdModel()
	for _, language := range []string{"en", "zh_cn", "zh_Hant"} {
		m.cfg.Language = language
		for _, width := range []int{0, 1, 12, 40, 80, 120} {
			m.width = width
			out := m.footerHints()
			if lipgloss.Height(out) > 1 || lipgloss.Width(out) > width {
				t.Fatalf("%s at %d: footer overflows: %q", language, width, out)
			}
			if width == 120 && !strings.Contains(ansi.Strip(out), m.permissionModeLabel()) {
				t.Fatalf("footer missing mode: %q", out)
			}
		}
	}
}

func TestStyledChromePreservesBottomLayout(t *testing.T) {
	for _, language := range []string{"en", "zh_cn", "zh_Hant"} {
		for _, size := range [][2]int{{40, 24}, {80, 30}, {120, 40}} {
			t.Run(fmt.Sprintf("%s/%dx%d", language, size[0], size[1]), func(t *testing.T) {
				m := compactCmdModel()
				m.cfg.Language = language
				m.applyAppearance()
				m.Update(mkWinSize(size[0], size[1]))
				m.sessTitle = "优化界面"
				m.input.SetValue("ANCHOR-INPUT")
				m.layout()
				assertBottomAnchored(t, m)
				for _, line := range strings.Split(m.View(), "\n") {
					if lipgloss.Width(line) > m.width {
						t.Fatalf("frame overflow: %q", ansi.Strip(line))
					}
				}
				status := ansi.Strip(m.statusView())
				if !strings.HasSuffix(status, m.sessTitle) || lipgloss.Width(status) != m.width {
					t.Fatalf("title not right aligned: %q", status)
				}
			})
		}
	}
}
