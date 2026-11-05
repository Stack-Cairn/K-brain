package sandbox

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ProtectedPaths are the paths under a workspace that a later, unsandboxed
// session or the user's own git trusts without asking: project instructions
// and prompts, and the repository's hooks and configuration (which can name a
// hooks directory, an fsmonitor or a pager to run). Paths that do not exist yet
// are listed too; Seatbelt keeps them from being created.
func ProtectedPaths(root string) []string {
	paths := []string{filepath.Join(root, ".liveagent"), filepath.Join(root, ".k-brain")}
	return appendNew(paths, gitProtectedPaths(root)...)
}

// gitProtectedPaths covers a plain repository, a linked worktree (.git is a
// file naming the real git directory) and the hooks directory the git
// configuration may point at.
func gitProtectedPaths(root string) []string {
	dotGit := filepath.Join(root, ".git")
	gitDir := dotGit
	var paths []string
	if info, err := os.Lstat(dotGit); err == nil && info.Mode().IsRegular() {
		// A linked worktree or submodule: replacing the file would point git
		// at a directory of the command's choosing.
		paths = append(paths, dotGit)
		if target := readGitFile(dotGit); target != "" {
			gitDir = target
		}
	}
	common := gitDir
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		if dir := strings.TrimSpace(string(data)); dir != "" {
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(gitDir, dir)
			}
			common = filepath.Clean(dir)
		}
	}
	configs := []string{filepath.Join(common, "config"), filepath.Join(gitDir, "config.worktree")}
	if gitDir != common {
		configs = append(configs, filepath.Join(common, "config.worktree"))
	}
	paths = appendNew(paths, filepath.Join(common, "hooks"))
	paths = appendNew(paths, configs...)

	// core.hooksPath can come from the repository, an included file or the
	// user's global configuration; a relative value is taken from the
	// worktree's top level, so a global ".githooks" names a directory in
	// every workspace.
	var global []string
	if home, _ := os.UserHomeDir(); home != "" {
		global = append(global, filepath.Join(home, ".gitconfig"))
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".config")
		}
		global = append(global, filepath.Join(xdg, "git", "config"))
	}
	hooks, includes := scanGitConfigs(append(configs, global...))
	for _, hook := range hooks {
		paths = appendNew(paths, expandGitPath(hook, root))
	}
	// An included file can set core.hooksPath on the next run.
	return appendNew(paths, includes...)
}

// readGitFile returns the directory a "gitdir: <path>" file points at.
func readGitFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(data), "\n")
	dir, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if !ok {
		return ""
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(path), dir)
	}
	return filepath.Clean(dir)
}

// scanGitConfigs reads the core.hooksPath values and the include paths of the
// given files, following includes a few levels deep. Every includeIf counts:
// protecting a file whose condition does not match costs nothing.
func scanGitConfigs(files []string) (hooks, includes []string) {
	seen := map[string]bool{}
	var visit func(file string, depth int)
	visit = func(file string, depth int) {
		if depth > 5 || seen[file] {
			return
		}
		seen[file] = true
		values := readGitConfig(file)
		hooks = append(hooks, values.hooksPaths...)
		for _, include := range values.includes {
			include = expandGitPath(include, filepath.Dir(file))
			includes = appendNew(includes, include)
			visit(include, depth+1)
		}
	}
	for _, file := range files {
		visit(file, 0)
	}
	return hooks, includes
}

type gitConfigValues struct {
	hooksPaths []string
	includes   []string
}

// readGitConfig is a small reader for the two keys the sandbox cares about. It
// handles sections, subsections, comments and quoted values; anything it does
// not understand is skipped.
func readGitConfig(file string) gitConfigValues {
	var out gitConfigValues
	f, err := os.Open(file)
	if err != nil {
		return out
	}
	defer f.Close()
	section := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				continue
			}
			header := strings.TrimSpace(line[1:end])
			name, _, _ := strings.Cut(header, " ")
			section = strings.ToLower(name)
			if dot := strings.IndexByte(section, '.'); dot >= 0 {
				// The legacy [section.subsection] form.
				section = section[:dot]
			}
			line = strings.TrimSpace(line[end+1:])
			if line == "" {
				continue
			}
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = gitConfigValue(value)
		if value == "" {
			continue
		}
		switch {
		case section == "core" && key == "hookspath":
			out.hooksPaths = append(out.hooksPaths, value)
		case (section == "include" || section == "includeif") && key == "path":
			out.includes = append(out.includes, value)
		}
	}
	return out
}

func gitConfigValue(raw string) string {
	var b strings.Builder
	quoted := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '"':
			quoted = !quoted
		case c == '\\' && i+1 < len(raw):
			i++
			switch raw[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(raw[i])
			}
		case !quoted && (c == '#' || c == ';'):
			return strings.TrimSpace(b.String())
		default:
			b.WriteByte(c)
		}
	}
	return strings.TrimSpace(b.String())
}

