package imaging

import (
	"strconv"
	"strings"
	"testing"
)

func mustPlatform(t *testing.T, id string) Platform {
	t.Helper()
	p, ok := FindPlatform(id)
	if !ok {
		t.Fatalf("platform %s missing", id)
	}
	return p
}

func fieldFixed(fixes []ParamCorrection, field string) (ParamCorrection, bool) {
	for _, f := range fixes {
		if f.Field == field {
			return f, true
		}
	}
	return ParamCorrection{}, false
}

func TestNormalizeParamsKeepsSupportedOpenAIValues(t *testing.T) {
	compression := 80
	in := GenerationParams{Size: "1536x1024", Quality: "high", OutputFormat: "webp", OutputCompression: &compression, Moderation: "low", N: 3, TransparentOutput: true}
	out, fixes := NormalizeParams(mustPlatform(t, PlatformOpenAI), in)
	if len(fixes) != 0 {
		t.Fatalf("unexpected corrections: %+v", fixes)
	}
	if out.Size != "1536x1024" || out.Quality != "high" || out.OutputFormat != "webp" || *out.OutputCompression != 80 || out.Moderation != "low" || out.N != 3 || !out.TransparentOutput {
		t.Fatalf("values changed: %+v", out)
	}
}

func TestNormalizeParamsCorrectsUnsupportedFieldsPerPlatform(t *testing.T) {
	compression := 70
	in := GenerationParams{Size: "auto", Quality: "high", OutputFormat: "webp", OutputCompression: &compression, Moderation: "low", N: 4, TransparentOutput: true}

	ark, fixes := NormalizeParams(mustPlatform(t, PlatformVolcengine), in)
	for _, field := range []string{"quality", "outputCompression", "moderation", "transparentOutput", "n"} {
		if _, ok := fieldFixed(fixes, field); !ok {
			t.Fatalf("volcengine should correct %s; fixes=%+v", field, fixes)
		}
	}
	if ark.Quality != "auto" || ark.OutputCompression != nil || ark.Moderation != "" || ark.TransparentOutput || ark.N != 1 {
		t.Fatalf("volcengine output = %+v", ark)
	}
	// Ark supports choosing an output format; webp is valid and kept.
	if ark.OutputFormat != "webp" {
		t.Fatalf("volcengine format = %q", ark.OutputFormat)
	}
	if f, _ := fieldFixed(fixes, "quality"); !strings.Contains(f.Reason, "火山方舟") {
		t.Fatalf("reason should name the platform: %q", f.Reason)
	}

	google, fixes := NormalizeParams(mustPlatform(t, PlatformGoogle), in)
	if google.OutputFormat != "" {
		t.Fatalf("google must not send an output format, got %q", google.OutputFormat)
	}
	if _, ok := fieldFixed(fixes, "outputFormat"); !ok {
		t.Fatal("google format correction should be explained")
	}
}

func TestNormalizeParamsCompressionOnlyForLossyFormats(t *testing.T) {
	compression := 50
	out, fixes := NormalizeParams(mustPlatform(t, PlatformOpenAI), GenerationParams{Size: "auto", OutputFormat: "png", OutputCompression: &compression, N: 1})
	if out.OutputCompression != nil {
		t.Fatal("png must drop compression")
	}
	if f, ok := fieldFixed(fixes, "outputCompression"); !ok || f.To != "" {
		t.Fatalf("compression correction = %+v", f)
	}
	over := 150
	out, _ = NormalizeParams(mustPlatform(t, PlatformOpenAI), GenerationParams{Size: "auto", OutputFormat: "jpeg", OutputCompression: &over, N: 1})
	if out.OutputCompression == nil || *out.OutputCompression != 100 {
		t.Fatalf("compression should clamp to 100, got %v", out.OutputCompression)
	}
}

func TestNormalizeParamsClampsCountToPlatformLimit(t *testing.T) {
	out, fixes := NormalizeParams(mustPlatform(t, PlatformRollDek), GenerationParams{Size: "auto", N: 9})
	if out.N != 4 {
		t.Fatalf("rolldek n = %d, want 4", out.N)
	}
	if f, ok := fieldFixed(fixes, "n"); !ok || f.From != "9" || f.To != "4" {
		t.Fatalf("count correction = %+v", f)
	}
	out, _ = NormalizeParams(mustPlatform(t, PlatformOpenAI), GenerationParams{Size: "auto", N: 0})
	if out.N != 1 {
		t.Fatalf("n should default to 1, got %d", out.N)
	}
}

func dims(t *testing.T, size string) (int, int) {
	t.Helper()
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		t.Fatalf("bad size %q", size)
	}
	w, _ := strconv.Atoi(parts[0])
	h, _ := strconv.Atoi(parts[1])
	return w, h
}

func assertSafe(t *testing.T, size string) {
	t.Helper()
	w, h := dims(t, size)
	if w%SizeMultiple != 0 || h%SizeMultiple != 0 {
		t.Fatalf("%s is not a multiple of 16", size)
	}
	if w > MaxEdge || h > MaxEdge {
		t.Fatalf("%s exceeds the max edge", size)
	}
	ratio := float64(w) / float64(h)
	if ratio > MaxAspectRatio+1e-9 || 1/ratio > MaxAspectRatio+1e-9 {
		t.Fatalf("%s exceeds the aspect ratio limit", size)
	}
	if px := w * h; px < MinPixels || px > MaxPixels {
		t.Fatalf("%s has %d pixels, outside [%d, %d]", size, px, MinPixels, MaxPixels)
	}
}

func TestNormalizeSizeEnvelope(t *testing.T) {
	cases := map[string]string{
		"1000x1000": "",          // rounds to multiples of 16 and lifts to the pixel floor
		"8000x2000": "",          // too wide and too large
		"500x3000":  "",          // too tall
		"5000x5000": "",          // too many pixels
		"1024x1024": "1024x1024", // already safe, unchanged
	}
	for in, want := range cases {
		got, changed, reason := NormalizeSize(in)
		assertSafe(t, got)
		if want != "" {
			if got != want || changed {
				t.Fatalf("%s -> %s (changed=%v), want unchanged %s", in, got, changed, want)
			}
			continue
		}
		if !changed || reason == "" {
			t.Fatalf("%s should be corrected with a reason, got %s %q", in, got, reason)
		}
	}
	if got, changed, _ := NormalizeSize("auto"); got != "auto" || changed {
		t.Fatalf("auto must pass through, got %s", got)
	}
}

func TestPresetSizesAreAllSafe(t *testing.T) {
	for tier, ratios := range PresetSizes {
		if len(ratios) != 8 {
			t.Fatalf("%s has %d ratios, want 8", tier, len(ratios))
		}
		for ratio, size := range ratios {
			got, changed, _ := NormalizeSize(size)
			if changed {
				t.Fatalf("preset %s %s = %s is not already safe (normalized to %s)", tier, ratio, size, got)
			}
		}
	}
}
