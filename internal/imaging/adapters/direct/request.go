package direct

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

type openAIEditRequest struct {
	Fields openAIRequest
	Images []imaging.ImageContent
	Mask   *imaging.ImageContent
}
type openAIRequest struct {
	ResponseFormat string `json:"response_format,omitempty"`
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n"`
	Size           string `json:"size,omitempty"`
	Quality        string `json:"quality,omitempty"`
	Format         string `json:"output_format,omitempty"`
	Compression    *int   `json:"output_compression,omitempty"`
	Background     string `json:"background,omitempty"`
}

// Compatible providers may follow prompt framing over size; reinforce an explicit canvas on the wire.
func openAIProviderCanvasPrompt(prompt, size string) string {
	w, h, _ := strings.Cut(size, "x")
	width, _ := strconv.Atoi(w)
	height, _ := strconv.Atoi(h)
	orientation := "square"
	if width < height {
		orientation = "portrait"
	} else if width > height {
		orientation = "landscape"
	}
	divisor := gcd(width, height)
	return prompt + fmt.Sprintf("\n\nCanvas requirement from the image settings: %s orientation, aspect ratio %d:%d, width %d pixels and height %d pixels. This canvas requirement takes precedence over any conflicting aspect ratio or horizontal/vertical instruction above. Preserve the subject and visual details.", orientation, width/divisor, height/divisor, width, height)
}

// Agnes takes inputs and the response format under extra_body, never top level.
type agnesRequest struct {
	Model        string `json:"model"`
	Prompt       string `json:"prompt"`
	Size         string `json:"size"`
	Ratio        string `json:"ratio,omitempty"`
	ReturnBase64 bool   `json:"return_base64,omitempty"`
	ExtraBody    *struct {
		Image          []string `json:"image,omitempty"`
		ResponseFormat string   `json:"response_format"`
	} `json:"extra_body,omitempty"`
}
type inlineData struct {
	MIME string `json:"mimeType"`
	Data string `json:"data"`
}
type googlePart struct {
	Text    string      `json:"text,omitempty"`
	Inline  *inlineData `json:"inlineData,omitempty"`
	Thought bool        `json:"thought,omitempty"`
}
type googleContent struct {
	Role  string       `json:"role"`
	Parts []googlePart `json:"parts"`
}
type googleImageConfig struct {
	AspectRatio string `json:"aspectRatio,omitempty"`
	ImageSize   string `json:"imageSize,omitempty"`
}
type googleRequest struct {
	Contents []googleContent `json:"contents"`
	Config   struct {
		Modalities  []string           `json:"responseModalities"`
		ImageConfig *googleImageConfig `json:"imageConfig,omitempty"`
	} `json:"generationConfig"`
}
type arkRequest struct {
	Model          string   `json:"model"`
	Prompt         string   `json:"prompt"`
	Images         []string `json:"image,omitempty"`
	Size           string   `json:"size,omitempty"`
	ResponseFormat string   `json:"response_format"`
	Format         string   `json:"output_format,omitempty"`
	Watermark      *bool    `json:"watermark,omitempty"`
	Optimize       *struct {
		Mode string `json:"mode"`
	} `json:"optimize_prompt_options,omitempty"`
}

func badInput() error    { return failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent) }
func unsupported() error { return failure(imaging.CodeUnsupportedOperation, imaging.DeliveryNotSent) }