// expandGitPath resolves a path from a git configuration value: "~/" is the
// home directory and a relative path is taken from base.
func expandGitPath(path, base string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, _ := os.UserHomeDir(); home != "" {
			return filepath.Join(home, rest)
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path)
}

func appendNew(list []string, items ...string) []string {
	for _, item := range items {
		if item != "" && !slices.Contains(list, item) {
			list = append(list, item)
		}
	}
	return list
}

// protection is what a backend must enforce for the protected paths.
type protection struct {
	// paths are the protected paths inside a grant, spelled as given and with
	// symlinks resolved; Seatbelt denies both spellings.
	paths []string
	// resolved are the same paths with symlinks resolved only.
	resolved []string
	// ancestors are the existing directories between a grant and a protected
	// path. Renaming one would carry the protected path away and let the
	// command create a replacement at the old location.
	ancestors []string
}

// errSymlinkedProtected marks a protected path reached through a symlink, which
// bubblewrap cannot keep: the link itself stays replaceable.
var errSymlinkedProtected = errors.New("is a symlink")

// protect plans the protection of p.Protected inside grants. Paths outside
// every grant are already read-only and are dropped (bubblewrap would
// otherwise bind them back over a mask). A protected path that encloses a
// grant would make the workspace itself read-only and is refused, as is a
// protected file with a second hard link that a command could write instead.
// With refuseSymlinks, a symlink on the way to a protected path is refused.
func (p *Policy) protect(root string, grants []string, refuseSymlinks bool) (protection, error) {
	var out protection
	for _, path := range p.Protected {
		if strings.TrimSpace(path) == "" {
			continue
		}
		spelled, err := resolvePath(root, path)
		if err != nil {
			return out, err
		}
		resolved, err := canonicalPath(spelled)
		if err != nil {
			return out, err
		}
		for _, grant := range grants {
			if encloses(resolved, grant) {
				return out, fmt.Errorf("sandbox refuses protected path %q: it contains the writable %q; protecting it would make the workspace read-only", resolved, grant)
			}
		}
		grant := enclosingGrant(grants, resolved)
		spelledGrant := enclosingGrant(grants, spelled)
		if grant == "" && spelledGrant == "" {
			continue
		}
		if refuseSymlinks && spelledGrant != "" {
			if link := symlinkBelow(spelledGrant, spelled); link != "" {
				return out, fmt.Errorf("sandbox refuses protected path %q: %q %w; replace the link with the directory it points to or use per-call confirmation", spelled, link, errSymlinkedProtected)
			}
		}
		if err := refuseHardLinks(resolved); err != nil {
			return out, err
		}
		out.paths = appendNew(out.paths, spelled, resolved)
		out.resolved = appendNew(out.resolved, resolved)
		if grant != "" {
			out.ancestors = appendNew(out.ancestors, dirsBetween(grant, resolved)...)
		}
		if spelledGrant != "" {
			out.ancestors = appendNew(out.ancestors, dirsBetween(spelledGrant, spelled)...)
		}
	}
	return out, nil
}

func enclosingGrant(grants []string, path string) string {
	for _, grant := range grants {
		if encloses(grant, path) {
			return grant
		}
	}
	return ""
}

// dirsBetween lists the existing directories strictly between ancestor and
// path, outermost first.
func dirsBetween(ancestor, path string) []string {
	var dirs []string
	for dir := filepath.Dir(path); dir != ancestor && encloses(ancestor, dir); dir = filepath.Dir(dir) {
		if info, err := os.Lstat(dir); err == nil && info.IsDir() {
			dirs = append([]string{dir}, dirs...)
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return dirs
}

// symlinkBelow returns the first symlink on the way from ancestor (exclusive)
// to path (inclusive).
func symlinkBelow(ancestor, path string) string {
	rel, err := filepath.Rel(ancestor, path)
	if err != nil || rel == "." {
		return ""
	}
	current := ancestor
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return ""
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return current
		}
	}
	return ""
}

// refuseHardLinks fails when a regular file at or under path has another hard
// link: writes through that other name would change the protected file.
func refuseHardLinks(path string) error {
	const limit = 4096
	seen := 0
	return filepath.WalkDir(path, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			// A missing or unreadable entry has nothing to check.
			return nil
		}
		if seen++; seen > limit {
			return filepath.SkipAll
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if links := hardLinks(info); links > 1 {
			return fmt.Errorf("sandbox refuses protected file %q: it has %d hard links, and writing another of them would change it", file, links)
		}
		return nil
	})
}
