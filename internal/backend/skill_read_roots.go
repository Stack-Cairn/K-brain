package backend

import (
	"os"
	"path/filepath"

	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/skills"
)

// runReadRoots lists the read-only directories a run may read outside its workspace:
// the skill directories, plus the session's compaction directory, which the compaction
// summary points the model at. The compaction directory is granted even before it
// exists so a compaction in the middle of a run is readable in that same run.
func runReadRoots(store *session.Store, id, workdir string) []string {
	dirs := skillReadRoots(workdir)
	if store != nil {
		if dir := store.CompactionDir(id); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// skillReadRoots lists the directories the system prompt can advertise skills from for
// this workspace (see prompts.SkillsPrompt): the managed user Skills root plus the
// project/user compatibility scan directories. Only existing directories are returned.
func skillReadRoots(workdir string) []string {
	var dirs []string
	if manager, err := skills.NewManager(); err == nil && manager.Root() != "" {
		dirs = append(dirs, manager.Root())
	}
	dirs = append(dirs, skills.DirsFor(workdir)...)
	seen := map[string]bool{}
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		dir = filepath.Clean(dir)
		if dir == "" || seen[dir] {
			continue
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	return out
}
