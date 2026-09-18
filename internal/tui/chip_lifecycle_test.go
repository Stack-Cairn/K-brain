package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSteerExpandsImageChips(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	m.busy = true

	dir := t.TempDir()
	img := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(img, []byte("\x89PNG\r\n\x1a\nfake"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.imageSeq++
	p := pastedImage{n: 1, path: img, display: "shot.png"}
	m.images = append(m.images, p)

	steerText := m.expandImageChips("check " + p.chipText() + " please")
	if !strings.Contains(steerText, "@"+img) {
		t.Errorf("steer text did not expand the chip: %q", steerText)
	}
}

func TestClearResetsImageRegistry(t *testing.T) {
	m := compactCmdModel()
	m.busy = false

	m.imageSeq = 1
	m.images = append(m.images, pastedImage{n: 1, path: "/tmp/shot.png", display: "shot.png"})

	m.command("/clear")

	if len(m.images) != 0 || m.imageSeq != 0 {
		t.Errorf("/clear left image registry: images=%d imageSeq=%d", len(m.images), m.imageSeq)
	}
}

func TestRecalledChipAfterClearStaysLiteral(t *testing.T) {
	m := compactCmdModel()
	m.busy = false
	m.command("/clear")
	if got := m.expandImageChips("[Image 1]"); got != "[Image 1]" {
		t.Errorf("recalled chip after /clear resolved: %q", got)
	}
}
