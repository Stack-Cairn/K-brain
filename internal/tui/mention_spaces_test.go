package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImagePartsPathWithSpaces(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "Screenshot 2026-09-04 at 3.21.45 PM.png")
	if err := os.WriteFile(img, []byte("\x89PNG\r\n\x1a\nfake"), 0o644); err != nil {
		t.Fatal(err)
	}
	parts, _ := imageParts("look at @" + img + " please")
	if len(parts) != 1 {
		t.Fatalf("imageParts with a space-containing path = %d parts, want 1", len(parts))
	}
}

func TestPastedImagePathBackslashEscaped(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "Screenshot 2026-09-04 at 3.21.45 PM.png")
	if err := os.WriteFile(img, []byte("\x89PNG\r\n\x1a\nfake"), 0o644); err != nil {
		t.Fatal(err)
	}
	escaped := strings.ReplaceAll(img, " ", `\ `)
	if got, ok := pastedImagePath(escaped); !ok || got != img {
		t.Errorf("pastedImagePath(%q) = (%q, %v), want (%q, true)", escaped, got, ok, img)
	}
}
