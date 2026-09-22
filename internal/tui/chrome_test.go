package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestChromeColumnsFitsTerminal(t *testing.T) {
	for _, width := range []int{0, 1, 2, 3, 8, 24, 25, 40, 80, 120} {
		for _, right := range []string{"✦ high", "✦ 高", strings.Repeat("指令", 40)} {
			out := chromeColumns(dimStyle.Render(strings.Repeat("模型名称", 40)), accentStyle.Render(right), width)
			plain := ansi.Strip(out)
			if strings.Contains(plain, "\n") {
				t.Fatalf("width %d: columns must render one row: %q", width, plain)
			}
			if got := lipgloss.Width(plain); got != width {
				t.Fatalf("width %d: columns rendered %d cells: %q", width, got, plain)
			}
			if width >= 24 && lipgloss.Width(right) <= width-2 && !strings.HasSuffix(plain, right) {
				t.Fatalf("long left cell pushed out the right cell at width %d: %q", width, plain)
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
			if width == 120 {
				plain := ansi.Strip(out)
				if !strings.Contains(plain, m.permissionModeLabel()) {
					t.Fatalf("footer missing mode: %q", out)
				}
				if !strings.Contains(plain, m.modelName) {
					t.Fatalf("footer missing model: %q", out)
				}
				if !strings.HasSuffix(plain, m.effortChip()) {
					t.Fatalf("effort chip should end the row: %q", out)
				}
			}
		}
	}
}

func TestStyledChromePreservesFrameLayout(t *testing.T) {
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
				assertTopAnchored(t, m)
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