func (a *Adapter) request(ctx context.Context, r imaging.ExecuteRequest) (string, any, error) {
	if r.Request.Count != 1 || !utf8.ValidString(r.Request.Prompt) || strings.TrimSpace(r.Request.Prompt) == "" || utf8.RuneCountInString(r.Request.Prompt) > 32000 || len(r.Continuation) != 0 {
		return "", nil, badInput()
	}
	if (r.Target.Adapter != a.id && !(a.profile == "rolldek" && r.Target.Adapter == "rolldek-images")) || r.Target.AdapterVersion != Version || r.Target.Kind != "model" || r.Target.WorkflowMapping != nil {
		return "", nil, unsupported()
	}
	switch a.id {
	case OpenAIID:
		if ImageModelAdapter(r.Target.RemoteID) != OpenAIID && !(r.Target.Source == "provider" && ValidImageModelID(r.Target.RemoteID)) && !(a.profile == "rolldek" && r.Target.RemoteID == "gpt-image-2.5") {
			return "", nil, unsupported()
		}
	case GoogleID:
		if ImageModelAdapter(r.Target.RemoteID) != GoogleID && !(r.Target.Source == "provider" && ValidImageModelID(r.Target.RemoteID)) {
			return "", nil, unsupported()
		}
	case VolcengineID:
		// Endpoint IDs are allowed only when the registry has independently bound
		// that endpoint to this exact Seedream 5.0 pro profile.
		if r.Target.RemoteID != "doubao-seedream-5-0-pro-260628" && !endpointID(r.Target.RemoteID) {
			return "", nil, unsupported()
		}
	}
	inputs, mask, err := a.inputs(ctx, r)
	if err != nil {
		return "", nil, err
	}
	switch a.id {
	case OpenAIID:
		if a.profile == "agnes" {
			return a.agnesRequest(r, inputs, mask)
		}
		b := openAIRequest{Model: r.Target.RemoteID, Prompt: r.Request.Prompt, N: 1}
		if a.profile == "rolldek" {
			if r.Request.Operation == imaging.OperationMask || len(r.Request.Parameters) != 0 {
				return "", nil, unsupported()
			}
			// Only the JSON generations body carries response_format; edit results
			// may arrive as URLs, which the rolldek response path already accepts.
			b.ResponseFormat = "b64_json"
		}
		flexible := openAIFlexibleModel.MatchString(r.Target.RemoteID)
		var ratio, tier string
		for k, v := range r.Request.Parameters {
			switch {
			case k == "size" && flexible:
				b.Size, err = openAIPixelSize(v)
			case k == "size":
				b.Size, err = enum(v, "1024x1024", "1536x1024", "1024x1536")
			case k == "aspectRatio" && flexible:
				ratio, err = enum(v, openAIAspectRatios...)
			case k == "imageSize" && flexible:
				tier, err = enum(v, openAIImageSizes...)
			case k == "quality":
				b.Quality, err = enum(v, openAIQualities(r.Target.RemoteID)...)
			case k == "output_format":
				b.Format, err = enum(v, "png", "jpeg", "webp")
			case k == "background":
				b.Background, err = enum(v, "opaque", "transparent", "auto")
			case k == "output_compression":
				b.Compression, err = integer(v, 0, 100)
			default:
				err = badInput()
			}
			if err != nil {
				return "", nil, err
			}
		}
		if ratio != "" || tier != "" {
			if b.Size != "" {
				return "", nil, badInput()
			}
			// A ratio alone uses the 1K budget; a tier alone is square.
			b.Size = openAISize(cmp.Or(ratio, "1:1"), cmp.Or(tier, "1K"))
		}
		if b.Compression != nil && b.Format != "jpeg" && b.Format != "webp" {
			return "", nil, badInput()
		}
		if b.Background == "transparent" && b.Format != "png" && b.Format != "webp" {
			return "", nil, badInput()
		}
		if r.Target.Source == "provider" && !a.officialOpenAI && a.profile == "" && b.Size != "" {
			b.Prompt = openAIProviderCanvasPrompt(b.Prompt, b.Size)
			if utf8.RuneCountInString(b.Prompt) > 32000 {
				return "", nil, badInput()
			}
		}
		if len(inputs) > 0 {
			return "/images/edits", openAIEditRequest{Fields: b, Images: inputs, Mask: mask}, nil
		}
		return "/images/generations", b, nil
	case GoogleID:
		imageConfig := &googleImageConfig{}
		for key, value := range r.Request.Parameters {
			switch key {
			case "aspectRatio":
				imageConfig.AspectRatio, err = enum(value, googleAspectRatios...)
			case "imageSize":
				imageConfig.ImageSize, err = enum(value, googleImageSizes(r.Target.RemoteID)...)
			default:
				err = badInput()
			}
			if err != nil {
				return "", nil, err
			}
		}
		parts := []googlePart{{Text: r.Request.Prompt}}
		for _, in := range inputs {
			parts = append(parts, googlePart{Inline: &inlineData{MIME: in.MIME, Data: base64.StdEncoding.EncodeToString(in.Data)}})
		}
		b := googleRequest{Contents: []googleContent{{Role: "user", Parts: parts}}}
		b.Config.Modalities = []string{"TEXT", "IMAGE"}
		if len(r.Request.Parameters) > 0 {
			b.Config.ImageConfig = imageConfig
		}
		return "/models/" + r.Target.RemoteID + ":generateContent", b, nil
	case VolcengineID:
		// Ark defaults to JPEG. Freeze the local default to PNG so the
		// minimal verification profile receives the format it certifies.
		b := arkRequest{Model: r.Target.RemoteID, Prompt: r.Request.Prompt, ResponseFormat: "b64_json", Format: "png"}
		for k, v := range r.Request.Parameters {
			switch k {
			case "size":
				b.Size, _ = v.(string)
				if !arkSize(b.Size) {
					err = badInput()
				}
			case "response_format":
				b.ResponseFormat, err = enum(v, "b64_json", "url")
			case "output_format":
				b.Format, err = enum(v, "png", "jpeg")
			case "watermark":
				n, ok := v.(bool)
				if !ok {
					err = badInput()
				} else {
					b.Watermark = &n
				}
			case "optimize_prompt_options.mode":
				var mode string
				mode, err = enum(v, "standard", "fast")
				b.Optimize = &struct {
					Mode string `json:"mode"`
				}{Mode: mode}
			default:
				err = badInput()
			}
			if err != nil {
				return "", nil, err
			}
		}
		for _, in := range inputs {
			b.Images = append(b.Images, dataURL(in))
		}
		return "/images/generations", b, nil
	}
	return "", nil, unsupported()
}

