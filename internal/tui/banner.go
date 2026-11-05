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

// bannerLogoASCII is the mark for terminals without block glyphs: a 3-row K.
var bannerLogoASCII = [][2]string{
	{"#", "  #"},
	{"#", "##"},
	{"#", "  #"},
}

const (
	bannerLogoWidth  = 10
	bannerLogoGutter = 4
)

// bannerLogoRows paints the mark, using the upper-arm colour above the junction
// and the lower-arm colour below it.
func bannerLogoRows() []string {
	logo := bannerLogo
	if asciiMode {
		logo = bannerLogoASCII
	}
	rows := make([]string, len(logo))
	mid := len(logo) / 2
	for i, seg := range logo {
		arm := tokLogoUpper
		if i > mid || (asciiMode && i == mid) {
			arm = tokLogoLower
		}
		cell := fgTok(tokLogoStem).Render(seg[0]) + fgTok(arm).Render(seg[1])
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

// The card's title and footer ride in its border. The banner block only holds
// text, so bannerBody marks those two lines for renderBannerBox to lift out.
const (
	bannerTitleMark  = "\x00title\x00"
	bannerFooterMark = "\x00footer\x00"
	bannerSplit      = "\x00"
)

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
	if Version == "" {
		return "dev"
	}
	return Version
}

// bannerReadyCell summarises what loaded at startup. Counts stay in the plain
// value colour; only a count that needs attention takes a colour, and carries
// a glyph so it still reads without colour.
func (m *model) bannerReadyCell() string {
	var parts []string
	if m.stats.skills > 0 {
		skills := fmt.Sprintf("%d %s", m.stats.skills, m.tr("skills"))
		if m.stats.skillWarn > 0 {
			skills += fmt.Sprintf(" (%s %d)", glyphs.Warn, m.stats.skillWarn)
			parts = append(parts, warnStyle.Render(skills))
		} else {
			parts = append(parts, chromeStyle.Render(skills))
		}
	}
	if total := m.stats.mcpReady + m.stats.mcpFailed; total > 0 {
		servers := fmt.Sprintf("%d MCP (%d %s)", m.stats.mcpReady, m.stats.mcpTools, m.tr("tools"))
		if m.stats.mcpFailed > 0 {
			servers = fmt.Sprintf("%s %d/%d MCP", glyphs.Fail, m.stats.mcpFailed, total)
			parts = append(parts, errStyle.Render(servers))
		} else {
			parts = append(parts, chromeStyle.Render(servers))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, dimStyle.Render(" · "))
}

// bannerKeyHint is one "key action" pair: the key bold, the action a hint.
func bannerKeyHint(key, action string) string {
	return textHintStyle.Bold(true).Render(key) + " " + textHintStyle.Render(action)
}

// bannerRows returns the card's content rows: what you are talking to, what
// loaded, and how to drive it. The row count is fixed for a given model state
// so the card's height never shoves the composer around.
func (m *model) bannerRows() []string {
	// The model name is what you look for; the provider only qualifies it.
	model := chromeStyle.Render(m.modelName)
	if m.provName != "" {
		model += dimStyle.Render(" · " + m.provName)
	}
	if m.agent != nil && m.agent.Effort != "" {
		model += dimStyle.Render(" · " + m.tr("effort") + " " + m.tr(effortLabel(m.agent.Effort)))
	}
	ready := m.bannerReadyCell()
	if ready == "" {
		ready = dimStyle.Render(m.tr("Directory") + " " + shortCWD())
	}
	type row struct{ left, action string }
	rows := []row{
		{model, "/model"},
		{ready, "/mcp"},
		{"", ""},
		{bannerKeyHint("@", m.tr("attach a file")) + "   " + bannerKeyHint("!", m.tr("run shell")) + "   " + bannerKeyHint("/", m.tr("commands")), ""},
		{bannerKeyHint("Ctrl+P", m.tr("menu")) + "   " + bannerKeyHint("Shift+Tab", m.tr("mode")) + "   " + bannerKeyHint("Esc Esc", m.tr("rewind")), ""},
	}
	// The actions line up just right of the rows they act on; the key hints
	// below may run past them.
	col := 0
	for i := range rows {
		rows[i].left = chromeGlyphs(rows[i].left)
		if rows[i].action != "" {
			col = max(col, lipgloss.Width(rows[i].left))
		}
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.left
		if r.action != "" {
			// /model and /mcp are the actionable spans, so they take the accent.
			out[i] += strings.Repeat(" ", col-lipgloss.Width(r.left)+4) + accentStyle.Render(r.action)
		}
	}
	return withBannerLogo(out)
}

// bannerTitle is the card's top-border title: the wordmark and the version on
// the left, the working directory on the right.
func (m *model) bannerTitle() (left, right string) {
	return brandStyle.Render(m.bannerName()) + " " + dimStyle.Render(versionLabel()), chromeStyle.Render(chromeGlyphs(shortCWD()))
}

// bannerFooter rides the bottom border, only when an update is waiting.
func (m *model) bannerFooter() string {
	if m.updateLatest == "" {
		return ""
	}
	return warnStyle.Render(glyphs.Warn+" "+m.tr("Update")+" "+m.updateLatest) + dimStyle.Render(": ") + accentStyle.Render("kn update")
}

// withBannerLogo sets the mark beside the data rows, centring whichever column
// is shorter against the other so the card never reads as ragged.
func withBannerLogo(rows []string) []string {
	logo := bannerLogoRows()
	const gutter = bannerLogoGutter
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
	left, right := m.bannerTitle()
	lines := append([]string{bannerTitleMark + left + bannerSplit + right}, m.bannerRows()...)
	if f := m.bannerFooter(); f != "" {
		lines = append(lines, bannerFooterMark+f)
	}
	return strings.Join(lines, "\n")
}

// splitBannerBody lifts the title and footer back out of a banner body.
func splitBannerBody(body string) (titleL, titleR string, rows []string, footer string) {
	rows = strings.Split(body, "\n")
	if len(rows) > 0 && strings.HasPrefix(rows[0], bannerTitleMark) {
		titleL, titleR, _ = strings.Cut(strings.TrimPrefix(rows[0], bannerTitleMark), bannerSplit)
		rows = rows[1:]
	}
	if n := len(rows); n > 0 && strings.HasPrefix(rows[n-1], bannerFooterMark) {
		footer = strings.TrimPrefix(rows[n-1], bannerFooterMark)
		rows = rows[:n-1]
	}
	return titleL, titleR, rows, footer
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
	titleL, titleR, lines, footer := splitBannerBody(body)
	if width < 24 {
		out := lines
		if titleL != "" {
			out = append([]string{titleL}, out...)
		}
		if footer != "" {
			out = append(out, footer)
		}
		for i, line := range out {
			out[i] = ansi.Truncate(line, max(width, 1), "…")
		}
		return strings.Join(out, "\n")
	}
	content := 0
	for _, line := range lines {
		content = max(content, lipgloss.Width(line))
	}
	// On a narrow screen the data rows matter more than the mark: drop it
	// before anything has to be cut short.
	if content+4 > width {
		lines = dropBannerLogo(lines)
		content = 0
		for _, line := range lines {
			content = max(content, lipgloss.Width(line))
		}
	}

	// 2 border cells plus 1 padding cell either side.
	boxW := bannerBoxWidth(content, width)
	inner := max(boxW-4, 1)
	b := glyphs.Border
	rule := chromeRuleStyle
	var out []string
	out = append(out, borderTitled(b.TopLeft, b.Top, b.TopRight, titleL, titleR, boxW))
	body2 := append([]string{""}, lines...)
	for _, row := range body2 {
		for _, line := range flowBannerRow(row, inner) {
			line = ansi.Truncate(line, inner, chromeGlyphs("…"))
			out = append(out, rule.Render(b.Left)+" "+line+strings.Repeat(" ", max(inner-lipgloss.Width(line), 0))+" "+rule.Render(b.Right))
		}
	}
	out = append(out, borderTitled(b.BottomLeft, b.Bottom, b.BottomRight, "", footer, boxW))
	box := strings.Join(out, "\n")
	// Centre the card horizontally in the terminal instead of hugging the left
	// gutter.
	pad := max(width-boxW, 0) / 2
	if pad <= 0 {
		return box
	}
	indent := strings.Repeat(" ", pad)
	return indent + strings.ReplaceAll(box, "\n", "\n"+indent)
}

// flowBannerRow fits one card row into width. The row's cells are separated by
// runs of three or more spaces (key hints, a value and its action); a row too
// wide for the card breaks between cells rather than losing its tail.
func flowBannerRow(row string, width int) []string {
	if lipgloss.Width(row) <= width {
		return []string{row}
	}
	var cells []string
	for _, c := range strings.Split(row, "   ") {
		if c = strings.TrimSpace(c); c != "" {
			cells = append(cells, c)
		}
	}
	var out []string
	cur := ""
	for _, c := range cells {
		switch {
		case cur == "":
			cur = c
		case lipgloss.Width(cur)+3+lipgloss.Width(c) <= width:
			cur += "   " + c
		default:
			out = append(out, cur)
			cur = c
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// dropBannerLogo cuts the mark column off the card's rows, then the rows that
// only held the mark.
func dropBannerLogo(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, ansi.Cut(line, bannerLogoWidth+bannerLogoGutter, lipgloss.Width(line)))
	}
	for len(out) > 0 && strings.TrimSpace(ansi.Strip(out[0])) == "" {
		out = out[1:]
	}
	for len(out) > 0 && strings.TrimSpace(ansi.Strip(out[len(out)-1])) == "" {
		out = out[:len(out)-1]
	}
	return out
}

// borderTitled draws one horizontal border of a card with an optional title on
// the left and a cell on the right, dropping the right cell first, then
// truncating the title, when the card is too narrow for both.
func borderTitled(lc, fill, rc, left, right string, w int) string {
	return borderTitledIn(chromeRuleStyle, lc, fill, rc, left, right, w)
}

// borderTitledIn is borderTitled with the border drawn in rule.
func borderTitledIn(rule lipgloss.Style, lc, fill, rc, left, right string, w int) string {
	avail := w - 2 // corners
	seg := func(s string) int {
		if s == "" {
			return 0
		}
		return lipgloss.Width(s) + 2
	}
	const lead, tail = 1, 1
	if seg(left)+seg(right)+lead+tail > avail {
		right = ""
	}
	if seg(left)+lead+tail > avail {
		left = ansi.Truncate(left, max(avail-lead-tail-2, 0), "…")
	}
	var b strings.Builder
	b.WriteString(rule.Render(lc))
	used := 0
	if left != "" {
		b.WriteString(rule.Render(strings.Repeat(fill, lead)) + " " + left + " ")
		used += lead + seg(left)
	}
	gap := avail - used - seg(right) - tail
	if right == "" {
		gap = avail - used
	}
	b.WriteString(rule.Render(strings.Repeat(fill, max(gap, 0))))
	if right != "" {
		b.WriteString(" " + right + " " + rule.Render(strings.Repeat(fill, tail)))
	}
	b.WriteString(rule.Render(rc))
	return b.String()
}
