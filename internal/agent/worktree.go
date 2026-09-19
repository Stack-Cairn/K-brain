package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func gitWorktreeRoot(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("find git worktree root: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func provisionSubagentWorktree(ctx context.Context, taskID string) (path string, err error) {
	return provisionSubagentWorktreeAt(ctx, "", taskID)
}

func provisionSubagentWorktreeAt(ctx context.Context, wd, taskID string) (path string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if taskID == "" {
		return "", errors.New("invalid worktree task id")
	}
	for _, ch := range taskID {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return "", errors.New("invalid worktree task id")
		}
	}
	if wd == "" {
		wd, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	root, err := gitWorktreeRoot(ctx, wd)
	if err != nil {
		return "", err
	}
	if root == "" {
		return "", errors.New("git worktree root is empty")
	}
	name := taskID[:min(len(taskID), 48)] + "-" + rand.Text()
	branch := "subagent/" + name
	dirName := filepath.Base(root) + "-wt-" + name
	path = filepath.Join(filepath.Dir(root), dirName)
	cmd := exec.CommandContext(ctx, "git", "-C", root, "worktree", "add", "-b", branch, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("git worktree add at %s: %w", path, ctx.Err())
		}
		return "", fmt.Errorf("git worktree add at %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return path, nil
}
