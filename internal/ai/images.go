package ai

import (
	"bytes"
	"image"

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
