package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/permissions"
)

func tempImagePNG(t *testing.T, path string) {
	t.Helper()
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6WQAAAABJRU5ErkJggg==")
	if err := os.WriteFile(path, png, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A UNC path is refused before anything is opened: no SMB connection is made
// and the gate never sees it. The loopback admin share stands in for a host.
func TestLiveAgentImageUNCPathNotOpenedBeforeGate(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("UNC paths are a Windows form")
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "a.png")
	tempImagePNG(t, target)
	unc := `\\127.0.0.1\` + strings.Replace(target, ":", "$", 1)
	if _, err := os.Stat(unc); err != nil {
		t.Skipf("loopback admin share unavailable: %v", err)
	}
	var gated []string
	ctx := WithGate(WithWorkingDir(context.Background(), t.TempDir()), func(r GateRequest) (GateDecision, string) {
		gated = append(gated, r.Command)
		return GateReject, "denied by test"
	})
	ts := LiveAgentCatalog()
	for _, p := range []string{unc, filepath.ToSlash(unc)} {
		gated = nil
		raw, _ := json.Marshal(map[string]any{"path": p})
		result := ExecuteResult(ctx, ts, "Image", raw, true)
		if !result.Failed || len(gated) != 0 {
			t.Errorf("%s: network path reached the filesystem: failed=%v gated=%q text=%q", p, result.Failed, gated, result.Text)
		}
	}
}

// A deny rule written for a temp file's normal path also stops its other
// spellings: device-namespace forms are refused outright, and trailing-dot and
// 8.3 forms reach the gate as the resolved long path.
func TestLiveAgentImageDenyRuleCoversOtherSpellings(t *testing.T) {
	outside := t.TempDir()
	target := filepath.Join(outside, "a.png")
	tempImagePNG(t, target)
	work := t.TempDir()
	rules := permissions.RuleSet{Scope: permissions.ScopeUser, Rules: []permissions.StoredRule{{Rule: "Image(" + filepath.ToSlash(outside) + "/**)", Effect: permissions.Deny}}}
	ctx := WithGate(WithWorkingDir(context.Background(), work), func(r GateRequest) (GateDecision, string) {
		d := permissions.Evaluate(permissions.Request{Tool: r.Tool, Command: r.Command}, permissions.Options{ProjectRoot: work, AllowReadOnly: true}, rules)
		if d.Effect == permissions.Deny {
			return GateReject, "denied by permission rule " + d.Rule
		}
		return GateAllowOnce, ""
	})
	ts := LiveAgentCatalog()
	spellings := []string{target}
	if runtime.GOOS == "windows" {
		// Device namespace, a trailing dot Win32 strips from the directory name,
		// and the 8.3 short name all open the same file.
		spellings = append(spellings, `\\?\`+target, `\\.\`+target, outside+`.\a.png`)
		if short := tempImageShortPath(outside); short != "" && !strings.EqualFold(short, outside) {
			spellings = append(spellings, filepath.Join(short, "a.png"))
		}
	}
	link := filepath.Join(work, "link.png")
	if err := os.Symlink(target, link); err == nil {
		spellings = append(spellings, "link.png")
	} else {
		t.Logf("symlink case skipped: %v", err)
	}
	for _, p := range spellings {
		raw, _ := json.Marshal(map[string]any{"path": p})
		result := ExecuteResult(ctx, ts, "Image", raw, true)
		if !result.Failed || len(result.Parts) != 0 {
			t.Errorf("%s: shown despite the deny rule for %s: text=%q", p, outside, result.Text)
		}
	}
}

// Only real SVG markup counts as an image: a text file that merely mentions
// <svg near the top must not be attached whole as data:image/svg+xml.
func TestLiveAgentImageExternalTextMentioningSVGIsNotAnImage(t *testing.T) {
	outside := t.TempDir()
	notes := filepath.Join(outside, "notes.html")
	if err := os.WriteFile(notes, []byte("<p>icon: <svg/></p>\nAPI_KEY=sk-test-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkingDir(context.Background(), t.TempDir())
	raw, _ := json.Marshal(map[string]any{"path": notes})
	result := ExecuteResult(ctx, LiveAgentCatalog(), "Image", raw, true)
	for _, part := range result.Parts {
		if part.ImageURL == nil {
			continue
		}
		_, body, _ := strings.Cut(part.ImageURL.URL, ",")
		if data, _ := base64.StdEncoding.DecodeString(body); strings.Contains(string(data), "API_KEY") {
			t.Fatalf("external text file attached as %s: %q", part.MimeType, data)
		}
	}
	if !result.Failed {
		t.Fatalf("external non-image accepted: %q", result.Text)
	}
}
