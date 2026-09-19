package tui

import (
	"os"
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/fileuri"
)

const fileNamePart = `[\p{L}\p{N}_@+~-][\p{L}\p{N}_@+~.%-]*`

var fileRefRE = regexp.MustCompile(
	`(?:[A-Za-z]:[\\/]|\\\\)` + fileNamePart + `(?:[\\/]` + fileNamePart + `)*(?::\d+)?` +
		`|/?` + fileNamePart + `(?:[\\/]` + fileNamePart + `)+(?::\d+)?` +
		`|/?` + fileNamePart + `\.[A-Za-z]{2,10}(?::\d+)?` +
		`|\.{1,2}[\\/]` + fileNamePart + `(?:[\\/]` + fileNamePart + `)*(?::\d+)?`,
)

func linkifyFilePaths(s string, exists func(string) bool) string {
	return replaceMatches(s, fileRefRE, func(m string, before byte) string {

		if strings.ContainsRune("([]/\\:;\"`", rune(before)) {
			return m
		}
		path, line := splitLineRef(m)
		if !isFileRef(path) || !exists(path) {
			return m
		}
		return hyperlink(absFileURI(path, line), m)
	})
}

func isFileRef(path string) bool {
	dot := strings.LastIndexByte(path, '.')
	if dot < 0 {

		return strings.ContainsAny(path, `/\`)
	}
	ext := path[dot+1:]
	if strings.ContainsAny(ext, `/\`) {
		return false
	}
	if strings.Contains(path, "/") {
		return len(ext) >= 1
	}

	return len(ext) >= 1
}

func hyperlink(uri, text string) string {
	return ansi.SetHyperlink(uri) + text + ansi.ResetHyperlink()
}

func replaceMatches(s string, re *regexp.Regexp, fn func(m string, before byte) string) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/4)
	last := 0
	for _, loc := range re.FindAllStringIndex(s, -1) {
		var before byte
		if loc[0] > 0 {
			before = s[loc[0]-1]
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(fn(s[loc[0]:loc[1]], before))
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

func realFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func splitLineRef(ref string) (path, line string) {
	i := strings.LastIndexByte(ref, ':')
	if i > 0 && i < len(ref)-1 && isDigits(ref[i+1:]) {
		return strings.TrimRight(ref[:i], ".,;:!?"), ref[i+1:]
	}
	return strings.TrimRight(ref, ".,;:!?"), ""
}

func isDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

func absFileURI(path, line string) string {
	if line != "" {
		path += ":" + line
	}
	return fileuri.FromPath(path)
}

const (
	linkTextSGRDark  = "\x1b[38;5;35;1m"
	linkTextSGRLight = "\x1b[38;5;29;1m"
	linkSGRDark      = "\x1b[38;5;30;4m"
	linkSGRLight     = "\x1b[38;5;36;4m"
	sgrReset         = "\x1b[0m"
)

type linkAtom struct {
	start, end int
	sgr        string
	kind       byte
	text       string
}

func parseLinkAtoms(s string) []linkAtom {
	var atoms []linkAtom
	for i := 0; i < len(s); {
		sgr, kind := linkAtomAt(s[i:])
		if kind == 0 {
			i++
			continue
		}
		end, text := scanAtom(s, i, sgr)
		if end < 0 {
			i++
			continue
		}
		atoms = append(atoms, linkAtom{start: i, end: end, sgr: sgr, kind: kind, text: text})
		i = end
	}
	return atoms
}

type linkGroup struct {
	labels     []linkAtom
	hrefs      []linkAtom
	start, end int
	hasLabel   bool
}

func groupLinkAtoms(s string, atoms []linkAtom) []linkGroup {
	var groups []linkGroup
	i := 0
	for i < len(atoms) {
		a := atoms[i]
		if a.kind == 'h' {

			g := linkGroup{start: a.start}
			for i < len(atoms) && atoms[i].kind == 'h' &&
				(len(g.hrefs) == 0 || gapOK(s[g.hrefs[len(g.hrefs)-1].end:atoms[i].start])) {
				g.hrefs = append(g.hrefs, atoms[i])
				g.end = atoms[i].end
				i++
			}
			groups = append(groups, g)
			continue
		}

		g := linkGroup{hasLabel: true, start: a.start}
		for i < len(atoms) && atoms[i].kind == 't' {
			g.labels = append(g.labels, atoms[i])
			i++
		}

		prevEnd := g.labels[len(g.labels)-1].end
		for i < len(atoms) && atoms[i].kind == 'h' && gapOK(s[prevEnd:atoms[i].start]) {
			g.hrefs = append(g.hrefs, atoms[i])
			prevEnd = atoms[i].end
			i++
		}
		if len(g.hrefs) > 0 {
			g.end = g.hrefs[len(g.hrefs)-1].end
		} else {
			g.end = g.labels[len(g.labels)-1].end
		}
		groups = append(groups, g)
	}
	return groups
}

func gapOK(gap string) bool {
	for i := 0; i < len(gap); {
		c := gap[i]
		if c == ' ' || c == '\n' || c == '\t' {
			i++
			continue
		}
		if c == 0x1b {

			j := i + 1
			for j < len(gap) && gap[j] != 'm' {
				j++
			}
			if j >= len(gap) {
				return false
			}
			i = j + 1
			continue
		}
		return false
	}
	return true
}

func hyperlinkGlamourLinks(s string, exists func(string) bool) string {
	atoms := parseLinkAtoms(s)
	if len(atoms) == 0 {
		return s
	}
	groups := groupLinkAtoms(s, atoms)

	type repl struct {
		start, end int
		out        string
	}
	var repls []repl
	for _, g := range groups {
		if len(g.hrefs) == 0 {
			continue

		}
		var sb strings.Builder
		for _, h := range g.hrefs {
			sb.WriteString(h.text)
		}
		hrefText := sb.String()
		uri := targetURI(hrefText, exists)
		if uri == "" {
			continue
		}
		if !g.hasLabel {

			var out strings.Builder
			for _, h := range g.hrefs {
				out.WriteString(hyperlink(uri, h.sgr+h.text+sgrReset))
			}
			repls = append(repls, repl{g.start, g.end, out.String()})
			continue
		}

		var label strings.Builder
		for _, l := range g.labels {
			label.WriteString(l.sgr + l.text + sgrReset)
		}
		repls = append(repls, repl{g.start, g.end, hyperlink(uri, label.String())})
	}

	var b strings.Builder
	b.Grow(len(s) + len(s)/4)
	last := 0
	for _, r := range repls {
		b.WriteString(s[last:r.start])
		b.WriteString(r.out)
		last = r.end
	}
	b.WriteString(s[last:])
	return b.String()
}

func linkAtomAt(s string) (string, byte) {
	for _, cand := range []struct {
		sgr  string
		kind byte
	}{
		{linkTextSGRDark, 't'},
		{linkTextSGRLight, 't'},
		{linkSGRDark, 'h'},
		{linkSGRLight, 'h'},
	} {
		if strings.HasPrefix(s, cand.sgr) {
			return cand.sgr, cand.kind
		}
	}
	return "", 0
}

func scanAtom(s string, start int, sgr string) (end int, text string) {
	body := start + len(sgr)
	if body > len(s) {
		return -1, ""
	}
	rest := s[body:]
	nl := strings.IndexByte(rest, '\n')
	rs := strings.Index(rest, sgrReset)
	if rs < 0 {
		return -1, ""
	}
	if nl >= 0 && nl < rs {
		return body + rs + len(sgrReset), rest[:nl]
	}
	return body + rs + len(sgrReset), rest[:rs]
}

func linkifyRenderedFilePaths(s string, exists func(string) bool) string {
	return replaceMatches(s, fileRefRE, func(m string, before byte) string {
		if before == 0x1b || strings.ContainsRune("([]/\\:;\"`m", rune(before)) {

			return m
		}
		path, line := splitLineRef(m)
		if !exists(path) {
			return m
		}
		return hyperlink(absFileURI(path, line), m)
	})
}

func targetURI(dest string, exists func(string) bool) string {
	low := strings.ToLower(dest)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") ||
		strings.HasPrefix(low, "mailto:") || strings.HasPrefix(low, "file://") {
		return dest
	}
	if strings.HasPrefix(dest, "#") {
		return ""
	}
	path, line := splitLineRef(dest)

	candidates := []string{path}
	if strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") {
		candidates = append(candidates, "."+path)
	}
	for _, c := range candidates {
		if exists(c) {
			return absFileURI(c, line)
		}
	}
	return ""
}
