package tui

import (
	"regexp"
	"strings"
	"sync"

	chromaStyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/glamour"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/x/ansi"
)

func renderMarkdown(s string, width int) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	width = max(width, 8)
	out, err := mdRenderer(width).Render(s)
	if err != nil {
		return s
	}
	rendered := stripLinePadding(strings.Trim(out, "\n"))
	linked := hyperlinkGlamourLinks(rendered, realFileExists)
	linked = linkifyRenderedFilePaths(linked, realFileExists)
	return wrapWideLines(linked, width)
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
)

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
	mdMu.Unlock()
}

func mdStyle() glamouransi.StyleConfig {
	var st glamouransi.StyleConfig
	switch {
	case !mdKnown:
		st = neutralStyle()
	case mdLight:
		st = styles.LightStyleConfig
		st.Code.Color = new("124")
		st.Code.BackgroundColor = new("255")
	default:
		st = styles.DarkStyleConfig
		st.Document.Color = new("252")
		st.Heading.Color = new("75")
		st.H1.Color = new("78")
		st.H2.Color = new("75")
		st.HorizontalRule.Color = new("240")
		st.CodeBlock.BackgroundColor = new("235")
	}
	st.Table.ColumnSeparator = new("│")
	st.Table.CenterSeparator = new("┼")
	st.Table.RowSeparator = new("─")
	zero := uint(0)
	st.Table.Margin = &zero
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

	if mdRendererC != nil && mdAtWidth == width && mdAtLight == mdLight && mdAtKnown == mdKnown {
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
	)
	if err != nil {
		mdRendererErr = true
		return nil
	}
	mdRendererC, mdAtWidth, mdAtLight, mdAtKnown = r, width, mdLight, mdKnown
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
