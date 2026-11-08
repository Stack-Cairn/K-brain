package backend

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/session"
)

// The compaction summary points the model at the session's segment files, which live
// in the session store outside the workspace; the run must be able to read them.
// tools.WithSkillReadRoots enforcement itself is covered in internal/tools.
func TestRunReadRootsGrantCompactionSegments(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	store, err := session.OpenProjectHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspace := t.TempDir()
	id, err := store.Create(workspace, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	dir := store.CompactionDir(id)
	if dir != filepath.Join(filepath.Dir(store.TranscriptPath(id)), "compaction") {
		t.Fatalf("compaction dir = %s", dir)
	}
	if roots := runReadRoots(store, id, workspace); !slices.Contains(roots, dir) {
		t.Fatalf("roots %v should include the compaction dir %s even before it exists", roots, dir)
	}
	if roots := runReadRoots(nil, id, workspace); slices.Contains(roots, dir) {
		t.Fatal("no store, no compaction root")
	}
}

// A pack skill's SKILL.md points at files next to it (references/, scripts/), several
// levels below the managed root. The managed root grant must cover them without
// widening to the user's home directory.
func TestRunReadRootsCoverSkillPackFilesButNotHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LIVEAGENT_HOME", filepath.Join(home, ".liveagent"))
	t.Setenv("K_BRAIN_HOME", "")
	ref := filepath.Join(home, ".liveagent", "skills", "demo-pack", "skills", "radare2", "references", "commands.md")
	if err := os.MkdirAll(filepath.Dir(ref), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ref, []byte("ref"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	roots := runReadRoots(nil, "", workspace)
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	realRef, err := filepath.EvalSymlinks(ref)
	if err != nil {
		t.Fatal(err)
	}
	covered := false
	for _, root := range roots {
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if isWithin(realHome, realRoot) {
			t.Fatalf("read roots %v grant the home directory via %s", roots, root)
		}
		if isWithin(realRef, realRoot) {
			covered = true
		}
	}
	if !covered {
		t.Fatalf("read roots %v do not cover the pack reference %s", roots, ref)
	}
}

// isWithin reports whether path is root or below it.
func isWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
