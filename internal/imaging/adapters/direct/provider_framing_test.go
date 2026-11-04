package direct

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

const portraitCanvasRequirement = "\n\nCanvas requirement from the image settings: portrait orientation, aspect ratio 9:16, width 720 pixels and height 1280 pixels. This canvas requirement takes precedence over any conflicting aspect ratio or horizontal/vertical instruction above. Preserve the subject and visual details."

func TestProviderOpenAICanvasRequirementBudget(t *testing.T) {
	a := adapter(t, OpenAIID, options(&http.Client{}, "https://example.invalid"))
	r := request(OpenAIID)
	r.Target.Source = "provider"
	r.Target.RemoteID = "gpt-image-2.5-flare"
	r.Request.Parameters = map[string]any{"aspectRatio": "9:16"}
	budget := 32000 - utf8.RuneCountInString(portraitCanvasRequirement)
	for _, length := range []int{budget, budget + 1, 32000} {
		r.Request.Prompt = strings.Repeat("画", length)
		_, body, err := a.request(context.Background(), r)
		if length > budget {
			if err == nil {
				t.Fatal("wire prompt exceeded provider budget")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		raw, _, err := a.encodeRequest(context.Background(), body)
		if err != nil {
			t.Fatal(err)
		}
		var wire openAIRequest
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		if wire.Prompt != r.Request.Prompt+portraitCanvasRequirement || utf8.RuneCountInString(wire.Prompt) != 32000 {
			t.Fatal("exact 32k wire prompt boundary changed")
		}
	}
	r.Request.Parameters = nil
	if _, _, err := a.request(context.Background(), r); err != nil {
		t.Fatalf("automatic canvas changed prompt budget: %v", err)
	}
	official := adapter(t, OpenAIID, options(&http.Client{}, "https://api.openai.com/v1"))
	r.Request.Parameters = map[string]any{"aspectRatio": "9:16"}
	if _, _, err := official.request(context.Background(), r); err != nil {
		t.Fatalf("official canvas changed prompt budget: %v", err)
	}
}

func TestProviderOpenAICanvasRequirement(t *testing.T) {
	prompt := "Create one horizontal still image, aspect ratio 16:9. Preserve the portrait details."
	for _, tc := range []struct {
		name, source, base string
		params             map[string]any
		size, requirement  string
	}{
		{"portrait overrides conflicting prompt", "provider", "https://example.invalid", map[string]any{"aspectRatio": "9:16"}, "720x1280", portraitCanvasRequirement},
		{"landscape", "provider", "https://example.invalid", map[string]any{"aspectRatio": "16:9"}, "1280x720", "\n\nCanvas requirement from the image settings: landscape orientation, aspect ratio 16:9, width 1280 pixels and height 720 pixels. This canvas requirement takes precedence over any conflicting aspect ratio or horizontal/vertical instruction above. Preserve the subject and visual details."},
		{"resolution alone", "provider", "https://example.invalid", map[string]any{"imageSize": "2K"}, "1920x1920", "\n\nCanvas requirement from the image settings: square orientation, aspect ratio 1:1, width 1920 pixels and height 1920 pixels. This canvas requirement takes precedence over any conflicting aspect ratio or horizontal/vertical instruction above. Preserve the subject and visual details."},
		{"persisted pixel size", "provider", "https://example.invalid", map[string]any{"size": "1024x1536"}, "1024x1536", "\n\nCanvas requirement from the image settings: portrait orientation, aspect ratio 2:3, width 1024 pixels and height 1536 pixels. This canvas requirement takes precedence over any conflicting aspect ratio or horizontal/vertical instruction above. Preserve the subject and visual details."},
		{"automatic framing", "provider", "https://example.invalid", nil, "", ""},
		{"legacy framing", "", "https://example.invalid", map[string]any{"aspectRatio": "9:16"}, "720x1280", ""},
		{"official provider", "provider", "https://api.openai.com/v1", map[string]any{"aspectRatio": "9:16"}, "720x1280", ""},
		{"official mixed case", "provider", "https://API.OpenAI.COM/v1", map[string]any{"aspectRatio": "9:16"}, "720x1280", ""},
		{"official port", "provider", "https://api.openai.com:443/v1", map[string]any{"aspectRatio": "9:16"}, "720x1280", ""},
		{"official mixed case and port", "provider", "https://API.OpenAI.COM:443/v1", map[string]any{"aspectRatio": "9:16"}, "720x1280", ""},
		{"lookalike suffix", "provider", "https://api.openai.com.example.invalid/v1", map[string]any{"aspectRatio": "9:16"}, "720x1280", portraitCanvasRequirement},
		{"lookalike prefix", "provider", "https://proxy-api.openai.com/v1", map[string]any{"aspectRatio": "9:16"}, "720x1280", portraitCanvasRequirement},
	} {
		for _, edit := range []bool{false, true} {
			name := "generation"
			if edit {
				name = "edit"
			}
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				a := adapter(t, OpenAIID, options(&http.Client{}, tc.base))
				r := request(OpenAIID)
				r.Target.RemoteID = "gpt-image-2.5-flare"
				r.Target.Source = tc.source
				r.Request.Prompt = prompt
				r.Request.Parameters = tc.params
				input := syntheticPNG(t, 16, 40, false)
				var closed atomic.Int32
				if edit {
					r.Request.Operation = imaging.OperationEdit
					r.Request.ParentAssetID = "source"
					addInput(&r, "source", "source", input, &closed)
				}
				for range 2 {
					path, body, err := a.request(context.Background(), r)
					if err != nil {
						t.Fatal(err)
					}
					raw, contentType, err := a.encodeRequest(context.Background(), body)
					if err != nil {
						t.Fatal(err)
					}
					var wire openAIRequest
					if edit {
						req, err := http.NewRequest(http.MethodPost, tc.base+path, bytes.NewReader(raw))
						if err != nil {
							t.Fatal(err)
						}
						req.Header.Set("Content-Type", contentType)
						parts := parseImageMultipart(t, req)
						wire.Prompt, wire.Size = parts.Fields["prompt"], parts.Fields["size"]
						if path != "/images/edits" || len(parts.Files["image[]"]) != 1 || !bytes.Equal(parts.Files["image[]"][0], input) {
							t.Fatal("edit route or synthetic input changed")
						}
					} else {
						if contentType != "application/json" || path != "/images/generations" {
							t.Fatalf("path=%q contentType=%q", path, contentType)
						}
						if err := json.Unmarshal(raw, &wire); err != nil {
							t.Fatal(err)
						}
					}
					if wire.Size != tc.size || wire.Prompt != prompt+tc.requirement {
						t.Fatalf("encoded size=%q prompt=%q", wire.Size, wire.Prompt)
					}
					if r.Request.Prompt != prompt || strings.Count(wire.Prompt, "Canvas requirement from the image settings:") > 1 {
						t.Fatal("stored prompt changed or canvas requirements accumulated")
					}
				}
			})
		}
	}
}

