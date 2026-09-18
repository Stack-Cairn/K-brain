package tui

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
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
	dirty, err := gitOut(ctx, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return 0, err
	}
	if _, err := gitOut(ctx, "checkout", ref, "--", "."); err != nil {
		return 0, err
	}
	dropSnapshot(ref)
	if dirty == "" {
		return 0, nil
	}
	return len(strings.Split(dirty, "\n")), nil
}

func gitOut(ctx context.Context, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = cwd()
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
	return strings.TrimSpace(out.String()), nil
}
