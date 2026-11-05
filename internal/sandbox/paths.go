package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// canonicalPath returns the absolute path with symlinks resolved, as the kernel
// sees it (macOS /var is /private/var). A path that does not exist yet keeps its
// missing tail under the resolved nearest existing parent.
func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var tail []string
	for dir := abs; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(append([]string{resolved}, tail...)...), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs, nil
		}
		tail = append([]string{filepath.Base(dir)}, tail...)
	}
}

func resolveAll(root string, paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		resolved, err := resolvePath(root, path)
		if err != nil {
			return nil, err
		}
		if resolved, err = canonicalPath(resolved); err != nil {
			return nil, err
		}
		if !slices.Contains(out, resolved) {
			out = append(out, resolved)
		}
	}
	return out, nil
}

// encloses reports whether descendant is ancestor or lies inside it.
func encloses(ancestor, descendant string) bool {
	rel, err := filepath.Rel(ancestor, descendant)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// checkRoot refuses a root whose write grant would re-expose a masked directory
// (a workspace at the home directory encloses ~/.ssh).
func checkRoot(root string, masked []string) error {
	resolved, err := resolveAll(root, masked)
	if err != nil {
		return err
	}
	for _, dir := range resolved {
		if encloses(root, dir) {
			return fmt.Errorf("sandbox refuses workspace %q: it contains the protected directory %q; choose a workspace that does not contain credential or LiveAgent data directories", root, dir)
		}
	}
	return nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func homeDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	if resolved, err := canonicalPath(home); err == nil {
		return resolved
	}
	return home
}

// tempDirs lists the system temporary directories commands may write. A
// candidate that is the filesystem root or contains an excluded directory (the
// home directory, a mask) is dropped: granting it would re-open those.
func tempDirs(exclude []string) []string {
	var candidates []string
	if tmp := strings.TrimRight(os.Getenv("TMPDIR"), "/"); filepath.IsAbs(tmp) {
		// The per-user /var/folders/<xx>/<id>/T has a sibling C cache directory
		// (clang module cache and similar); grant their parent for that layout only.
		if parent := darwinUserTempParent(tmp); parent != "" {
			candidates = append(candidates, parent)
		}
		candidates = append(candidates, tmp)
	}
	candidates = append(candidates, os.TempDir(), "/tmp", "/var/tmp", "/private/tmp", "/private/var/tmp")
	var out []string
	for _, candidate := range candidates {
		if !isDir(candidate) {
			continue
		}
		resolved, err := canonicalPath(candidate)
		if err != nil || resolved == string(filepath.Separator) || slices.Contains(out, resolved) {
			continue
		}
		if slices.ContainsFunc(exclude, func(dir string) bool { return dir != "" && encloses(resolved, dir) }) {
			continue
		}
		out = append(out, resolved)
	}
	return out
}

func darwinUserTempParent(tmp string) string {
	parts := strings.Split(strings.Trim(filepath.ToSlash(tmp), "/"), "/")
	n := len(parts)
	if n < 4 || parts[n-1] != "T" || parts[n-4] != "folders" {
		return ""
	}
	prefix := strings.Join(parts[:n-4], "/")
	if prefix != "var" && prefix != "private/var" {
		return ""
	}
	return "/" + strings.Join(parts[:n-1], "/")
}

// resolveBwrap finds bubblewrap at a pinned system path first, then on absolute
// PATH entries outside the workspace, so a bwrap planted in node_modules/.bin or
// a virtualenv cannot stand in for the real one.
func resolveBwrap(pathEnv, root string) string {
	for _, pinned := range []string{"/usr/bin/bwrap", "/usr/local/bin/bwrap"} {
		if isExecutable(pinned) {
			return pinned
		}
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if !filepath.IsAbs(dir) || (root != "" && encloses(root, dir)) {
			continue
		}
		candidate := filepath.Join(dir, "bwrap")
		if isExecutable(candidate) {
			return candidate
		}
	}
	return ""
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}