func TestProviderCanvasPreservesVendorProfiles(t *testing.T) {
	for _, profile := range []string{"agnes", "rolldek"} {
		t.Run(profile, func(t *testing.T) {
			base := "https://rolldek.com/v1"
			if profile == "agnes" {
				base = "https://apihub.agnes-ai.com/v1"
			}
			o := options(&http.Client{}, base)
			o.Profile = profile
			a := adapter(t, OpenAIID, o)
			r := request(OpenAIID)
			r.Target.Source = "provider"
			if profile == "agnes" {
				r.Target.RemoteID = "agnes-image-2.5-flash"
				r.Request.Parameters = map[string]any{"aspectRatio": "9:16", "imageSize": "2K"}
			}
			path, body, err := a.request(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			raw, contentType, err := a.encodeRequest(context.Background(), body)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			if path != "/images/generations" || contentType != "application/json" || wire["prompt"] != r.Request.Prompt {
				t.Fatal("vendor route or prompt changed")
			}
			if profile == "agnes" {
				if wire["size"] != "2K" || wire["ratio"] != "9:16" || wire["return_base64"] != true {
					t.Fatalf("Agnes dialect changed: %s", raw)
				}
			} else if wire["response_format"] != "b64_json" || wire["size"] != nil {
				t.Fatalf("RollDek dialect changed: %s", raw)
			}
		})
	}
}
