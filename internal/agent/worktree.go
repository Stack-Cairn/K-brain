package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func gitWorktreeRoot(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func provisionSubagentWorktree(ctx context.Context, taskID string) (path string, err error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root := gitWorktreeRoot(ctx, wd)
	if root == "" {
		return "", errors.New("not inside a git work tree")
	}

	branch := "subagent/" + taskID
	dirName := filepath.Base(root) + "-wt-" + taskID
	path = filepath.Join(filepath.Dir(root), dirName)
	cmd := exec.CommandContext(ctx, "git", "-C", root, "worktree", "add", "-b", branch, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return path, nil
}
