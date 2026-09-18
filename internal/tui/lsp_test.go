package tui

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/lsp"
)

func TestLSPCommandNoManager(t *testing.T) {
	m := tasksModel("http://unused")
	m.lspMgr = nil
	m.command("/lsp")
	last := m.blocks[len(m.blocks)-1].text
	if !strings.Contains(last, "no LSP servers configured") {
		t.Errorf("got %q", last)
	}
}

func TestLSPCommandStatusRows(t *testing.T) {
	m := tasksModel("http://unused")
	m.lspMgr = lsp.NewManager(map[string]lsp.ServerSpec{
		"gopls":  {Command: []string{"gopls"}, Extensions: []string{".go"}},
		"zz-lsp": {Command: []string{"zz"}, Extensions: []string{".zz"}},
	})
	m.command("/lsp")
	last := m.blocks[len(m.blocks)-1].text
	if !strings.Contains(last, "LSP servers:") {
		t.Fatalf("got %q", last)
	}
	for _, want := range []string{"gopls", "zz-lsp", "not started"} {
		if !strings.Contains(last, want) {
			t.Errorf("status view missing %q: %q", want, last)
		}
	}
}
