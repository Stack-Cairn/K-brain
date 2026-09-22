package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// gitStatusTimeout caps the async git readout so a slow or hung git never
// stalls the UI.
const gitStatusTimeout = 700 * time.Millisecond

// gitStatus is the lightweight git identity shown in the footer's status row:
// repo name, branch (or short sha when detached), and dirty counters.
type gitStatus struct {
	Repo      string
	Branch    string
	Detached  bool
	Added     int
	Removed   int
	Untracked int
}

// gitStatusMsg carries the latest git readout to the TUI.
type gitStatusMsg struct{ status gitStatus }

// fetchGitStatus runs the git readout off the UI thread.
func fetchGitStatus() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), gitStatusTimeout)
		defer cancel()
		status, err := loadGitStatus(ctx, cwd())
		if err != nil {
			return gitStatusMsg{}
		}
		return gitStatusMsg{status: status}
	}
}

// loadGitStatus collects repo identity and dirty counters with a handful of
// fast porcelain-free commands.
func loadGitStatus(ctx context.Context, dir string) (gitStatus, error) {
	root, err := gitRawAt(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return gitStatus{}, err
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return gitStatus{}, fmt.Errorf("empty git root")
	}

	status := gitStatus{Repo: filepath.Base(root)}
	if branch, err := gitRawAt(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && strings.TrimSpace(branch) != "" {
		status.Branch = strings.TrimSpace(branch)
	} else if sha, err := gitRawAt(ctx, root, "rev-parse", "--short", "HEAD"); err == nil && strings.TrimSpace(sha) != "" {
		status.Branch = strings.TrimSpace(sha)
		status.Detached = true
	}

	if out, err := gitRawAt(ctx, root, "diff", "--numstat", "HEAD", "--"); err == nil {
		status.Added, status.Removed = parseGitNumstat(out)
	}
	if out, err := gitRawAt(ctx, root, "status", "--porcelain=v1", "--untracked-files=normal"); err == nil {
		status.Untracked = countUntracked(out)
	}
	if err := ctx.Err(); err != nil {
		return gitStatus{}, err
	}
	return status, nil
}

// parseGitNumstat sums added/removed lines from `git diff --numstat` output.
func parseGitNumstat(out string) (added int, removed int) {
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] != "-" {
			if n, err := strconv.Atoi(fields[0]); err == nil {
				added += n
			}
		}
		if fields[1] != "-" {
			if n, err := strconv.Atoi(fields[1]); err == nil {
				removed += n
			}
		}
	}
	return added, removed
}

// countUntracked counts `?? ` entries in porcelain status output.
func countUntracked(out string) int {
	n := 0
	for line := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		if strings.HasPrefix(line, "?? ") {
			n++
		}
	}
	return n
}

// gitRepoStyle is the git identity colour: the theme's warn yellow
// (#d9a441 on dark terminals, #b68120 on light ones).
var gitRepoStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "136", Dark: "179"})

// render formats the git readout as `repo@branch  +N -M ?K` using the TUI's
// semantic styles: the repo name takes the warn yellow, the branch stays
// neutral, and the dirty counters keep their success/warn/error colours.
// It returns "" when there is no git identity to show.
func (s gitStatus) render() string {
	if strings.TrimSpace(s.Repo) == "" || strings.TrimSpace(s.Branch) == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(gitRepoStyle.Render(s.Repo))
	b.WriteString(dimStyle.Render("@"))
	if s.Detached {
		b.WriteString(gitRepoStyle.Render(s.Branch))
	} else {
		// A branch name is identity, not a success condition — neutral chrome.
		b.WriteString(chromeStyle.Render(s.Branch))
	}

	var parts []string
	if s.Added > 0 || s.Removed > 0 {
		parts = append(parts, growStyle.Render(fmt.Sprintf("+%d", s.Added)), errStyle.Render(fmt.Sprintf("-%d", s.Removed)))
	}
	if s.Untracked > 0 {
		parts = append(parts, gitRepoStyle.Render(fmt.Sprintf("?%d", s.Untracked)))
	}
	if len(parts) > 0 {
		b.WriteString("  ")
		b.WriteString(strings.Join(parts, " "))
	}
	return b.String()
}
