package tui

import (
	"regexp"
	"strings"
	"sync"

	chromaStyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/glamour"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// renderMarkdown draws s unit by unit (mdstream.go), the way a streaming
// reply is drawn.
func renderMarkdown(s string, width int) string {
	return strings.Join(markdownRows(nil, s, width), "\n")
}

// renderMarkdownWhole draws s as one document, its fenced code blocks cut
// out for renderCodeBlock. A reply with a reference link definition is drawn
// this way, since the definition may serve a link in any unit.
func renderMarkdownWhole(s string, width int) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	width = max(width, 8)
	var parts []string
	for _, seg := range splitFences(s) {
		if seg.code {
			parts = append(parts, renderCodeBlock(seg.text, seg.lang, width))
			continue
		}
		if strings.TrimSpace(seg.text) == "" {
			continue
		}
		r := mdRenderer(width)
		if r == nil {
			return s
		}
		text, held := holdCodeHyphens(seg.text)
		out, err := r.Render(text)
		if err != nil {
			return s
		}
		if held {
			out = strings.ReplaceAll(out, heldHyphen, "-")
		}
		parts = append(parts, stripLinePadding(strings.Trim(out, "\n")))
	}
	rendered := strings.Join(parts, "\n\n")
	linked := hyperlinkGlamourLinks(rendered, realFileExists)
	linked = linkifyRenderedFilePaths(linked, realFileExists)
	return wrapWideLines(linked, width)
}

// mdSegment is a run of markdown: prose for glamour, or one top-level fenced
// code block that renderCodeBlock draws itself.
type mdSegment struct {
	text, lang string
	code       bool
}

var fenceOpenRE = regexp.MustCompile("^(`{3,}|~{3,})[ \\t]*([^`\\s]*)[^`]*$")

// splitFences pulls the top-level fenced code blocks (fence at column 0) out of
// a markdown document. Fences inside lists or quotes stay with glamour. An
// unclosed fence runs to the end, as it does in CommonMark — which is also what
// a reply still streaming its code looks like.
func splitFences(s string) []mdSegment {
	var segs []mdSegment
	lines := strings.Split(s, "\n")
	var prose []string
	flush := func() {
		if len(prose) > 0 {
			segs = append(segs, mdSegment{text: strings.Join(prose, "\n")})
			prose = nil
		}
	}
	for i := 0; i < len(lines); i++ {
		m := fenceOpenRE.FindStringSubmatch(lines[i])
		if m == nil {
			prose = append(prose, lines[i])
			continue
		}
		fence := m[1]
		var code []string
		j := i + 1
		for ; j < len(lines); j++ {
			t := strings.TrimRight(lines[j], " \t")
			if trimmed := strings.TrimLeft(t, " "); len(t)-len(trimmed) <= 3 && strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]) == "" {
				break
			}
			code = append(code, lines[j])
		}
		flush()
		segs = append(segs, mdSegment{text: strings.Join(code, "\n"), lang: m[2], code: true})
		i = j
	}
	flush()
	return segs
}

// renderCodeBlock draws a fenced block as a full-width band with the language
// label right-aligned on its top row and syn.* colours. Without background
// colour (16 colours, mono) the band would vanish, so the fences stay instead.
// Long lines wrap inside the band rather than spilling past it.
func renderCodeBlock(code, lang string, width int) string {
	cb := newCodeBand(lang, width)
	out := append([]string{cb.head()}, cb.body(code)...)
	if foot := cb.foot(); foot != "" {
		out = append(out, foot)
	}
	return strings.Join(out, "\n")
}

// codeBand draws a fenced block in parts: the head row, the rows of some
// code, and the closing row, so an open fence can be drawn a group of lines
// at a time.
type codeBand struct {
	lang         string
	width, inner int
	band         bool
	base         lipgloss.Style
}

const codeMargin, codePad = 2, 2

func newCodeBand(lang string, width int) codeBand {
	cb := codeBand{lang: lang, width: width, inner: max(width-codeMargin-codePad, 4), band: backgroundsActive()}
	if cb.band {
		cb.base = codeBandStyle
	}
	return cb
}

