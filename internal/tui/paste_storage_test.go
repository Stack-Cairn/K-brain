package tui

import (
	"bytes"
	"image"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestClipboardImageRejectsInvalidExtensions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	for _, ext := range []string{"", "../../escaped", `..\escaped`, "/tmp/a", `C:\a`, "png:stream", "png\x00", "png\n", ".png", "png ", "svg+xml", "exe"} {
		if path, err := saveClipboardImage(ext, []byte("image")); err == nil || path != "" {
			t.Errorf("extension %q accepted: %q, %v", ext, path, err)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(home, "pastes")); err == nil && len(entries) != 0 {
		t.Fatalf("invalid extensions created files: %v", entries)
	}
}

func TestClipboardImageSupportedExtensionsAndUniqueFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	seen := map[string]bool{}
	for range 3 {
		for _, ext := range []string{"png", "jpg", "jpeg", "gif", "webp", "bmp", "PNG"} {
			path, err := saveClipboardImage(ext, []byte("image"))
			if err != nil {
				t.Fatalf("%s: %v", ext, err)
			}
			if filepath.Dir(path) != filepath.Join(home, "pastes") || filepath.Ext(path) != "."+strings.ToLower(ext) || seen[path] {
				t.Fatalf("unexpected or reused image path: %q", path)
			}
			seen[path] = true
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "image" {
				t.Fatalf("saved image: %q, %v", data, err)
			}
		}
	}
}

func TestClipboardImageSelectsSupportedMIME(t *testing.T) {
	for _, tt := range []struct{ types, want string }{
		{"text/plain\nimage/svg+xml\nimage/png\n", "png"},
		{"image/../../file\nimage/jpeg\n", "jpeg"},
		{"text/plain\nimage/svg+xml\n", ""},
	} {
		ext, ok := hasImageType([]byte(tt.types))
		if ext != tt.want || ok != (tt.want != "") {
			t.Errorf("MIME selection %q = %q, %v", tt.types, ext, ok)
		}
	}
}

func TestClipboardImageNormalizationChangesExtension(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, ai.NormalizeMaxDim+10, 1))); err != nil {
		t.Fatal(err)
	}
	path, err := saveClipboardImage("PNG", data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(path) != ".jpg" {
		t.Fatalf("normalized file extension = %q", filepath.Ext(path))
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, format, err := image.Decode(bytes.NewReader(encoded))
	if err != nil || format != "jpeg" || img.Bounds().Dx() > ai.NormalizeMaxDim {
		t.Fatalf("normalized image: format=%q error=%v", format, err)
	}
}

func TestPastedImageFileURI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "截图 100% #1.png")
	if err := os.WriteFile(path, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	uriPath := filepath.ToSlash(path)
	if runtime.GOOS == "windows" {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath}
	for _, host := range []string{"", "localhost", "LOCALHOST"} {
		uri.Host = host
		if got, ok := pastedImagePath(uri.String()); !ok || got != path {
			t.Errorf("image URI %q = %q, %v; want %q", uri.String(), got, ok, path)
		}
	}
	uri.Host = "remote.example"
	if _, ok := pastedImagePath(uri.String()); ok {
		t.Fatal("remote file URI should not be treated as a local image")
	}
	uri.Host = ""
	for _, suffix := range []string{"?query", "#fragment", "%ZZ"} {
		if _, ok := pastedImagePath(uri.String() + suffix); ok {
			t.Errorf("invalid image URI was accepted: %q", suffix)
		}
	}
}
