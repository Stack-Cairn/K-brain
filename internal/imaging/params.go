package imaging

import "math"

// Unified generation parameters. One shape for every platform; the adapter
// translates it and may correct unsupported fields before submission.

const (
	SizeAuto = "auto"

	MaxEdge        = 3840
	SizeMultiple   = 16
	MaxAspectRatio = 3.0
	MinPixels      = 655360
	MaxPixels      = 8294400
	Max1KPixels    = 1572864
	Max2KPixels    = 4194304
)

// GenerationParams is what the workbench submits.
type GenerationParams struct {
	Size              string `json:"size"`
	Quality           string `json:"quality,omitempty"`
	OutputFormat      string `json:"outputFormat,omitempty"`
	OutputCompression *int   `json:"outputCompression,omitempty"`
	Moderation        string `json:"moderation,omitempty"`
	N                 int    `json:"n,omitempty"`
	TransparentOutput bool   `json:"transparentOutput,omitempty"`
}

// ParamCorrection explains one value the backend had to change, so the card can
// show what was actually used.
type ParamCorrection struct {
	Field  string `json:"field"`
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

var validFormats = map[string]bool{"png": true, "jpeg": true, "webp": true}
var validQualities = map[string]bool{"auto": true, "low": true, "medium": true, "high": true}

// DefaultParams are the values the workbench opens with.
func DefaultParams() GenerationParams {
	return GenerationParams{Size: SizeAuto, Quality: "auto", OutputFormat: "png", N: 1}
}

// NormalizeParams corrects a submission for one platform and reports what changed.
// It never rejects a value that can be corrected, and never invents a value for a
// field the platform does not support (that field is dropped instead).
func NormalizeParams(p Platform, in GenerationParams) (GenerationParams, []ParamCorrection) {
	out := in
	var fixes []ParamCorrection
	add := func(field, from, to, reason string) {
		fixes = append(fixes, ParamCorrection{Field: field, From: from, To: to, Reason: reason})
	}

	if out.N < 1 {
		add("n", itoa(out.N), "1", "数量至少为 1")
		out.N = 1
	}
	if limit := p.MaxOutputImages; limit > 0 && out.N > limit {
		add("n", itoa(out.N), itoa(limit), p.Name+" 单次最多 "+itoa(limit)+" 张")
		out.N = limit
	}

	if p.SupportsOutputFormat {
		if out.OutputFormat == "" {
			out.OutputFormat = "png"
		}
		if !validFormats[out.OutputFormat] {
			add("outputFormat", out.OutputFormat, "png", "不支持的输出格式")
			out.OutputFormat = "png"
		}
	} else if out.OutputFormat != "" {
		add("outputFormat", out.OutputFormat, "", p.Name+" 不支持选择输出格式")
		out.OutputFormat = ""
	}

	if p.SupportsQuality {
		if out.Quality == "" {
			out.Quality = "auto"
		}
		if !validQualities[out.Quality] {
			add("quality", out.Quality, "auto", "不支持的质量档位")
			out.Quality = "auto"
		}
	} else if out.Quality != "" && out.Quality != "auto" {
		add("quality", out.Quality, "auto", p.Name+" 不支持质量参数")
		out.Quality = "auto"
	}

	if p.SupportsCompression {
		if out.OutputCompression != nil {
			v := *out.OutputCompression
			if v < 0 || v > 100 {
				clamped := 0
				if v > 100 {
					clamped = 100
				}
				add("outputCompression", itoa(v), itoa(clamped), "压缩率范围为 0-100")
				out.OutputCompression = &clamped
			}
			// Compression only applies to jpeg and webp.
			if out.OutputFormat == "png" {
				add("outputCompression", itoa(*out.OutputCompression), "", "PNG 不使用压缩率")
				out.OutputCompression = nil
			}
		}
	} else if out.OutputCompression != nil {
		add("outputCompression", itoa(*out.OutputCompression), "", p.Name+" 不支持压缩率")
		out.OutputCompression = nil
	}

	if !p.SupportsModeration {
		if out.Moderation != "" && out.Moderation != "auto" {
			add("moderation", out.Moderation, "auto", p.Name+" 不支持审核参数")
		}
		out.Moderation = ""
	} else if out.Moderation == "" {
		out.Moderation = "auto"
	}

	if !p.SupportsTransparentBackground {
		if out.TransparentOutput {
			add("transparentOutput", "true", "false", p.Name+" 不支持透明背景")
		}
		out.TransparentOutput = false
	}

	if out.Size == "" {
		out.Size = SizeAuto
	}
	if out.Size != SizeAuto {
		normalized, changed, reason := NormalizeSize(out.Size)
		if changed {
			add("size", out.Size, normalized, reason)
		}
		out.Size = normalized
	}
	return out, fixes
}

// NormalizeSize rounds a WxH size into the platform-safe envelope: multiples of
// 16, longest edge at most 3840, aspect ratio at most 3, and total pixels between
// MinPixels and MaxPixels. Values that are not WxH pass through unchanged.
func NormalizeSize(size string) (string, bool, string) {
	w, h, ok := parseSize(size)
	if !ok {
		return size, false, ""
	}
	width := roundToMultiple(w, SizeMultiple)
	height := roundToMultiple(h, SizeMultiple)
	reason := ""
	for i := 0; i < 4; i++ {
		before := [2]int{width, height}
		if maxEdge := maxInt(width, height); maxEdge > MaxEdge {
			scale := float64(MaxEdge) / float64(maxEdge)
			width = floorToMultiple(float64(width)*scale, SizeMultiple)
			height = floorToMultiple(float64(height)*scale, SizeMultiple)
			reason = "边长超过 3840，已缩放"
		}
		if float64(width)/float64(height) > MaxAspectRatio {
			width = floorToMultiple(float64(height)*MaxAspectRatio, SizeMultiple)
			reason = "宽高比超过 3:1，已收敛"
		} else if float64(height)/float64(width) > MaxAspectRatio {
			height = floorToMultiple(float64(width)*MaxAspectRatio, SizeMultiple)
			reason = "宽高比超过 3:1，已收敛"
		}
		pixels := width * height
		if pixels > MaxPixels {
			scale := math.Sqrt(float64(MaxPixels) / float64(pixels))
			width = floorToMultiple(float64(width)*scale, SizeMultiple)
			height = floorToMultiple(float64(height)*scale, SizeMultiple)
			reason = "总像素超过上限，已缩放"
		} else if pixels < MinPixels {
			scale := math.Sqrt(float64(MinPixels) / float64(pixels))
			width = ceilToMultiple(float64(width)*scale, SizeMultiple)
			height = ceilToMultiple(float64(height)*scale, SizeMultiple)
			reason = "总像素低于下限，已放大"
		}
		if before == [2]int{width, height} {
			break
		}
	}
	out := itoa(width) + "x" + itoa(height)
	if out == size {
		return size, false, ""
	}
	if reason == "" {
		reason = "已规整为 16 的倍数"
	}
	return out, true, reason
}

func roundToMultiple(v float64, m int) int {
	r := int(math.Round(v/float64(m))) * m
	if r < m {
		return m
	}
	return r
}
func floorToMultiple(v float64, m int) int {
	r := int(math.Floor(v/float64(m))) * m
	if r < m {
		return m
	}
	return r
}
func ceilToMultiple(v float64, m int) int {
	r := int(math.Ceil(v/float64(m))) * m
	if r < m {
		return m
	}
	return r
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// PresetSizes holds the ready-made values offered by the size picker. 21:9 keeps
// the display-standard size with a small multiple-of-16 adjustment.
var PresetSizes = map[string]map[string]string{
	"1K": {
		"1:1": "1024x1024", "3:2": "1536x1024", "2:3": "1024x1536", "16:9": "1280x720",
		"9:16": "720x1280", "4:3": "1024x768", "3:4": "768x1024", "21:9": "1280x544",
	},
	"2K": {
		"1:1": "2048x2048", "3:2": "2160x1440", "2:3": "1440x2160", "16:9": "2560x1440",
		"9:16": "1440x2560", "4:3": "2048x1536", "3:4": "1536x2048", "21:9": "2560x1088",
	},
	"4K": {
		"1:1": "2880x2880", "3:2": "3520x2352", "2:3": "2352x3520", "16:9": "3840x2160",
		"9:16": "2160x3840", "4:3": "3200x2400", "3:4": "2400x3200", "21:9": "3840x1648",
	},
}
