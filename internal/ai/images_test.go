package ai

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"
)

func pngFixture(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return buf.Bytes()
}

func TestImageTokensPixelFormula(t *testing.T) {

	if got := ImageTokens(3410, 2646); got != 122*95 {
		t.Fatalf("3410×2646: got %d, want %d", got, 122*95)
	}
	if got := ImageTokens(2556, 2734); got != 92*98 {
		t.Fatalf("2556×2734: got %d, want %d", got, 92*98)
	}

	if got := ImageTokens(0, 0); got != 1200 {
		t.Fatalf("unknown dims: got %d, want 1200 fallback", got)
	}

	if got := ImageTokens(8, 8); got != ImageTokenFloor {
		t.Fatalf("8×8: got %d, want floor %d", got, ImageTokenFloor)
	}
}

func TestDecodeImageSize(t *testing.T) {
	png := pngFixture(t, 640, 480)
	w, h, ok := DecodeImageSize(png)
	if !ok || w != 640 || h != 480 {
		t.Fatalf("png: got %d×%d ok=%v", w, h, ok)
	}
	if _, _, ok := DecodeImageSize([]byte("not an image")); ok {
		t.Fatal("garbage must not decode")
	}
}

func TestImagePartRecordsDimensions(t *testing.T) {
	png := pngFixture(t, 560, 420)
	p := ImagePart("png", png)
	if p.W != 560 || p.H != 420 {
		t.Fatalf("dims: got %d×%d", p.W, p.H)
	}
	if got := PartTokens(p); got != 300 {
		t.Fatalf("PartTokens: got %d, want 300", got)
	}

	tp := ContentPart{Type: "text", Text: strings.Repeat("a", 100)}
	if got := PartTokens(tp); got != 25 {
		t.Fatalf("text PartTokens: got %d, want 25", got)
	}
}

func TestContentPartDimsPersistedAndStripped(t *testing.T) {
	p := ImagePart("png", pngFixture(t, 280, 140))
	m := Message{Role: "user", Content: "look", Parts: []ContentPart{p}}

	raw, err := m.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"w":280`)) || !bytes.Contains(raw, []byte(`"h":140`)) {
		t.Fatalf("persisted form should carry dims: %s", raw)
	}

	out := stripAuthored([]Message{m})
	for _, part := range out[0].Parts {
		if part.W != 0 || part.H != 0 {
			t.Fatalf("wire form must strip dims, got %d×%d", part.W, part.H)
		}
	}

	if m.Parts[0].W != 280 {
		t.Fatal("stripAuthored must copy, not mutate the caller's slice")
	}
}

func TestPartTokensLazyDecode(t *testing.T) {
	p := ImagePart("png", pngFixture(t, 280, 140))
	p.W, p.H = 0, 0
	if got := PartTokens(p); got != ImageTokens(280, 140) {
		t.Fatalf("lazy decode: got %d, want %d", got, ImageTokens(280, 140))
	}
}
