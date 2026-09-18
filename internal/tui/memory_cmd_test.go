package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/memory"
)

func TestMemoryEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)

	m := compactCmdModel()

	callTool := func(name, args string) string {
		t.Helper()
		for _, tool := range m.agent.Tools {
			if tool.Def.Function.Name == name {
				out, err := tool.Run(t.Context(), []byte(args))
				if err != nil {
					return "Error: " + err.Error()
				}
				return out
			}
		}
		t.Fatal(name + " not registered")
		return ""
	}
	if out := callTool("remember", `{"text":"user prefers pnpm over npm"}`); strings.HasPrefix(out, "Error:") {
		t.Fatal(out)
	}

	data, err := os.ReadFile(filepath.Join(home, "memory.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "- [ ] user prefers pnpm over npm\n" {
		t.Fatalf("memory.md:\n%s", data)
	}

	m.prepareTurn("hello")
	sys := m.agent.Messages[0].Content
	if !strings.Contains(sys, "user prefers pnpm over npm") || !strings.Contains(sys, "<memory>") {
		t.Fatalf("memory not injected into the system prompt:\n%s", sys)
	}

	m.memoryCommand(nil)
	var listed bool
	for _, b := range m.blocks {
		r := ansi.Strip(b.render(m.width))
		if strings.Contains(r, "installation") && strings.Contains(r, "user prefers pnpm") {
			listed = true
		}
	}
	if !listed {
		t.Fatal("/memory should list the installation entry")
	}

	m.memoryCommand([]string{"1"})
	data, _ = os.ReadFile(filepath.Join(home, "memory.md"))
	if !strings.Contains(string(data), "- [x] user prefers pnpm") {
		t.Fatalf("entry should be struck, not deleted:\n%s", data)
	}
	m.prepareTurn("hello again")
	if strings.Contains(m.agent.Messages[0].Content, "pnpm") {
		t.Fatal("struck entries must stop being injected")
	}
}

func TestSessionMemoryScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)

	m := compactCmdModel()
	m.agent.SetSessionID("sess1")
	if err := memory.Session("sess1").Remember("this repo uses ./scripts/ship.sh to deploy"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "sessions", "sess1.memory.md")); err != nil {
		t.Fatal("session memory should live under sessions/")
	}
	m.sessionID = "sess1"
	m.prepareTurn("hi")
	if !strings.Contains(m.agent.Messages[0].Content, "ship.sh") {
		t.Fatal("session memory should inject while the session is active")
	}
}
