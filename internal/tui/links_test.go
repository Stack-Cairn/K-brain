package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func existsAll(string) bool  { return true }
func existsNone(string) bool { return false }

func TestLinkifyFilePaths(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		exists func(string) bool
		want   string
	}{
		{"relative path", "see internal/tui/tui.go now", existsAll, "]8;;file://"},
		{"dot-relative", "see ./docs/features.md now", existsAll, "]8;;file://"},
		{"absolute", "see /etc/hostname now", existsAll, "file:///etc/hostname"},
		{"line ref kept", "see internal/tui/tui.go:42 now", existsAll, "]8;;file://"},
		{"line ref in uri", "internal/tui/tui.go:42", existsAll, "tui.go:42\x07"},
		{"missing file untouched", "see internal/tui/tui.go now", existsNone, ""},
		{"bare filename, exists", "see tui.go now", existsAll, "]8;;file://"},
		{"bare filename, missing", "see ghost.go now", existsNone, ""},
		{"no extension untouched", "see internal/tui now", existsNone, ""},
		{"markdown link target skipped", "[x](internal/tui/tui.go)", existsAll, ""},
		{"markdown link text skipped", "[internal/tui/tui.go](https://x)", existsAll, ""},
		{"url untouched", "see https://example.com/a/b.html now", existsAll, "https://example.com/a/b.html"},
		{"trailing dot excluded", "see internal/tui/tui.go. Done", existsAll, "file://"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := linkifyFilePaths(tt.in, tt.exists)
			if tt.want == "" {
				if got != tt.in {
					t.Errorf("input should pass through unchanged:\n in: %q\ngot: %q", tt.in, got)
				}
				return
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("output missing %q:\n in: %q\ngot: %q", tt.want, tt.in, got)
			}
		})
	}
}

func TestLinkifyFilePathsWidthNeutral(t *testing.T) {
	in := "open internal/tui/tui.go:42 please"
	got := linkifyFilePaths(in, existsAll)
	if ansi.Strip(got) != in {
		t.Errorf("stripped output must equal input:\n in: %q\ngot: %q", in, ansi.Strip(got))
	}
	if ansi.StringWidth(got) != ansi.StringWidth(in) {
		t.Errorf("width changed: %d vs %d", ansi.StringWidth(got), ansi.StringWidth(in))
	}
}

func TestLinkifyFilePathsKeepsVisibleText(t *testing.T) {
	in := "fix internal/tui/tui.go:42"
	got := linkifyFilePaths(in, existsAll)
	if !strings.Contains(ansi.Strip(got), "internal/tui/tui.go:42") {
		t.Errorf("visible text lost: %q", ansi.Strip(got))
	}
}

func TestSplitLineRef(t *testing.T) {
	tests := []struct{ ref, path, line string }{
		{"a/b.go:42", "a/b.go", "42"},
		{"a/b.go", "a/b.go", ""},
		{"a/b.go:x", "a/b.go:x", ""},
		{"a/b.go:", "a/b.go", ""},
		{"/a/b:1/c.go", "/a/b:1/c.go", ""},
	}
	for _, tt := range tests {
		p, l := splitLineRef(tt.ref)
		if p != tt.path || l != tt.line {
			t.Errorf("splitLineRef(%q) = (%q, %q), want (%q, %q)", tt.ref, p, l, tt.path, tt.line)
		}
	}
}

func TestTargetURI(t *testing.T) {
	tests := []struct {
		dest   string
		exists func(string) bool
		want   string
	}{
		{"https://example.com/x", existsNone, "https://example.com/x"},
		{"http://example.com", existsNone, "http://example.com"},
		{"mailto:a@b.c", existsNone, "mailto:a@b.c"},
		{"file:///etc/hostname", existsNone, "file:///etc/hostname"},
		{"#anchor", existsAll, ""},
		{"docs/features.md", existsAll, "file://"},
		{"./docs/features.md", existsAll, "file://"},
		{"docs/features.md", existsNone, ""},
		{"not a path at all", existsNone, ""},
		{"/docs/features.md", existsAll, "file://"},
		{"internal/tui/tui.go:7", existsAll, "file://"},
	}
	for _, tt := range tests {
		got := targetURI(tt.dest, tt.exists)
		if tt.want == "" {
			if got != "" {
				t.Errorf("targetURI(%q) = %q, want unlinked", tt.dest, got)
			}
			continue
		}
		if !strings.Contains(got, tt.want) {
			t.Errorf("targetURI(%q) = %q, want substring %q", tt.dest, got, tt.want)
		}
	}
}

func renderRaw(t *testing.T, s string, width int) string {
	t.Helper()
	out, err := mdRenderer(width).Render(s)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return stripLinePadding(strings.Trim(out, "\n"))
}

func TestHyperlinkGlamourLinksMarkdownLink(t *testing.T) {
	raw := renderRaw(t, "See [the docs](https://example.com/docs) end.", 80)
	linked := hyperlinkGlamourLinks(raw, existsNone)

	if !strings.Contains(linked, ansi.SetHyperlink("https://example.com/docs")) {
		t.Errorf("label should open an OSC 8 link to the href:\n%q", linked)
	}
	if !strings.Contains(linked, "the docs") {
		t.Errorf("label text lost: %q", ansi.Strip(linked))
	}

	if strings.Contains(ansi.Strip(linked), "https://example.com/docs") {
		t.Errorf("href should not be duplicated visibly: %q", ansi.Strip(linked))
	}

	if !strings.Contains(ansi.Strip(raw), "the docs https://example.com/docs") {
		t.Fatalf("fixture assumption broken, raw stripped: %q", ansi.Strip(raw))
	}
}

