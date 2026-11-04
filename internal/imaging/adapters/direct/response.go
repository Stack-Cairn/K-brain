package direct

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

type imageResponse struct {
	Data []struct {
		Base64 string          `json:"b64_json"`
		URL    string          `json:"url"`
		Format string          `json:"output_format"`
		Error  json.RawMessage `json:"error"`
	} `json:"data"`
	Error json.RawMessage `json:"error"`
	// Tokens/generated_images are not currency amounts. The public Usage type
	// only holds monetary usage, so this adapter deliberately leaves Usage nil.
}
type googleResponse struct {
	Candidates []struct {
		Content googleContent `json:"content"`
		Finish  string        `json:"finishReason"`
	} `json:"candidates"`
	Feedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	Error json.RawMessage `json:"error"`
}

func present(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
func unknown() (imaging.RemoteResult, error) {
	return failed(failure(imaging.CodeResultUnknown, imaging.DeliveryUncertain))
}
func noImage() (imaging.RemoteResult, error) {
	return imaging.RemoteResult{State: "failed", ErrorCode: imaging.CodeNoImageOutput}, nil
}

func (a *Adapter) response(ctx context.Context, b []byte, providerTarget bool) (imaging.RemoteResult, error) {
	if a.id == GoogleID {
		return a.googleResponse(ctx, b)
	}
	var r imageResponse
	if json.Unmarshal(b, &r) != nil {
		return unknown()
	}
	// This profile is n=1 / Seedream pro single-image. Unexpected groups or
	// error+success combinations are not silently flattened into success.
	if present(r.Error) {
		return noImage()
	}
	if len(r.Data) == 0 {
		return noImage()
	}
	if len(r.Data) != 1 {
		return unknown()
	}
	item := r.Data[0]
	if present(item.Error) {
		return noImage()
	}
	if item.Base64 != "" && item.URL != "" {
		return unknown()
	}
	var out imaging.OutputSource
	if item.Base64 != "" {
		mime := ""
		if item.Format != "" {
			switch item.Format {
			case "png":
				mime = "image/png"
			case "jpeg":
				mime = "image/jpeg"
			case "webp":
				if a.id != OpenAIID {
					return unknown()
				}
				mime = "image/webp"
			default:
				return unknown()
			}
		}
		var err error
		out, err = a.decodeOutput(ctx, item.Base64, mime)
		if err != nil {
			return unknown()
		}
		if a.id == VolcengineID && out.MIME != "image/png" && out.MIME != "image/jpeg" {
			return unknown()
		}
	} else if item.URL != "" {
		// Provider-backed OpenAI-compatible models may return HTTPS URLs.
		// The official OpenAI profile remains base64-only.
		if (a.id != VolcengineID && a.profile == "" && (!providerTarget || a.officialOpenAI)) || !outputURL(item.URL) {
			return unknown()
		}
		mime := ""
		switch item.Format {
		case "":
		case "png":
			mime = "image/png"
		case "jpeg":
			mime = "image/jpeg"
		case "webp":
			if a.id != OpenAIID {
				return unknown()
			}
			mime = "image/webp"
		default:
			return unknown()
		}
		// This is an internal source, not a verified image or a public asset.
		// The engine must safely download, decode and save before job success.
		out = imaging.OutputSource{Kind: "url", URL: item.URL, MIME: mime}
	} else {
		return noImage()
	}
	return imaging.RemoteResult{State: "completed", Outputs: []imaging.OutputSource{out}}, nil
}

func (a *Adapter) googleResponse(ctx context.Context, b []byte) (imaging.RemoteResult, error) {
	var r googleResponse
	if json.Unmarshal(b, &r) != nil {
		return unknown()
	}
	if present(r.Error) || r.Feedback.BlockReason != "" && r.Feedback.BlockReason != "BLOCK_REASON_UNSPECIFIED" {
		return noImage()
	}
	if len(r.Candidates) > 16 {
		return unknown()
	}
	var outputs []imaging.OutputSource
	for _, c := range r.Candidates {
		switch c.Finish {
		case "STOP":
		case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION", "NO_IMAGE", "MISSING_THOUGHT_SIGNATURE":
			return noImage()
		default:
			return unknown()
		}
		if c.Content.Role != "model" || len(c.Content.Parts) > 256 {
			return unknown()
		}
		for _, p := range c.Content.Parts {
			if p.Thought || p.Inline == nil {
				continue
			}
			if p.Text != "" || p.Inline.MIME != "image/png" && p.Inline.MIME != "image/jpeg" && p.Inline.MIME != "image/webp" {
				return unknown()
			}
			out, err := a.decodeOutput(ctx, p.Inline.Data, p.Inline.MIME)
			if err != nil {
				return unknown()
			}
			outputs = append(outputs, out)
			if len(outputs) > 16 {
				return unknown()
			}
		}
	}
	if len(outputs) == 0 {
		return noImage()
	}
	return imaging.RemoteResult{State: "completed", Outputs: outputs}, nil
}

func (a *Adapter) decodeOutput(ctx context.Context, encoded, mime string) (imaging.OutputSource, error) {
	// StdEncoding alone tolerates CR/LF. Reject all noncanonical encodings,
	// including newlines and nonzero pad bits, before allocating decoded bytes.
	if len(encoded) == 0 || len(encoded)%4 != 0 || int64(len(encoded)) > int64(base64.StdEncoding.EncodedLen(int(a.limits.MaxImageBytes))) || strings.ContainsAny(encoded, "\r\n") {
		return imaging.OutputSource{}, badInput()
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || int64(len(raw)) > a.limits.MaxImageBytes {
		return imaging.OutputSource{}, badInput()
	}
	in, err := imaging.DecodeImageContent(ctx, bytes.NewReader(raw), mime, imaging.ImageLimits{MaxBytes: a.limits.MaxImageBytes, MaxPixels: a.limits.MaxPixels})
	if err != nil {
		return imaging.OutputSource{}, badInput()
	}
	return imaging.OutputSource{Kind: "bytes", MIME: in.MIME, Data: in.Data}, nil
}
func outputURL(s string) bool {
	if len(s) > 8192 {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && u.Opaque == ""
}
