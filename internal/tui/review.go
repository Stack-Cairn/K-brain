package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/process"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

func parseReviewArgs(args []string) (string, bool, error) {
	branch := ""
	fix := false
	for _, arg := range args {
		if arg == "--fix" {
			if fix {
				return "", false, fmt.Errorf("usage: /review <branch> [--fix]")
			}
			fix = true
			continue
		}
		if strings.HasPrefix(arg, "-") || branch != "" {
			return "", false, fmt.Errorf("usage: /review <branch> [--fix]")
		}
		branch = arg
	}
	if branch == "" {
		return "", false, fmt.Errorf("usage: /review <branch> [--fix]")
	}
	return branch, fix, nil
}

func reviewDiff(ctx context.Context, root, branch string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "--no-pager", "diff", "--no-ext-diff", "--no-textconv", "--no-color", branch+"...HEAD", "--")
	cmd.Dir = root
	process.Configure(cmd, false)
	cmd.WaitDelay = time.Second
	var out diffOutput
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git diff %s: %w\n%s", branch, err, tools.TruncateTail(string(out.data)))
	}
	if strings.TrimSpace(string(out.data)) == "" {
		return "", fmt.Errorf("no changes found between %s and HEAD", branch)
	}
	text := string(out.data)
	if out.truncated {
		text += "\n(diff truncated at 1 MiB)"
	}
	return text, nil
}

func reviewPrompt(branch, diff string, fix bool) string {
	mode := "Do not modify files."
	if fix {
		mode = "Apply safe fixes for confirmed findings using the available tools, then summarize every change."
	}
	return fmt.Sprintf("Review the changes in the current working tree against branch %q.\n%s\nReturn a concise structured review with severity (critical, high, medium, low), file and line, finding, and a concrete recommendation. Do not praise or repeat unchanged code.\n\nGit diff:\n```diff\n%s\n```", branch, mode, diff)
}

func (m *model) reviewCommand(args []string) tea.Cmd {
	branch, fix, err := parseReviewArgs(args)
	if err != nil {
		m.append(errStyle.Render(err.Error()))
		return nil
	}
	if m.busy {
		m.append(dimStyle.Render("(busy — /review after this turn)"))
		return nil
	}
	root, err := currentRoot()
	if err != nil {
		m.append(errStyle.Render("/review: " + err.Error()))
		return nil
	}
	if m.agent == nil || m.agent.Client == nil {
		m.append(errStyle.Render("/review: no API client configured"))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	ctx = sandbox.WithPolicy(ctx, m.sandboxPolicy)
	m.cancel = cancel
	m.busy = true
	m.append(dimStyle.Render("◎ reviewing " + branch + "…"))
	run := func() tea.Msg {
		diff, err := reviewDiff(ctx, root, branch)
		if err != nil {
			cancel()
			return reviewMsg{err: err, fix: fix}
		}
		result, usage, err := m.agent.Client.Complete(ctx, ai.Request{
			Model:     m.agent.Model,
			MaxTokens: 8192,
			Messages:  []ai.Message{{Role: "user", Content: reviewPrompt(branch, diff, fix)}},
		})
		m.agent.AddUsage(usage)
		cancel()
		return reviewMsg{result: result, err: err, fix: fix}
	}
	if m.prog == nil {
		msg := run()
		m.Update(msg)
		return nil
	}
	return run
}
