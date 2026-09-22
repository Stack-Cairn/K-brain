package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// bannerName is the wordmark, translated: the product is K-brain in English and
// 氪脑 in Chinese, not both at once. The ⬢ glyph it used to carry is gone — the
// block logo beside it is the mark now.
func (m *model) bannerName() string { return m.tr("K-brain") }

// bannerLogo is the K mark from the product icon, drawn with solid blocks so it
// survives any font and any colour depth. Each row is split into the stem and
// the arm so the logo's own gradient — blue stem, cyan upper arm, violet lower
// arm — comes through.
var bannerLogo = [][2]string{
	{"███", "    ███"},
	{"███", "  ███"},
	{"███", "███"},
	{"███", "███"},
	{"███", "  ███"},
	{"███", "    ███"},
}

const bannerLogoWidth = 10

// bannerLogoRows paints the mark, using the upper-arm colour above the junction
// and the lower-arm colour below it.
func bannerLogoRows() []string {
	rows := make([]string, len(bannerLogo))
	mid := len(bannerLogo) / 2
	for i, seg := range bannerLogo {
		arm := logoUpperColor
		if i > mid {
			arm = logoLowerColor
		}
		cell := lipgloss.NewStyle().Foreground(logoStemColor).Render(seg[0]) +
			lipgloss.NewStyle().Foreground(arm).Render(seg[1])
		rows[i] = cell + strings.Repeat(" ", max(bannerLogoWidth-lipgloss.Width(ansi.Strip(cell)), 0))
	}
	return rows
}

// The card is sized to its content — the mark plus the widest data row — and
// only floored so a short model name cannot shrink it into a stub.
const bannerMinWidth = 64

// bannerBoxWidth is how wide the card should be, never narrower than its own
// content and never wider than the terminal.
func bannerBoxWidth(content, width int) int {
	return min(max(content+4, bannerMinWidth), width)
}

// bannerFrame uses the same border colour as the composer rules so the startup
// card and the prompt read as one system.
var bannerFrame = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(borderColor).
	Padding(0, 1)

// bannerStats is what the card's "ready" row summarises. It is filled by
// startupReport and refreshed as MCP servers finish connecting.
type bannerStats struct {
	skills    int
	skillWarn int
	mcpReady  int
	mcpFailed int
	mcpTools  int
}

func versionLabel() string {
	if Version == "" || Version == "dev" {
		return "dev"
	}
	return "v" + Version
}

// bannerLabel right-pads a label to a fixed column so values line up, counting
// display cells so CJK labels align too.
func bannerLabel(label string) string {
	const col = 12
	label += ":"
	return dimStyle.Render(label + strings.Repeat(" ", max(col-lipgloss.Width(label), 1)))
}

