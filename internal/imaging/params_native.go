package imaging

import (
	"math"
	"strconv"
	"strings"
)

// NativeParameters translates normalized unified parameters into the keys each
// built-in adapter reads. The adapters keep validating their own keys, so a value
// that slips through here is still rejected before any request is sent.
func NativeParameters(platform string, p GenerationParams) map[string]any {
	out := map[string]any{}
	switch platform {
	case PlatformOpenAI:
		if p.Size != "" && p.Size != SizeAuto {
			out["size"] = p.Size
		}
		if p.Quality != "" {
			out["quality"] = p.Quality
		}
		if p.OutputFormat != "" {
			out["output_format"] = p.OutputFormat
		}
		if p.OutputCompression != nil {
			out["output_compression"] = *p.OutputCompression
		}
		if p.Moderation != "" {
			out["moderation"] = p.Moderation
		}
		if p.TransparentOutput {
			out["background"] = "transparent"
		}
	case PlatformRollDek:
		// The only RollDek request with a live receipt is the parameterless one.
		// Size and format are sent when chosen; other fields stay off.
		if p.Size != "" && p.Size != SizeAuto {
			out["size"] = p.Size
		}
		if p.OutputFormat != "" && p.OutputFormat != "png" {
			out["output_format"] = p.OutputFormat
		}
	case PlatformVolcengine:
		if p.Size != "" && p.Size != SizeAuto {
			out["size"] = arkSizeFor(p.Size)
		}
		if p.OutputFormat == "jpeg" {
			out["output_format"] = "jpeg"
		}
	case PlatformGoogle:
		if ratio, tier, ok := googleImageConfig(p.Size); ok {
			out["aspect_ratio"] = ratio
			out["image_size"] = tier
		}
	}
	return out
}

// arkSizeFor keeps Seedream inside its pixel band [921600, 4624220]; a WxH that
// is already inside is sent unchanged.
func arkSizeFor(size string) string {
	w, h, ok := parseSize(size)
	if !ok {
		return size
	}
	px := w * h
	const lo, hi = 921600.0, 4624220.0
	if px >= lo && px <= hi {
		return size
	}
	scale := math.Sqrt(math.Max(lo, math.Min(hi, px)) / px)
	nw := floorToMultiple(w*scale, SizeMultiple)
	nh := floorToMultiple(h*scale, SizeMultiple)
	for float64(nw*nh) < lo {
		nw += SizeMultiple
		nh = int(math.Round(float64(nw)*h/w/float64(SizeMultiple))) * SizeMultiple
	}
	return strconv.Itoa(nw) + "x" + strconv.Itoa(nh)
}

// Gemini takes a ratio and a 1K/2K/4K tier rather than pixels.
var googleRatios = []string{"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"}

func googleImageConfig(size string) (string, string, bool) {
	w, h, ok := parseSize(size)
	if !ok {
		return "", "", false
	}
	target := w / h
	best, bestDelta := "1:1", math.MaxFloat64
	for _, r := range googleRatios {
		parts := strings.Split(r, ":")
		a, _ := strconv.ParseFloat(parts[0], 64)
		b, _ := strconv.ParseFloat(parts[1], 64)
		if d := math.Abs(math.Log(target) - math.Log(a/b)); d < bestDelta {
			best, bestDelta = r, d
		}
	}
	px := w * h
	tier := "1K"
	switch {
	case px > Max2KPixels:
		tier = "4K"
	case px > Max1KPixels:
		tier = "2K"
	}
	return best, tier, true
}
