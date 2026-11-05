package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
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
		return noticeMsg(copyReportNotice(clipboardFor().deliver(text)))
	}
}

// copyReportNotice says what /copy achieved: confirmed, unconfirmed or
// failed, and where the backup copy is when one was written.
func copyReportNotice(r copyReport) string {
	switch {
	case r.err != nil && r.backup != "":
		return "/copy: " + r.err.Error() + "; the reply is saved in " + r.backup
	case r.err != nil:
		return "/copy: " + r.err.Error() + "; use /copy [N] <file> instead"
	case r.route == copyOSC52 && r.backup != "":
		return "Reply sent to the terminal clipboard (OSC 52, unconfirmed); backup in " + r.backup
	case r.route == copyOSC52:
		return "Reply sent to the terminal clipboard (OSC 52, unconfirmed)"
	case r.verified:
		return "Reply copied to clipboard (verified)"
	default:
		return "Reply copied to clipboard"
	}
}
