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
)

// The Win32 file namespace spelling \\?\D:\... names the same file as D:\...;
// tools such as screenshot helpers print it, and models copy it verbatim.
func requireWindowsPaths(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("Windows path namespaces")
	}
}

func extended(path string) string { return `\\?\` + path }

func TestLiveAgentPathAcceptsExtendedDriveSpelling(t *testing.T) {
	requireWindowsPaths(t)
	root := t.TempDir()
	file := filepath.Join(root, "shots", "a.png")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	lower := extended(strings.ToLower(file[:1]) + file[1:])
	for _, tc := range []struct{ name, root, target string }{
		{"plain root, extended target", root, extended(file)},
		{"extended root, plain target", extended(root), file},
		{"extended root, extended target", extended(root), extended(file)},
		{"lower-case drive letter", root, lower},
		{"extended root, relative target", extended(root), filepath.Join("shots", "a.png")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithWorkingDir(WithWorkspaceRoots(context.Background(), []WorkspaceRoot{{Path: tc.root, Access: "write"}}), tc.root)
			for _, access := range []string{"read", "write"} {
				got, err := liveagentPath(ctx, tc.target, access, "Read", false)
				if err != nil {
					t.Fatalf("%s: %v", access, err)
				}
				if strings.HasPrefix(got, `\\`) || !strings.EqualFold(got, file) {
					t.Fatalf("%s resolved to %q, want the ordinary spelling of %q", access, got, file)
				}
			}
		})
	}
}

func TestLiveAgentPathRefusesOtherWindowsNamespaces(t *testing.T) {
	requireWindowsPaths(t)
	root := t.TempDir()
	ctx := WithWorkspaceRoots(context.Background(), []WorkspaceRoot{{Path: root, Access: "write"}})
	inside := filepath.Join(root, "a.png")
	for _, target := range []string{
		`\\?\UNC\server`,
		`\\?\UNC\server\\a.png`,
		`\\?\UNC\server\share\..\a.png`,
		`\\.\` + inside,
		`\??\` + inside,
		`//?/` + filepath.ToSlash(inside),
		`\\?\Volume{01234567-89ab-cdef-0123-456789abcdef}\a.png`,
		`\\?\GLOBALROOT\Device\HarddiskVolume1\a.png`,
		`\\?\C:`,
		`\\?\C:a.png`,
		extended(root + `\sub\..\a.png`),
		extended(root + `\.\a.png`),
		extended(root + `/a.png`),
		extended(root + `\\a.png`),
		extended(root + `\a.png.`),
		extended(root + `\a.png `),
		extended(root + `\a.png:stream`),
		extended(root + `\nul.png`),
		extended(root + `\COM1`),
	} {
		if got, err := liveagentPath(ctx, target, "read", "Read", false); err == nil {
			t.Errorf("%q accepted as %q", target, got)
		}
	}
}

func TestLiveAgentPathExtendedSpellingKeepsBoundaries(t *testing.T) {
	requireWindowsPaths(t)
	parent := t.TempDir()
	work, sibling, readonly := filepath.Join(parent, "work"), filepath.Join(parent, "work-other"), filepath.Join(parent, "work", "ro")
	for _, dir := range []string{work, sibling, readonly} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx := WithWorkspaceRoots(context.Background(), []WorkspaceRoot{{Path: work, Access: "write"}, {Path: extended(readonly), Access: "read"}})
	for _, target := range []string{extended(filepath.Join(sibling, "a.png")), extended(filepath.Join(parent, "a.png"))} {
		if _, err := liveagentPath(ctx, target, "read", "Read", false); err == nil || !strings.Contains(err.Error(), "outside the authorized workspace roots") {
			t.Errorf("%q: %v", target, err)
		}
	}
	for _, target := range []string{extended(filepath.Join(readonly, "a.txt")), filepath.Join(readonly, "a.txt")} {
		if _, err := liveagentPath(ctx, target, "write", "Write", false); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Errorf("write %q: %v", target, err)
		}
	}
}

func TestLiveAgentImageReadsExtendedDrivePath(t *testing.T) {
	requireWindowsPaths(t)
	root := t.TempDir()
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6WQAAAABJRU5ErkJggg==")
	shot := filepath.Join(root, ".codex-artifacts", "desktop", "pilot-entry.png")
	if err := os.MkdirAll(filepath.Dir(shot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shot, png, 0o600); err != nil {
		t.Fatal(err)
	}
	var gated []string
	ctx := WithGate(WithWorkingDir(WithWorkspaceRoots(context.Background(), []WorkspaceRoot{{Path: root, Access: "write"}}), root),
		func(req GateRequest) (GateDecision, string) {
			gated = append(gated, req.Command)
			return GateAllowOnce, ""
		})
	// The call from the report: the extended spelling in sources, empty placeholders elsewhere.
	raw, _ := json.Marshal(map[string]any{"source": "", "sources": []string{extended(shot)}, "url": "", "urls": []string{""}})
	result := ExecuteResult(ctx, LiveAgentCatalog(), "Image", raw, true)
	if result.Failed || len(result.Parts) != 1 {
		t.Fatalf("%+v", result)
	}
	for _, path := range gated {
		if strings.HasPrefix(path, `\\`) {
			t.Fatalf("gate saw %q; permission rules match the ordinary spelling", path)
		}
	}
}

func TestLiveAgentPathReadsLongExtendedPath(t *testing.T) {
	requireWindowsPaths(t)
	root := t.TempDir()
	dir := root
	for len(dir) < 280 {
		dir = filepath.Join(dir, strings.Repeat("d", 40))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "long.png")
	if err := os.WriteFile(file, []byte("long"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkspaceRoots(context.Background(), []WorkspaceRoot{{Path: root, Access: "read"}})
	got, err := liveagentPath(ctx, extended(file), "read", "Read", false)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(got); err != nil || string(data) != "long" {
		t.Fatalf("read %q: %q %v", got, data, err)
	}
}

func TestSameDirectoryIgnoresExtendedSpelling(t *testing.T) {
	requireWindowsPaths(t)
	dir := t.TempDir()
	if !sameDirectory(extended(dir), dir) {
		t.Fatal("extended spelling of the working directory counted as another directory")
	}
}
