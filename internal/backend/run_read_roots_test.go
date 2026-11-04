package backend

import (
	"path/filepath"
	"slices"
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
