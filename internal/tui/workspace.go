package tui

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func snapshotWorkspace() string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	commit, err := gitOut(ctx, "stash", "create")
	if err != nil {
		return ""
	}
	if commit == "" {
		commit, err = gitOut(ctx, "commit-tree", "HEAD^{tree}", "-m", "k-brain turn snapshot")
		if err != nil {
			return ""
		}
	}
	if _, err := gitOut(ctx, "update-ref", "refs/k-brain/snapshots/"+commit, commit); err != nil {
		return ""
	}
	return commit
}

func workspaceClean() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := gitOut(ctx, "status", "--porcelain", "--untracked-files=no")
	return err == nil && out == ""
}

func dropSnapshot(ref string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = gitOut(ctx, "update-ref", "-d", "refs/k-brain/snapshots/"+ref)
}

func restoreWorkspace(ref string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if len(ref) != 40 && len(ref) != 64 || strings.Trim(ref, "0123456789abcdef") != "" {
		return 0, fmt.Errorf("invalid workspace snapshot")
	}
	parents, err := gitOut(ctx, "rev-list", "--parents", "-1", ref)
	if err != nil {
		return 0, err
	}
	indexRef := ref
	if fields := strings.Fields(parents); len(fields) >= 3 {
		indexRef = fields[2]
	}
	if err := checkSnapshotCollisions(ctx, ref); err != nil {
		return 0, err
	}
	changed := make(map[string]bool)
	for _, args := range [][]string{
		{"diff", "--name-only", "-z", ref, "--", ":/"},
		{"diff", "--cached", "--name-only", "-z", indexRef, "--", ":/"},
	} {
		out, err := gitRaw(ctx, args...)
		if err != nil {
			return 0, err
		}
		for _, name := range strings.Split(out, "\x00") {
			if name != "" {
				changed[name] = true
			}
		}
	}
	if len(changed) == 0 {
		return 0, nil
	}
	if _, err := gitOut(ctx, "restore", "--source="+ref, "--staged", "--worktree", "--", ":/"); err != nil {
		return 0, err
	}
	if _, err := gitOut(ctx, "read-tree", indexRef); err != nil {
		return 0, err
	}
	return len(changed), nil
}

func checkSnapshotCollisions(ctx context.Context, ref string) error {
	tree, err := gitRaw(ctx, "ls-tree", "-r", "-z", "--name-only", "--full-tree", ref)
	if err != nil {
		return err
	}
	untracked, err := gitRaw(ctx, "ls-files", "--others", "--full-name", "-z", "--", ":/")
	if err != nil {
		return err
	}
	normalize := func(name string) string {
		name = strings.TrimSuffix(name, "/")
		if runtime.GOOS == "windows" {
			name = strings.ToLower(name)
		}
		return name
	}
	files, dirs := make(map[string]bool), make(map[string]bool)
	for _, name := range strings.Split(tree, "\x00") {
		if name == "" {
			continue
		}
		name = normalize(name)
		files[name] = true
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	for _, name := range strings.Split(untracked, "\x00") {
		if name == "" {
			continue
		}
		key := normalize(name)
		collision := dirs[key]
		for p := key; p != "."; p = path.Dir(p) {
			collision = collision || files[p]
		}
		if collision {
			return fmt.Errorf("workspace rewind would overwrite untracked file %q", name)
		}
	}
	return nil
}

func gitOut(ctx context.Context, args ...string) (string, error) {
	out, err := gitRaw(ctx, args...)
	return strings.TrimSpace(out), err
}

func gitRaw(ctx context.Context, args ...string) (string, error) {
	return gitRawAt(ctx, cwd(), args...)
}

func gitRawAt(ctx context.Context, dir string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = dir
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		line, _, _ := strings.Cut(strings.TrimSpace(errb.String()), "\n")
		if line == "" {
			line = err.Error()
		}
		config.LogEvent("workspace.git", strings.Join(args, " ")+": "+line)
		return "", fmt.Errorf("%s", line)
	}
	return out.String(), nil
}
