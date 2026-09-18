package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func (m *model) exportCommand(arg string) {
	path := strings.TrimSpace(arg)
	if path == "" {
		path = "k-brain-transcript-" + m.sessionID + ".md"
	}
	if m.agent == nil || len(m.agent.Messages) == 0 {
		m.append(dimStyle.Render("(nothing to export yet)"))
		return
	}
	if err := exportTranscript(path, m.agent.Messages); err != nil {
		m.append(errStyle.Render("export failed: " + err.Error()))
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	m.append(dimStyle.Render("⤓ transcript exported → " + abs))
}

func exportTranscript(path string, msgs []ai.Message) error {
	var b strings.Builder
	b.WriteString("# Session transcript\n\n")
	for _, msg := range msgs {
		switch msg.Role {
		case "tool":
			b.WriteString("#### Tool result\n\n" + msg.TextContent() + "\n\n")
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n", displayRole(msg.Role))
		if c := msg.TextContent(); c != "" {
			b.WriteString(c + "\n\n")
		}
		for _, tc := range msg.ToolCalls {
			fmt.Fprintf(&b, "`%s`\n\n", tc.Function.Name)
		}
	}

	return os.WriteFile(path, []byte(strings.TrimRight(b.String(), "\n")+"\n"), 0o600)
}

func displayRole(role string) string {
	switch role {
	case "user":
		return "User"
	case "assistant":
		return "Assistant"
	case "system":
		return "System"
	default:

		if role == "" {
			return role
		}
		return strings.ToUpper(role[:1]) + role[1:]
	}
}
