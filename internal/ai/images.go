package ai

import (
	"bytes"
	"encoding/base64"
	"image"
	"strings"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

const ImageTokenFloor = 85

const imagePatch = 28

func DecodeImageSize(data []byte) (w, h int, ok bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

func ImageTokens(w, h int) int {
	if w <= 0 || h <= 0 {
		return 1200
	}
	patchCeil := func(n int) int { return (n + imagePatch - 1) / imagePatch }
	t := patchCeil(w) * patchCeil(h)
	if t < ImageTokenFloor {
		return ImageTokenFloor
	}
	return t
}

func PartTokens(p ContentPart) int {
	if p.Type == "image_url" {
		if p.W > 0 {
			return ImageTokens(p.W, p.H)
		}

		if w, h, ok := p.DecodeDimensions(); ok {
			return ImageTokens(w, h)
		}
		return ImageTokens(0, 0)
	}
	return (len(p.Text) + 3) / 4
}

func imageDataURL(ext string, data []byte) string {
	mime := "image/" + ext
	if ext == "jpg" {
		mime = "image/jpeg"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func ImagePart(ext string, data []byte) ContentPart {
	p := ContentPart{Type: "image_url"}
	p.ImageURL = &struct {
		URL string `json:"url"`
	}{URL: imageDataURL(ext, data)}
	p.W, p.H, _ = DecodeImageSize(data)
	return p
}

func (p ContentPart) DecodeDimensions() (w, h int, ok bool) {
	if p.ImageURL == nil {
		return 0, 0, false
	}
	const prefix = ";base64,"
	i := strings.Index(p.ImageURL.URL, prefix)
	if i < 0 {
		return 0, 0, false
	}

	b64 := p.ImageURL.URL[i+len(prefix):]
	if len(b64) > 65536 {
		b64 = b64[:65536]
	}
	head, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return 0, 0, false
	}
	return DecodeImageSize(head)
}
