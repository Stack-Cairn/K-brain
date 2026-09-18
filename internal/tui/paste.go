package tui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func readClipboardImage() (string, []byte, error) {
	ext, data, err := macOSPasteImage()
	if err != nil || data != nil {
		return ext, data, err
	}

	for _, tool := range []struct {
		name string
		fn   func() (string, []byte, error)
	}{
		{"wl-paste", wlPasteImage},
		{"xclip", xclipImage},
		{"xsel", xselImage},
		{"pngpaste", pngpasteImage},
		{"powershell.exe", powershellImage},
	} {
		if _, err := exec.LookPath(tool.name); err != nil {
			continue
		}
		ext, data, err := tool.fn()
		if err != nil || data != nil {
			return ext, data, err
		}
	}
	return "", nil, nil
}

const macOSPasteImageScript = `ObjC.import('AppKit')

function run(argv) {
  const pasteboard = $.NSPasteboard.generalPasteboard
  let data = pasteboard.dataForType($.NSPasteboardTypePNG)
  if (!data) {
    const image = $.NSImage.alloc.initWithPasteboard(pasteboard)
    if (image) {
      const rep = $.NSBitmapImageRep.imageRepWithData(image.TIFFRepresentation)
      if (rep) {
        data = rep.representationUsingTypeProperties(
          $.NSBitmapImageFileTypePNG,
          $.NSDictionary.dictionary,
        )
      }
    }
  }
  if (data) data.writeToFileAtomically($(argv[0]), true)
}`

func macOSPasteImage() (string, []byte, error) {
	if runtime.GOOS != "darwin" {
		return "", nil, nil
	}
	if _, err := exec.LookPath("osascript"); err != nil {
		return "", nil, nil
	}

	tmp, err := os.CreateTemp("", "k-brain-paste-*.png")
	if err != nil {
		return "", nil, err
	}
	if err := tmp.Close(); err != nil {
		return "", nil, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := run("osascript", "-l", "JavaScript", "-e", macOSPasteImageScript, tmp.Name()); err != nil {
		return "", nil, err
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return "", nil, err
	}
	if len(data) == 0 {
		return "", nil, nil
	}
	return "png", data, nil
}

func hasImageType(types []byte) (string, bool) {
	for line := range strings.SplitSeq(string(types), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "image/"); ok {
			return after, true
		}
	}
	return "", false
}

func run(name string, args ...string) ([]byte, error) {

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

func wlPasteImage() (string, []byte, error) {
	types, err := run("wl-paste", "--list-types")
	if err != nil {
		return "", nil, err
	}
	ext, ok := hasImageType(types)
	if !ok {
		return "", nil, nil
	}
	data, err := run("wl-paste", "--type", "image/"+ext)
	return ext, data, err
}

func xclipImage() (string, []byte, error) {
	targets, err := run("xclip", "-selection", "clipboard", "-o", "-t", "TARGETS")
	if err != nil {
		return "", nil, err
	}
	ext, ok := hasImageType(targets)
	if !ok {
		return "", nil, nil
	}
	data, err := run("xclip", "-selection", "clipboard", "-o", "-t", "image/"+ext)
	return ext, data, err
}

func xselImage() (string, []byte, error) {

	data, err := run("xsel", "--clipboard", "--output", "--target", "image/png")
	if err != nil || len(data) == 0 {
		return "", nil, err
	}
	return "png", data, nil
}

func pngpasteImage() (string, []byte, error) {
	tmp, err := os.CreateTemp("", "k-brain-paste-*.png")
	if err != nil {
		return "", nil, err
	}
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := exec.CommandContext(context.Background(), "pngpaste", tmp.Name()).Run(); err != nil {
		return "", nil, nil
	}
	data, err := os.ReadFile(tmp.Name())
	if len(data) == 0 {
		return "", nil, err
	}
	return "png", data, err
}

func powershellImage() (string, []byte, error) {
	const script = `Add-Type -AssemblyName System.Windows.Forms; ` +
		`$img = [Windows.Forms.Clipboard]::GetImage(); ` +
		`if ($img -eq $null) { exit 1 }; ` +
		`$img.Save([Console]::OpenStandardOutput(), [System.Drawing.Imaging.ImageFormat]::Png)`
	data, err := exec.CommandContext(context.Background(), "powershell.exe", "-NoProfile", "-Command", script).Output()
	if err != nil || len(data) == 0 {
		return "", nil, err
	}
	return "png", data, nil
}

func pastedImagePath(text string) (string, bool) {
	path := strings.TrimSpace(text)
	if u, err := url.Parse(path); err == nil && u.Scheme == "file" {
		if u.Host != "" && u.Host != "localhost" {
			return "", false
		}
		path = u.Path
	}
	path = unescapePath(path)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	if imageFileExtension(path, nil) != "" {
		return path, true
	}

	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	header := make([]byte, 12)
	n, err := f.Read(header)
	if err != nil && n == 0 {
		return "", false
	}
	return path, imageFileExtension("", header[:n]) != ""
}

func imageFileExtension(path string, data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case len(data) >= 3 && bytes.Equal(data[:3], []byte("\xff\xd8\xff")):
		return "jpg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "gif"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "webp"
	case bytes.HasPrefix(data, []byte("BM")):
		return "bmp"
	}

	ext := strings.ToLower(filepath.Ext(path))
	if imageExtsForMention[ext] {
		return strings.TrimPrefix(ext, ".")
	}
	return ""
}

