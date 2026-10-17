package imaging

import (
	"crypto/sha256"
	"encoding/hex"
)

// ProfileTargetID derives the engine target id for one model of a profile. The
// engine accepts only "it_" + [A-Za-z0-9_-]{1,253}, and model names are free
// text, so the pair is hashed; FindProfileTarget resolves it by scanning the
// small profile document. The id is stable across restarts, which keeps
// idempotent job replays matching the same target.
func ProfileTargetID(profileID, model string) string {
	sum := sha256.Sum256([]byte(profileID + "\x00" + model))
	return "it_" + hex.EncodeToString(sum[:16])
}

// profileParameters declares the native keys NativeParameters may emit for a
// platform, so request validation accepts them. Values are already normalized
// by NormalizeParams, so the specs stay permissive and carry no enums.
func profileParameters(p Platform) []ParameterSpec {
	str := func(key, label string) ParameterSpec { return ParameterSpec{Key: key, Label: label, Type: "string"} }
	specs := []ParameterSpec{str("size", "Size")}
	if p.SupportsQuality {
		specs = append(specs, str("quality", "Quality"))
	}
	if p.SupportsModeration {
		specs = append(specs, str("moderation", "Moderation"))
	}
	if p.SupportsOutputFormat {
		specs = append(specs, str("output_format", "Output format"))
	}
	if p.SupportsCompression {
		lo, hi := 0.0, 100.0
		specs = append(specs, ParameterSpec{Key: "output_compression", Label: "Compression", Type: "integer", Minimum: &lo, Maximum: &hi})
	}
	if p.SupportsTransparentBackground {
		specs = append(specs, str("background", "Background"))
	}
	if p.ID == PlatformGoogle {
		specs = append(specs, str("aspect_ratio", "Aspect ratio"), str("image_size", "Image size"))
	}
	return specs
}
