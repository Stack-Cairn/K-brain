package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/tui/mermaid"
	"github.com/charmbracelet/x/ansi"
)

// A closed ```mermaid fence in a reply is drawn as a text diagram
// (internal/tui/mermaid) under a one-line caption. A fence still streaming,
// a diagram type the terminal renderer does not know, or source it cannot
// read shows as ordinary code, the last two with a one-line note under it.

// isMermaidLang reports whether a fence's info string names Mermaid.
func isMermaidLang(lang string) bool { return strings.EqualFold(lang, "mermaid") }

// renderFenceBlock draws a fenced block: a closed Mermaid fence as a
// diagram when it can, anything else as code.
func renderFenceBlock(code, lang string, open bool, width int) string {
	if !open && isMermaidLang(lang) {
		return renderMermaidBlock(code, width)
	}
	return renderCodeBlock(code, lang, width)
}

type mermaidKey struct {
	code  string
	width int
	md    mdKey
}

// mermaidCache keeps drawn diagrams: the tail unit of a streaming reply is
// redrawn on every update, and a diagram's layout is not free.
var mermaidCache = struct {
	sync.Mutex
	m map[mermaidKey]string
}{m: map[mermaidKey]string{}}

const mermaidCacheMax = 64

func renderMermaidBlock(code string, width int) string {
	key := mermaidKey{code: code, width: width, md: currentMDKey()}
	mermaidCache.Lock()
	out, ok := mermaidCache.m[key]
	mermaidCache.Unlock()
	if ok {
		return out
	}
	out = drawMermaid(code, width)
	mermaidCache.Lock()
	if len(mermaidCache.m) >= mermaidCacheMax {
		mermaidCache.m = map[mermaidKey]string{}
	}
	mermaidCache.m[key] = out
	mermaidCache.Unlock()
	return out
}

func drawMermaid(code string, width int) string {
	lead := strings.Repeat(" ", codeMargin)
	room := max(width-codeMargin, 8)
	d, err := mermaid.Render(code, mermaid.Options{Width: room, ASCII: asciiMode})
	if err != nil {
		note := lead + textMutedStyle.Render(ansi.Truncate("mermaid: "+err.Error()+chromeGlyphs(" — ")+"shown as code", room, ellipsis()))
		return renderCodeBlock(code, "mermaid", width) + "\n" + note
	}
	caption := "mermaid " + d.Kind
	if d.Truncated {
		caption += fmt.Sprintf(chromeGlyphs(" · ")+"cut to %d of %d columns", room, d.FullWidth)
	}
	rows := []string{lead + textMutedStyle.Render(ansi.Truncate(caption, room, ellipsis()))}
	for _, l := range d.Lines {
		if l == "" {
			rows = append(rows, "")
			continue
		}
		rows = append(rows, lead+l)
	}
	return strings.Join(rows, "\n")
}

// mermaidSources are the closed top-level ```mermaid fences in markdown.
func mermaidSources(text string) []string {
	var out []string
	for _, seg := range splitFences(text) {
		if seg.code && !seg.open && isMermaidLang(seg.lang) {
			out = append(out, seg.text)
		}
	}
	return out
}

// blockMermaid is the Mermaid source in a transcript block, nil when it
// has none.
func blockMermaid(b *block) []string {
	if b == nil || b.kind != blockAssistant {
		return nil
	}
	return mermaidSources(b.text)
}

// mermaidOpener opens a file with the system's default application. Tests
// replace it.
var mermaidOpener = func(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// mermaidTempDir is where diagram sources are written for the viewer.
var mermaidTempDir = os.TempDir

// openBlockMermaid writes the focused block's diagrams to .mmd files and
// opens each in the system viewer for the file type.
func (m *model) openBlockMermaid(bi int) {
	srcs := blockMermaid(&m.blocks[bi])
	if len(srcs) == 0 {
		m.flashNotice(m.tr("no mermaid diagram in this block"))
		return
	}
	dir, err := os.MkdirTemp(mermaidTempDir(), "kn-mermaid-")
	if err != nil {
		m.flashNotice(m.tr("mermaid: could not write the diagram: ") + err.Error())
		return
	}
	for i, src := range srcs {
		path := filepath.Join(dir, fmt.Sprintf("diagram-%d.mmd", i+1))
		if err := os.WriteFile(path, []byte(src+"\n"), 0o600); err != nil {
			m.flashNotice(m.tr("mermaid: could not write the diagram: ") + err.Error())
			return
		}
		if err := mermaidOpener(path); err != nil {
			m.flashNotice(m.tr("mermaid: could not open the diagram: ") + err.Error())
			return
		}
	}
	m.flashNotice(fmt.Sprintf(m.tr("opened %d mermaid diagram(s) from %s"), len(srcs), dir))
}