// bannerReadyCell summarises what loaded at startup. Counts stay in the plain
// value colour; only a count that needs attention takes a colour.
func (m *model) bannerReadyCell() string {
	var parts []string
	if m.stats.skills > 0 {
		skills := fmt.Sprintf("%d %s", m.stats.skills, m.tr("skills"))
		if m.stats.skillWarn > 0 {
			skills += fmt.Sprintf(" (%d ⚠)", m.stats.skillWarn)
			parts = append(parts, warnStyle.Render(skills))
		} else {
			parts = append(parts, chromeStyle.Render(skills))
		}
	}
	if total := m.stats.mcpReady + m.stats.mcpFailed; total > 0 {
		servers := fmt.Sprintf("%d MCP (%d %s)", m.stats.mcpReady, m.stats.mcpTools, m.tr("tools"))
		if m.stats.mcpFailed > 0 {
			servers = fmt.Sprintf("%d/%d MCP ✗", m.stats.mcpFailed, total)
			parts = append(parts, errStyle.Render(servers))
		} else {
			parts = append(parts, chromeStyle.Render(servers))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, dimStyle.Render("  ·  "))
}

// bannerRows returns the card's rows unstyled by reveal state. The row count is
// fixed for a given model state so the reveal animation can never change the
// card's height, which would shove the composer around.
func (m *model) bannerRows() []string {
	// The model name is what you look for; the provider only qualifies it, so it
	// drops a level rather than taking a colour of its own. That leaves exactly
	// one bright span before the accent.
	model := chromeStyle.Render(m.modelName)
	if m.provName != "" {
		model += dimStyle.Render("@" + m.provName)
	}
	// /model is the one actionable thing in the card, so it is the one thing
	// that gets the accent. The reasoning effort is deliberately absent: the
	// footer already carries it, accented.
	model += strings.Repeat(" ", 4) + accentStyle.Render("/model") + dimStyle.Render(" "+m.tr("to change"))

	rows := []string{
		brandStyle.Render(m.bannerName()) + "   " + dimStyle.Render(versionLabel()),
		"",
		bannerLabel(m.tr("Model")) + model,
		"",
		bannerLabel(m.tr("Directory")) + chromeStyle.Render(shortCWD()),
	}
	if ready := m.bannerReadyCell(); ready != "" {
		rows = append(rows, bannerLabel(m.tr("Ready"))+ready)
	}
	if m.updateLatest != "" {
		rows = append(rows, bannerLabel(m.tr("Update"))+
			warnStyle.Render(m.updateLatest)+dimStyle.Render(" · ")+accentStyle.Render("kn update"))
	}
	return withBannerLogo(rows)
}

// withBannerLogo sets the mark beside the data rows, centring whichever column
// is shorter against the other so the card never reads as ragged.
func withBannerLogo(rows []string) []string {
	logo := bannerLogoRows()
	const gutter = 4
	blank := strings.Repeat(" ", bannerLogoWidth)

	height := max(len(logo), len(rows))
	logoTop := (height - len(logo)) / 2
	rowsTop := (height - len(rows)) / 2

	out := make([]string, height)
	for i := range out {
		mark := blank
		if j := i - logoTop; j >= 0 && j < len(logo) {
			mark = logo[j]
		}
		text := ""
		if j := i - rowsTop; j >= 0 && j < len(rows) {
			text = rows[j]
		}
		out[i] = mark + strings.Repeat(" ", gutter) + text
	}
	return out
}

// bannerBody renders the card whole. It is painted in a single frame on
// purpose: revealing it row by row reads as a flicker on startup.
func (m *model) bannerBody() string {
	return strings.Join(m.bannerRows(), "\n")
}

// appendBanner seeds the startup card. The card is the only welcome chrome:
// there is no tip line.
func (m *model) appendBanner() {
	m.appendRaw(blockBanner, m.bannerBody())
}

// refreshBanner restates the card in place. Its text is derived from model
// state, so regenerating it and marking the block stale is what makes
// renderAtMode — which caches on width alone — draw it again.
func (m *model) refreshBanner() {
	for i := range m.blocks {
		if m.blocks[i].kind == blockBanner {
			m.blocks[i].text, m.blocks[i].stale = m.bannerBody(), true
			m.refreshVP()
			return
		}
	}
}

// resetTranscript drops every rendered block and reseeds the banner, keeping it
// as block 0 for resume, rewind and /clear alike.
func (m *model) resetTranscript() {
	m.blocks, m.msgBlock = nil, nil
	m.appendBanner()
}

func renderBannerBox(body string, width int) string {
	lines := strings.Split(body, "\n")
	if width < 24 {
		for i, line := range lines {
			lines[i] = ansi.Truncate(line, max(width, 1), "…")
		}
		return strings.Join(lines, "\n")
	}
	content := 0
	for _, line := range lines {
		content = max(content, lipgloss.Width(line))
	}

	// 2 border cells plus 1 padding cell either side.
	boxW := bannerBoxWidth(content, width)
	inner := max(boxW-4, 1)
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, inner, "…")
	}

	box := bannerFrame.Width(max(boxW-2, 1)).Render(strings.Join(lines, "\n"))
	// Centre the card horizontally in the terminal instead of hugging the left
	// gutter.
	pad := max(width-boxW, 0) / 2
	if pad <= 0 {
		return box
	}
	indent := strings.Repeat(" ", pad)
	return indent + strings.ReplaceAll(box, "\n", "\n"+indent)
}
