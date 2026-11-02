package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
)

// Raw base64 (here a GIF, without the PNG/JPEG prefixes) repeated into sources
// and base64s by a strict-schema model is one image, not a path plus an image.
func TestLiveAgentImageRawGIFBase64RepeatedInSources(t *testing.T) {
	gif := base64.StdEncoding.EncodeToString([]byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;"))
	ctx := WithWorkingDir(context.Background(), t.TempDir())
	ts := LiveAgentCatalog()
	// Control: the raw GIF alone in base64 displays.
	raw, _ := json.Marshal(map[string]any{"base64": gif})
	if result := ExecuteResult(ctx, ts, "Image", raw, true); result.Failed || len(result.Parts) != 1 {
		t.Fatalf("control: failed=%v parts=%d text=%q", result.Failed, len(result.Parts), result.Text)
	}
	raw, _ = json.Marshal(map[string]any{"base64": "", "base64s": []string{gif}, "mimeType": "", "path": "", "paths": []string{""}, "source": "", "sources": []string{gif}, "url": "", "urls": []string{""}})
	if result := ExecuteResult(ctx, ts, "Image", raw, true); result.Failed || len(result.Parts) != 1 {
		t.Fatalf("raw GIF repeated in sources: failed=%v parts=%d text=%q", result.Failed, len(result.Parts), result.Text)
	}
}