func pasteImageFileCmd(path string) tea.Msg {
	data, err := os.ReadFile(path)
	if err != nil {
		return imageMsg{err: err}
	}
	ext := imageFileExtension(path, data)
	if ext == "" {
		return imageMsg{err: errors.New("pasted file is not a supported image")}
	}
	display := filepath.Base(path)
	path, err = saveClipboardImage(ext, data)
	if err != nil {
		return imageMsg{err: err}
	}
	return imageMsg{path: path, display: display}
}

func saveClipboardImage(ext string, data []byte) (string, error) {
	ext, data = ai.NormalizeImage(ext, data)
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "pastes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	b := make([]byte, 3)
	rand.Read(b)
	name := fmt.Sprintf("%s-%s.%s", time.Now().Format("20060102-150405"), hex.EncodeToString(b), ext)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if err := root.WriteFile(name, data, 0o600); err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func pasteImageCmd() tea.Msg {
	ext, data, err := readClipboardImage()
	if err != nil {
		return imageMsg{err: err}
	}
	if data == nil {
		return imageMsg{}
	}
	path, err := saveClipboardImage(ext, data)
	if err != nil {
		return imageMsg{err: err}
	}

	return imageMsg{path: path}
}

type pastedImage struct {
	n       int
	path    string
	display string
}

const chipSentinel = "\u200b"

func (p pastedImage) chipText() string {
	if p.display == "" {
		return fmt.Sprintf("[Image %d%s]", p.n, chipSentinel)
	}
	display := strings.NewReplacer("[", "(", "]", ")").Replace(p.display)
	return fmt.Sprintf("[Image %d: %s%s]", p.n, truncateImageName(display, maxImageNameRunes), chipSentinel)
}

const maxImageNameRunes = 24

func truncateImageName(name string, limit int) string {
	if runewidth.StringWidth(name) <= limit {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	const ellipsis = "…"
	budget := limit - runewidth.StringWidth(ellipsis) - runewidth.StringWidth(ext)
	if budget < 1 {

		if runewidth.StringWidth(ext) <= limit {
			return ext
		}
		return ellipsis
	}

	var cut int
	width := 0
	for _, r := range stem {
		w := runewidth.RuneWidth(r)
		if width+w > budget {
			break
		}
		width += w
		cut += len(string(r))
	}
	return stem[:cut] + ellipsis + ext
}

func (m *model) expandImageChips(text string) string {
	return imageChipRe.ReplaceAllStringFunc(text, func(tok string) string {
		sm := imageChipRe.FindStringSubmatch(tok)
		if len(sm) < 2 {
			return tok
		}
		n, err := strconv.Atoi(sm[1])
		if err != nil || n < 1 || n > len(m.images) {
			return tok
		}
		return "@" + m.images[n-1].path
	})
}

var imageChipRe = regexp.MustCompile(`\[Image\s+(\d+)(?::[^\]\x{200b}]*)?\x{200b}\]`)
