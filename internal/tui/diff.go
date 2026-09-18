package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/process"
	"github.com/Stack-Cairn/K-brain/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

type diffOutput struct {
	data      []byte
	truncated bool
}

func (b *diffOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (1 << 20) - len(b.data)
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return n, nil
}

func gitDiff(ctx context.Context, root string, flags []string) (string, error) {
	args := []string{"--no-pager", "diff", "--no-ext-diff", "--no-textconv", "--no-color"}
	for _, flag := range flags {
		switch flag {
		case "--staged", "--cached", "--stat":
			args = append(args, flag)
		default:
			return "", fmt.Errorf("usage: /diff [--staged] [--stat]")
		}
	}
	cmd := exec.CommandContext(ctx, "git", append(args, "--")...)
	cmd.Dir = root
	process.Configure(cmd, false)
	cmd.WaitDelay = time.Second
	var out diffOutput
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	text := string(out.data)
	if err != nil {
		return "", fmt.Errorf("git diff: %w\n%s", err, tools.TruncateTail(text))
	}
	if strings.TrimSpace(text) == "" {
		text = "(no tracked changes; untracked files are not included)"
	}
	if out.truncated {
		text += "\n(diff truncated at 1 MiB)"
	}
	return text, nil
}

func (m *model) diffCommand(flags []string) tea.Cmd {
	for _, flag := range flags {
		if flag != "--staged" && flag != "--cached" && flag != "--stat" {
			m.append(errStyle.Render("usage: /diff [--staged] [--stat]"))
			return nil
		}
	}
	root, err := currentRoot()
	if err != nil {
		m.append(errStyle.Render("/diff: " + err.Error()))
		return nil
	}
	flags = append([]string(nil), flags...)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		text, err := gitDiff(ctx, root, flags)
		if err != nil {
			text = err.Error()
		}
		return shellDoneMsg{cmd: "git diff", out: text, localOnly: true}
	}
}
