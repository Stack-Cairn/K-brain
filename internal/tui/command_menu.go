package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/skills"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m *model) commandArgumentHint() string {
	value := m.input.Value()
	if m.namePrompt != nil || m.iactive != nil || !strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\n\r\t") {
		return ""
	}
	name := strings.TrimRight(value, " ")
	if strings.Contains(name, " ") {
		return ""
	}
	for _, template := range m.promptCatalog.items {
		if "/"+template.Name == name {
			return template.ArgumentHint
		}
	}
	if entry := registryFind(name); entry != nil {
		return entry.Args
	}
	return ""
}

func (m *model) inputArgumentView(view string) string {
	hint := m.commandArgumentHint()
	if hint == "" || m.input.ShowLineNumbers || m.input.LineInfo().CharOffset != utf8.RuneCountInString(m.input.Value()) {
		return view
	}
	lines := strings.Split(view, "\n")
	width := lipgloss.Width(lines[0])
	start := lipgloss.Width(m.input.Prompt) + lipgloss.Width(m.input.Value()) + 1
	if start >= width || m.input.LineInfo().RowOffset != 0 {
		return view
	}
	hint = ansi.Truncate(strings.Join(strings.Fields(hint), " "), width-start, "…")
	end := start + lipgloss.Width(hint)
	lines[0] = ansi.Cut(lines[0], 0, start) + dimStyle.Render(hint) + ansi.Cut(lines[0], end, width)
	return strings.Join(lines, "\n")
}

func (m *model) menuView() string {
	if m.menu == nil || len(m.menu.cands) == 0 {
		return ""
	}
	width := max(m.width, 1)
	start := max(m.menu.idx-menuRows+1, 0)
	end := min(start+menuRows, len(m.menu.cands))
	nameWidth := 0
	for _, candidate := range m.menu.cands {
		nameWidth = max(nameWidth, lipgloss.Width(candidate.Text))
	}
	nameWidth = min(nameWidth, max(width-2, 0))
	var b strings.Builder
	for i := start; i < end; i++ {
		candidate := m.menu.cands[i]
		name := ansi.Truncate(candidate.Text, nameWidth, ellipsisGlyph())
		line := name
		if descriptionWidth := width - 2 - nameWidth - 2; descriptionWidth > 0 {
			line += strings.Repeat(" ", nameWidth-lipgloss.Width(name)+2)
			description := ansi.Truncate(strings.Join(strings.Fields(m.chromeText(candidate.Desc)), " "), descriptionWidth, ellipsisGlyph())
			if i == m.menu.idx {
				line += description
			} else {
				line += dimStyle.Render(description)
			}
		}
		if i == m.menu.idx {
			line = selectedRowStyle.Width(width).Render(menuMark() + line)
		} else {
			line = "  " + line
		}
		b.WriteString(ansi.Truncate(line, width, ellipsisGlyph()))
		b.WriteByte('\n')
	}
	footer := fmt.Sprintf("  %d/%d", m.menu.idx+1, len(m.menu.cands))
	b.WriteString(dimStyle.Render(ansi.Truncate(footer, width, ellipsisGlyph())))
	return b.String()
}

type menu struct {
	head   string
	cands  []cand
	idx    int
	base   string
	cyc    bool
	cycled bool
	frozen []cand
}

func (m *model) openMenu() {
	head, cands := m.promptCompletions(m.input.Value())
	if len(cands) == 0 {
		return
	}
	m.menu = &menu{head: head, cands: cands}
	m.menuCycle(0)
}

func (m *model) refreshMenu() {
	if m.menu != nil && m.menu.cyc && m.menu.frozen != nil && m.menu.idx < len(m.menu.frozen) &&
		m.input.Value() == m.menu.head+m.menu.frozen[m.menu.idx].Text {
		return
	}
	val := m.input.Value()
	token := val[strings.LastIndexAny(val, " \n")+1:]
	if strings.HasPrefix(val, "/") || strings.HasPrefix(token, "@") || strings.HasPrefix(token, "$") {
		head, cands := m.promptCompletions(val)
		if len(cands) > 0 {
			idx := 0
			if m.menu != nil && m.menu.idx < len(cands) && m.menu.frozen == nil {
				idx = m.menu.idx
			}
			m.menu = &menu{head: head, cands: cands, idx: idx}
			return
		}
	}
	m.menu = nil
}

func (m *model) previewCand() {
	m.input.SetValue(m.menu.head + m.menu.cands[m.menu.idx].Text)
	m.refreshMenu()
}

func (m *model) acceptPreview() {
	m.menu.cyc, m.menu.frozen = false, nil
	v := m.input.Value()
	if !strings.HasSuffix(v, "/") {
		m.input.SetValue(v + " ")
	}
	m.refreshMenu()
}

func (m *model) menuCycle(delta int) {
	mu := m.menu
	if mu.frozen == nil {
		mu.cyc, mu.frozen = true, mu.cands
		mu.base = mu.head + mu.frozen[mu.idx].Text
	}
	if mu.cycled {
		mu.idx = (mu.idx + delta + len(mu.cands)) % len(mu.cands)
	} else {
		mu.cycled = true
	}
	m.previewCand()
}

func (m *model) accept() bool {
	c := m.menu.cands[m.menu.idx]
	v := m.menu.head + c.Text
	if !strings.HasSuffix(c.Text, "/") {
		v += " "
	}
	if strings.TrimRight(m.input.Value(), " ") == strings.TrimRight(v, " ") {
		m.menu = nil
		return false
	}
	m.input.SetValue(v)
	m.menu = nil
	m.refreshMenu()
	return true
}

func (m *model) modelCands() []cand {
	out := make([]cand, 0, len(m.cfg.Models))
	for name, mdl := range m.cfg.Models {
		out = append(out, cand{name, "via " + strings.Join(mdl.Providers, ", ")})
	}

	for _, it := range buildModelItems(m.cfg) {
		if it.fromCatalog {
			out = append(out, cand{it.model, "via " + it.provider + " (catalog)"})
		}
	}
	return out
}

func (m *model) providerCands() []cand {
	out := make([]cand, 0, len(m.cfg.Providers))
	for name, p := range m.cfg.Providers {
		out = append(out, cand{name, p.BaseURL})
	}
	return out
}

func (m *model) skillCands() []cand {
	sk := skills.Scan(skills.DefaultDirs()...)
	out := make([]cand, 0, len(sk))
	for _, s := range sk {
		d := s.Description
		if s.Pack != "" {
			d = "[" + s.Pack + "] " + d
		}
		out = append(out, cand{"$" + s.Name, skills.TruncateDescription(d, 80)})
	}
	return out
}

const menuRows = 8

// menuMark is the pointer before the selected row of a menu or panel.
func menuMark() string {
	if asciiMode {
		return "> "
	}
	return "› "
}

// ellipsisGlyph is the truncation mark, ASCII in ASCII mode.
func ellipsisGlyph() string { return chromeGlyphs("…") }

// chromeText is a description as the menu shows it: glyphs swapped for ASCII
// in that mode.
func (m *model) chromeText(s string) string { return chromeGlyphs(s) }
