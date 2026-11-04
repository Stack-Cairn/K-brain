package tools

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// htmlSkipped are elements whose content is not page text; site navigation is
// dropped too, as it repeats on every page.
var htmlSkipped = map[string]bool{
	"head": true, "script": true, "style": true, "noscript": true, "svg": true, "math": true,
	"template": true, "iframe": true, "object": true, "canvas": true, "select": true,
	"datalist": true, "nav": true,
}

// htmlBreaks is the number of line breaks around a block element: 2 leaves a blank
// line, 1 starts a new line.
var htmlBreaks = map[string]int{
	"p": 2, "h1": 2, "h2": 2, "h3": 2, "h4": 2, "h5": 2, "h6": 2, "pre": 2, "blockquote": 2,
	"table": 2, "hr": 2, "ul": 1, "ol": 1, "dl": 1, "li": 1, "dt": 1, "dd": 1, "tr": 1,
	"div": 1, "section": 1, "article": 1, "header": 1, "footer": 1, "aside": 1,
	"main": 1, "figure": 1, "figcaption": 1, "form": 1, "fieldset": 1, "legend": 1,
	"address": 1, "details": 1, "summary": 1, "caption": 1, "center": 1,
}

// textWriter builds readable text: whitespace collapses to single spaces, and block
// boundaries become at most one blank line.
type textWriter struct {
	b      strings.Builder
	breaks int    // line breaks owed before the next text
	space  bool   // a space owed before the next word
	prefix string // a list or heading marker owed before the next text
}

func (w *textWriter) brk(n int) {
	w.breaks = max(w.breaks, n)
	w.space = false
}

// start writes what is owed before visible text: no break or space opens the page.
func (w *textWriter) start() {
	if w.b.Len() > 0 {
		if w.breaks > 0 {
			w.b.WriteString(strings.Repeat("\n", w.breaks))
		} else if w.space {
			w.b.WriteByte(' ')
		}
	}
	w.breaks, w.space = 0, false
	if w.prefix != "" {
		w.b.WriteString(w.prefix)
		w.prefix = ""
	}
}

func (w *textWriter) text(s string) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		if s != "" {
			w.space = true
		}
		return
	}
	if strings.TrimLeftFunc(s, unicode.IsSpace) != s {
		w.space = true
	}
	w.start()
	w.b.WriteString(strings.Join(fields, " "))
	w.space = strings.TrimRightFunc(s, unicode.IsSpace) != s
}

// raw writes preformatted text as is.
func (w *textWriter) raw(s string) {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	if s == "" {
		return
	}
	w.start()
	w.b.WriteString(s)
}

// htmlConverter walks a parsed page. The parser repairs unclosed and misnested
// tags the way browsers do, so a stray <nav> cannot swallow the rest of the page.
type htmlConverter struct {
	w     textWriter
	base  *url.URL
	title string
	pre   int
	lists []int // per open list: -1 for ul, else the next ol number
	cells int
}

// htmlToText converts an HTML page to readable text and returns its title. Links
// keep their text, followed by the absolute URL when it adds something.
func htmlToText(page string, base *url.URL) (text, title string) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return cleanFetchText(page), ""
	}
	c := &htmlConverter{base: base}
	c.head(doc)
	c.walk(doc)
	return cleanFetchText(c.w.b.String()), c.title
}

// head finds the page title and a <base href> that relative links resolve against.
func (c *htmlConverter) head(n *html.Node) {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "svg", "math", "template":
			return
		case "title":
			if c.title == "" {
				c.title = strings.Join(strings.Fields(nodeText(n)), " ")
			}
			return
		case "base":
			if href := attrOf(n, "href"); href != "" && c.base != nil {
				if u, err := c.base.Parse(href); err == nil {
					c.base = u
				}
			}
		}
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.head(ch)
	}
}

func (c *htmlConverter) walk(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		if c.pre > 0 {
			c.w.raw(n.Data)
		} else {
			c.w.text(n.Data)
		}
		return
	case html.DocumentNode:
		c.children(n)
		return
	case html.ElementNode:
	default:
		return
	}
	name := n.Data
	if htmlSkipped[name] || name == "title" || hiddenNode(n) {
		return
	}
	w := &c.w
	if b := htmlBreaks[name]; b > 0 {
		w.brk(b)
	}
	switch name {
	case "br":
		w.breaks = min(w.breaks+1, 2)
		w.space = false
	case "hr":
		w.start()
		w.b.WriteString("---")
		w.brk(2)
	case "h1", "h2", "h3", "h4", "h5", "h6":
		level, _ := strconv.Atoi(name[1:])
		w.prefix = strings.Repeat("#", level) + " "
		c.children(n)
	case "ul", "ol":
		if len(c.lists) == 0 {
			w.brk(2) // a blank line around a top-level list
		}
		next := -1
		if name == "ol" {
			next = 1
			if v, err := strconv.Atoi(attrOf(n, "start")); err == nil {
				next = v
			}
		}
		c.lists = append(c.lists, next)
		c.children(n)
		c.lists = c.lists[:len(c.lists)-1]
		if len(c.lists) == 0 {
			w.brk(2)
		}
	case "li":
		marker := "- "
		if len(c.lists) > 0 && c.lists[len(c.lists)-1] >= 0 {
			marker = strconv.Itoa(c.lists[len(c.lists)-1]) + ". "
			c.lists[len(c.lists)-1]++
		}
		w.prefix = strings.Repeat("  ", max(len(c.lists)-1, 0)) + marker
		c.children(n)
	case "tr":
		c.cells = 0
		c.children(n)
	case "td", "th":
		if c.cells > 0 && w.b.Len() > 0 && w.breaks == 0 {
			w.b.WriteString(" | ")
			w.space = false
		}
		c.cells++
		c.children(n)
	case "pre":
		c.pre++
		c.children(n)
		c.pre--
	case "a":
		href, from := fetchLinkTarget(attrOf(n, "href"), c.base), w.b.Len()
		c.children(n)
		if label := strings.TrimSpace(w.b.String()[from:]); href != "" && label != "" && strings.TrimSuffix(label, "/") != strings.TrimSuffix(href, "/") {
			w.b.WriteString(" (" + href + ")")
		}
	default:
		c.children(n)
	}
	if b := htmlBreaks[name]; b > 0 {
		w.brk(b)
	}
}

func (c *htmlConverter) children(n *html.Node) {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.walk(ch)
	}
}

// hiddenNode reports an element the page hides: the hidden attribute, aria-hidden,
// or an inline display:none.
func hiddenNode(n *html.Node) bool {
	for _, a := range n.Attr {
		switch a.Key {
		case "hidden":
			return true
		case "aria-hidden":
			if strings.EqualFold(strings.TrimSpace(a.Val), "true") {
				return true
			}
		case "style":
			if strings.Contains(strings.ReplaceAll(strings.ToLower(a.Val), " ", ""), "display:none") {
				return true
			}
		}
	}
	return false
}

func attrOf(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var collect func(*html.Node)
	collect = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			collect(ch)
		}
	}
	collect(n)
	return b.String()
}

// fetchLinkTarget is the absolute http(s) URL of a link, or "" for in-page anchors,
// scripts and other schemes.
func fetchLinkTarget(href string, base *url.URL) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") || base == nil {
		return ""
	}
	u, err := base.Parse(href)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.String()
}

// cleanFetchText trims trailing spaces from each line and keeps at most one blank
// line in a row.
func cleanFetchText(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t ")
		if line == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}