func (a *Adapter) agnesRequest(r imaging.ExecuteRequest, inputs []imaging.ImageContent, mask *imaging.ImageContent) (string, any, error) {
	if mask != nil {
		return "", nil, unsupported()
	}
	b := agnesRequest{Model: r.Target.RemoteID, Prompt: r.Request.Prompt, Size: "1K"}
	var err error
	for k, v := range r.Request.Parameters {
		switch k {
		case "imageSize":
			b.Size, err = enum(v, agnesSizes...)
		case "aspectRatio":
			b.Ratio, err = enum(v, agnesRatios...)
		default:
			err = badInput()
		}
		if err != nil {
			return "", nil, err
		}
	}
	// Base64 keeps results local: text-to-image uses return_base64, image inputs
	// need extra_body.response_format instead.
	if len(inputs) == 0 {
		b.ReturnBase64 = true
		return "/images/generations", b, nil
	}
	b.ExtraBody = &struct {
		Image          []string `json:"image,omitempty"`
		ResponseFormat string   `json:"response_format"`
	}{ResponseFormat: "b64_json"}
	for _, in := range inputs {
		b.ExtraBody.Image = append(b.ExtraBody.Image, dataURL(in))
	}
	return "/images/generations", b, nil
}

func endpointID(s string) bool {
	if !strings.HasPrefix(s, "ep-") || len(s) <= 3 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
func enum(v any, allowed ...string) (string, error) {
	s, ok := v.(string)
	if ok {
		for _, a := range allowed {
			if s == a {
				return s, nil
			}
		}
	}
	return "", badInput()
}
func integer(v any, low, high int) (*int, error) {
	var f float64
	switch n := v.(type) {
	case int:
		f = float64(n)
	case int64:
		f = float64(n)
	case float64:
		f = n
	case json.Number:
		var err error
		f, err = n.Float64()
		if err != nil {
			return nil, badInput()
		}
	default:
		return nil, badInput()
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f < float64(low) || f > float64(high) || math.Trunc(f) != f {
		return nil, badInput()
	}
	n := int(f)
	return &n, nil
}
func arkSize(s string) bool {
	if s == "1K" || s == "1.5K" || s == "2K" {
		return true
	}
	parts := strings.Split(s, "x")
	if len(parts) != 2 {
		return false
	}
	w, e1 := strconv.ParseInt(parts[0], 10, 32)
	h, e2 := strconv.ParseInt(parts[1], 10, 32)
	if e1 != nil || e2 != nil || w < 1 || h < 1 || w > 4_624_220 || h > 4_624_220 || strconv.FormatInt(w, 10) != parts[0] || strconv.FormatInt(h, 10) != parts[1] {
		return false
	}
	return w*h >= 921600 && w*h <= 4624220 && w <= h*16 && h <= w*16
}
func dataURL(in imaging.ImageContent) string {
	return "data:" + in.MIME + ";base64," + base64.StdEncoding.EncodeToString(in.Data)
}

func (a *Adapter) inputs(ctx context.Context, r imaging.ExecuteRequest) ([]imaging.ImageContent, *imaging.ImageContent, error) {
	maxInputs := 16
	if a.id == GoogleID {
		maxInputs = 14
	}
	if a.id == VolcengineID {
		maxInputs = 10
	}
	if len(r.Inputs) != len(r.Request.Inputs) || len(r.Inputs) > maxInputs {
		return nil, nil, badInput()
	}
	roles := map[string]int{}
	seen := map[string]bool{}
	for i, in := range r.Request.Inputs {
		if in.AssetID == "" || seen[in.AssetID] || in.AssetID != r.Inputs[i].AssetID {
			return nil, nil, badInput()
		}
		seen[in.AssetID] = true
		if in.Role != "source" && in.Role != "reference" && in.Role != "mask" {
			return nil, nil, badInput()
		}
		roles[in.Role]++
	}
	switch r.Request.Operation {
	case imaging.OperationGenerate:
		if len(r.Inputs) != 0 {
			return nil, nil, badInput()
		}
	case imaging.OperationReference:
		if roles["reference"] == 0 || roles["source"] != 0 || roles["mask"] != 0 {
			return nil, nil, badInput()
		}
	case imaging.OperationEdit:
		if roles["source"] != 1 || roles["mask"] != 0 {
			return nil, nil, badInput()
		}
	case imaging.OperationMask:
		if a.id != OpenAIID {
			return nil, nil, unsupported()
		}
		if roles["source"] != 1 || roles["mask"] != 1 {
			return nil, nil, badInput()
		}
	default:
		return nil, nil, unsupported()
	}
	var out []imaging.ImageContent
	var mask *imaging.ImageContent
	var budget int64
	for i, asset := range r.Inputs {
		if asset.Open == nil || asset.Bytes < 1 || asset.Bytes > a.limits.MaxImageBytes {
			return nil, nil, badInput()
		}
		// Check encoded size before opening/allocating. The final JSON size is
		// checked separately (including text, escapes and structural overhead).
		if a.id == OpenAIID && a.profile != "agnes" {
			budget += asset.Bytes + 512 // Multipart file bytes plus conservative part overhead.
		} else {
			budget += int64(base64.StdEncoding.EncodedLen(int(asset.Bytes))) + 64
		}
		if budget > a.limits.MaxRequestBytes {
			return nil, nil, failure(imaging.CodeTooLarge, imaging.DeliveryNotSent)
		}
		in, err := a.readInput(ctx, asset)
		if err != nil {
			return nil, nil, err
		}
		if a.id == VolcengineID && (in.Width <= 14 || in.Height <= 14 || int64(in.Width)*int64(in.Height) > 36_000_000 || in.Width > in.Height*16 || in.Height > in.Width*16) {
			return nil, nil, badInput()
		}
		if r.Request.Inputs[i].Role == "mask" {
			mask = &in
		} else {
			// Do not reorder user references to silently change the mask target.
			if r.Request.Operation == imaging.OperationMask && len(out) == 0 && r.Request.Inputs[i].Role != "source" {
				return nil, nil, badInput()
			}
			if r.Request.Inputs[i].Role == "source" && r.Request.ParentAssetID != "" && r.Request.ParentAssetID != asset.AssetID {
				return nil, nil, badInput()
			}
			out = append(out, in)
		}
	}
	if mask != nil {
		if len(out) == 0 || mask.MIME != "image/png" || out[0].MIME != mask.MIME || mask.Width != out[0].Width || mask.Height != out[0].Height || !transparent(*mask) {
			return nil, nil, badInput()
		}
	}
	return out, mask, nil
}
func (a *Adapter) readInput(ctx context.Context, asset imaging.ResolvedAsset) (imaging.ImageContent, error) {
	rc, err := asset.Open()
	if err != nil || rc == nil {
		return imaging.ImageContent{}, badInput()
	}
	defer rc.Close()
	// DecodeImageContent checks actual MIME, byte and pixel limits, and fully
	// decodes offline. The caller already authorized the resolved asset.
	in, err := imaging.DecodeImageContent(ctx, io.LimitReader(rc, a.limits.MaxImageBytes+1), asset.MIME, imaging.ImageLimits{MaxBytes: a.limits.MaxImageBytes, MaxPixels: a.limits.MaxPixels})
	if err != nil || int64(len(in.Data)) != asset.Bytes {
		return imaging.ImageContent{}, badInput()
	}
	return in, nil
}
func transparent(in imaging.ImageContent) bool {
	img, _, err := image.Decode(bytes.NewReader(in.Data))
	if err != nil {
		return false
	}
	for y := 0; y < in.Height; y++ {
		for x := 0; x < in.Width; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a == 0 {
				return true
			}
		}
	}
	return false
}
