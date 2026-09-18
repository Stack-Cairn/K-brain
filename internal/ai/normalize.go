package ai

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"

	"golang.org/x/image/draw"
)

const (
	NormalizeMaxDim = 2000

	NormalizeMaxBytes = 5 * 1024 * 1024

	NormalizeMaxPixels = 64_000_000

	normalizeMinScale = 0.05
)

var jpegQualities = []int{80, 70, 55, 40}

func NormalizeImage(ext string, data []byte) (string, []byte) {

	w, h, ok := DecodeImageSize(data)
	if !ok {
		return ext, data
	}
	if len(data) <= NormalizeMaxBytes && w <= NormalizeMaxDim && h <= NormalizeMaxDim {
		return ext, data
	}
	if w*h > NormalizeMaxPixels {

		return ext, data
	}
	src, err := decodeAny(data)
	if err != nil {
		return ext, data
	}

	img := scaleToFit(src, NormalizeMaxDim, NormalizeMaxDim)
	for range 32 {
		for _, q := range jpegQualities {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
				return ext, data
			}
			if buf.Len() <= NormalizeMaxBytes {
				return "jpg", buf.Bytes()
			}
		}

		nw, nh := img.Bounds().Dx()*3/4, img.Bounds().Dy()*3/4
		if nw < int(float64(NormalizeMaxDim)*normalizeMinScale) || nh < 8 || nw < 8 {
			break
		}
		img = scaleToFit(img, nw, nh)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQualities[len(jpegQualities)-1]}); err != nil {
		return ext, data
	}
	return "jpg", buf.Bytes()
}

func decodeAny(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

func scaleToFit(src image.Image, maxW, maxH int) image.Image {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	nw, nh := w, h
	if w > maxW || h > maxH {
		scale := min(float64(maxW)/float64(w), float64(maxH)/float64(h))
		nw, nh = max(int(float64(w)*scale), 1), max(int(float64(h)*scale), 1)
	}

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))

	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}
