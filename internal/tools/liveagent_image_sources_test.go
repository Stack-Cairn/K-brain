package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// root:// and skill:// are documented Image path forms; repeated into url/urls
// by a strict-schema model they stay paths and are not rejected as URLs.
func TestLiveAgentImageNamedRootPathRepeatedInURLs(t *testing.T) {
	work, other, skill := t.TempDir(), t.TempDir(), t.TempDir()
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6WQAAAABJRU5ErkJggg==")
	for _, dir := range []string{other, skill} {
		if err := os.WriteFile(filepath.Join(dir, "shot.png"), png, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := WithLiveAgentNamedRoots(WithWorkingDir(context.Background(), work), map[string]WorkspaceRoot{
		"root://other":   {Path: other, Access: "read"},
		"skill://helper": {Path: skill, Access: "read"},
	})
	ts := LiveAgentCatalog()
	// Control: each named-root path alone displays.
	for _, path := range []string{"root://other/shot.png", "skill://helper/shot.png"} {
		raw, _ := json.Marshal(map[string]any{"path": path})
		if result := ExecuteResult(ctx, ts, "Image", raw, true); result.Failed || len(result.Parts) != 1 {
			t.Fatalf("control %s: failed=%v parts=%d text=%q", path, result.Failed, len(result.Parts), result.Text)
		}
	}
	for name, call := range map[string]map[string]any{
		"root path in every field":  {"base64": "", "base64s": []string{""}, "mimeType": "", "path": "root://other/shot.png", "paths": []string{"root://other/shot.png"}, "source": "", "sources": []string{"root://other/shot.png"}, "url": "", "urls": []string{"root://other/shot.png"}},
		"skill path in every field": {"path": "skill://helper/shot.png", "url": "skill://helper/shot.png", "urls": []string{""}},
	} {
		raw, _ := json.Marshal(call)
		result := ExecuteResult(ctx, ts, "Image", raw, true)
		if result.Failed || len(result.Parts) != 1 {
			t.Errorf("%s: failed=%v parts=%d text=%q", name, result.Failed, len(result.Parts), result.Text)
		}
	}
}

// A path made only of base64 characters (no extension) repeated into base64s
// is still a path: base64 must decode to real image bytes.
func TestLiveAgentImageExtensionlessPathRepeatedInBase64s(t *testing.T) {
	work := t.TempDir()
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6WQAAAABJRU5ErkJggg==")
	if err := os.MkdirAll(filepath.Join(work, "shots"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "shots", "latest"), png, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkingDir(context.Background(), work)
	ts := LiveAgentCatalog()
	// Control: the path alone displays.
	raw, _ := json.Marshal(map[string]any{"paths": []string{"shots/latest"}})
	if result := ExecuteResult(ctx, ts, "Image", raw, true); result.Failed || len(result.Parts) != 1 {
		t.Fatalf("control: failed=%v parts=%d text=%q", result.Failed, len(result.Parts), result.Text)
	}
	raw, _ = json.Marshal(map[string]any{"base64": "", "base64s": []string{"shots/latest"}, "mimeType": "", "path": "shots/latest", "paths": []string{"shots/latest"}, "url": "", "urls": []string{""}})
	if result := ExecuteResult(ctx, ts, "Image", raw, true); result.Failed || len(result.Parts) != 1 {
		t.Fatalf("path repeated in base64s: failed=%v parts=%d text=%q", result.Failed, len(result.Parts), result.Text)
	}
}

// One file spelled two ways (./pa.png, pa.png) is one image, so a 12-image
// paths call mirrored into path stays within the limit.
func TestLiveAgentImageSameFileSpelledTwiceCountsOnce(t *testing.T) {
	work := t.TempDir()
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6WQAAAABJRU5ErkJggg==")
	var twelve []string
	for i := 0; i < 12; i++ {
		name := "p" + string(rune('a'+i)) + ".png"
		if err := os.WriteFile(filepath.Join(work, name), png, 0o600); err != nil {
			t.Fatal(err)
		}
		twelve = append(twelve, name)
	}
	ctx := WithWorkingDir(context.Background(), work)
	ts := LiveAgentCatalog()
	raw, _ := json.Marshal(map[string]any{"path": "./pa.png", "paths": []string{"pa.png"}})
	if result := ExecuteResult(ctx, ts, "Image", raw, true); result.Failed || len(result.Parts) != 1 {
		t.Errorf("one file two spellings: failed=%v parts=%d text=%q", result.Failed, len(result.Parts), result.Text)
	}
	raw, _ = json.Marshal(map[string]any{"path": "./pa.png", "paths": twelve})
	if result := ExecuteResult(ctx, ts, "Image", raw, true); result.Failed || len(result.Parts) != 12 {
		t.Errorf("twelve files, one mirrored into path: failed=%v parts=%d text=%q", result.Failed, len(result.Parts), result.Text)
	}
}

// Values moved to another branch by content stay gated: an https value in
// path reaches the gate as the URL, and a local path in url reaches it as the
// resolved absolute path, the same command the path field produces.
func TestLiveAgentImageReclassifiedSourcesStillGated(t *testing.T) {
	work := t.TempDir()
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6WQAAAABJRU5ErkJggg==")
	if err := os.WriteFile(filepath.Join(work, "a.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}
	var gated []string
	ctx := WithGate(WithWorkingDir(context.Background(), work), func(r GateRequest) (GateDecision, string) {
		gated = append(gated, r.Command)
		return GateReject, "denied by test"
	})
	ts := LiveAgentCatalog()
	for _, c := range []struct {
		call map[string]any
		want string
	}{
		{map[string]any{"path": "https://example.test/a.png"}, "https://example.test/a.png"},
		{map[string]any{"url": "a.png"}, filepath.Join(work, "a.png")},
		{map[string]any{"urls": []string{"a.png"}}, filepath.Join(work, "a.png")},
	} {
		gated = nil
		raw, _ := json.Marshal(c.call)
		result := ExecuteResult(ctx, ts, "Image", raw, true)
		if !result.Failed || len(gated) != 1 || gated[0] != c.want {
			t.Errorf("%v: failed=%v gated=%q want %q text=%q", c.call, result.Failed, gated, c.want, result.Text)
		}
	}
}
