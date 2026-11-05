package tui

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// The fetch family. web_fetch results fold to their character count like the
// other reads (quietResultSummary). Hosted web searches — run by the
// provider, not by a local tool — arrive through OnHostedSearch and draw as
// their own block, updated in place as the search progresses:
//
//	◆ Web search  "bubbletea viewport"                     ✓ 5 sources
//	  └ Viewport — github.com
//	    Bubbles docs — pkg.go.dev
//	    ⋯ +3 sources · ctrl+e

// blockHostedSearch is a provider-side web search with its sources.
const blockHostedSearch blockKind = 200

func init() {
	registerBlockKind(blockHostedSearch, blockKindSpec{render: renderHostedSearch, toggle: toggleExpanded})
}

const hostedSourcePreview = 3

// onHostedSearch adds the search's block or updates it in place.
func (m *model) onHostedSearch(s ai.HostedSearch) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		b := &m.blocks[i]
		if b.kind == blockHostedSearch && b.toolID == s.ID {
			b.ensureView().search = &s
			b.invalidate()
			m.refreshVP()
			return
		}
	}
	m.flushThink()
	m.flushCurrent()
	m.keepFollow()
	b := block{kind: blockHostedSearch, toolID: s.ID, toolName: "web_search"}
	b.ensureView().search = &s
	m.blocks = append(m.blocks, b)
	m.refreshVP()
}

func renderHostedSearch(b *block, width int) string {
	v := b.viewMeta()
	if v == nil || v.search == nil {
		return textMutedStyle.Render(glyphs.ToolDone + " Web search")
	}
	s := v.search
	quoted := make([]string, len(s.Queries))
	for i, q := range s.Queries {
		quoted[i] = strconv.Quote(q)
	}
	subject := strings.Join(quoted, ", ")
	var glyph, meta string
	n := len(s.Sources)
	sources := strconv.Itoa(n) + " sources"
	if n == 1 {
		sources = "1 source"
	}
	switch s.Status {
	case "searching":
		glyph = runningStyle.Bold(true).Render(glyphs.ToolRun)
		meta = textMutedStyle.Render("searching")
	case "failed":
		glyph = errorTitleStyle.Render(glyphs.Fail)
		meta = errStyle.Render("failed")
	default:
		glyph = textMutedStyle.Render(glyphs.ToolDone)
		meta = okStyle.Render(glyphs.OK) + textMutedStyle.Render(" "+sources)
	}
	head := glyph + " " + toolNameStyle.Render("Web search")
	if subject != "" {
		head += "  " + textSecondaryStyle.Render(subject)
	}
	out := []string{withMeta(chromeGlyphs(head), meta, width)}
	elbow := textMutedStyle.Render("  " + glyphs.Result + " ")
	if s.Error != "" {
		out = append(out, wrapHanging(elbow, textSecondaryStyle.Render(s.Error), width))
		elbow = "    "
	}
	shown := s.Sources
	if !b.expanded && len(shown) > hostedSourcePreview {
		shown = shown[:hostedSourcePreview]
	}
	for _, src := range shown {
		out = append(out, truncCells(elbow+sourceLine(src), width))
		elbow = "    "
	}
	if rest := len(s.Sources) - len(shown); rest > 0 {
		out = append(out, foldHint(rest, "sources"))
	}
	return strings.Join(out, "\n")
}

// sourceLine is one cited source: its title and domain, or the bare URL.
func sourceLine(src ai.SearchSource) string {
	domain := src.URL
	if u, err := url.Parse(src.URL); err == nil && u.Host != "" {
		domain = strings.TrimPrefix(u.Host, "www.")
	}
	title := strings.Join(strings.Fields(src.Title), " ")
	if title == "" {
		return textSecondaryStyle.Render(domain)
	}
	return textSecondaryStyle.Render(title) + textMutedStyle.Render(chromeGlyphs(" — ")+domain)
}