func (cb codeBand) head() string {
	lead := strings.Repeat(" ", codeMargin)
	if !cb.band {
		return lead + textMutedStyle.Render("```"+cb.lang)
	}
	label := ""
	if cb.lang != "" {
		label = ansi.Truncate(cb.lang, max(cb.inner-1, 1), "")
	}
	gap := max(cb.width-codeMargin-lipgloss.Width(label)-1, 0)
	return lead + cb.base.Render(strings.Repeat(" ", gap)) + withBG(textMutedStyle, cb.base).Render(label+" ")
}

func (cb codeBand) body(code string) []string {
	lead := strings.Repeat(" ", codeMargin)
	var out []string
	for _, l := range highlightLines(code, codeLexer(cb.lang, ""), cb.base) {
		for _, piece := range strings.Split(ansi.Hardwrap(l, cb.inner, true), "\n") {
			row := cb.base.Render(strings.Repeat(" ", codePad)) + piece
			if cb.band {
				gap := max(cb.width-codeMargin-lipgloss.Width(row), 0)
				out = append(out, lead+row+cb.base.Render(strings.Repeat(" ", gap)))
			} else {
				out = append(out, strings.TrimRight(lead+row, " "))
			}
		}
	}
	return out
}

// foot is the closing fence row, drawn only without a band. The band ends
// with the code: a blank band row would be dropped as an empty line by the
// transcript's indenting anyway.
func (cb codeBand) foot() string {
	if cb.band {
		return ""
	}
	return strings.Repeat(" ", codeMargin) + textMutedStyle.Render("```")
}

// withBG lays a foreground style over a base's background.
func withBG(fg, base lipgloss.Style) lipgloss.Style {
	if bg := base.GetBackground(); bg != nil {
		if _, none := bg.(lipgloss.NoColor); !none {
			return fg.Background(bg)
		}
	}
	return fg
}

// heldHyphen stands in for a hyphen inside an inline code span while glamour
// wraps the prose: its word wrap breaks after any hyphen, which split spans
// such as `--json` across two lines. It has the same width and is put back
// once the paragraph is laid out.
const heldHyphen = "\u2011"

var inlineCodeRE = regexp.MustCompile("`[^`\n]+`")

func holdCodeHyphens(s string) (string, bool) {
	if strings.Contains(s, heldHyphen) || !strings.Contains(s, "`") {
		return s, false
	}
	held := false
	out := inlineCodeRE.ReplaceAllStringFunc(s, func(span string) string {
		if !strings.Contains(span, "-") {
			return span
		}
		held = true
		return strings.ReplaceAll(span, "-", heldHyphen)
	})
	return out, held
}

func wrapWideLines(s string, width int) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if ansi.StringWidth(l) > width {
			lines[i] = ansi.Hardwrap(l, width, true)
		}
	}
	return strings.Join(lines, "\n")
}

var padStripRE = regexp.MustCompile(`(?:\x1b\[[0-9;]*m[ \t]*)+(\x1b\[[0-9;]*m)?$`)

