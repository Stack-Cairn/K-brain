package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/process"
	tea "github.com/charmbracelet/bubbletea"
)

func parseCopyArgs(arg string) (int, string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return 1, "", nil
	}
	fields := strings.Fields(arg)
	n, err := strconv.Atoi(fields[0])
	if err == nil {
		if n < 1 {
			return 0, "", fmt.Errorf("N must be at least 1")
		}
		return n, unquoteCopyPath(strings.TrimSpace(strings.TrimPrefix(arg, fields[0]))), nil
	}
	if strings.Trim(fields[0], "0123456789") == "" {
		return 0, "", fmt.Errorf("N is too large")
	}
	return 1, unquoteCopyPath(arg), nil
}

func unquoteCopyPath(path string) string {
	if len(path) >= 2 && (path[0] == '"' && path[len(path)-1] == '"' || path[0] == '\'' && path[len(path)-1] == '\'') {
		return path[1 : len(path)-1]
	}
	return path
}

func assistantReply(msgs []ai.Message, n int) (string, error) {
	if n < 1 {
		return "", fmt.Errorf("N must be at least 1")
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role != "assistant" || strings.TrimSpace(msg.TextContent()) == "" {
			continue
		}
		n--
		if n == 0 {
			return msg.TextContent(), nil
		}
	}
	return "", fmt.Errorf("not enough assistant replies")
}

func writeClipboard(text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var commands [][]string
	switch runtime.GOOS {
	case "windows":
		commands = [][]string{{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "[Console]::InputEncoding = [System.Text.UTF8Encoding]::new(); Set-Clipboard -Value ([Console]::In.ReadToEnd())"}}
	case "darwin":
		commands = [][]string{{"pbcopy"}}
	default:
		commands = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	}
	var last error
	for _, args := range commands {
		path, err := exec.LookPath(args[0])
		if err != nil {
			last = err
			continue
		}
		cmd := exec.CommandContext(ctx, path, args[1:]...)
		process.Configure(cmd, false)
		cmd.WaitDelay = time.Second
		cmd.Stdin = strings.NewReader(text)
		if err = cmd.Run(); err == nil {
			return nil
		}
		last = err
	}
	return fmt.Errorf("clipboard unavailable: %v; use /copy [N] <file> instead", last)
}

func (m *model) copyCommand(arg string) tea.Cmd {
	n, path, err := parseCopyArgs(arg)
	if err != nil {
		m.append(errStyle.Render("/copy: " + err.Error()))
		return nil
	}
	if m.agent == nil {
		m.append(errStyle.Render("/copy: no assistant replies"))
		return nil
	}
	text, err := assistantReply(m.agent.MessagesSnapshot(), n)
	if err != nil {
		m.append(errStyle.Render("/copy: " + err.Error()))
		return nil
	}
	if path != "" {
		path, err = filepath.Abs(path)
		if err != nil {
			m.append(errStyle.Render("/copy: " + err.Error()))
			return nil
		}
	}
	return func() tea.Msg {
		if path != "" {
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return noticeMsg("/copy: " + err.Error())
			}
			_, err = f.WriteString(text)
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				return noticeMsg("/copy: " + err.Error())
			}
			return noticeMsg("Reply saved to " + path)
		}
		if err := writeClipboard(text); err != nil {
			return noticeMsg("/copy: " + err.Error())
		}
		return noticeMsg("Reply copied to clipboard")
	}
}