func TestHyperlinkGlamourLinksAutolink(t *testing.T) {
	raw := renderRaw(t, "Go to https://bare.example.com/x now.", 80)
	linked := hyperlinkGlamourLinks(raw, existsNone)

	if !strings.Contains(linked, ansi.SetHyperlink("https://bare.example.com/x")) {
		t.Errorf("autolink should become clickable:\n%q", linked)
	}

	if ansi.Strip(linked) != ansi.Strip(raw) {
		t.Errorf("autolink strip mismatch:\nraw:    %q\nlinked: %q", ansi.Strip(raw), ansi.Strip(linked))
	}
}

func TestHyperlinkGlamourLinksFileDestination(t *testing.T) {
	raw := renderRaw(t, "Open [the feature map](./docs/features.md) here.", 80)
	linked := hyperlinkGlamourLinks(raw, existsAll)

	if !strings.Contains(linked, "]8;;file://") {
		t.Errorf("existing relative file destination should become file://:\n%q", linked)
	}
	if strings.Contains(ansi.Strip(linked), "/docs/features.md") {
		t.Errorf("file href should not render visibly: %q", ansi.Strip(linked))
	}

	plain := hyperlinkGlamourLinks(raw, existsNone)
	if strings.Contains(plain, "]8;") {
		t.Errorf("missing file must not become a link: %q", plain)
	}
	if !strings.Contains(ansi.Strip(plain), "/docs/features.md") {
		t.Errorf("unlinked href should stay visible: %q", ansi.Strip(plain))
	}
}

func TestHyperlinkGlamourLinksAnchorUntouched(t *testing.T) {
	raw := renderRaw(t, "Jump [below](#section) now.", 80)
	linked := hyperlinkGlamourLinks(raw, existsAll)
	if strings.Contains(linked, "]8;") {
		t.Errorf("anchors are never hyperlinked: %q", linked)
	}
}

func TestHyperlinkGlamourLinksWrapSafe(t *testing.T) {
	md := "See [the documentation page](https://example.com/some/long/path) for details."
	linked := wrapWideLines(hyperlinkGlamourLinks(renderRaw(t, md, 40), existsNone), 40)
	for i, l := range strings.Split(linked, "\n") {
		if w := ansi.StringWidth(l); w > 40 {
			t.Errorf("line %d exceeds width 40 (%d): %q", i, w, l)
		}
	}

	if !strings.Contains(linked, ansi.SetHyperlink("https://example.com/some/long/path")) {
		t.Errorf("hyperlink broken by wrapping: %q", linked)
	}
}

func TestRenderMarkdownLinksClickable(t *testing.T) {
	out := renderMarkdown("See [the docs](https://example.com/docs) and https://bare.example.com/x.", 80)
	if !strings.Contains(out, ansi.SetHyperlink("https://example.com/docs")) {
		t.Errorf("markdown link should be clickable: %q", out)
	}
	if !strings.Contains(out, ansi.SetHyperlink("https://bare.example.com/x")) {
		t.Errorf("autolink should be clickable: %q", out)
	}
	plain := ansi.Strip(out)
	if strings.Count(plain, "https://example.com/docs") != 0 {
		t.Errorf("href rendered as duplicate text: %q", plain)
	}
	if strings.Count(plain, "https://bare.example.com/x") != 1 {
		t.Errorf("autolink should render exactly once: %q", plain)
	}
}

func TestRenderMarkdownBareFilePath(t *testing.T) {

	out := renderMarkdown("The bug is in links.go and links_test.go.", 80)
	if !strings.Contains(out, "]8;;file://") {
		t.Errorf("bare existing file path should be linkified: %q", out)
	}

	plain := strings.Join(strings.Fields(ansi.Strip(out)), " ")
	if !strings.Contains(plain, "links_test.go") {
		t.Errorf("visible path text lost: %q", plain)
	}

	out = renderMarkdown("Ghost at no/such/file.go remains text.", 80)
	if strings.Contains(out, "]8;") {
		t.Errorf("nonexistent file must not linkify: %q", out)
	}
	if !strings.Contains(ansi.Strip(out), "no/such/file.go") {
		t.Errorf("plain path text lost: %q", ansi.Strip(out))
	}
}

func TestUserMessageFileLink(t *testing.T) {
	m := compactCmdModel()
	m.width = 80
	m.append(youStyle.Render("❯ ") + linkifyFilePaths("look at links_test.go please", realFileExists))
	rendered := m.blocks[len(m.blocks)-1].render(80)
	if !strings.Contains(rendered, "]8;;file://") {
		t.Errorf("user file ref should be clickable: %q", rendered)
	}
	if !strings.Contains(ansi.Strip(rendered), "❯ look at links_test.go please") {
		t.Errorf("user text must render verbatim: %q", ansi.Strip(rendered))
	}
}

func TestRealFileExists(t *testing.T) {
	if !realFileExists("links_test.go") {
		t.Error("own test file should exist relative to package CWD")
	}
	if realFileExists("no/such/ghost.go") {
		t.Error("ghost path should not exist")
	}
	if !realFileExists(filepath.Join(mustWd(t), "links_test.go")) {
		t.Error("absolute path to own test file should exist")
	}
	if realFileExists(".") {
		t.Error("a directory is not a linkable file")
	}
}

func mustWd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}