func stripLinePadding(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = padStripRE.ReplaceAllString(l, "$1")
		// Under a profile with no colour glamour pads with bare spaces.
		l = strings.TrimRight(l, " ")
		if ansi.StringWidth(l) == 0 || strings.TrimSpace(ansi.Strip(l)) == "" {
			l = ""
		} else {
			l = selfTerminate(l)
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}

func selfTerminate(l string) string {
	if strings.Contains(l, "\x1b[") && !strings.HasSuffix(l, "\x1b[0m") {
		l += "\x1b[0m"
	}
	return l
}

var (
	mdMu          sync.Mutex
	mdAtWidth     int
	mdAtLight     bool
	mdAtKnown     bool
	mdRendererC   *glamour.TermRenderer
	mdRendererErr bool
	mdLight       bool
	mdKnown       bool
	mdScheme      string
	mdAtProfile   termenv.Profile
	mdAtMono      bool
	mdAtASCII     bool
	mdAtDark      bool

	// mdLinkTextSGR and mdLinkHrefSGR open a link label and an href in the
	// current renderer's output.
	mdLinkTextSGR, mdLinkHrefSGR string
)

func mdLinkSGRs() (text, href string) {
	mdMu.Lock()
	defer mdMu.Unlock()
	return mdLinkTextSGR, mdLinkHrefSGR
}

// glamourSGR is the escape sequence glamour opens a span of style p with,
// built the way glamour builds it (colour, background, then attributes).
func glamourSGR(profile termenv.Profile, p glamouransi.StylePrimitive) string {
	out := termenv.String("x")
	if p.Color != nil {
		out = out.Foreground(profile.Color(*p.Color))
	}
	if p.BackgroundColor != nil {
		out = out.Background(profile.Color(*p.BackgroundColor))
	}
	if p.Underline != nil && *p.Underline {
		out = out.Underline()
	}
	if p.Bold != nil && *p.Bold {
		out = out.Bold()
	}
	if p.Italic != nil && *p.Italic {
		out = out.Italic()
	}
	sgr, _, _ := strings.Cut(out.String(), "x")
	return sgr
}

func SetLightTheme(light bool) {
	mdMu.Lock()
	mdLight, mdKnown = light, true
	mdRendererC, mdAtWidth = nil, 0
	mdMu.Unlock()
}

func SetUnknownTheme() {
	mdMu.Lock()
	mdKnown = false
	mdRendererC, mdAtWidth = nil, 0
	mdMu.Unlock()
}

func setSchemeOverride(s string) {
	mdMu.Lock()
	mdScheme = s
	mdMu.Unlock()
}

func CurrentTheme() string {
	mdMu.Lock()
	defer mdMu.Unlock()
	if mdScheme != "" {
		return mdScheme
	}
	if !mdKnown {
		return "auto"
	}
	if mdLight {
		return "light"
	}
	return "dark"
}

func unregisterChromaStyle() {
	delete(chromaStyles.Registry, "charm")
}

func invalidateMDRenderer() {
	mdMu.Lock()
	mdRendererC, mdAtWidth = nil, 0
	mdGen++
	mdMu.Unlock()
}

func mdStyle() glamouransi.StyleConfig {
	var st glamouransi.StyleConfig
	if !mdKnown && !monoMode {
		st = neutralStyle()
	} else {
		st = kryptonMarkdownStyle()
	}
	st.Table.ColumnSeparator = new(glyphs.Border.Left)
	st.Table.CenterSeparator = new(glyphs.Border.Middle)
	st.Table.RowSeparator = new(glyphs.Border.Top)
	zero := uint(0)
	st.Table.Margin = &zero
	// Link labels and hrefs are found again in glamour's output by their
	// escape sequences, so the two must differ from each other and from bold
	// text under every profile, mono included: labels are bold and underlined,
	// hrefs only underlined.
	yes := true
	st.LinkText.Bold, st.LinkText.Underline = &yes, &yes
	st.Link.Underline, st.Link.Bold = &yes, nil
	if asciiMode {
		st.Item.BlockPrefix = "* "
		st.Task.Ticked, st.Task.Unticked = "[x] ", "[ ] "
		st.BlockQuote.IndentToken = new(glyphs.Rail + " ")
		st.HorizontalRule.Format = "\n--------\n"
		st.DefinitionDescription.BlockPrefix = "\n-> "
	}
	return st
}

// kryptonMarkdownStyle paints markdown from the theme tokens: headings by
// weight and the accent, inline code on the band, links in state.running and
// underlined, quotes on a muted rail. Body text keeps the terminal default.
// Without colour, heading markers and backticks stay so structure survives.
func kryptonMarkdownStyle() glamouransi.StyleConfig {
	st := styles.DarkStyleConfig
	col := func(id tokenID) *string {
		if v := tokString(id); v != "" {
			return &v
		}
		return nil
	}
	yes := true
	st.Document.Color = nil
	st.Heading.Color = nil
	st.Heading.Bold = &yes
	st.H1 = glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Color: col(tokAccent), Bold: &yes}}
	st.H2 = glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Bold: &yes}}
	st.H3 = glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Color: col(tokTextSecondary), Bold: &yes}}
	for _, h := range []*glamouransi.StyleBlock{&st.H4, &st.H5, &st.H6} {
		*h = glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Color: col(tokTextSecondary)}}
	}
	if monoMode {
		st.H1.Prefix, st.H2.Prefix, st.H3.Prefix = "# ", "## ", "### "
		st.H4.Prefix, st.H5.Prefix, st.H6.Prefix = "#### ", "##### ", "###### "
	}
	st.HorizontalRule.Color = col(tokLineRule)
	st.BlockQuote.Color = col(tokTextMuted)
	st.BlockQuote.IndentToken = new(glyphs.Rail + " ")
	st.Link = glamouransi.StylePrimitive{Color: col(tokStateRunning), Underline: &yes}
	st.LinkText = glamouransi.StylePrimitive{Color: col(tokStateRunning), Underline: &yes}
	st.Image = glamouransi.StylePrimitive{Color: col(tokStateRunning), Underline: &yes}
	st.ImageText = glamouransi.StylePrimitive{Color: col(tokTextMuted), Format: "Image: {{.text}} →"}
	if backgroundsActive() {
		st.Code = glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Prefix: " ", Suffix: " ", BackgroundColor: col(tokSurfaceBand)}}
	} else {
		st.Code = glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Prefix: "`", Suffix: "`"}}
	}
	st.CodeBlock.Color = nil
	st.CodeBlock.Chroma = glamourChroma()
	st.Enumeration.Color = col(tokTextMuted)
	st.Item.Color = nil
	return st
}

