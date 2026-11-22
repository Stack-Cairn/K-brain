package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const mermaidReply = "The request path:\n\n```mermaid\ngraph TD\n  C[Client] --> G{Gateway}\n  G -->|cached| R[(Cache)]\n  G -->|miss| S[Service]\n  S --> R\n```\n\nThe cache sits behind the gateway."

const mermaidSeqReply = "```mermaid\nsequenceDiagram\n  participant U as User\n  participant K as kn\n  U->>K: prompt\n  K-->>U: reply\n```"

// The diagram scenes pin a reply with a diagram on the whole screen in every
// colour mode and size the other goldens use.
func TestMermaidTranscriptGoldens(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)
	defer SetLightTheme(false)
	scenes := []struct {
		name, reply string
	}{
		{"mermaid-flow", mermaidReply},
		{"mermaid-seq", mermaidSeqReply},
		{"mermaid-fallback", "```mermaid\npie title Pets\n  \"Dogs\" : 3\n```"},
	}
	for _, sc := range scenes {
		for _, mode := range goldenModes {
			for _, size := range goldenSizes {
				name := fmt.Sprintf("%s_%s_%dx%d", sc.name, mode.name, size[0], size[1])
				t.Run(name, func(t *testing.T) {
					restore := useProfile(t, mode.profile, mode.dark)
					defer restore()
					SetLightTheme(!mode.dark)
					if mode.mono {
						setMonoMode(true, true)
					}
					setASCIIGlyphs(mode.ascii)
					m := compactCmdModel()
					m.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
					m.applyAppearance()
					m.Update(mkWinSize(size[0], size[1]))
					m.appendRaw(blockUser, "draw the request path")
					m.Update(textMsg(sc.reply))
					m.flushCurrent()
					m.layout()
					view := m.View()
					for i, l := range strings.Split(view, "\n") {
						if w := lipgloss.Width(l); w > size[0] {
							t.Errorf("row %d is %d cells wide on a %d-column screen: %q", i, w, size[0], ansi.Strip(l))
						}
					}
					if mode.ascii {
						for _, r := range ansi.Strip(view) {
							if r > 0x7e {
								t.Errorf("ascii view contains %q", r)
								break
							}
						}
					}
					if mode.name == "truecolor-dark" {
						checkGolden(t, name+".ansi", view)
					}
					checkGolden(t, name+".txt", stripView(view))
				})
			}
		}
	}
}

func TestMermaidDrawnOnlyOnceClosed(t *testing.T) {
	setASCIIGlyphs(false)
	open := "Here:\n\n```mermaid\ngraph LR\n  A --> B\n"
	rows := ansi.Strip(strings.Join(markdownRows(nil, open, 80), "\n"))
	if !strings.Contains(rows, "A --> B") || strings.Contains(rows, "┌───┐") {
		t.Fatalf("a streaming fence should show its code:\n%s", rows)
	}
	closed := open + "```\n"
	c := &mdCache{}
	markdownRows(c, open, 80)
	rows = ansi.Strip(strings.Join(markdownRows(c, closed, 80), "\n"))
	if strings.Contains(rows, "A --> B") || !strings.Contains(rows, "│ A ├──►│ B │") {
		t.Fatalf("a closed fence should draw the diagram:\n%s", rows)
	}
	if !strings.Contains(rows, "mermaid flowchart") {
		t.Errorf("diagram has no caption:\n%s", rows)
	}
}

func TestMermaidWholeDocumentPath(t *testing.T) {
	setASCIIGlyphs(false)
	// A reference definition sends the reply through renderMarkdownWhole.
	s := "See [docs][d].\n\n```mermaid\ngraph LR\n  A --> B\n```\n\n[d]: https://example.invalid/docs\n"
	rows := ansi.Strip(strings.Join(markdownRows(nil, s, 80), "\n"))
	if !strings.Contains(rows, "│ A ├──►│ B │") {
		t.Fatalf("whole-document render should draw the diagram:\n%s", rows)
	}
	open := "```mermaid\ngraph LR\n  A --> B"
	if out := ansi.Strip(renderMarkdownWhole(open, 80)); !strings.Contains(out, "A --> B") {
		t.Fatalf("an open fence stays code in the whole-document path:\n%s", out)
	}
}

func TestMermaidFallbacks(t *testing.T) {
	setASCIIGlyphs(false)
	for src, note := range map[string]string{
		"pie title x\n  \"a\" : 1":               "pie diagrams are not drawn",
		"graph TD\n  A[unclosed --> B":           "mermaid: line 2",
		"classDiagram\n  A <|-- B":               "classDiagram diagrams are not drawn",
		"graph TD\n  subgraph S\n  end\n  S-->A": "edges to subgraphs",
	} {
		out := ansi.Strip(renderMermaidBlock(src, 80))
		if !strings.Contains(out, note) || !strings.Contains(out, "shown as code") {
			t.Errorf("%q: no fallback note %q:\n%s", src, note, out)
		}
		if !strings.Contains(out, strings.SplitN(src, "\n", 2)[0]) {
			t.Errorf("%q: fallback should show the source:\n%s", src, out)
		}
	}
}

func TestMermaidASCIIAndTruncation(t *testing.T) {
	setASCIIGlyphs(true)
	defer setASCIIGlyphs(false)
	out := ansi.Strip(renderMermaidBlock("graph LR\n  A --> B", 80))
	if !strings.Contains(out, "| A +-->| B |") {
		t.Fatalf("ASCII mode should draw with ASCII:\n%s", out)
	}
	setASCIIGlyphs(false)
	var b strings.Builder
	b.WriteString("graph LR\n")
	for i := range 10 {
		fmt.Fprintf(&b, "  n%d[Stage %d] --> n%d\n", i, i, i+1)
	}
	out = ansi.Strip(renderMermaidBlock(b.String(), 60))
	if !strings.Contains(out, "cut to 58 of") {
		t.Fatalf("a cut diagram should say so:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if ansi.StringWidth(l) > 60 {
			t.Fatalf("row wider than 60: %q", l)
		}
	}
}

func TestMermaidKeyOpensSource(t *testing.T) {
	dir := t.TempDir()
	prevDir, prevOpen := mermaidTempDir, mermaidOpener
	defer func() { mermaidTempDir, mermaidOpener = prevDir, prevOpen }()
	mermaidTempDir = func() string { return dir }
	var opened []string
	mermaidOpener = func(path string) error {
		opened = append(opened, path)
		return nil
	}

	m := focusModel(80, 40)
	m.appendAssistantBlock(mermaidReply + "\n\n```mermaid\ngraph LR\n  X --> Y\n```")
	m.refreshVP()
	m.layout()
	m = keys(t, m, "alt+up")
	if v := stripView(m.View()); !strings.Contains(v, "m open diagram") {
		t.Fatalf("focus bar should offer the diagram key:\n%s", v)
	}
	m = keys(t, m, "m")
	if len(opened) != 2 {
		t.Fatalf("opened %v, want two .mmd files", opened)
	}
	for _, p := range opened {
		if filepath.Ext(p) != ".mmd" || !strings.HasPrefix(p, dir) {
			t.Errorf("opened %s, want a .mmd file under the temp dir", p)
		}
	}
	src, err := os.ReadFile(opened[0])
	if err != nil || !strings.Contains(string(src), "G -->|cached| R[(Cache)]") {
		t.Errorf("first file holds %q (%v)", src, err)
	}
	if !strings.Contains(m.transientNotice, "opened 2 mermaid diagram") {
		t.Errorf("notice = %q", m.transientNotice)
	}

	// A block without a diagram says so and opens nothing.
	opened = nil
	m = keys(t, m, "k")
	if v := stripView(m.View()); strings.Contains(v, "m open diagram") {
		t.Errorf("the hint should only show on a block with a diagram:\n%s", v)
	}
	m = keys(t, m, "m")
	if len(opened) != 0 || !strings.Contains(m.transientNotice, "no mermaid diagram") {
		t.Errorf("opened %v, notice %q", opened, m.transientNotice)
	}
}