func neutralStyle() glamouransi.StyleConfig {
	st := styles.DarkStyleConfig
	st.Document.Color = nil
	st.Heading.Color = new("4")
	st.H1.Color, st.H1.BackgroundColor = nil, nil
	st.H1.Prefix, st.H1.Suffix = "# ", ""
	st.H6.Color = nil
	st.HorizontalRule.Color = new("8")
	st.Link.Color = new("4")
	st.LinkText.Color = new("6")
	st.Image.Color = new("4")
	st.ImageText.Color = new("8")
	st.Code.Color = new("1")
	st.Code.BackgroundColor = nil
	st.CodeBlock.Color = nil
	st.CodeBlock.Chroma = nil
	return st
}

func mdRenderer(width int) *glamour.TermRenderer {
	mdMu.Lock()
	defer mdMu.Unlock()
	if mdRendererErr {
		return nil
	}

	profile := lipgloss.ColorProfile()
	dark := lipgloss.HasDarkBackground()
	if mdRendererC != nil && mdAtWidth == width && mdAtLight == mdLight && mdAtKnown == mdKnown &&
		mdAtProfile == profile && mdAtMono == monoMode && mdAtASCII == asciiMode && mdAtDark == dark {
		return mdRendererC
	}
	unregisterChromaStyle()
	st := mdStyle()
	margin := uint(2)
	st.Document.Margin = &margin
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(st),
		glamour.WithWordWrap(width),
		glamour.WithPreservedNewLines(),
		glamour.WithColorProfile(profile),
		glamour.WithChromaFormatter(chromaFormatter()),
	)
	if err != nil {
		mdRendererErr = true
		return nil
	}
	mdRendererC, mdAtWidth, mdAtLight, mdAtKnown = r, width, mdLight, mdKnown
	mdLinkTextSGR, mdLinkHrefSGR = glamourSGR(profile, st.LinkText), glamourSGR(profile, st.Link)
	mdAtProfile, mdAtMono, mdAtASCII, mdAtDark = profile, monoMode, asciiMode, dark
	return r
}

var bareSGR = strings.NewReplacer("\x1b[m", "\x1b[0m")

func sanitizeView(s string) string {
	s = bareSGR.Replace(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {

		lines[i] = selfTerminate(l)
	}
	return strings.Join(lines, "\n")
}

func sanitizeInputView(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = selfTerminate(l)
	}
	return strings.Join(lines, "\n")
}
